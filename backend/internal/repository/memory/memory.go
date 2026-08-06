// Package memory provides in-memory implementations of the domain repository
// interfaces for fast, deterministic unit tests. It is never wired into the
// running server — production uses repository/mongo.
package memory

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"qomranote/backend/internal/domain"
)

// ElementRepo is an in-memory domain.ElementRepository.
//
// Guarded because the thing it stands in for is a database several goroutines
// talk to at once, and the contract that matters most about the element store —
// that two people patching disjoint fields of the same card both keep their
// change — cannot even be STATED against a store that must be called one caller
// at a time. A double that is quietly less capable than the real thing turns
// green tests into evidence of nothing.
type ElementRepo struct {
	mu    sync.Mutex
	items map[string]*domain.Element
}

// NewElementRepo constructs an empty store.
func NewElementRepo() *ElementRepo { return &ElementRepo{items: map[string]*domain.Element{}} }

var _ domain.ElementRepository = (*ElementRepo)(nil)

// clone hands out a copy that shares nothing with the store.
//
// A shallow struct copy is not enough: Content is a map and LabelIDs a slice,
// so the caller would be handed the repository's own state and could rewrite
// stored data by editing what it was given. Mongo cannot do that — every read
// there decodes fresh — so a double that can is a double that hides bugs.
func clone(e *domain.Element) *domain.Element {
	cp := *e
	cp.Content = cloneContent(e.Content)
	if e.LabelIDs != nil {
		cp.LabelIDs = append([]string(nil), e.LabelIDs...)
	}
	if e.ACL != nil {
		acl := *e.ACL
		if e.ACL.Editors != nil {
			acl.Editors = append([]string(nil), e.ACL.Editors...)
		}
		cp.ACL = &acl
	}
	if e.DeletedAt != nil {
		at := *e.DeletedAt
		cp.DeletedAt = &at
	}
	return &cp
}

func cloneContent(c domain.Content) domain.Content {
	if c == nil {
		return nil
	}
	out := make(domain.Content, len(c))
	for k, v := range c {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case domain.Content:
		return cloneContent(t)
	case map[string]any:
		return map[string]any(cloneContent(domain.Content(t)))
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = cloneValue(x)
		}
		return out
	case []string:
		return append([]string(nil), t...)
	default:
		return v
	}
}

func (r *ElementRepo) Insert(_ context.Context, el *domain.Element) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[el.ID]; ok {
		return domain.ErrConflict
	}
	r.items[el.ID] = clone(el)
	return nil
}

func (r *ElementRepo) Get(_ context.Context, id string) (*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if el, ok := r.items[id]; ok {
		return clone(el), nil
	}
	return nil, domain.ErrNotFound
}

func (r *ElementRepo) GetMany(_ context.Context, ids []string) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	for _, id := range ids {
		if el, ok := r.items[id]; ok {
			out = append(out, clone(el))
		}
	}
	return out, nil
}

func (r *ElementRepo) Children(_ context.Context, f domain.ElementFilter) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	for _, el := range r.items {
		if el.Location.ParentID != f.ParentID {
			continue
		}
		if !f.IncludeDeleted && el.IsDeleted() {
			continue
		}
		if f.Section != "" && el.Location.Section != f.Section {
			continue
		}
		out = append(out, clone(el))
	}
	return out, nil
}

func (r *ElementRepo) Descendants(ctx context.Context, rootID string, includeDeleted bool) ([]*domain.Element, error) {
	els, _, err := r.DescendantsLimited(ctx, rootID, includeDeleted, 0)
	return els, err
}

// DescendantsLimited is Descendants with a ceiling, reporting whether it stopped
// short. A limit of 0 means no ceiling. See the Mongo adapter for why the flag
// exists rather than a silently short list.
func (r *ElementRepo) DescendantsLimited(_ context.Context, rootID string, includeDeleted bool, limit int) ([]*domain.Element, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	frontier := []string{rootID}
	for len(frontier) > 0 {
		var next []string
		for _, el := range r.items {
			for _, p := range frontier {
				if el.Location.ParentID == p {
					if !includeDeleted && el.IsDeleted() {
						continue
					}
					if limit > 0 && len(out) >= limit {
						return out, true, nil
					}
					out = append(out, clone(el))
					if el.Type.IsContainer() {
						next = append(next, el.ID)
					}
				}
			}
		}
		frontier = next
	}
	return out, false, nil
}

// patchableRoots must stay identical to repository/mongo's list. The contract
// in repository/repotest is what actually holds the two together; this comment
// only says where to look.
var patchableRoots = map[string]bool{"content": true, "location": true, "labelIds": true}

// MergePatch applies RFC-7386 semantics over the whitelisted roots, the same
// way the Mongo repository does.
//
// It used to hand-roll a shortcut: content keys merged, and of location only
// parentId. Every other field — position, section, index, width, height — was
// silently dropped, which meant a MOVE was a no-op in every test that used this
// repository while being a real write in production. That is precisely the
// failure a test double is supposed to make impossible.
func (r *ElementRepo) MergePatch(_ context.Context, id string, patch domain.Content) (*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	el, ok := r.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	for key, val := range patch {
		if !patchableRoots[key] {
			continue
		}
		switch key {
		case "content":
			merged, _ := mergeValue(map[string]any(el.Content), val).(map[string]any)
			el.Content = domain.Content(merged)
		case "location":
			// Round-tripped through the location's own json tags so a partial
			// patch means the same thing here as it does on the wire.
			current, err := asMap(el.Location)
			if err != nil {
				return nil, err
			}
			merged, _ := mergeValue(current, val).(map[string]any)
			var next domain.Location
			if err := decode(merged, &next); err != nil {
				return nil, err
			}
			el.Location = next
		case "labelIds":
			el.LabelIDs = toStrings(val)
		}
	}
	el.UpdatedAt = time.Now().UTC()
	return clone(el), nil
}

// mergeValue is RFC-7386: nested objects merge recursively, null deletes,
// everything else replaces. Mirrors mongo.mergeValue.
func mergeValue(existing, patch any) any {
	patchMap, patchIsMap := objectOf(patch)
	if !patchIsMap {
		return patch
	}
	existingMap, existingIsMap := objectOf(existing)
	if !existingIsMap {
		existingMap = map[string]any{}
	}
	for k, v := range patchMap {
		if v == nil {
			delete(existingMap, k)
			continue
		}
		existingMap[k] = mergeValue(existingMap[k], v)
	}
	return existingMap
}

// objectOf recognizes the several spellings of "a JSON object" that reach this
// layer: domain.Content from Go callers, map[string]any from the wire.
func objectOf(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case domain.Content:
		return map[string]any(m), true
	default:
		return nil, false
	}
}

func asMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func decode(m map[string]any, into any) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

// toStrings accepts labelIds as []string (Go callers) or []any (JSON).
func toStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func (r *ElementRepo) SetACL(_ context.Context, id string, acl *domain.ACL) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	el, ok := r.items[id]
	if !ok {
		return domain.ErrNotFound
	}
	el.ACL = acl
	return nil
}

func (r *ElementRepo) SoftDelete(_ context.Context, ids []string, by, batchID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if el, ok := r.items[id]; ok {
			el.DeletedAt = &at
			el.DeletedBy = by
			el.TrashBatchID = batchID
		}
	}
	return nil
}

func (r *ElementRepo) Restore(_ context.Context, ids []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if el, ok := r.items[id]; ok {
			el.DeletedAt = nil
			el.DeletedBy = ""
			el.TrashBatchID = ""
		}
	}
	return nil
}

func (r *ElementRepo) RestoreBatch(_ context.Context, batchID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, el := range r.items {
		if el.TrashBatchID == batchID {
			el.DeletedAt = nil
			el.DeletedBy = ""
			el.TrashBatchID = ""
		}
	}
	return nil
}

func (r *ElementRepo) HardDelete(_ context.Context, ids []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		delete(r.items, id)
	}
	return nil
}

func (r *ElementRepo) Trashed(_ context.Context, ownerSub string) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	for _, el := range r.items {
		if el.IsDeleted() && (el.DeletedBy == ownerSub || el.CreatedBy == ownerSub) {
			out = append(out, clone(el))
		}
	}
	return out, nil
}

func (r *ElementRepo) Search(_ context.Context, ownerSub, query string, limit int) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	for _, el := range r.items {
		if el.IsDeleted() {
			continue
		}
		if strings.Contains(strings.ToLower(el.Title()), strings.ToLower(query)) {
			out = append(out, clone(el))
		}
	}
	return out, nil
}

func (r *ElementRepo) CloneInstances(_ context.Context, sourceID string) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	for _, el := range r.items {
		if el.Type == domain.TypeClone && !el.IsDeleted() {
			if src, _ := el.Content["cloneSourceId"].(string); src == sourceID {
				out = append(out, clone(el))
			}
		}
	}
	return out, nil
}

func (r *ElementRepo) BoardsOwnedBy(_ context.Context, sub string, templatesOnly bool) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	for _, el := range r.items {
		if el.Type != domain.TypeBoard || el.IsDeleted() || el.ACL == nil {
			continue
		}
		owned := el.ACL.OwnerID == sub
		for _, e := range el.ACL.Editors {
			owned = owned || e == sub
		}
		if !owned {
			continue
		}
		if templatesOnly {
			if t, _ := el.Content["isTemplate"].(bool); !t {
				continue
			}
		}
		out = append(out, clone(el))
	}
	return out, nil
}

func (r *ElementRepo) DueTaskReminders(_ context.Context, now time.Time, limit int) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	cutoff := now.UTC().Format(time.RFC3339)
	for _, el := range r.items {
		if el.Type != domain.TypeTask || el.IsDeleted() {
			continue
		}
		if done, _ := el.Content["done"].(bool); done {
			continue
		}
		if sent, _ := el.Content["reminderSent"].(bool); sent {
			continue
		}
		at, _ := el.Content["reminderAt"].(string)
		if at != "" && at <= cutoff {
			out = append(out, clone(el))
		}
	}
	return out, nil
}

func (r *ElementRepo) OwnedBoards(_ context.Context, sub string, includeDeleted bool) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	for _, el := range r.items {
		if el.Type != domain.TypeBoard || el.ACL == nil || el.ACL.OwnerID != sub {
			continue
		}
		if !includeDeleted && el.IsDeleted() {
			continue
		}
		out = append(out, clone(el))
	}
	return out, nil
}

func (r *ElementRepo) RemoveEditorEverywhere(_ context.Context, sub string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, el := range r.items {
		if el.Type != domain.TypeBoard || el.ACL == nil {
			continue
		}
		kept := el.ACL.Editors[:0]
		for _, e := range el.ACL.Editors {
			if e != sub {
				kept = append(kept, e)
			}
		}
		el.ACL.Editors = kept
	}
	return nil
}

func (r *ElementRepo) BoardsByShareToken(_ context.Context, token string) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	for _, el := range r.items {
		if el.Type != domain.TypeBoard || el.ACL == nil {
			continue
		}
		if el.ACL.PublicEditLink == token || (el.ACL.ViewLink != nil && el.ACL.ViewLink.Token == token) {
			out = append(out, clone(el))
		}
	}
	return out, nil
}

func (r *ElementRepo) CountsByParent(_ context.Context, parentIDs []string) (map[string]map[domain.ElementType]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]map[domain.ElementType]int64{}
	set := map[string]bool{}
	for _, id := range parentIDs {
		set[id] = true
	}
	for _, el := range r.items {
		if el.IsDeleted() || !set[el.Location.ParentID] {
			continue
		}
		if out[el.Location.ParentID] == nil {
			out[el.Location.ParentID] = map[domain.ElementType]int64{}
		}
		out[el.Location.ParentID][el.Type]++
	}
	return out, nil
}

func (r *ElementRepo) PurgeExpired(_ context.Context, olderThan time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for id, el := range r.items {
		if el.IsDeleted() && el.DeletedAt.Before(olderThan) {
			delete(r.items, id)
			n++
		}
	}
	return n, nil
}

// TransactionRepo is an in-memory domain.TransactionRepository.
type TransactionRepo struct{ items []*domain.Transaction }

// NewTransactionRepo constructs an empty log.
func NewTransactionRepo() *TransactionRepo { return &TransactionRepo{} }

var _ domain.TransactionRepository = (*TransactionRepo)(nil)

// Insert refuses a duplicate id, because _id is unique in Mongo and the whole
// idempotency story rests on that: a caller that pins its transaction id claims
// the row by inserting it, and the database losing that race is what tells a
// retry it is a retry. A double that accepted the second insert would have made
// the claim look like it worked while doing nothing.
func (r *TransactionRepo) Insert(_ context.Context, t *domain.Transaction) error {
	for _, existing := range r.items {
		if existing.ID == t.ID {
			return domain.ErrConflict
		}
	}
	r.items = append(r.items, t)
	return nil
}

// RedactElements strips the content of ops naming permanently-deleted elements,
// keeping the op itself. See the Mongo adapter for why.
func (r *TransactionRepo) RedactElements(_ context.Context, elementIDs []string) (int64, error) {
	doomed := make(map[string]bool, len(elementIDs))
	for _, id := range elementIDs {
		doomed[id] = true
	}
	var n int64
	for _, t := range r.items {
		touched := false
		for i := range t.Ops {
			if !doomed[t.Ops[i].ElementID] || t.Ops[i].Redacted {
				continue
			}
			t.Ops[i] = domain.Op{
				ElementID: t.Ops[i].ElementID, Action: t.Ops[i].Action, Redacted: true,
			}
			touched = true
		}
		if touched {
			n++
		}
	}
	return n, nil
}

// Complete settles a claimed row. See service.TransactionReserver.
func (r *TransactionRepo) Complete(_ context.Context, txn *domain.Transaction) error {
	for i, existing := range r.items {
		if existing.ID != txn.ID {
			continue
		}
		settled := *txn
		settled.State = domain.TxnCommitted
		r.items[i] = &settled
		return nil
	}
	return domain.ErrNotFound
}

// Abandon releases a claim whose work did not stand.
func (r *TransactionRepo) Abandon(_ context.Context, id string) error {
	for i, existing := range r.items {
		if existing.ID == id {
			r.items = append(r.items[:i], r.items[i+1:]...)
			return nil
		}
	}
	return nil
}

func (r *TransactionRepo) Get(_ context.Context, id string) (*domain.Transaction, error) {
	for _, t := range r.items {
		if t.ID == id {
			cp := *t
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r *TransactionRepo) ListByBoard(_ context.Context, boardID string, limit int) ([]*domain.Transaction, error) {
	var out []*domain.Transaction
	for _, t := range r.items {
		if t.BoardID == boardID {
			out = append(out, t)
		}
	}
	return out, nil
}

// Count returns how many transactions were recorded (test helper).
func (r *TransactionRepo) Count() int { return len(r.items) }

// DeleteByUser removes every transaction one person authored. See the Mongo
// adapter for why the journal is the collection this matters most for.
func (r *TransactionRepo) DeleteByUser(_ context.Context, sub string) error {
	kept := r.items[:0]
	for _, t := range r.items {
		if t.UserID != sub {
			kept = append(kept, t)
		}
	}
	r.items = kept
	return nil
}

// DeleteOlderThan trims the journal past its retention window.
func (r *TransactionRepo) DeleteOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	kept := r.items[:0]
	var n int64
	for _, t := range r.items {
		if t.CreatedAt.Before(cutoff) {
			n++
			continue
		}
		kept = append(kept, t)
	}
	r.items = kept
	return n, nil
}

// AttachmentRefs answers which attachment ids the given elements point at.
//
// Trash purge deletes the elements and nothing has ever looked at
// content.attachmentId on the way past, so every "Delete forever" and every
// Empty Trash left the actual bytes behind — reachable, because the blob route
// is unauthenticated and the URL is still in any board snapshot, export or
// offline mirror that ever saw it. A filmmaker deleting a rejected casting photo
// deleted the card, not the photograph.
func (r *ElementRepo) AttachmentRefs(_ context.Context, ids []string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, el := range r.items {
		if !want[el.ID] {
			continue
		}
		if att, _ := el.Content["attachmentId"].(string); att != "" && !seen[att] {
			seen[att] = true
			out = append(out, att)
		}
	}
	return out, nil
}

// ElementsByAttachment is the reverse index from an uploaded file to the cards
// that show it — the only way to ask whether a caller may read a blob.
func (r *ElementRepo) ElementsByAttachment(_ context.Context, attachmentID string) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	for _, el := range r.items {
		if att, _ := el.Content["attachmentId"].(string); att == attachmentID {
			out = append(out, clone(el))
		}
	}
	return out, nil
}

// AttachmentReferrers counts the LIVE elements pointing at each attachment, so
// a duplicated card cannot cause its twin's image to be collected.
func (r *ElementRepo) AttachmentReferrers(_ context.Context, attachmentIDs []string) (map[string]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := make(map[string]bool, len(attachmentIDs))
	for _, id := range attachmentIDs {
		want[id] = true
	}
	out := map[string]int64{}
	for _, el := range r.items {
		att, _ := el.Content["attachmentId"].(string)
		if att == "" || !want[att] {
			continue
		}
		out[att]++
	}
	return out, nil
}

// ExpiringSoon lists the trashed elements a purge at this cutoff will remove,
// so their attachments can be read BEFORE the rows naming them are gone.
func (r *ElementRepo) ExpiringSoon(_ context.Context, olderThan time.Time) ([]*domain.Element, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.Element
	for _, el := range r.items {
		if el.IsDeleted() && el.DeletedAt.Before(olderThan) {
			out = append(out, clone(el))
		}
	}
	return out, nil
}

// CommentRepo is an in-memory domain.CommentRepository. It exists so the
// deletion paths comments were missing from can be asserted at all.
type CommentRepo struct{ rows map[string]*domain.Comment }

// NewCommentRepo constructs an empty store.
func NewCommentRepo() *CommentRepo { return &CommentRepo{rows: map[string]*domain.Comment{}} }

var _ domain.CommentRepository = (*CommentRepo)(nil)

func (r *CommentRepo) Insert(_ context.Context, c *domain.Comment) error {
	cp := *c
	r.rows[c.ID] = &cp
	return nil
}

func (r *CommentRepo) Get(_ context.Context, id string) (*domain.Comment, error) {
	c, ok := r.rows[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *CommentRepo) ListByThread(_ context.Context, threadID string) ([]*domain.Comment, error) {
	var out []*domain.Comment
	for _, c := range r.rows {
		if c.ThreadID == threadID {
			cp := *c
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *CommentRepo) Update(_ context.Context, c *domain.Comment) error {
	if _, ok := r.rows[c.ID]; !ok {
		return domain.ErrNotFound
	}
	cp := *c
	r.rows[c.ID] = &cp
	return nil
}

func (r *CommentRepo) Delete(_ context.Context, id string) error {
	delete(r.rows, id)
	return nil
}

func (r *CommentRepo) DeleteByThread(_ context.Context, threadID string) error {
	for id, c := range r.rows {
		if c.ThreadID == threadID {
			delete(r.rows, id)
		}
	}
	return nil
}

func (r *CommentRepo) DeleteByAuthor(_ context.Context, sub string) error {
	for id, c := range r.rows {
		if c.AuthorID == sub {
			delete(r.rows, id)
		}
	}
	return nil
}

// Count reports how many messages survive (test helper — the assertion DL12 is
// about is "zero", and only a walk of the collection can make it).
func (r *CommentRepo) Count() int { return len(r.rows) }
