package service

import (
	"context"
	"errors"
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
	notifier     *Notifier // optional: task-assignment notifications
	newID        IDGenerator
	log          *zap.Logger
}

// AttachNotifier enables assignment notifications on the write path. Optional
// so tests and minimal wiring can skip it.
func (s *TransactionService) AttachNotifier(n *Notifier) { s.notifier = n }

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

// TxnMeta carries optional provenance for a transaction. The zero value is a
// plain human edit, which is why every existing caller is unaffected.
type TxnMeta struct {
	// TxnID pins the transaction's id instead of generating one. The agent
	// derives it deterministically from its run so a journal entry is traceable
	// back to the exact run step that produced it.
	TxnID string
	// Origin is domain.OriginHuman or domain.OriginAgent.
	Origin string
	// AgentRunID links the transaction to an agent run, enabling run-level revert.
	AgentRunID string
}

// Apply validates, persists, and broadcasts one transaction. A multi-select
// drag of N cards is one call with N ops.
func (s *TransactionService) Apply(ctx context.Context, p *domain.Principal, boardID, clientID string, ops []domain.Op) (*domain.Transaction, error) {
	return s.ApplyWithMeta(ctx, p, boardID, clientID, ops, TxnMeta{})
}

// ApplyWithMeta is Apply plus provenance. The AI agent uses it so its writes
// are attributable and revertible; everything else calls Apply.
//
// There is deliberately no separate agent write path: the agent's ops traverse
// the same pre-validation, the same IDOR guard, the same broadcast, and produce
// the same precomputed inverses that power Ctrl+Z. What the agent gets EXTRA is
// a Delegation on its principal, which can only ever subtract authority.
func (s *TransactionService) ApplyWithMeta(ctx context.Context, p *domain.Principal, boardID, clientID string, ops []domain.Op, meta TxnMeta) (*domain.Transaction, error) {
	if len(ops) == 0 || boardID == "" {
		return nil, domain.ErrValidation
	}
	if _, err := s.access.RequireEdit(ctx, boardID, p); err != nil {
		return nil, err
	}

	// A delegated (agent) principal is bounded before anything else is checked:
	// wrong board, expired grant, or an oversized batch fails the whole call.
	if d := p.Delegation; d != nil {
		if d.Expired(time.Now().UTC()) {
			return nil, domain.ErrForbidden
		}
		if d.RootBoardID == "" || d.OnBehalfOf != p.Sub {
			return nil, domain.ErrForbidden
		}
		if d.MaxOps > 0 && len(ops) > d.MaxOps {
			s.log.Warn("agent transaction exceeds delegated op budget",
				zap.String("run", d.RunID), zap.Int("ops", len(ops)), zap.Int("max", d.MaxOps))
			return nil, domain.ErrForbidden
		}
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
		if err := s.verifyDelegation(ctx, p, &ops[i], boardCache); err != nil {
			s.log.Warn("op rejected by delegation",
				zap.String("run", p.Delegation.RunID),
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

	txnID := meta.TxnID
	if txnID == "" {
		txnID = s.newID()
	}
	origin := meta.Origin
	if origin == "" {
		origin = domain.OriginHuman
	}
	txn := &domain.Transaction{
		ID:         txnID,
		BoardID:    boardID,
		UserID:     p.Sub,
		ClientID:   clientID,
		Ops:        ops,
		CreatedAt:  now,
		Origin:     origin,
		AgentRunID: meta.AgentRunID,
	}
	if err := s.transactions.Insert(ctx, txn); err != nil {
		return nil, err
	}
	if s.broadcaster != nil {
		s.broadcaster.BroadcastTransaction(boardID, txn)
	}
	s.fanOutCloneUpdates(ctx, boardID, txn)
	s.notifyAssignments(ctx, p, boardID, ops)
	return txn, nil
}

// notifyAssignments tells users when a task gets assigned to them (§4.11).
// Assignment arrives as content.assigneeId inside create/update ops.
func (s *TransactionService) notifyAssignments(ctx context.Context, p *domain.Principal, boardID string, ops []domain.Op) {
	if s.notifier == nil {
		return
	}
	for i := range ops {
		op := &ops[i]
		if op.Action != domain.ActionCreate && op.Action != domain.ActionUpdate {
			continue
		}
		content, ok := op.Changes["content"].(map[string]any)
		if !ok {
			continue
		}
		assignee, _ := content["assigneeId"].(string)
		if assignee == "" || assignee == p.Sub {
			continue // unassigned, cleared, or self-assignment
		}
		text := ""
		if el, err := s.elements.Get(ctx, op.ElementID); err == nil {
			if el.Type != domain.TypeTask {
				continue
			}
			text, _ = el.Content["text"].(string)
		}
		s.notifier.Notify(ctx, &domain.Notification{
			ID: s.newID(), UserID: assignee, Kind: domain.NotifyAssignment,
			ActorID: p.Sub, BoardID: boardID, ElementID: op.ElementID,
			Message:   p.Name + " assigned you a task: \"" + text + "\"",
			CreatedAt: time.Now().UTC(),
		})
	}
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
			// Synced notes (§4.15): editing a CLONE edits its source CARD,
			// which lives on ANOTHER board. Allow the update when one of the
			// source's clone instances sits inside the declared board.
			if op.Action == domain.ActionUpdate {
				if ok, cerr := s.cloneInstanceWithin(ctx, op.ElementID, boardID, cache); cerr == nil && ok {
					return nil
				}
			}
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
// declared board (no reparenting into a foreign board via a move op) and that
// the move does not create a containment cycle.
func (s *TransactionService) verifyMoveTarget(ctx context.Context, op *domain.Op, boardID string, cache map[string]bool) error {
	loc, ok := op.Changes["location"].(map[string]any)
	if !ok {
		return nil
	}
	newParent, ok := loc["parentId"].(string)
	if !ok || newParent == "" {
		return nil
	}
	if err := s.assertWithin(ctx, newParent, boardID, cache); err != nil {
		return err
	}
	// Reparenting an element INTO ITS OWN SUBTREE detaches that subtree from the
	// board entirely — it becomes unreachable from Home while still existing.
	// Only the depth cap on the ancestor walk kept this from looping forever.
	if op.Action == domain.ActionMove || op.Action == domain.ActionUpdate {
		return s.assertNoCycle(ctx, op.ElementID, newParent)
	}
	return nil
}

// assertNoCycle errors when newParent is elementID itself or one of its
// descendants — i.e. when the move would make the element its own ancestor.
func (s *TransactionService) assertNoCycle(ctx context.Context, elementID, newParent string) error {
	if elementID == "" || newParent == "" {
		return nil
	}
	id := newParent
	for depth := 0; id != "" && depth < maxDepth; depth++ {
		if id == elementID {
			return domain.ErrValidation
		}
		el, err := s.elements.Get(ctx, id)
		if err != nil {
			// A parent that does not exist yet is a same-transaction create;
			// verifyOpScope already proved it is in-board.
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}
		id = el.Location.ParentID
	}
	return nil
}

// verifyDelegation applies the EXTRA restrictions an agent grant imposes. It is
// a no-op for human principals, so nothing about the existing write path
// changes for them.
//
// Two independent gates must both pass for an agent write: the human's ACL role
// (checked by AccessResolver above) and this grant. The grant can only ever
// subtract — it never widens what the human could already do.
func (s *TransactionService) verifyDelegation(ctx context.Context, p *domain.Principal, op *domain.Op, cache map[string]bool) error {
	d := p.Delegation
	if d == nil {
		return nil
	}
	// Capability: absent capability is denied. There is no implicit grant.
	if !d.Allows(domain.CapabilityForAction(op.Action)) {
		return domain.ErrForbidden
	}
	// Consequence ceiling: a REVERSIBLE_WRITE grant cannot delete.
	if !d.Consequence.AtLeast(domain.ConsequenceForAction(op.Action)) {
		return domain.ErrForbidden
	}
	// Containment: every op must land inside the grant's root board, which was
	// computed server-side at admission and never came from the model.
	target := op.ElementID
	if op.Action == domain.ActionCreate {
		target = createParentID(op)
	}
	// Containment is now "inside the root, OR inside a destination an approved
	// plan named". Destinations are validated against the human's own edit
	// rights before the grant is minted, so this can never reach further than
	// the person the agent acts for.
	if err := s.assertWithinAny(ctx, target, d.Roots(), cache); err != nil {
		return err
	}
	if err := s.verifyMoveTargetAny(ctx, op, d.Roots(), cache); err != nil {
		return err
	}
	// Structural denials the agent may never reach, whatever it proposes.
	if el, err := s.elements.Get(ctx, op.ElementID); err == nil {
		if isHome(el) {
			return domain.ErrHomeBoard
		}
	}
	if _, touchesACL := op.Changes["acl"]; touchesACL {
		return domain.ErrForbidden
	}
	if content, ok := op.Changes["content"].(map[string]any); ok {
		// isHome / isTemplate are privileged flags that live in freely-patchable
		// content. An agent must never be able to set them.
		for _, k := range []string{"isHome", "isTemplate"} {
			if _, present := content[k]; present {
				return domain.ErrForbidden
			}
		}
	}
	return nil
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

// cloneInstanceWithin reports whether any CLONE instance of sourceID lives
// inside boardID (the cross-board synced-note edit path).
func (s *TransactionService) cloneInstanceWithin(ctx context.Context, sourceID, boardID string, cache map[string]bool) (bool, error) {
	instances, err := s.elements.CloneInstances(ctx, sourceID)
	if err != nil {
		return false, err
	}
	for _, inst := range instances {
		if ok, err := s.withinBoard(ctx, inst.ID, boardID, cache); err == nil && ok {
			return true, nil
		}
	}
	return false, nil
}

// fanOutCloneUpdates re-broadcasts a committed transaction to every OTHER
// board holding a clone instance of an updated card, so synced notes update
// live everywhere (§4.15) — not just in the room the edit happened in.
func (s *TransactionService) fanOutCloneUpdates(ctx context.Context, boardID string, txn *domain.Transaction) {
	if s.broadcaster == nil {
		return
	}
	seen := map[string]bool{boardID: true}
	for _, op := range txn.Ops {
		if op.Action != domain.ActionUpdate {
			continue
		}
		instances, err := s.elements.CloneInstances(ctx, op.ElementID)
		if err != nil {
			continue
		}
		for _, inst := range instances {
			b, err := s.nearestBoard(ctx, inst.ID)
			if err != nil || b == "" || seen[b] {
				continue
			}
			seen[b] = true
			s.broadcaster.BroadcastTransaction(b, txn)
		}
	}
}

// nearestBoard walks an element's containment chain to its owning board.
func (s *TransactionService) nearestBoard(ctx context.Context, elementID string) (string, error) {
	id := elementID
	for depth := 0; id != "" && depth < 64; depth++ {
		el, err := s.elements.Get(ctx, id)
		if err != nil {
			return "", err
		}
		if el.Type == domain.TypeBoard {
			return el.ID, nil
		}
		id = el.Location.ParentID
	}
	return "", nil
}

// MoveAcrossBoards reparents elements into another board's Unsorted tray —
// the drag-onto-breadcrumb / drag-onto-board-tile gesture (§5). The op is
// validated against BOTH sides (edit on every element and on the target),
// recorded as a transaction on the target board, and broadcast to every
// affected room.
func (s *TransactionService) MoveAcrossBoards(ctx context.Context, p *domain.Principal, ids []string, targetBoardID string) error {
	if len(ids) == 0 || targetBoardID == "" {
		return domain.ErrValidation
	}
	target, err := s.elements.Get(ctx, targetBoardID)
	if err != nil {
		return err
	}
	if target.Type != domain.TypeBoard {
		return domain.ErrValidation
	}
	if _, err := s.access.RequireEdit(ctx, targetBoardID, p); err != nil {
		return err
	}

	now := time.Now().UTC()
	sourceBoards := map[string]bool{}
	ops := make([]domain.Op, 0, len(ids))
	for _, id := range ids {
		el, err := s.elements.Get(ctx, id)
		if err != nil {
			return err
		}
		if el.Type == domain.TypeLine || isHome(el) || el.ID == targetBoardID {
			continue
		}
		if _, err := s.access.RequireEdit(ctx, id, p); err != nil {
			return err
		}
		if src, err := s.nearestBoard(ctx, el.Location.ParentID); err == nil && src != "" {
			sourceBoards[src] = true
		}
		ops = append(ops, domain.Op{
			ElementID: id,
			Action:    domain.ActionMove,
			Changes: domain.Content{"location": map[string]any{
				"parentId": targetBoardID,
				"section":  string(domain.SectionUnsorted),
				"index":    float64(now.UnixMilli()) / 1000,
			}},
			UndoChanges: domain.Content{"location": map[string]any{
				"parentId": el.Location.ParentID,
				"section":  string(el.Location.Section),
				"index":    el.Location.Index,
			}},
		})
	}
	if len(ops) == 0 {
		return domain.ErrValidation
	}
	for i := range ops {
		if _, err := s.elements.MergePatch(ctx, ops[i].ElementID, ops[i].Changes); err != nil {
			return err
		}
	}
	txn := &domain.Transaction{
		ID: s.newID(), BoardID: targetBoardID, UserID: p.Sub, Ops: ops, CreatedAt: now,
	}
	if err := s.transactions.Insert(ctx, txn); err != nil {
		return err
	}
	if s.broadcaster != nil {
		s.broadcaster.BroadcastTransaction(targetBoardID, txn)
		for b := range sourceBoards {
			if b != targetBoardID {
				s.broadcaster.BroadcastTransaction(b, txn)
			}
		}
	}
	return nil
}

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

// assertWithinAny passes when the element sits inside ANY permitted root. The
// error surfaced is the primary root's, so a denial reads as an ordinary
// containment failure rather than naming boards the caller never asked about.
func (s *TransactionService) assertWithinAny(ctx context.Context, id string, roots []string, cache map[string]bool) error {
	if len(roots) == 0 {
		return domain.ErrForbidden
	}
	var first error
	for i, root := range roots {
		err := s.assertWithin(ctx, id, root, cache)
		if err == nil {
			return nil
		}
		if i == 0 {
			first = err
		}
	}
	return first
}

// verifyMoveTargetAny is the same widening for a move's destination parent.
func (s *TransactionService) verifyMoveTargetAny(ctx context.Context, op *domain.Op, roots []string, cache map[string]bool) error {
	if len(roots) == 0 {
		return domain.ErrForbidden
	}
	var first error
	for i, root := range roots {
		err := s.verifyMoveTarget(ctx, op, root, cache)
		if err == nil {
			return nil
		}
		if i == 0 {
			first = err
		}
	}
	return first
}
