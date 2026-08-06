package service

import (
	"context"
	"time"

	"qomranote/backend/internal/domain"
)

// ElementService covers element reads and the structured operations that are
// more than a merge patch: duplication (deep copies), synced-note conversion
// (§4.15), and the Trash lifecycle (§3.4).
type ElementService struct {
	elements  domain.ElementRepository
	access    *AccessResolver
	newID     IDGenerator
	collector *Collector // optional: the non-element half of a permanent delete
}

// NewElementService constructs the service.
func NewElementService(elements domain.ElementRepository, access *AccessResolver, newID IDGenerator) *ElementService {
	return &ElementService{elements: elements, access: access, newID: newID}
}

// AttachCollector lets "Delete forever" reach the uploaded bytes and the
// journal's verbatim copy of what it deleted.
//
// Optional so a minimal wiring still compiles, but a deployment without it has
// a delete gesture that stops at the element row — see Collector for what
// survives.
func (s *ElementService) AttachCollector(c *Collector) { s.collector = c }

// Get returns one element after a view check.
func (s *ElementService) Get(ctx context.Context, p *domain.Principal, id string) (*domain.Element, error) {
	el, err := s.elements.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	role, _, err := s.access.RequireView(ctx, id, p)
	if err != nil {
		return nil, err
	}
	return redact(el, role), nil
}

// Duplicate deep-copies an element; containers (boards, columns, task lists)
// copy their whole live subtree with remapped ids (§5).
func (s *ElementService) Duplicate(ctx context.Context, p *domain.Principal, id string) ([]*domain.Element, error) {
	src, err := s.elements.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if isHome(src) {
		return nil, domain.ErrHomeBoard
	}
	if _, err := s.access.RequireEdit(ctx, id, p); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	idMap := map[string]string{src.ID: s.newID()}

	subtree := []*domain.Element{src}
	if src.Type.IsContainer() {
		// Bounded, and REFUSING rather than truncating: a duplicate missing
		// cards nobody can name is worse than a duplicate that did not happen.
		descendants, err := subtreeOf(ctx, s.elements, src.ID, false)
		if err != nil {
			return nil, err
		}
		for _, d := range descendants {
			idMap[d.ID] = s.newID()
		}
		subtree = append(subtree, descendants...)
	}

	created := make([]*domain.Element, 0, len(subtree))
	for _, orig := range subtree {
		copyEl := *orig
		copyEl.ID = idMap[orig.ID]
		copyEl.CreatedBy = p.Sub
		copyEl.CreatedAt = now
		copyEl.UpdatedAt = now
		copyEl.Content = cloneContent(orig.Content)
		if orig.ID == src.ID {
			// The duplicate lands just beside the original.
			copyEl.Location.Position.X += 32
			copyEl.Location.Position.Y += 32
			copyEl.Location.Index = orig.Location.Index + 0.5
			delete(copyEl.Content, "isTemplate")
		} else if mapped, ok := idMap[orig.Location.ParentID]; ok {
			copyEl.Location.ParentID = mapped
		}
		if copyEl.Type == domain.TypeBoard {
			copyEl.ACL = &domain.ACL{OwnerID: p.Sub, Editors: []string{}}
		}
		if err := s.elements.Insert(ctx, &copyEl); err != nil {
			return nil, err
		}
		created = append(created, &copyEl)
	}
	return created, nil
}

// UseTemplate stamps a fresh editable copy of a template board's subtree into
// a target board (§5). Runs server-side so it doesn't trip the cross-board
// move guard: the caller needs view access to the template and edit access to
// the destination.
func (s *ElementService) UseTemplate(ctx context.Context, p *domain.Principal, templateID, targetBoardID string, pos domain.Point) (*domain.Element, error) {
	tpl, err := s.elements.Get(ctx, templateID)
	if err != nil {
		return nil, err
	}
	if tpl.Type != domain.TypeBoard {
		return nil, domain.ErrValidation
	}
	isTemplate, _ := tpl.Content["isTemplate"].(bool)
	systemOwned := tpl.ACL != nil && tpl.ACL.OwnerID == "system"
	// System templates are usable by everyone; otherwise the caller must be
	// able to view the template board they're copying.
	if !(isTemplate && systemOwned) {
		if _, _, err := s.access.RequireView(ctx, templateID, p); err != nil {
			return nil, err
		}
	}
	if _, err := s.access.RequireEdit(ctx, targetBoardID, p); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	idMap := map[string]string{tpl.ID: s.newID()}
	descendants, err := s.elements.Descendants(ctx, tpl.ID, false)
	if err != nil {
		return nil, err
	}
	for _, d := range descendants {
		idMap[d.ID] = s.newID()
	}

	var root *domain.Element
	for _, orig := range append([]*domain.Element{tpl}, descendants...) {
		copyEl := *orig
		copyEl.ID = idMap[orig.ID]
		copyEl.CreatedBy = p.Sub
		copyEl.CreatedAt = now
		copyEl.UpdatedAt = now
		copyEl.Content = cloneContent(orig.Content)
		if orig.ID == tpl.ID {
			// Root lands in the target board at the requested position and is
			// no longer a template.
			copyEl.Location.ParentID = targetBoardID
			copyEl.Location.Section = domain.SectionCanvas
			copyEl.Location.Position = pos
			delete(copyEl.Content, "isTemplate")
			copyEl.ACL = &domain.ACL{OwnerID: p.Sub, Editors: []string{}}
		} else if mapped, ok := idMap[orig.Location.ParentID]; ok {
			copyEl.Location.ParentID = mapped
			if copyEl.Type == domain.TypeBoard {
				copyEl.ACL = &domain.ACL{OwnerID: p.Sub, Editors: []string{}}
			}
		}
		if err := s.elements.Insert(ctx, &copyEl); err != nil {
			return nil, err
		}
		if orig.ID == tpl.ID {
			root = &copyEl
		}
	}
	return root, nil
}

// ConvertToClone implements synced notes: duplicating a note and answering
// "keep in sync" creates a CLONE instance sharing the source card's content.
// Restricted to text notes, exactly like Milanote (§4.15).
func (s *ElementService) ConvertToClone(ctx context.Context, p *domain.Principal, sourceID, targetParentID string, pos domain.Point) (*domain.Element, error) {
	src, err := s.elements.Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if src.Type != domain.TypeCard {
		return nil, domain.ErrValidation
	}
	if _, err := s.access.RequireEdit(ctx, sourceID, p); err != nil {
		return nil, err
	}
	if _, err := s.access.RequireEdit(ctx, targetParentID, p); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	clone := &domain.Element{
		ID:   s.newID(),
		Type: domain.TypeClone,
		Location: domain.Location{
			ParentID: targetParentID,
			Section:  domain.SectionCanvas,
			Position: pos,
			Width:    src.Location.Width,
		},
		Content:   domain.Content{"cloneSourceId": src.ID},
		CreatedBy: p.Sub,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.elements.Insert(ctx, clone); err != nil {
		return nil, err
	}
	return clone, nil
}

// CloneInstances lists where a synced note's siblings live — the footer each
// copy renders (§4.15). Returns the instances plus their parent board titles.
//
// Every instance is authorised on its OWN merits, and that is the point. View
// rights on the SOURCE were the only check, and the answer this returns is a list
// of OTHER people's boards: a collaborator who clones your card onto their
// private board puts that board's id and its title into your reply, and the
// reverse leaks yours to them. The read path already learned this lesson once, in
// BoardService.Children, where resolving a clone's source unchecked was a
// cross-tenant disclosure; this is the same relationship read from the other end
// and it kept the bug.
//
// Instances the caller cannot view are DROPPED rather than erroring: a copy on a
// board you were never shown is a normal state, not a failed request. The count
// therefore tells the truth about what you can see, not about what exists.
func (s *ElementService) CloneInstances(ctx context.Context, p *domain.Principal, sourceID string) ([]map[string]any, error) {
	if _, _, err := s.access.RequireView(ctx, sourceID, p); err != nil {
		return nil, err
	}
	clones, err := s.elements.CloneInstances(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(clones))
	for _, c := range clones {
		if _, _, err := s.access.RequireView(ctx, c.ID, p); err != nil {
			continue
		}
		entry := map[string]any{"id": c.ID, "parentId": c.Location.ParentID}
		if parent, err := s.elements.Get(ctx, c.Location.ParentID); err == nil {
			entry["boardTitle"] = parent.Title()
		}
		out = append(out, entry)
	}
	return out, nil
}

// ---- Trash (§3.4): per-account, 3-month retention, restore by action ----

// TrashItem annotates a trashed element for the split
// "deleted by me / deleted by others" view.
type TrashItem struct {
	Element     *domain.Element `json:"element"`
	DeletedByMe bool            `json:"deletedByMe"`
	// PurgeAt is the moment this stops being recoverable — DL17's hard clause.
	//
	// The UI used to compute this itself, from a hard-coded "3 months" that sat
	// in the trash panel's EMPTY state, so the one sentence stating the window
	// rendered only when there was nothing left to lose. Retention here is 90
	// days, not three months; those differ by up to a day at the boundary, and a
	// day is long enough for someone to believe a deleted sequence is still
	// coming back when it is already gone. Serving the deadline means a change
	// to domain.TrashRetention moves every countdown in the product at once,
	// instead of silently making all of them wrong.
	//
	// Omitted rather than zero-valued when the row somehow has no DeletedAt: the
	// client renders nothing for a missing deadline, which is honest, where a
	// zero time renders as "expired" for something that is not.
	PurgeAt *time.Time `json:"purgeAt,omitempty"`
}

// Trash lists the caller's trash.
func (s *ElementService) Trash(ctx context.Context, p *domain.Principal) ([]TrashItem, error) {
	items, err := s.elements.Trashed(ctx, p.Sub)
	if err != nil {
		return nil, err
	}
	out := make([]TrashItem, 0, len(items))
	for _, el := range items {
		// A trashed sub-board is still an Element with an ACL on it, and the
		// trash view is one more place that shipped it whole.
		item := TrashItem{Element: redact(el, RoleView), DeletedByMe: el.DeletedBy == p.Sub}
		if el.DeletedAt != nil {
			purge := el.DeletedAt.Add(domain.TrashRetention)
			item.PurgeAt = &purge
		}
		out = append(out, item)
	}
	return out, nil
}

// mayActOnTrash answers who may restore or destroy a trashed element.
//
// Authorship was the WHOLE test — "you deleted it, or you made it" — and
// authorship is a fact about the past that no revocation can take back. The trash
// query is `deletedBy = me OR createdBy = me` across the entire database, so
// every element you ever created on somebody else's board stays in your trash
// list after they remove you from it. From there, DELETE /trash/:id destroyed the
// element AND, when it is a container, its whole live subtree — a column you made
// as a guest, later filled with fifty of the owner's cards, hard-deleted along
// with their attachments and the journal's copy of them, by someone who at that
// point cannot open the board at all.
//
// The two conditions are both necessary. Authorship alone is a claim about who
// made something; the ACL is the claim about who may act on it now. Everything on
// this path is irreversible or structural, so it needs both — the ordinary case
// (your own board, your own card) satisfies them without noticing.
//
// Fails CLOSED when the chain cannot be resolved: an element whose ancestors are
// gone is exactly the case where nobody can prove they are allowed.
func (s *ElementService) mayActOnTrash(ctx context.Context, p *domain.Principal, el *domain.Element) error {
	if el.DeletedBy != p.Sub && el.CreatedBy != p.Sub {
		return domain.ErrForbidden
	}
	role, _, err := s.access.Resolve(ctx, el.ID, p)
	if err != nil {
		return domain.ErrForbidden
	}
	if !role.CanEdit() {
		return domain.ErrForbidden
	}
	return nil
}

// RestoreFromTrash brings one element back.
func (s *ElementService) RestoreFromTrash(ctx context.Context, p *domain.Principal, id string) error {
	el, err := s.elements.Get(ctx, id)
	if err != nil {
		return err
	}
	if !el.IsDeleted() {
		return domain.ErrValidation
	}
	if err := s.mayActOnTrash(ctx, p, el); err != nil {
		return err
	}
	// Restore the whole delete batch so a trashed board comes back with its
	// contents (§3.4).
	if el.TrashBatchID != "" {
		return s.elements.RestoreBatch(ctx, el.TrashBatchID)
	}
	return s.elements.Restore(ctx, []string{id})
}

// DeletePermanently removes one trashed element and its subtree, irreversibly.
func (s *ElementService) DeletePermanently(ctx context.Context, p *domain.Principal, id string) error {
	el, err := s.elements.Get(ctx, id)
	if err != nil {
		return err
	}
	if !el.IsDeleted() {
		return domain.ErrValidation
	}
	if err := s.mayActOnTrash(ctx, p, el); err != nil {
		return err
	}
	// Everything the delete is about to destroy the only record of, read FIRST.
	// A trashed element names its blob in content.attachmentId and that link dies
	// with the row, so it has to be taken before the delete rather than looked
	// for afterwards.
	//
	// This gesture never went near the sweeper: it hard-deletes on the spot, so
	// the purged rows never appear in ExpiringSoon and the only garbage collector
	// in the product never learned they existed. Every "Delete forever" left the
	// actual bytes in the bucket, still fetchable, and the journal's verbatim
	// copy of the content readable through board history.
	doomed := []*domain.Element{el}
	ids := []string{id}
	if el.Type.IsContainer() {
		descendants, err := s.elements.Descendants(ctx, id, true)
		if err != nil {
			return err
		}
		for _, d := range descendants {
			ids = append(ids, d.ID)
			doomed = append(doomed, d)
		}
	}
	attachments := AttachmentRefsOf(doomed)
	if err := s.elements.HardDelete(ctx, ids); err != nil {
		return err
	}
	if s.collector != nil {
		s.collector.Collect(ctx, ids, attachments)
	}
	return nil
}

// EmptyTrash permanently deletes everything in the caller's trash.
func (s *ElementService) EmptyTrash(ctx context.Context, p *domain.Principal) (int, error) {
	items, err := s.elements.Trashed(ctx, p.Sub)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, el := range items {
		if err := s.DeletePermanently(ctx, p, el.ID); err == nil {
			count++
		}
	}
	return count, nil
}

func cloneContent(c domain.Content) domain.Content {
	out := make(domain.Content, len(c))
	for k, v := range c {
		out[k] = v // payload values are treated as immutable snapshots
	}
	return out
}
