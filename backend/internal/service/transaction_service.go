package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// TransactionService is THE write path: every mutation of the element graph
// arrives as a transaction of ops carrying forward changes plus precomputed
// inverse undoChanges (§9.5). One pattern powers optimistic local apply,
// undo/redo, the audit trail, and the realtime broadcast.
type TransactionService struct {
	elements     domain.ElementRepository
	transactions domain.TransactionRepository
	access       *AccessResolver
	broadcaster  domain.TransactionBroadcaster
	newID        IDGenerator
	log          *zap.Logger
}

// NewTransactionService constructs the service.
func NewTransactionService(
	elements domain.ElementRepository,
	transactions domain.TransactionRepository,
	access *AccessResolver,
	broadcaster domain.TransactionBroadcaster,
	newID IDGenerator,
	log *zap.Logger,
) *TransactionService {
	return &TransactionService{
		elements: elements, transactions: transactions, access: access,
		broadcaster: broadcaster, newID: newID, log: log.Named("txn"),
	}
}

// Apply validates, persists, and broadcasts one transaction. A multi-select
// drag of N cards is one call with N ops.
func (s *TransactionService) Apply(ctx context.Context, p *domain.Principal, boardID, clientID string, ops []domain.Op) (*domain.Transaction, error) {
	if len(ops) == 0 || boardID == "" {
		return nil, domain.ErrValidation
	}
	if _, err := s.access.RequireEdit(ctx, boardID, p); err != nil {
		return nil, err
	}

	// Pre-validate EVERY op before mutating anything. This is both an IDOR
	// guard (an op may not target an element outside the declared board's
	// subtree — else any editor of any board could mutate any element by
	// lying about boardID) and a partial-write guard (a transaction is
	// all-or-nothing in intent, so we never apply op 1 then fail on op 2).
	boardCache := map[string]bool{} // memoized "elementID|boardID" → within?
	for i := range ops {
		if err := s.verifyOpScope(ctx, &ops[i], boardID, boardCache); err != nil {
			s.log.Warn("op rejected in pre-validation",
				zap.String("action", string(ops[i].Action)),
				zap.String("element", ops[i].ElementID),
				zap.Error(err))
			return nil, err
		}
	}

	now := time.Now().UTC()
	for i := range ops {
		if err := s.applyOp(ctx, p, &ops[i], now); err != nil {
			s.log.Warn("op failed",
				zap.String("action", string(ops[i].Action)),
				zap.String("element", ops[i].ElementID),
				zap.Error(err))
			return nil, err
		}
	}

	txn := &domain.Transaction{
		ID:        s.newID(),
		BoardID:   boardID,
		UserID:    p.Sub,
		ClientID:  clientID,
		Ops:       ops,
		CreatedAt: now,
	}
	if err := s.transactions.Insert(ctx, txn); err != nil {
		return nil, err
	}
	if s.broadcaster != nil {
		s.broadcaster.BroadcastTransaction(boardID, txn)
	}
	return txn, nil
}

// verifyOpScope confirms an op targets the declared board's subtree. "Within"
// means boardID is the element itself or one of its ancestor containers — so
// a card, a column, and a nested sub-board all count as inside their board.
func (s *TransactionService) verifyOpScope(ctx context.Context, op *domain.Op, boardID string, cache map[string]bool) error {
	switch op.Action {
	case domain.ActionCreate:
		// The new element's parent must live in this board (or BE this board).
		parentID := createParentID(op)
		if parentID == "" {
			return domain.ErrValidation
		}
		if err := s.assertWithin(ctx, parentID, boardID, cache); err != nil {
			return err
		}
		// Seed the cache so later ops in the same transaction referencing
		// this freshly-created element (client-generated id) pass scope.
		if op.ElementID != "" {
			cache[key(op.ElementID, boardID)] = true
		}
		return s.verifyMoveTarget(ctx, op, boardID, cache)

	case domain.ActionUpdate, domain.ActionMove:
		if len(op.Changes) == 0 {
			return domain.ErrValidation
		}
		if err := s.assertWithin(ctx, op.ElementID, boardID, cache); err != nil {
			return err
		}
		return s.verifyMoveTarget(ctx, op, boardID, cache)

	case domain.ActionDelete, domain.ActionRestore:
		return s.assertWithin(ctx, op.ElementID, boardID, cache)

	default:
		return domain.ErrValidation
	}
}

// verifyMoveTarget ensures a new location.parentId still sits inside the
// declared board (no reparenting into a foreign board via a move op).
func (s *TransactionService) verifyMoveTarget(ctx context.Context, op *domain.Op, boardID string, cache map[string]bool) error {
	loc, ok := op.Changes["location"].(map[string]any)
	if !ok {
		return nil
	}
	newParent, ok := loc["parentId"].(string)
	if !ok || newParent == "" {
		return nil
	}
	return s.assertWithin(ctx, newParent, boardID, cache)
}

// assertWithin errors unless boardID is an ancestor-or-self of elementID.
func (s *TransactionService) assertWithin(ctx context.Context, elementID, boardID string, cache map[string]bool) error {
	if elementID == "" {
		return domain.ErrValidation
	}
	ok, err := s.withinBoard(ctx, elementID, boardID, cache)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrForbidden
	}
	return nil
}

// withinBoard walks an element's containment chain upward; it is "within"
// boardID if the walk passes through boardID (including the element itself).
func (s *TransactionService) withinBoard(ctx context.Context, elementID, boardID string, cache map[string]bool) (bool, error) {
	if v, ok := cache[key(elementID, boardID)]; ok {
		return v, nil
	}
	id := elementID
	for depth := 0; id != "" && depth < 64; depth++ {
		if id == boardID {
			cache[key(elementID, boardID)] = true
			return true, nil
		}
		el, err := s.elements.Get(ctx, id)
		if err != nil {
			return false, err
		}
		id = el.Location.ParentID
	}
	cache[key(elementID, boardID)] = false
	return false, nil
}

func key(elementID, boardID string) string { return elementID + "|" + boardID }

// createParentID pulls the parent id out of a create op's changes payload.
func createParentID(op *domain.Op) string {
	loc, ok := op.Changes["location"].(map[string]any)
	if !ok {
		return ""
	}
	pid, _ := loc["parentId"].(string)
	return pid
}

func (s *TransactionService) applyOp(ctx context.Context, p *domain.Principal, op *domain.Op, now time.Time) error {
	switch op.Action {
	case domain.ActionCreate:
		return s.applyCreate(ctx, p, op, now)

	case domain.ActionUpdate, domain.ActionMove:
		if len(op.Changes) == 0 {
			return domain.ErrValidation
		}
		_, err := s.elements.MergePatch(ctx, op.ElementID, op.Changes)
		return err

	case domain.ActionDelete:
		el, err := s.elements.Get(ctx, op.ElementID)
		if err != nil {
			return err
		}
		if isHome(el) {
			return domain.ErrHomeBoard
		}
		// Cascade: trashing a container trashes its live subtree under one
		// batch id, so children don't leak into search and the whole delete
		// restores as a unit (§3.4).
		ids := []string{el.ID}
		if el.Type.IsContainer() {
			descendants, derr := s.elements.Descendants(ctx, el.ID, false)
			if derr != nil {
				return derr
			}
			for _, d := range descendants {
				ids = append(ids, d.ID)
			}
		}
		return s.elements.SoftDelete(ctx, ids, p.Sub, el.ID, now)

	case domain.ActionRestore:
		// Restore the whole batch this element was trashed in.
		el, err := s.elements.Get(ctx, op.ElementID)
		if err != nil {
			return err
		}
		if el.TrashBatchID != "" {
			return s.elements.RestoreBatch(ctx, el.TrashBatchID)
		}
		return s.elements.Restore(ctx, []string{op.ElementID})

	default:
		return domain.ErrValidation
	}
}

// applyCreate builds an element from the op's changes payload. Clients
// pre-generate 24-hex ids so creation is optimistic (§9.4/§9.5); the server
// validates shape and uniqueness.
func (s *TransactionService) applyCreate(ctx context.Context, p *domain.Principal, op *domain.Op, now time.Time) error {
	id := op.ElementID
	if id == "" {
		id = s.newID()
		op.ElementID = id
	}
	if !domain.ObjectIDPattern.MatchString(id) {
		return domain.ErrValidation
	}

	typRaw, _ := op.Changes["type"].(string)
	typ := domain.ElementType(typRaw)
	if !typ.Valid() || typ == domain.TypeSkeleton {
		return domain.ErrValidation
	}

	el := &domain.Element{
		ID:        id,
		Type:      typ,
		Location:  decodeLocation(op.Changes["location"]),
		Content:   decodeContent(op.Changes["content"]),
		CreatedBy: p.Sub,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if typ == domain.TypeBoard {
		el.ACL = &domain.ACL{OwnerID: p.Sub, Editors: []string{}}
	}
	return s.elements.Insert(ctx, el)
}

// History pages a board's transaction log (audit view).
func (s *TransactionService) History(ctx context.Context, p *domain.Principal, boardID string, limit int) ([]*domain.Transaction, error) {
	if _, _, err := s.access.RequireView(ctx, boardID, p); err != nil {
		return nil, err
	}
	return s.transactions.ListByBoard(ctx, boardID, limit)
}

// ---- payload decoding helpers ----

func decodeContent(v any) domain.Content {
	if m, ok := v.(map[string]any); ok {
		return domain.Content(m)
	}
	return domain.Content{}
}

func decodeLocation(v any) domain.Location {
	loc := domain.Location{Section: domain.SectionCanvas}
	m, ok := v.(map[string]any)
	if !ok {
		return loc
	}
	if s, ok := m["parentId"].(string); ok {
		loc.ParentID = s
	}
	if s, ok := m["section"].(string); ok && s != "" {
		loc.Section = domain.Section(s)
	}
	if pos, ok := m["position"].(map[string]any); ok {
		loc.Position.X = toFloat(pos["x"])
		loc.Position.Y = toFloat(pos["y"])
	}
	loc.Index = toFloat(m["index"])
	loc.Width = toFloat(m["width"])
	loc.Height = toFloat(m["height"])
	return loc
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func isHome(el *domain.Element) bool {
	home, _ := el.Content["isHome"].(bool)
	return el.Type == domain.TypeBoard && home
}
