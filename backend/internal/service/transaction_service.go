package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	notifier     *Notifier              // optional: task-assignment notifications
	labels       domain.LabelRepository // optional: label ownership on create
	newID        IDGenerator
	bus          *CommitBus
	log          *zap.Logger
}

// Subscribe registers a reaction to every committed transaction.
//
// The hook A2 ("when something lands in Unsorted, file it"), A3 ("tell me when
// the budget column changes") and the search index refresh all need did not
// exist: the only consumer of the transaction stream was the WebSocket hub, in
// process and fire-and-forget, and each new reaction would have been another
// bespoke line at the bottom of ApplyWithMeta.
func (s *TransactionService) Subscribe(name string, sub domain.TransactionSubscriber) {
	s.bus.Subscribe(name, sub)
}

// AttachNotifier enables assignment notifications on the write path. Optional
// so tests and minimal wiring can skip it.
func (s *TransactionService) AttachNotifier(n *Notifier) { s.notifier = n }

// AttachLabels lets a create op carry labelIds. Optional like the notifier: a
// deployment without it refuses labelled creates rather than silently dropping
// the labels, which is the same choice made about acl below and for the same
// reason.
func (s *TransactionService) AttachLabels(l domain.LabelRepository) { s.labels = l }

// NewTransactionService constructs the service.
func NewTransactionService(
	elements domain.ElementRepository,
	transactions domain.TransactionRepository,
	access *AccessResolver,
	broadcaster domain.TransactionBroadcaster,
	newID IDGenerator,
	log *zap.Logger,
) *TransactionService {
	s := &TransactionService{
		elements: elements, transactions: transactions, access: access,
		broadcaster: broadcaster, newID: newID,
		bus: NewCommitBus(log), log: log.Named("txn"),
	}
	// The built-in reactions go on the bus like everything that comes later,
	// rather than staying as privileged lines inside the commit. Registered here
	// so a service is never constructed with a bus nobody uses — a seam with no
	// traffic through it is a seam nobody trusts.
	s.bus.Subscribe("rooms", subscriberFunc(s.broadcastRooms))
	s.bus.Subscribe("synced-notes", subscriberFunc(s.fanOutCloneUpdates))
	return s
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
	// Source overrides the principal's transport for writes whose real origin
	// is not the request that triggered them — a scheduled rule firing is not
	// the web request that installed the rule.
	Source string
	// ApprovedByHuman says a person looked at this plan and pressed Apply.
	//
	// It exists so the notifications an agent write rings can tell the truth
	// about WHOSE decision they carry: "Omar approved the plan" is a checkable
	// claim about a human, and an unattended run has no such claim to borrow.
	ApprovedByHuman bool
	// CrossBoard names boards this transaction touches BESIDES the one it
	// declares — the source boards of a drag onto another board's tile.
	//
	// Declared, never trusted: ApplyWithMeta proves edit rights on each before
	// widening anything, so a caller cannot buy reach by asking for it. This
	// exists so the one gesture that legitimately spans boards can go through
	// the ordinary write path instead of around it.
	CrossBoard []string
}

// TransactionReserver is the half of the journal that can claim an id before
// the work starts and settle it afterwards.
//
// Optional and discovered by assertion, like every other capability the storage
// layer grew after its port was written: a deployment that has not wired it
// still commits, it just falls back to the narrower duplicate check.
type TransactionReserver interface {
	// Complete settles a reserved row: its ops, the notifications it rang, and
	// the state that turns a claim into a commit.
	Complete(ctx context.Context, txn *domain.Transaction) error
	// Abandon removes a reservation whose work did not stand, so a retry is
	// genuinely a retry rather than a lookup of a commit that never happened.
	Abandon(ctx context.Context, id string) error
}

// maxOpsPerTransaction bounds every principal, not just the delegated ones.
//
// The agent has had a budget since delegation existed (MaxActions*4 = 240); the
// human path's only limit was the router's body size, so a client bug or a
// scripted caller could hand this method an unbounded batch and every op costs
// containment walks before a byte is written. 500 is comfortably above any real
// gesture — the largest observed is a multi-select drag of a few hundred — and
// far below the point where one request monopolises the database.
const maxOpsPerTransaction = 500

// maxTransactionBytes is the size a batch may serialize to.
//
// Mongo's hard per-document limit is 16 MB and the journal row carries every
// op's Changes AND UndoChanges — a note save is the new document and the old
// document, so the row is roughly twice the content it describes. The router
// accepted 64 MB, which meant a batch could be applied IN FULL and then fail to
// journal: no inverse, no broadcast, collaborators silently diverged, and it was
// reachable with a few large sketches in one multi-select drag rather than by
// any adversary. Refusing before the first write, and naming the size, is
// categorically better than a partial commit nobody can undo.
const maxTransactionBytes = 8 << 20

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
	if len(ops) > maxOpsPerTransaction {
		return nil, fmt.Errorf("%w: that is %d changes at once and the limit is %d — split it up",
			domain.ErrValidation, len(ops), maxOpsPerTransaction)
	}
	if size := transactionSize(ops); size > maxTransactionBytes {
		return nil, fmt.Errorf("%w: those changes are %d MB and the limit is %d MB — the largest items need to be saved separately",
			domain.ErrValidation, size>>20, maxTransactionBytes>>20)
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
	// The IDOR boundary is the set of roots this principal is authorised for,
	// not one board id.
	//
	// For a human that set is exactly {boardID} and nothing changes. For a
	// DELEGATED principal it is the run's root plus the destinations an
	// approved plan named — and this is where cross-board filing died. The
	// delegation gate below already widened to Roots(), but it never got the
	// chance: this loop ran first against the single declared board and
	// returned forbidden, so filing.go, DestinationBoardIDs,
	// authorizeDestinations, the discovered set and the whole file_to verb
	// 403'd on every case they exist for. Two gates designed as AND, where the
	// first predates delegation and hardcoded the run's root as the boundary,
	// makes the second gate's widening unreachable.
	//
	// Widening here is not a loosening: destinations are validated against the
	// human's own edit rights before the grant is minted, so this can never
	// reach further than the person the agent acts for, and the delegation gate
	// still runs independently afterwards.
	roots := []string{boardID}
	if d := p.Delegation; d != nil {
		roots = d.Roots()
	}
	// Extra boards a caller declared. Each is checked here rather than taken on
	// the caller's word, so the widening cannot outrun the person's own rights.
	for _, extra := range meta.CrossBoard {
		if extra == "" || extra == boardID {
			continue
		}
		if _, err := s.access.RequireEdit(ctx, extra, p); err != nil {
			return nil, err
		}
		roots = append(roots, extra)
	}

	// ONE containment resolve for the whole transaction, before any of the four
	// passes that ask about it. See ancestry for what the four were costing.
	graph := newAncestry(s.elements)
	graph.preload(ctx, ops)

	for i := range ops {
		if err := s.verifyOpScope(ctx, p, &ops[i], roots, graph); err != nil {
			s.log.Warn("op rejected in pre-validation",
				zap.String("action", string(ops[i].Action)),
				zap.String("element", ops[i].ElementID),
				zap.Error(err))
			return nil, err
		}
		if err := s.verifyStandingRules(ctx, p, &ops[i]); err != nil {
			s.log.Warn("standing-rule write rejected: not the board's owner",
				zap.String("element", ops[i].ElementID), zap.String("sub", p.Sub))
			return nil, err
		}
		if err := s.verifyDelegation(ctx, p, &ops[i], graph); err != nil {
			s.log.Warn("op rejected by delegation",
				zap.String("run", p.Delegation.RunID),
				zap.String("action", string(ops[i].Action)),
				zap.String("element", ops[i].ElementID),
				zap.Error(err))
			return nil, err
		}
	}

	now := time.Now().UTC()

	// CLAIM THE ID BEFORE DOING THE WORK.
	//
	// The journal row is the commit record, and it was written LAST — after
	// every op had already landed. So a retried commit re-ran the whole batch
	// and only then discovered the duplicate id: creates failed mid-loop on an
	// element duplicate-key, leaving a partial write with no journal row and no
	// inverse, while moves and updates were re-applied idempotently by luck of
	// MergePatch. The run state machine was the only thing preventing it, which
	// made durability a property of an in-process goroutine — and a scheduled
	// run has no human to not-double-click.
	//
	// Claiming first inverts that: a retry observes the FIRST commit instead of
	// repeating it. The id is only pinned when a caller supplies one (the agent
	// derives it from its run precisely so a retry is recognisable); a
	// generated id cannot collide, so ordinary human writes are unaffected and
	// keep their single journal write at the end.
	//
	// The claim is a real INSERT, not a lookup. A lookup leaves the window it is
	// trying to close: two retries in flight at once both read "absent" and both
	// apply. Inserting an empty row in an `applying` state makes the database the
	// arbiter — the second one loses on the unique _id and can then say, truly,
	// that this commit is already happening.
	reserved := false
	if meta.TxnID != "" {
		existing, err := s.reserve(ctx, p, boardID, clientID, meta, now)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
		reserved = true
	}

	// Where the things about to move currently live, read BEFORE anything is
	// patched. A move is a blind MergePatch, so once it lands there is no way to
	// find the canvas the element came from — and that canvas is the only place
	// its connectors can be.
	moved := s.captureMoves(ctx, ops, graph)

	// The pre-validation above is not a partial-write guard, whatever the
	// comment on it used to claim. It covers scope and delegation; it cannot
	// pre-empt a duplicate key, a validation failure inside applyCreate, or the
	// database going away between op 1 and op 2. When that happened the elements
	// existed and NOTHING recorded them: no journal row, so no inverse; no
	// broadcast, so the originating client's undo stack never saw the ops; and
	// the run's outcome card told the person "the changes were rejected", which
	// is the exact opposite of what had happened to their board.
	//
	// So a failed batch compensates itself: everything already applied in THIS
	// call is inverted before the error goes back. What compensation cannot undo
	// is journalled anyway — a write that is real but not journalled is a write
	// undo cannot see, and the whole point of the failure path is to leave the
	// person an exit.
	applied := make([]domain.Op, 0, len(ops))
	for i := range ops {
		if err := s.applyOp(ctx, p, &ops[i], now); err != nil {
			s.log.Warn("op failed",
				zap.String("action", string(ops[i].Action)),
				zap.String("element", ops[i].ElementID),
				zap.Error(err))
			standing := s.compensate(ctx, p, applied, now)
			if len(standing) == 0 {
				// Nothing survived, so nothing happened — and a claim left behind
				// would make the next attempt look up a commit that never was.
				s.abandon(ctx, meta.TxnID)
				return nil, err
			}
			partial, jerr := s.journal(ctx, p, boardID, clientID, standing, meta, now, moved, reserved)
			if jerr != nil {
				s.log.Error("changes are standing on the board with no way to undo them",
					zap.String("board", boardID), zap.Int("ops", len(standing)), zap.Error(jerr))
				return nil, err
			}
			return partial, err
		}
		applied = append(applied, ops[i])
	}
	// After EVERY op, not after each one: a transaction that moves both ends of a
	// connection into the same new board must see the finished state, or the
	// first move would judge the pair split and trash a line the second move was
	// about to make valid again.
	//
	// The connector writes come back as OPS and join the transaction rather than
	// happening beside it. Undoing a grouping run used to leave the arrows it had
	// silently retired retired, while the outcome card said the board was back as
	// it was — because InvertOps works from txn.Ops and these were not in it.
	// Everything a commit does to the element graph belongs in the transaction it
	// is committing; their inverses then come free.
	ops = append(ops, s.rehomeConnectors(ctx, p, moved, now)...)

	return s.journal(ctx, p, boardID, clientID, ops, meta, now, moved, reserved)
}

// transactionSize approximates what this batch will occupy in the journal row.
//
// JSON rather than BSON because the service layer does not know about the
// driver, and the two are within a small constant of each other — which is why
// the ceiling is 8 MB against a hard 16 MB limit rather than 15.
func transactionSize(ops []domain.Op) int {
	body, err := json.Marshal(ops)
	if err != nil {
		return 0
	}
	return len(body)
}

// reserve claims a pinned transaction id before any op runs.
//
// Returns the EXISTING transaction when the id is already taken — a retry
// observing the first commit, which is the whole point — or an error when that
// first commit is still in flight, because "this is already being applied" is
// the only true thing to say and repeating the batch is the one thing that must
// not happen.
func (s *TransactionService) reserve(
	ctx context.Context, p *domain.Principal, boardID, clientID string, meta TxnMeta, now time.Time,
) (*domain.Transaction, error) {
	if _, ok := s.transactions.(TransactionReserver); !ok {
		// No settle half wired: fall back to the narrower check rather than
		// leaving a permanently `applying` row behind.
		if existing, err := s.transactions.Get(ctx, meta.TxnID); err == nil && existing != nil {
			return existing, nil
		}
		return nil, nil
	}
	claim := &domain.Transaction{
		ID: meta.TxnID, BoardID: boardID, UserID: p.Sub, ClientID: clientID,
		CreatedAt: now, Origin: originOf(meta), AgentRunID: meta.AgentRunID,
		State: domain.TxnApplying,
	}
	err := s.transactions.Insert(ctx, claim)
	if err == nil {
		return nil, nil
	}
	if !errors.Is(err, domain.ErrConflict) {
		return nil, err
	}
	existing, gerr := s.transactions.Get(ctx, meta.TxnID)
	if gerr != nil || existing == nil {
		return nil, err
	}
	// The pinned id can come from a client (an Idempotency-Key header), so
	// handing back whatever row already has that id would be a way to read
	// somebody else's transaction — ops, content and all — by guessing. A claim
	// only ever resolves to the claimant's own commit on the same board.
	if existing.UserID != p.Sub || existing.BoardID != boardID {
		s.log.Warn("a transaction id was claimed that already belongs to someone else",
			zap.String("txn", meta.TxnID), zap.String("sub", p.Sub))
		return nil, domain.ErrConflict
	}
	if existing.State == domain.TxnApplying {
		s.log.Warn("a second commit of the same transaction arrived while the first was still applying",
			zap.String("txn", meta.TxnID))
		return nil, fmt.Errorf("%w: those changes are already being applied", domain.ErrConflict)
	}
	s.log.Info("commit already recorded; returning the first one", zap.String("txn", meta.TxnID))
	return existing, nil
}

// abandon releases a claim whose work did not stand, so the next attempt is a
// real retry rather than a lookup of a commit that never happened.
func (s *TransactionService) abandon(ctx context.Context, txnID string) {
	reserver, ok := s.transactions.(TransactionReserver)
	if !ok || txnID == "" {
		return
	}
	if err := reserver.Abandon(ctx, txnID); err != nil {
		s.log.Error("a claimed transaction id could not be released; a retry will see a commit that never happened",
			zap.String("txn", txnID), zap.Error(err))
	}
}

// sourceOf resolves the transport a write came in on. An explicit meta value
// wins (a scheduled rule is not the request that installed it); otherwise the
// principal's, stamped at the auth boundary; otherwise web, which is what every
// row written before this field existed actually was.
func sourceOf(p *domain.Principal, meta TxnMeta) string {
	if meta.Source != "" {
		return meta.Source
	}
	if p != nil && p.Source != "" {
		return p.Source
	}
	return domain.SourceWeb
}

func originOf(meta TxnMeta) string {
	if meta.Origin == "" {
		return domain.OriginHuman
	}
	return meta.Origin
}

// journal writes the transaction row and fans it out. Split from ApplyWithMeta
// so the partial-failure path can record what is standing through exactly the
// same code the success path uses.
func (s *TransactionService) journal(
	ctx context.Context, p *domain.Principal, boardID, clientID string,
	ops []domain.Op, meta TxnMeta, now time.Time, moved []movedFrom, reserved bool,
) (*domain.Transaction, error) {
	txnID := meta.TxnID
	if txnID == "" {
		txnID = s.newID()
	}
	txn := &domain.Transaction{
		ID:         txnID,
		BoardID:    boardID,
		UserID:     p.Sub,
		ClientID:   clientID,
		Ops:        ops,
		CreatedAt:  now,
		Origin:     originOf(meta),
		AgentRunID: meta.AgentRunID,
		// Every write records where it came from. Read off the principal rather
		// than passed in, so a caller cannot omit it by forgetting a field.
		RequestID: p.RequestID,
		Source:    sourceOf(p, meta),
	}
	// Notifications are rung BEFORE the row is settled so their ids can go into
	// it. A bell that no transaction names is a bell a revert cannot retract:
	// undo a run that assigned four tasks and four colleagues keep an inbox item
	// for work that no longer exists, under a sentence saying the board is back
	// as it was. The cost of this order is a stray bell if the journal write then
	// fails — which is strictly smaller than an unrecorded one, because that path
	// is already reporting standing changes nobody can take back.
	txn.NotificationIDs = s.notifyAssignments(ctx, p, boardID, ops, meta)
	if err := s.settle(ctx, txn, reserved); err != nil {
		return nil, err
	}
	// Where things LEFT is the one reaction the transaction cannot describe on
	// its own: a move op names its destination and nothing records the canvas it
	// came from, so this stays with the caller that read it before the write.
	s.broadcastToSourceBoards(boardID, moved, txn)
	s.bus.Publish(ctx, txn)
	return txn, nil
}

// broadcastRooms tells every room this transaction touched.
//
// The declared board, plus every OTHER board an op wrote into. Cross-board
// filing was impossible until the IDOR boundary widened to the grant's roots, so
// one room was always the right answer; it no longer is — a card filed from A
// into B produces a journal entry stamped A, and B's room hears nothing, so
// anyone looking at B watches a card appear only if they reload.
func (s *TransactionService) broadcastRooms(ctx context.Context, txn *domain.Transaction) {
	if s.broadcaster == nil {
		return
	}
	s.broadcaster.BroadcastTransaction(txn.BoardID, txn)
	s.fanOutDestinations(ctx, txn.BoardID, txn.Ops, txn)
}

// settle writes the finished transaction — either as a fresh row (a human's
// generated id, which cannot collide and so was never claimed) or by filling in
// the claim this call made before it started working.
func (s *TransactionService) settle(ctx context.Context, txn *domain.Transaction, reserved bool) error {
	reserver, ok := s.transactions.(TransactionReserver)
	if !reserved || !ok {
		return s.transactions.Insert(ctx, txn)
	}
	return reserver.Complete(ctx, txn)
}

// fanOutDestinations re-broadcasts to every OTHER board this transaction wrote
// into, so a room sees work that arrived from elsewhere.
//
// Reads the destination out of the ops rather than out of the delegation: what
// matters is where an element actually landed, and a human MoveAcrossBoards has
// no delegation to read.
func (s *TransactionService) fanOutDestinations(ctx context.Context, boardID string, ops []domain.Op, txn *domain.Transaction) {
	if s.broadcaster == nil {
		return
	}
	seen := map[string]bool{boardID: true}
	for i := range ops {
		parentID := createParentID(&ops[i])
		if parentID == "" {
			if loc, ok := ops[i].Changes["location"].(map[string]any); ok {
				parentID, _ = loc["parentId"].(string)
			}
		}
		if parentID == "" {
			continue
		}
		b, err := s.nearestBoard(ctx, parentID)
		if err != nil || b == "" || seen[b] {
			continue
		}
		seen[b] = true
		s.broadcaster.BroadcastTransaction(b, txn)
	}
}

// compensate replays the inverse of the ops this call already applied, newest
// first, and returns the ones it could NOT take back.
//
// A compensating write rather than a database transaction: this deployment does
// not require a replica set, and needing one to keep a two-op drag consistent
// would be a large operational cost for a small correctness win. What it must
// never do is lie — an op whose inverse also fails is REPORTED, so the caller
// journals it and the person still has a way back.
func (s *TransactionService) compensate(ctx context.Context, p *domain.Principal, applied []domain.Op, now time.Time) []domain.Op {
	var standing []domain.Op
	for i := len(applied) - 1; i >= 0; i-- {
		inverse, ok := inverseOf(applied[i])
		if !ok {
			standing = append(standing, applied[i])
			continue
		}
		if err := s.applyOp(ctx, p, &inverse, now); err != nil {
			s.log.Warn("could not roll back an op after a later one failed",
				zap.String("action", string(applied[i].Action)),
				zap.String("element", applied[i].ElementID),
				zap.Error(err))
			standing = append(standing, applied[i])
		}
	}
	return standing
}

// inverseOf builds the op that undoes one op. Deliberately a copy of the rule
// the agent's InvertOps applies, because the agent package imports this one and
// the dependency cannot run the other way; the two are held together by the fact
// that both are derived from UndoChanges, which every writer has always had to
// compute.
func inverseOf(op domain.Op) (domain.Op, bool) {
	switch op.Action {
	case domain.ActionCreate:
		return domain.Op{ElementID: op.ElementID, Action: domain.ActionDelete}, true
	case domain.ActionDelete:
		return domain.Op{ElementID: op.ElementID, Action: domain.ActionRestore}, true
	case domain.ActionRestore:
		return domain.Op{ElementID: op.ElementID, Action: domain.ActionDelete}, true
	case domain.ActionUpdate, domain.ActionMove:
		if len(op.UndoChanges) == 0 {
			// No precomputed inverse means no honest way back. Saying so is the
			// point: the caller journals it instead of pretending.
			return domain.Op{}, false
		}
		return domain.Op{
			ElementID: op.ElementID, Action: op.Action,
			Changes: op.UndoChanges, UndoChanges: op.Changes,
		}, true
	}
	return domain.Op{}, false
}

// notifyAssignments tells users when a task gets assigned to them (§4.11).
// Assignment arrives as content.assigneeId inside create/update ops.
//
// It RETURNS the ids it rang so the transaction can carry them. Undo was not the
// inverse of commit while these were fire-and-forget: reverting a run took the
// tasks back and left the colleague's bell in place, pointing at an element that
// no longer exists.
func (s *TransactionService) notifyAssignments(ctx context.Context, p *domain.Principal, boardID string, ops []domain.Op, meta TxnMeta) []string {
	if s.notifier == nil {
		return nil
	}
	var rang []string
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
		// The assignee is client-supplied, and the notification carries
		// attacker-influenced text. Without this check, anyone who knows a
		// victim's subject id can push them arbitrary messages by assigning a
		// task on a board the victim cannot even see.
		//
		// Notifying only people who can actually open the board also makes the
		// notification honest: a link to something you cannot read is noise.
		if !s.canView(ctx, boardID, assignee) {
			s.log.Warn("assignment to someone without board access — no notification sent",
				zap.String("board", boardID), zap.String("assignee", assignee))
			continue
		}
		text := ""
		if el, err := s.elements.Get(ctx, op.ElementID); err == nil {
			if el.Type != domain.TypeTask {
				continue
			}
			text, _ = el.Content["text"].(string)
		}
		id := s.newID()
		s.notifier.Notify(ctx, &domain.Notification{
			ID: id, UserID: assignee, Kind: domain.NotifyAssignment,
			ActorID: p.Sub, BoardID: boardID, ElementID: op.ElementID,
			Origin: originOf(meta), AgentRunID: meta.AgentRunID,
			Message:   assignmentMessage(p, meta, text),
			CreatedAt: time.Now().UTC(),
		})
		rang = append(rang, id)
	}
	return rang
}

// assignmentMessage names who really decided this.
//
// "Omar assigned you a task" when Omar never saw the assignment is the most
// corrosive lie an agent can tell in a team: it converts a model's guess into a
// social commitment from a colleague, and colleagues act on those. The write
// path has always known the truth — the origin is two frames up the stack — and
// threw it away before composing the string.
//
// Preview and unattended runs are named apart on purpose. "Omar approved the
// plan" is a real, checkable claim about a human decision; an automatic run has
// no such claim to make and must not borrow one.
func assignmentMessage(p *domain.Principal, meta TxnMeta, text string) string {
	quoted := ": \"" + text + "\""
	if originOf(meta) != domain.OriginAgent {
		return p.Name + " assigned you a task" + quoted
	}
	if meta.ApprovedByHuman {
		return p.Name + "'s assistant assigned you a task — " + p.Name + " approved the plan" + quoted
	}
	return p.Name + "'s assistant assigned you a task in an automatic run" + quoted
}

// RetractNotifications takes back the bells a transaction rang.
//
// Called by the revert path, which until now undid the element graph and left
// the inbox alone — a colleague kept an item for a task that had ceased to
// exist, while the outcome card said the board was back as it was. Best effort
// and silent on failure: an undo that reports failure because a notification
// could not be withdrawn would be the larger lie.
func (s *TransactionService) RetractNotifications(ctx context.Context, txn *domain.Transaction) {
	if s.notifier == nil || txn == nil || len(txn.NotificationIDs) == 0 {
		return
	}
	s.notifier.Retract(ctx, txn.NotificationIDs)
}

// verifyStandingRules keeps a board's agent instructions an OWNER's field.
//
// content.agentInstructions is the board's standing note to the assistant, and
// the digest renders it labelled ⟨user⟩ — which means "the person asking" to
// the model. On a shared board it could mean "a different editor who wrote a
// standing instruction weeks ago that now steers your run". That is the
// multiplayer form of prompt injection, and the trust-label machinery cannot
// express it, because the write path let any editor author it.
//
// The AGENT already cannot write this key: the closed ActionSpec registry
// produces no action that emits it, and the delegation's content allowlist
// refuses what no kind declares. This closes the human half of the same door.
//
// Nothing here fires for an ordinary edit: the check runs only for ops that
// actually carry the key.
func (s *TransactionService) verifyStandingRules(ctx context.Context, p *domain.Principal, op *domain.Op) error {
	content, ok := op.Changes["content"].(map[string]any)
	if !ok {
		return nil
	}
	if _, touches := content["agentInstructions"]; !touches {
		return nil
	}
	role, _, err := s.access.Resolve(ctx, op.ElementID, p)
	if err != nil {
		return err
	}
	if role != RoleOwner {
		return domain.ErrForbidden
	}
	return nil
}

// verifyOpScope confirms an op targets the declared board's subtree. "Within"
// means boardID is the element itself or one of its ancestor containers — so
// a card, a column, and a nested sub-board all count as inside their board.
func (s *TransactionService) verifyOpScope(ctx context.Context, p *domain.Principal, op *domain.Op, roots []string, cache *ancestry) error {
	// roots[0] is the declared board: the one a create seeds its cache against
	// and the one a refusal names. The rest are delegated destinations.
	if len(roots) == 0 {
		return domain.ErrForbidden
	}
	boardID := roots[0]
	switch op.Action {
	case domain.ActionCreate:
		// The new element's parent must live in this board (or BE this board).
		parentID := createParentID(op)
		if parentID == "" {
			return domain.ErrValidation
		}
		if err := s.assertWithinAny(ctx, parentID, roots, cache); err != nil {
			return err
		}
		// Seed the cache so later ops in the same transaction referencing
		// this freshly-created element (client-generated id) pass scope.
		if op.ElementID != "" {
			cache.within[key(op.ElementID, boardID)] = true
		}
		return s.verifyMoveTargetAny(ctx, op, roots, cache)

	case domain.ActionUpdate, domain.ActionMove:
		if len(op.Changes) == 0 {
			return domain.ErrValidation
		}
		if err := s.assertWithinAny(ctx, op.ElementID, roots, cache); err != nil {
			// Synced notes (§4.15): editing a CLONE edits its source CARD,
			// which lives on ANOTHER board. Allow the update when one of the
			// source's clone instances sits inside the declared board.
			if op.Action == domain.ActionUpdate {
				if ok, cerr := s.cloneInstanceWithin(ctx, op.ElementID, boardID, cache); cerr == nil && ok {
					// Holding a clone is not authority over what it points at.
					//
					// The check above asks only "does a clone of this source sit
					// on the declared board" — and content.cloneSourceId is
					// never validated when the CLONE is created, only its
					// parent. So anyone could make a clone on their own board
					// naming any element id in the database, then update that
					// id through this branch: a cross-tenant write to a board
					// they cannot even read.
					//
					// The clone relationship decides SCOPE, never PERMISSION.
					// Editing through a clone now requires what editing the
					// source directly requires, which is the same rule the
					// read path learned when its own clone bypass was closed.
					if _, aerr := s.access.RequireEdit(ctx, op.ElementID, p); aerr != nil {
						s.log.Warn("clone update rejected: no edit right on the source",
							zap.String("element", op.ElementID), zap.String("board", boardID),
							zap.String("sub", p.Sub))
						return domain.ErrForbidden
					}
					return nil
				}
			}
			return err
		}
		return s.verifyMoveTargetAny(ctx, op, roots, cache)

	case domain.ActionDelete, domain.ActionRestore:
		return s.assertWithinAny(ctx, op.ElementID, roots, cache)

	default:
		return domain.ErrValidation
	}
}

// verifyMoveTarget ensures a new location.parentId still sits inside the
// declared board (no reparenting into a foreign board via a move op) and that
// the move does not create a containment cycle.
func (s *TransactionService) verifyMoveTarget(ctx context.Context, op *domain.Op, boardID string, cache *ancestry) error {
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
		return s.assertNoCycle(ctx, op.ElementID, newParent, cache)
	}
	return nil
}

// assertNoCycle errors when newParent is elementID itself or one of its
// descendants — i.e. when the move would make the element its own ancestor.
func (s *TransactionService) assertNoCycle(ctx context.Context, elementID, newParent string, graph *ancestry) error {
	if elementID == "" || newParent == "" {
		return nil
	}
	id := newParent
	// The transaction's shared resolve, not a third independent walk over
	// elements the validation pass has already loaded.
	for depth := 0; id != "" && depth < maxDepth; depth++ {
		if id == elementID {
			return domain.ErrValidation
		}
		el, err := graph.get(ctx, id)
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
func (s *TransactionService) verifyDelegation(ctx context.Context, p *domain.Principal, op *domain.Op, cache *ancestry) error {
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
		for k := range content {
			// The allowlist first: an op may write only what its own action
			// kind declares. A kind the grant does not name is REFUSED rather
			// than waved through — treating "not declared" as "no restrictions"
			// is how the denylist grew its exceptions in the first place.
			allowed, verbatim := d.AllowsContentKey(op.Kind, k)
			if !allowed {
				return domain.ErrForbidden
			}
			// A verbatim copy carries its source's content wholesale, so there
			// is no key set to check it against. It is the one kind that still
			// needs the denylist, and the only kind that gets it.
			if verbatim && privilegedContentKeys[k] {
				return domain.ErrForbidden
			}
		}
	}
	return nil
}

// denormalizeSearchText derives the searchable body from the document being
// written, at the one place every writer passes through.
//
// content.textPreview is a PREVIEW — capped at 500 characters by the editor —
// and search, markdown export and the agent's digest all read it as though it
// were the text. So a 6,000-word treatment was findable by its first 500
// characters, and the moment a person opened the agent's 20,000-character
// document to read it, the editor's own cap silently destroyed 97% of what
// those three consumers could see, while content.doc kept every word.
//
// Derived here rather than trusted from the client, because a client-supplied
// search body is a client-supplied claim about what a document says.
func denormalizeSearchText(op *domain.Op) {
	content, ok := op.Changes["content"].(map[string]any)
	if !ok {
		return
	}
	doc, has := content["doc"]
	if !has {
		return
	}
	if doc == nil {
		// The document was cleared; the derived field must go with it or the
		// old body stays findable forever.
		content["searchText"] = ""
		return
	}
	if text := domain.PlainTextOf(doc); text != "" {
		content["searchText"] = text
	}
}

// privilegedContentKeys is the last-resort denylist, and it now guards exactly
// one action kind: `duplicate`, whose ops carry the SOURCE element's content
// verbatim and therefore have no enumerable key set to check.
//
// It used to guard everything, and it was already wrong: it named isHome and
// isTemplate, and missed `locked` (a lock the write path would then not
// enforce), `cloneSourceId` (the field the cross-tenant clone escalation turned
// on) and — worst — `agentInstructions`, the standing rules that CONSTRAIN the
// agent, which live in freely-patchable content. The only thing stopping a run
// from rewriting its own instructions was that no tool happened to emit that
// key. A list maintained by everyone who adds a feature will be wrong.
//
// Every other kind is checked the other way round, against the allowlist the
// grant carries (Delegation.ContentKeys, derived by running the compiler), so
// these keys need no enumeration there: nothing produces them.
var privilegedContentKeys = map[string]bool{
	"isHome":            true,
	"isTemplate":        true,
	"locked":            true,
	"cloneSourceId":     true,
	"agentInstructions": true,
	// The flag that says "do not read this". A run that could clear its own
	// exclusions would turn a privacy control into a one-transaction detour:
	// unset the flag, and the next run reads everything.
	"agentExclude": true,
	// JN17's status flag. "We finished" is a statement only a person gets to
	// make about their own production, and the planner's own prompt offers
	// "archive the stale stuff" as a worked example of a good request — so the
	// key is one an eager run has every reason to reach for. On the verbatim
	// path it matters for a second reason: duplicating a wrapped board should
	// hand you a live copy to work in, not a second archived one that vanishes
	// from the drawer the moment it is made.
	"archived": true,
}

// assertWithin errors unless boardID is an ancestor-or-self of elementID.
func (s *TransactionService) assertWithin(ctx context.Context, elementID, boardID string, cache *ancestry) error {
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
func (s *TransactionService) withinBoard(ctx context.Context, elementID, boardID string, cache *ancestry) (bool, error) {
	if v, ok := cache.within[key(elementID, boardID)]; ok {
		return v, nil
	}
	id := elementID
	for depth := 0; id != "" && depth < 64; depth++ {
		if id == boardID {
			cache.within[key(elementID, boardID)] = true
			return true, nil
		}
		el, err := cache.get(ctx, id)
		if err != nil {
			return false, err
		}
		id = el.Location.ParentID
	}
	cache.within[key(elementID, boardID)] = false
	return false, nil
}

func key(elementID, boardID string) string { return elementID + "|" + boardID }

// ancestry is one batched resolve of every element a transaction names, plus
// their containment chains, held for the length of the request.
//
// The containment question was asked four independent times over the same
// elements: verifyOpScope, verifyDelegation and assertNoCycle each walked, and
// captureMoves walked a fourth time with its own un-memoised Get per op —
// standing ten lines below a cache built for exactly that and using neither it
// nor anything like it. On the canonical agent plan, where every action is a
// move, that fourth walk is the dominant term; on a human's multi-select drag of
// forty cards it is forty Gets plus forty walks for information the validation
// pass had already loaded. A 240-op plan over a four-deep workspace issued on the
// order of a thousand serial round trips before a byte was written, which is the
// real ceiling on plan size — latency, not the budget number.
//
// One level-at-a-time $in per DEPTH instead of one walk per element. It is the
// pattern SweepOrphanConnectors.loadWithAncestors already uses, and the
// principle rehomeConnectors' own comment already states: one read per canvas,
// not per moved element.
//
// PRE-WRITE ONLY. rehomeConnectors deliberately reads element state AFTER the
// moves land — it has to, or it would judge a pair split that the transaction
// has just brought back together — so it keeps reading live and is not given
// this.
type ancestry struct {
	repo   domain.ElementRepository
	known  map[string]*domain.Element // nil value: looked up, does not exist
	within map[string]bool            // memoised "elementID|boardID" → within?
}

func newAncestry(repo domain.ElementRepository) *ancestry {
	return &ancestry{
		repo:   repo,
		known:  map[string]*domain.Element{},
		within: map[string]bool{},
	}
}

// preload resolves the ops' targets and every ancestor above them, one batch
// per level.
//
// Best effort: a failed batch leaves the map short and every reader falls back
// to its own Get, so this can only make the request cheaper, never wrong.
func (a *ancestry) preload(ctx context.Context, ops []domain.Op) {
	frontier := make([]string, 0, len(ops)*2)
	for i := range ops {
		if ops[i].ElementID != "" {
			frontier = append(frontier, ops[i].ElementID)
		}
		if parent := createParentID(&ops[i]); parent != "" {
			frontier = append(frontier, parent)
		}
		if loc, ok := ops[i].Changes["location"].(map[string]any); ok {
			if parent, _ := loc["parentId"].(string); parent != "" {
				frontier = append(frontier, parent)
			}
		}
	}
	for depth := 0; len(frontier) > 0 && depth < maxDepth; depth++ {
		want := make([]string, 0, len(frontier))
		seen := map[string]bool{}
		for _, id := range frontier {
			if id == "" || seen[id] {
				continue
			}
			if _, already := a.known[id]; already {
				continue
			}
			seen[id] = true
			want = append(want, id)
		}
		if len(want) == 0 {
			return
		}
		els, err := a.repo.GetMany(ctx, want)
		if err != nil {
			return // leave the map as it stands; every reader falls back
		}
		for _, id := range want {
			a.known[id] = nil // looked up; overwritten below if it exists
		}
		frontier = frontier[:0]
		for _, el := range els {
			a.known[el.ID] = el
			if el.Location.ParentID != "" {
				frontier = append(frontier, el.Location.ParentID)
			}
		}
	}
}

// get answers from the batch when it can, and reads through when it cannot —
// an element created earlier in the same transaction was not there to preload.
func (a *ancestry) get(ctx context.Context, id string) (*domain.Element, error) {
	if el, looked := a.known[id]; looked {
		if el != nil {
			return el, nil
		}
		return nil, domain.ErrNotFound
	}
	el, err := a.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	a.known[id] = el
	return el, nil
}

// cloneInstanceWithin reports whether any CLONE instance of sourceID lives
// inside boardID (the cross-board synced-note edit path).
func (s *TransactionService) cloneInstanceWithin(ctx context.Context, sourceID, boardID string, cache *ancestry) (bool, error) {
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
func (s *TransactionService) fanOutCloneUpdates(ctx context.Context, txn *domain.Transaction) {
	if s.broadcaster == nil {
		return
	}
	seen := map[string]bool{txn.BoardID: true}
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
	// ONE WRITE PATH, ONE GATE.
	//
	// This used to patch the elements itself and hand-build a transaction, which
	// made it the only service method that legitimately crosses board boundaries
	// AND the only write path with no delegation check — while Apply's own
	// comment says "there is deliberately no separate agent write path". It was
	// exactly that. Two consequences, one live and one waiting:
	//
	//   - the journal lied: every drag onto a board tile wrote a transaction
	//     with an empty Origin, which the frontend reads as "absent means
	//     human". Accidentally right, and wrong the moment anything else called
	//     this;
	//   - it was a standing hole for the roadmap. Once cross-board filing works,
	//     the natural implementation of any of it is "just call
	//     MoveAcrossBoards" — one line that hands the caller unattenuated
	//     cross-board reach with no capability, consequence or budget check.
	//
	// So it composes ops and delegates. ApplyWithMeta already computes the
	// inverses, already re-homes the connectors this gesture strands, and now
	// carries the source-board fan-out this method used to own. Origin is
	// stamped explicitly so "empty" stops being a state that means anything.
	sources := make([]string, 0, len(sourceBoards))
	for b := range sourceBoards {
		sources = append(sources, b)
	}
	sort.Strings(sources) // deterministic authorization order, and a stable test
	for i := range ops {
		ops[i].UndoChanges = nil // ApplyWithMeta computes the inverse from live state
	}
	_, err = s.ApplyWithMeta(ctx, p, targetBoardID, "", ops, TxnMeta{
		Origin: domain.OriginHuman, CrossBoard: sources,
	})
	return err
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
		denormalizeSearchText(op)
		return s.applyCreate(ctx, p, op, now)

	case domain.ActionUpdate, domain.ActionMove:
		if len(op.Changes) == 0 {
			return domain.ErrValidation
		}
		denormalizeSearchText(op)
		// SEC1 — the label gate existed on create and nowhere else.
		//
		// `labelIds` is one of the three roots a merge patch may write
		// (patchableRoots, in both repositories), so an update op carrying it went
		// straight into MergePatch with the client's array verbatim. The create
		// branch two functions below has refused somebody else's label since
		// Create_RefusesSomebodyElsesLabel shipped; the update branch beside it
		// never learned the same thing. Same field, same repository call, one door
		// locked and the one next to it standing open — which is the shape the
		// clone escalation had, and the shape the read-side clone bypass had before
		// it.
		//
		// It is not theoretical: POST /transactions with
		// {action:"update", changes:{"labelIds":["<a stranger's label id>"]}} is a
		// request any authenticated person can make against an element they may
		// edit. Labels are private to whoever coined them, so the result is a
		// stranger's private vocabulary stamped onto content they have never seen,
		// surfacing in THEIR label filter, with their usage count moving from
		// outside their account. The agent reaches the same door — ActApplyLabel
		// compiles to exactly this op — and verifyDelegation checks `acl` and every
		// content key while never looking at `labelIds` at all.
		added, removed, err := s.authorizeLabelPatch(ctx, p, op)
		if err != nil {
			return err
		}
		if _, err := s.elements.MergePatch(ctx, op.ElementID, op.Changes); err != nil {
			return err
		}
		s.settleLabelUsage(ctx, added, removed)
		return nil

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
		// A connector to something that no longer exists is not a connector.
		//
		// Deleting a card left its lines behind pointing at a gone id. They
		// render as nothing — resolveEnd returns null for a missing endpoint —
		// so the board accumulates invisible elements that still occupy the
		// graph, still come back on a restore, and still turn up in any query
		// that walks the board. Seven had built up on one board.
		//
		// They ride the SAME batch id, so restoring the card restores its
		// connections with it. Anything else would quietly lose the
		// relationships on an undo.
		ids = append(ids, s.danglingConnectors(ctx, el, ids)...)
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

	content := decodeContent(op.Changes["content"])
	if err := requireShape(typ, content); err != nil {
		return err
	}

	// A create op was not a full element write, and nothing said so: labelIds
	// and acl were copied nowhere and dropped without a word. The caller got a
	// 201 and an untagged element — the same silent-drop class as the
	// content.cells and content.hex bugs, and it meant create and update had
	// different undocumented field sets, which is exactly the asymmetry a
	// compiler branch gets wrong. It became load-bearing the moment the staged
	// plan turned into a live overlay, because folding a label into the create
	// is how "make twelve cards and tag them all Q3" compiles.
	//
	// ACL is REFUSED rather than accepted or ignored, and that decision is the
	// point. Sharing is not a create-time concern — a board's ACL is authored
	// here from the creator, and honouring a supplied one would let any writer
	// hand themselves a board owned by somebody else. Silently ignoring it is
	// worse than refusing: the caller believes they shared something.
	if _, supplied := op.Changes["acl"]; supplied {
		s.log.Warn("create op carrying an acl refused",
			zap.String("element", id), zap.String("sub", p.Sub))
		return domain.ErrValidation
	}
	labelIDs, err := s.labelsForCreate(ctx, p, op)
	if err != nil {
		return err
	}

	el := &domain.Element{
		ID:        id,
		Type:      typ,
		Location:  decodeLocation(op.Changes["location"]),
		Content:   content,
		LabelIDs:  labelIDs,
		CreatedBy: p.Sub,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if typ == domain.TypeBoard {
		el.ACL = &domain.ACL{OwnerID: p.Sub, Editors: []string{}}
	}
	if err := s.elements.Insert(ctx, el); err != nil {
		return err
	}
	// Usage counts after the insert: a label whose count rose for an element
	// that failed to exist is a taxonomy that slowly lies about itself.
	for _, labelID := range labelIDs {
		if err := s.labels.IncrementUsage(ctx, labelID, 1); err != nil {
			s.log.Warn("label usage not incremented",
				zap.String("label", labelID), zap.Error(err))
		}
	}
	return nil
}

// labelsForCreate validates that every label a create op carries belongs to the
// caller, using the same ownership rule the attach path enforces — a label is
// private to the person who coined it, and a create must not become the one
// door through which somebody else's vocabulary can be read or attached.
func (s *TransactionService) labelsForCreate(ctx context.Context, p *domain.Principal, op *domain.Op) ([]string, error) {
	raw, present := op.Changes["labelIds"]
	if !present {
		return nil, nil
	}
	ids := decodeStringSlice(raw)
	if len(ids) == 0 {
		return nil, nil
	}
	if s.labels == nil {
		return nil, domain.ErrValidation
	}
	out := make([]string, 0, len(ids))
	for _, labelID := range ids {
		label, err := s.labels.Get(ctx, labelID)
		if err != nil || label.OwnerID != p.Sub {
			s.log.Warn("create op naming a label that is not the caller's",
				zap.String("label", labelID), zap.String("sub", p.Sub))
			return nil, domain.ErrForbidden
		}
		out = append(out, labelID)
	}
	return out, nil
}

// authorizeLabelPatch is the update-side twin of labelsForCreate, and it returns
// what the patch adds and what it removes so the usage counts can follow.
//
// The rule is the DELTA, not the whole list, and the difference is what makes
// this usable on a shared board. `labelIds` is replace-semantics in both
// repositories, so a collaborator adding one tag has to send the array that is
// already there — including any label a teammate put on the card, which they can
// read back from GET /elements/:id but do not own. Checking every id in the new
// list would refuse that ordinary echo. Checking only the ids that were not
// already on the element refuses exactly the thing that is wrong: ATTACHING a
// label that is not yours. Dropping one you can already see stays allowed,
// because anyone who may edit the element may untag it.
//
// A deployment with no label repository refuses rather than waving it through.
// That is deliberate and it is why agent.NewService now wires the repository
// into this service itself: a guard that silently disables when a constructor
// forgets one line is the failure this whole item is about, and "not configured"
// must never read as "no restrictions".
func (s *TransactionService) authorizeLabelPatch(ctx context.Context, p *domain.Principal, op *domain.Op) (added, removed []string, err error) {
	raw, present := op.Changes["labelIds"]
	if !present {
		// The overwhelmingly common case, and the reason this costs nothing: no
		// element read, no label read, no extra query in a 240-op batch.
		return nil, nil, nil
	}
	el, err := s.elements.Get(ctx, op.ElementID)
	if err != nil {
		return nil, nil, err
	}
	before := make(map[string]bool, len(el.LabelIDs))
	for _, id := range el.LabelIDs {
		before[id] = true
	}
	after := map[string]bool{}
	for _, id := range decodeStringSlice(raw) {
		if after[id] {
			continue
		}
		after[id] = true
		if before[id] {
			continue // already there; keeping it is not attaching it
		}
		if s.labels == nil {
			return nil, nil, domain.ErrValidation
		}
		label, gerr := s.labels.Get(ctx, id)
		if gerr != nil || label.OwnerID != p.Sub {
			s.log.Warn("update op attaching a label that is not the caller's",
				zap.String("element", op.ElementID), zap.String("label", id),
				zap.String("sub", p.Sub))
			return nil, nil, domain.ErrForbidden
		}
		added = append(added, id)
	}
	for _, id := range el.LabelIDs {
		if !after[id] {
			removed = append(removed, id)
		}
	}
	return added, removed, nil
}

// settleLabelUsage moves the counts after the patch has landed.
//
// After, for the same reason applyCreate increments after its insert: a count
// that rose for a write which then failed is a taxonomy that slowly lies about
// itself. A failure here is logged rather than returned — the element is already
// tagged, and reporting the whole transaction as failed over a counter would be a
// worse answer than a count that is one out.
//
// Removals are settled even when the label is somebody else's. Attaching one is
// forbidden above; DETACHING one that is genuinely on the element is ordinary
// editing, and leaving its count high would strand a phantom in the owner's
// filter list forever.
func (s *TransactionService) settleLabelUsage(ctx context.Context, added, removed []string) {
	if s.labels == nil {
		return
	}
	for _, id := range added {
		if err := s.labels.IncrementUsage(ctx, id, 1); err != nil {
			s.log.Warn("label usage not incremented", zap.String("label", id), zap.Error(err))
		}
	}
	for _, id := range removed {
		if err := s.labels.IncrementUsage(ctx, id, -1); err != nil {
			s.log.Warn("label usage not decremented", zap.String("label", id), zap.Error(err))
		}
	}
}

// decodeStringSlice reads a JSON array of strings out of an op's changes, where
// it arrives as []any through the wire and as []string from a Go caller.
func decodeStringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// requiredContentKeys is the minimum each type needs to be a real element of
// that type.
//
// Content is schemaless at the domain layer, so nothing has ever checked that a
// TABLE arrives with cells or a LINE with two endpoints. The failure mode is not
// an error, it is silence: the element is created, it renders as an empty box or
// as nothing at all, and the person is left to work out which of forty new cards
// is the broken one. Five content-key mismatches have shipped in this codebase
// that way, every one of them caught by a human looking at a board rather than
// by a test — this turns that class into a refusal at the write path, which is
// the only place every writer passes through.
var requiredContentKeys = map[domain.ElementType][]string{
	domain.TypeTable:       {"cells"},
	domain.TypeLine:        {"fromId", "toId"},
	domain.TypeColorSwatch: {"hex"},
	domain.TypeLink:        {"url"},
	domain.TypeClone:       {"cloneSourceId"},
}

func requireShape(typ domain.ElementType, content domain.Content) error {
	for _, key := range requiredContentKeys[typ] {
		v, present := content[key]
		if !present || v == nil {
			return fmt.Errorf("%w: a %s needs content.%s", domain.ErrValidation, typ, key)
		}
		if s, ok := v.(string); ok && s == "" {
			return fmt.Errorf("%w: a %s needs a non-empty content.%s", domain.ErrValidation, typ, key)
		}
	}
	return nil
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
func (s *TransactionService) assertWithinAny(ctx context.Context, id string, roots []string, cache *ancestry) error {
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
func (s *TransactionService) verifyMoveTargetAny(ctx context.Context, op *domain.Op, roots []string, cache *ancestry) error {
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

// canView reports whether a subject could open this board on their own.
//
// Uses the ordinary access resolver with a principal built from the subject
// alone — no share token, no delegation — so it answers "does this person have
// standing access", which is the question a notification should turn on.
func (s *TransactionService) canView(ctx context.Context, boardID, sub string) bool {
	if sub == "" {
		return false
	}
	role, _, err := s.access.Resolve(ctx, boardID, &domain.Principal{Sub: sub})
	return err == nil && role >= RoleView
}

// danglingConnectors finds the LINE elements that would be left pointing at
// nothing once the given ids are trashed.
//
// Scoped to the board the deletion happens on: a connector lives on the same
// canvas as the things it joins, so there is nowhere else to look.
func (s *TransactionService) danglingConnectors(ctx context.Context, el *domain.Element, doomed []string) []string {
	boardID := s.canvasOf(ctx, el)
	if boardID == "" {
		return nil
	}
	going := make(map[string]bool, len(doomed))
	for _, id := range doomed {
		going[id] = true
	}
	lines, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: boardID})
	if err != nil {
		return nil
	}
	var orphans []string
	for _, l := range lines {
		if l.Type != domain.TypeLine || l.IsDeleted() || going[l.ID] {
			continue
		}
		from, _ := l.Content["fromId"].(string)
		to, _ := l.Content["toId"].(string)
		if (from != "" && going[from]) || (to != "" && going[to]) {
			orphans = append(orphans, l.ID)
		}
	}
	return orphans
}

// movedFrom records one element's canvas as it stood before the move landed.
type movedFrom struct {
	elementID string
	canvas    string
}

// captureMoves reads, before anything is written, which canvas each element an
// op reparents is leaving.
//
// Only genuine reparents. A place and a resize both compile to a move op that
// repeats the element's existing parentId, and treating those as departures
// would make every drag on a board re-examine connectors that never moved.
//
// It reads through the transaction's shared ancestry rather than issuing its own
// Get and its own walk per op. It was the FOURTH independent walk over the same
// elements in the same request, standing ten lines below a cache built for
// exactly that, and on the canonical agent plan — a filing run where every action
// is a move — it was the dominant term.
func (s *TransactionService) captureMoves(ctx context.Context, ops []domain.Op, graph *ancestry) []movedFrom {
	var out []movedFrom
	for i := range ops {
		op := &ops[i]
		if op.Action != domain.ActionMove && op.Action != domain.ActionUpdate {
			continue
		}
		loc, ok := op.Changes["location"].(map[string]any)
		if !ok {
			continue
		}
		newParent, _ := loc["parentId"].(string)
		if newParent == "" {
			continue
		}
		el, err := graph.get(ctx, op.ElementID)
		if err != nil || el.Type == domain.TypeLine {
			continue
		}
		if el.Location.ParentID == newParent {
			continue
		}
		if canvas := s.drawnOnCached(ctx, el, graph); canvas != "" {
			out = append(out, movedFrom{elementID: el.ID, canvas: canvas})
		}
	}
	return out
}

// drawnOnCached is drawnOn over the transaction's batch. Separate from drawnOn
// rather than a parameter on it, because the post-write callers MUST read live:
// rehomeConnectors runs after the moves land and has to see where the endpoints
// ended up, not where the batch found them.
func (s *TransactionService) drawnOnCached(ctx context.Context, el *domain.Element, graph *ancestry) string {
	if el.Type == domain.TypeBoard {
		parent, err := graph.get(ctx, el.Location.ParentID)
		if err != nil {
			return ""
		}
		return s.canvasOfCached(ctx, parent, graph)
	}
	return s.canvasOfCached(ctx, el, graph)
}

func (s *TransactionService) canvasOfCached(ctx context.Context, el *domain.Element, graph *ancestry) string {
	if el.Type == domain.TypeBoard {
		return el.ID
	}
	cur := el
	for i := 0; i < 8; i++ {
		parentID := cur.Location.ParentID
		if parentID == "" {
			return ""
		}
		parent, err := graph.get(ctx, parentID)
		if err != nil {
			return ""
		}
		if parent.Type == domain.TypeBoard {
			return parent.ID
		}
		cur = parent
	}
	return ""
}

// rehomeConnectors takes the arrows along when the things they join move away.
//
// A line renders only when both of its endpoints resolve on the canvas the line
// itself sits on. Grouping a board — the very first thing the agent does well —
// files columns into nested boards and leaves every connector between them
// behind on the old canvas, pointing across two canvases at once. Four of those
// sat on one real board: elements that exist, occupy the graph, come back on a
// restore, and draw nothing.
//
// Two outcomes, because there are only two honest ones. Both endpoints ended up
// on one canvas: the relationship survived the move, so the line follows it
// there. They ended up apart: no canvas can draw the line, so it goes to trash —
// in its own batch, reversible, and invisible either way.
//
// Failures are logged rather than returned. The move itself has already
// committed, and reporting a successful reparent as a failed transaction
// because a connector could not be tidied would be the larger lie.
//
// It RETURNS the ops it performed so they can join the transaction that caused
// them. They used to be writes beside the journal rather than inside it, so
// InvertOps — which reads txn.Ops and nothing else — could not see them, and
// undoing a grouping run left the retired arrows retired underneath a sentence
// promising the board was back as it was.
func (s *TransactionService) rehomeConnectors(ctx context.Context, p *domain.Principal, moved []movedFrom, at time.Time) []domain.Op {
	if len(moved) == 0 {
		return nil
	}
	var done []domain.Op
	handled := map[string]bool{}
	// One read per CANVAS, not per moved element. A multi-select drag of forty
	// cards leaves the same canvas forty times, and the connectors on it are the
	// same list every time.
	byCanvas := map[string][]*domain.Element{}
	for _, m := range moved {
		lines, cached := byCanvas[m.canvas]
		if !cached {
			kids, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: m.canvas})
			if err != nil {
				s.log.Warn("could not read the old canvas for connectors",
					zap.String("canvas", m.canvas), zap.Error(err))
				byCanvas[m.canvas] = nil
				continue
			}
			for _, k := range kids {
				if k.Type == domain.TypeLine && !k.IsDeleted() {
					lines = append(lines, k)
				}
			}
			byCanvas[m.canvas] = lines
		}
		if len(lines) == 0 {
			continue // the common case: nothing was ever drawn here
		}
		// A column that moves takes its cards with it, and a line drawn to one of
		// those cards is stranded exactly as a line drawn to the column is. The
		// first version of this only looked at the element named in the op, and
		// left the arrows between the CARDS inside two regrouped columns behind.
		travelled := s.travellingWith(ctx, m.elementID)
		for _, l := range lines {
			if handled[l.ID] {
				continue
			}
			from, _ := l.Content["fromId"].(string)
			to, _ := l.Content["toId"].(string)
			if !travelled[from] && !travelled[to] {
				continue
			}
			handled[l.ID] = true
			fromCanvas := s.drawnOnID(ctx, from)
			toCanvas := s.drawnOnID(ctx, to)
			if fromCanvas != "" && fromCanvas == toCanvas {
				if l.Location.ParentID == fromCanvas {
					continue // already where both endpoints are
				}
				changes := domain.Content{"location": map[string]any{
					"parentId": fromCanvas,
					"section":  string(domain.SectionCanvas),
				}}
				if _, err := s.elements.MergePatch(ctx, l.ID, changes); err != nil {
					s.log.Warn("connector could not follow the elements it joins",
						zap.String("line", l.ID), zap.Error(err))
					continue
				}
				done = append(done, domain.Op{
					ElementID: l.ID, Action: domain.ActionMove, Changes: changes,
					UndoChanges: domain.Content{"location": map[string]any{
						"parentId": l.Location.ParentID,
						"section":  string(l.Location.Section),
					}},
				})
				continue
			}
			if err := s.elements.SoftDelete(ctx, []string{l.ID}, p.Sub, l.ID, at); err != nil {
				s.log.Warn("stranded connector could not be trashed",
					zap.String("line", l.ID), zap.Error(err))
				continue
			}
			done = append(done, domain.Op{ElementID: l.ID, Action: domain.ActionDelete})
		}
	}
	return done
}

// travellingWith is everything that changed canvas because this element did:
// the element itself plus, when it is a container, its live subtree.
func (s *TransactionService) travellingWith(ctx context.Context, elementID string) map[string]bool {
	out := map[string]bool{elementID: true}
	el, err := s.elements.Get(ctx, elementID)
	if err != nil || !el.Type.IsContainer() {
		return out
	}
	descendants, err := s.elements.Descendants(ctx, elementID, false)
	if err != nil {
		return out
	}
	for _, d := range descendants {
		out[d.ID] = true
	}
	return out
}

// drawnOn is the canvas a connector to this element is drawn on.
//
// Almost always canvasOf. The exception is a nested BOARD: its TILE sits on the
// canvas of the board ABOVE it, so an arrow to that tile lives one level up
// from what canvasOf reports. Reading it the other way would have every arrow
// to a sub-board look stranded the moment anything nearby moved.
func (s *TransactionService) drawnOn(ctx context.Context, el *domain.Element) string {
	if el.Type == domain.TypeBoard {
		parent, err := s.elements.Get(ctx, el.Location.ParentID)
		if err != nil {
			return ""
		}
		return s.canvasOf(ctx, parent)
	}
	return s.canvasOf(ctx, el)
}

// drawnOnID is drawnOn for an id that still has to be read.
func (s *TransactionService) drawnOnID(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	el, err := s.elements.Get(ctx, id)
	if err != nil || el.IsDeleted() {
		return ""
	}
	return s.drawnOn(ctx, el)
}

// canvasOf is the board whose canvas an element is drawn on.
//
// Not the same as its parent: a card in a column has the COLUMN as parent, but
// the lines drawn to it sit on the board the column is on. Walking up is the
// only way to find where its connectors live.
func (s *TransactionService) canvasOf(ctx context.Context, el *domain.Element) string {
	if el.Type == domain.TypeBoard {
		return el.ID
	}
	// Bounded: nesting is shallow, and the bound stops a cycle from hanging
	// the request if bad data ever gets written.
	cur := el
	for i := 0; i < 8; i++ {
		parentID := cur.Location.ParentID
		if parentID == "" {
			return ""
		}
		parent, err := s.elements.Get(ctx, parentID)
		if err != nil {
			return ""
		}
		if parent.Type == domain.TypeBoard {
			return parent.ID
		}
		cur = parent
	}
	return ""
}

// broadcastToSourceBoards tells the room an element LEFT.
//
// The destination fan-out covers where things arrived; this covers where they
// came from. Both halves are needed for one gesture: dragging a card from board
// A onto board B's tile changes what A shows and what B shows, and until this
// moved into the shared write path it lived only in MoveAcrossBoards — the one
// method that crossed boards and skipped every gate.
func (s *TransactionService) broadcastToSourceBoards(boardID string, moved []movedFrom, txn *domain.Transaction) {
	if s.broadcaster == nil || len(moved) == 0 {
		return
	}
	seen := map[string]bool{boardID: true}
	for _, m := range moved {
		if m.canvas == "" || seen[m.canvas] {
			continue
		}
		seen[m.canvas] = true
		s.broadcaster.BroadcastTransaction(m.canvas, txn)
	}
}
