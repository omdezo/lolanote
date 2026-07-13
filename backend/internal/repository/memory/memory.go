// Package memory provides in-memory implementations of the domain repository
// interfaces for fast, deterministic unit tests. It is never wired into the
// running server — production uses repository/mongo.
package memory

import (
	"context"
	"strings"
	"time"

	"qomranote/backend/internal/domain"
)

// ElementRepo is an in-memory domain.ElementRepository.
type ElementRepo struct {
	items map[string]*domain.Element
}

// NewElementRepo constructs an empty store.
func NewElementRepo() *ElementRepo { return &ElementRepo{items: map[string]*domain.Element{}} }

var _ domain.ElementRepository = (*ElementRepo)(nil)

func clone(e *domain.Element) *domain.Element {
	cp := *e
	return &cp
}

func (r *ElementRepo) Insert(_ context.Context, el *domain.Element) error {
	if _, ok := r.items[el.ID]; ok {
		return domain.ErrConflict
	}
	r.items[el.ID] = clone(el)
	return nil
}

func (r *ElementRepo) Get(_ context.Context, id string) (*domain.Element, error) {
	if el, ok := r.items[id]; ok {
		return clone(el), nil
	}
	return nil, domain.ErrNotFound
}

func (r *ElementRepo) GetMany(_ context.Context, ids []string) ([]*domain.Element, error) {
	var out []*domain.Element
	for _, id := range ids {
		if el, ok := r.items[id]; ok {
			out = append(out, clone(el))
		}
	}
	return out, nil
}

func (r *ElementRepo) Children(_ context.Context, f domain.ElementFilter) ([]*domain.Element, error) {
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

func (r *ElementRepo) Descendants(_ context.Context, rootID string, includeDeleted bool) ([]*domain.Element, error) {
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
					out = append(out, clone(el))
					if el.Type.IsContainer() {
						next = append(next, el.ID)
					}
				}
			}
		}
		frontier = next
	}
	return out, nil
}

func (r *ElementRepo) MergePatch(_ context.Context, id string, patch domain.Content) (*domain.Element, error) {
	el, ok := r.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if c, ok := patch["content"].(map[string]any); ok {
		if el.Content == nil {
			el.Content = domain.Content{}
		}
		for k, v := range c {
			if v == nil {
				delete(el.Content, k)
			} else {
				el.Content[k] = v
			}
		}
	}
	if loc, ok := patch["location"].(map[string]any); ok {
		if p, ok := loc["parentId"].(string); ok {
			el.Location.ParentID = p
		}
	}
	el.UpdatedAt = time.Now().UTC()
	return clone(el), nil
}

func (r *ElementRepo) SetACL(_ context.Context, id string, acl *domain.ACL) error {
	el, ok := r.items[id]
	if !ok {
		return domain.ErrNotFound
	}
	el.ACL = acl
	return nil
}

func (r *ElementRepo) SoftDelete(_ context.Context, ids []string, by, batchID string, at time.Time) error {
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
	for _, id := range ids {
		delete(r.items, id)
	}
	return nil
}

func (r *ElementRepo) Trashed(_ context.Context, ownerSub string) ([]*domain.Element, error) {
	var out []*domain.Element
	for _, el := range r.items {
		if el.IsDeleted() && (el.DeletedBy == ownerSub || el.CreatedBy == ownerSub) {
			out = append(out, clone(el))
		}
	}
	return out, nil
}

func (r *ElementRepo) Search(_ context.Context, ownerSub, query string, limit int) ([]*domain.Element, error) {
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

func (r *TransactionRepo) Insert(_ context.Context, t *domain.Transaction) error {
	r.items = append(r.items, t)
	return nil
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
