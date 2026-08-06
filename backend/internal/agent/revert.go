package agent

// Undoing a run — all of it, or one piece at a time.
//
// Split out of service.go, which had grown to carry admission, the run loop,
// the commit, every revert path and the reads in one 1200-line file. These
// belong together and apart from the rest: they are the only paths that write
// to the board WITHOUT a model in the loop, on a grant that is deliberately
// wider than the run's own (a revert must be able to delete what the run
// created), and they read their ops from the journal rather than recompiling a
// plan.

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/service"
)

// Cancel stops a run. In preview mode nothing has been written, so cancellation
// is total; after a commit the user wants Revert instead.
//
// Two things had to change for Stop to mean stop.
//
// It aborts the WORK, not just the row. Cancel used to write CANCELLED and
// return; the run goroutine's context came from context.Background() and the
// loop never read run state, so the model carried on for up to eight more
// minutes on the person's money — and freeing the board slot let them start a
// second run writing the same board alongside the first.
//
// And it refuses once a commit has begun. StateApplying → StateCancelled was a
// legal edge and the UI kept offering Stop through APPLYING and VERIFYING, so
// pressing it in the second the state flipped told the person, in two languages,
// that nothing had changed while forty elements landed on their board — and the
// run then had no transaction id recorded against it, so per-run revert, the
// per-action undo and the audit view could none of them see the work.
func (s *Service) Cancel(ctx context.Context, p *domain.Principal, runID string) (*Run, error) {
	run, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if run.State.Terminal() {
		return run, nil // idempotent
	}
	if run.State == StateApplying || run.State == StateVerifying {
		return nil, ErrAlreadyWriting
	}
	// Abort first, terminal state second: between the two the loop must not be
	// able to observe a live run row and take another turn.
	s.abort(runID)
	s.finishWithReason(ctx, run, StateCancelled, "")
	return run, nil
}

// ErrAlreadyWriting refuses a Stop that arrived after the write began. It names
// the exit rather than only the refusal, because "you cannot stop" with no next
// step is what makes a person reach for the browser's reload button.
var ErrAlreadyWriting = wrap(domain.ErrConflict,
	"this is already being written to the board — you can undo it once it lands")

// Revert undoes everything a completed run committed, as one new transaction.
//
// This is nearly free: the inverse of every op was computed when the op was
// built, because the element model has always required that of every writer.
func (s *Service) Revert(ctx context.Context, p *domain.Principal, runID string) (*Run, error) {
	// The board's permission, not the run's: an owner who watched forty
	// elements appear from an editor's run had to ask that editor to undo it.
	run, err := s.loadForBoard(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if err := revertable(run); err != nil {
		return nil, err
	}
	if _, err := s.access.RequireEdit(ctx, run.Task.RootBoardID, p); err != nil {
		return nil, err
	}
	defer s.notifyForeignRevert(ctx, run, p)
	agentPrincipal := &domain.Principal{Sub: p.Sub, Email: p.Email, Name: p.Name, Delegation: s.revertGrant(run, p)}
	undone, gone, err := s.revertTransactions(ctx, agentPrincipal, run)
	if err != nil {
		// Half an undo is still an undo, and the person has to be told which
		// half. Recording it here is what lets a retry RESUME rather than replay
		// deletes against already-deleted elements.
		s.recordReverted(ctx, run, undone)
		s.emit(ctx, run, EvError,
			fmt.Sprintf("undid %d change(s); the rest could not be undone", len(undone)),
			map[string]any{"elementIds": undone, "partial": true})
		return nil, err
	}
	// Nothing left that CAN be undone, and the reason is permanent. Marking the
	// run rather than transitioning it is JN8's honesty clause: the ledger reads
	// this field and draws the reason where the button was, instead of offering
	// an Undo that fails the same way every time it is pressed.
	if len(undone) == 0 && len(gone) > 0 {
		s.recordRevertBlocked(ctx, run, permanentlyGoneReason(len(gone)))
		s.emit(ctx, run, EvError, run.RevertBlockedReason,
			map[string]any{"elementIds": gone, "gone": true})
		return nil, wrap(domain.ErrConflict, run.RevertBlockedReason)
	}
	s.recordReverted(ctx, run, undone)
	if len(gone) > 0 {
		// Partial-and-report, JN20 clause (1). The person gets a number they can
		// act on rather than a success card that overstates what came back.
		s.emit(ctx, run, EvError,
			fmt.Sprintf("undid %d change(s); %s", len(undone), permanentlyGoneReason(len(gone))),
			map[string]any{"elementIds": gone, "gone": true, "partial": true})
	}
	s.emit(ctx, run, EvReverted, "run reverted", nil)
	s.auditReverted(ctx, p, run, len(undone), len(gone), false)
	// COMPLETED → REVERTED is a defined edge, so this goes through transition
	// rather than finishWithReason: the latter short-circuits on an already
	// terminal state, which is exactly what a completed run is.
	if err := s.transition(ctx, run, StateReverted, ""); err != nil {
		// The board is back; only the label failed to move. A run whose work is
		// undone must not keep offering Undo, so the failure is loud rather than
		// returned — returning it would tell the caller the revert did not happen.
		s.log.Warn("board reverted but the run could not be marked reverted",
			zap.String("run", run.ID), zap.String("state", string(run.State)), zap.Error(err))
	}
	s.learnFromRejection(ctx, run, nil, undone, true)
	return s.redactFor(run, p.Sub), nil
}

// notifyForeignRevert tells a person when somebody else takes their run back.
//
// Undoing a colleague's work silently is the multiplayer failure this whole
// corner is about, and revert is now a board permission — so the one signal the
// author gets must not be discovering it on the canvas.
func (s *Service) notifyForeignRevert(ctx context.Context, run *Run, by *domain.Principal) {
	if s.notifier == nil || run == nil || by == nil || run.Tenant == by.Sub || run.Tenant == "" {
		return
	}
	s.notifier.Notify(ctx, &domain.Notification{
		ID: s.newID(), UserID: run.Tenant, Kind: domain.NotifyAgentRun, ActorID: by.Sub,
		BoardID: run.Task.RootBoardID, AgentRunID: run.ID, Origin: domain.OriginAgent,
		Message:   sanitizeName(by.Name) + " undid your assistant run on this board",
		CreatedAt: time.Now().UTC(),
	})
}

// auditReverted records an undo in the tenant-wide log.
//
// The actor is whoever pressed the button, which on a shared board is often not
// the person the run belongs to — the run's own tenant is therefore in the meta
// rather than left implied, because "somebody undid the assistant's work" and
// "somebody undid SOMEBODY ELSE's assistant's work" are different events and the
// row has to tell them apart on its own.
func (s *Service) auditReverted(ctx context.Context, p *domain.Principal, run *Run, undone, gone int, partial bool) {
	s.audit.Emit(ctx, p, &domain.AuditEvent{
		Actor:  domain.AuditActor{AgentRunID: run.ID},
		Action: "agent.run_reverted",
		Target: domain.AuditTarget{Type: "board", ID: run.Task.RootBoardID},
		Meta: map[string]any{
			"runId": run.ID, "runTenant": run.Tenant,
			"undone": undone, "unreachable": gone, "partial": partial,
		},
	})
}

// revertable decides whether a run can still be undone.
//
// Invariant: ANY run that ever held a transaction id is revertible, whatever
// stopped it. The guard used to be a state whitelist — COMPLETED or PARTIAL —
// and the state machine models failure as "nothing happened" while three real
// paths produce failure with things having happened: a write that errored after
// applying some ops, postconditions that failed and could not be put back, and
// an apply refused after the labels were already in. Those runs showed the
// person a card asserting in their own language that the board was untouched,
// with no Undo, an API that answered "conflict", and an audit view that skipped
// the run entirely. The only recovery left was Ctrl+Z, which does not survive a
// reload.
func revertable(run *Run) error {
	if len(run.TransactionIDs) == 0 {
		// Nothing was ever written. This is the honest "nothing to undo", and it
		// is the only case that should refuse.
		return ErrNothingToUndo
	}
	if allReverted(run) {
		return ErrNothingToUndo
	}
	if run.RevertBlockedReason != "" {
		// Learned the hard way on a previous attempt, and permanent: the targets
		// were hard-deleted, and nothing brings them back. Refusing here rather
		// than re-walking the journal means the second press costs a comparison
		// instead of N element fetches, and — the part that matters — says why.
		return wrap(domain.ErrConflict, run.RevertBlockedReason)
	}
	return nil
}

// ErrNothingToUndo refuses a revert of a run that never wrote anything, or
// whose work is already back the way it was.
var ErrNothingToUndo = wrap(domain.ErrConflict, "this run has nothing left to undo")

// permanentlyGoneReason is the one sentence both revert paths and the ledger
// use for an element nobody can bring back.
//
// Written once because three surfaces say it — the event stream, the API error,
// and the ledger row — and three spellings of the same fact is how a person
// concludes they are three different problems.
func permanentlyGoneReason(n int) string {
	if n == 1 {
		return "1 item this run created was permanently deleted and cannot come back"
	}
	return fmt.Sprintf("%d items this run created were permanently deleted and cannot come back", n)
}

// recordRevertBlocked persists why the undo is over, through the same
// rev-refetching write every other run mutation uses.
func (s *Service) recordRevertBlocked(ctx context.Context, run *Run, reason string) {
	now := time.Now().UTC()
	_ = s.persist(ctx, run, "revert blocked reason", func(fresh *Run) {
		fresh.RevertBlockedReason = reason
		fresh.UpdatedAt = now
		run.RevertBlockedReason = reason
	})
}

// recordReverted persists which elements are no longer standing, through the
// same rev-refetching write the commit handle uses.
//
// Both revert paths used to accumulate this set in memory and DISCARD it on any
// error, so a revert that failed on its second transaction left the run claiming
// nothing had been undone — and the review list re-offered every row with a live
// undo button, replaying deletes against deleted elements.
func (s *Service) recordReverted(ctx context.Context, run *Run, undone []string) {
	if len(undone) == 0 {
		return
	}
	at := time.Now().UTC()
	ids := append([]string(nil), undone...)
	_ = s.persist(ctx, run, "reverted element ids", func(fresh *Run) {
		seen := map[string]bool{}
		for _, id := range fresh.RevertedElementIDs {
			seen[id] = true
		}
		if fresh.RevertedAt == nil {
			fresh.RevertedAt = map[string]time.Time{}
		}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			fresh.RevertedElementIDs = append(fresh.RevertedElementIDs, id)
			fresh.RevertedAt[id] = at
		}
		fresh.UpdatedAt = at
		run.RevertedElementIDs = fresh.RevertedElementIDs
		run.RevertedAt = fresh.RevertedAt
	})
}

// RevertOne undoes the part of a run that touched the given elements, leaving
// the rest of the run standing.
//
// Why this exists: revert was all-or-nothing, so a run that got four things
// right and one thing wrong had to be thrown away whole. In practice people
// then kept the wrong change rather than lose the right ones — which is the
// worst of both, and it made the review step feel like a bet instead of a
// decision.
//
// Creates cascade. Undoing a column the run made while leaving the notes it put
// inside would strand them in a container that no longer exists, so anything
// this run parented beneath a reverted element goes with it.
func (s *Service) RevertOne(ctx context.Context, p *domain.Principal, runID string, elementIDs []string) (*Run, error) {
	run, err := s.loadForBoard(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if len(elementIDs) == 0 {
		return s.Revert(ctx, p, runID)
	}
	if err := revertable(run); err != nil {
		return nil, err
	}
	if _, err := s.access.RequireEdit(ctx, run.Task.RootBoardID, p); err != nil {
		return nil, err
	}

	defer s.notifyForeignRevert(ctx, run, p)

	targets := expandWithinRun(run.Plan, elementIDs, run.RevertedElementIDs)
	if len(targets) == 0 {
		// Nothing left to undo — pressing the button twice is not an error.
		return s.redactFor(run, p.Sub), nil
	}

	agentPrincipal := &domain.Principal{Sub: p.Sub, Email: p.Email, Name: p.Name, Delegation: s.revertGrant(run, p)}
	undone, gone, err := s.revertOps(ctx, agentPrincipal, run, targets)
	// Whatever came back was really undone, error or not: record it BEFORE the
	// error is returned, so the rows the person can no longer act on stop being
	// offered to them.
	s.recordReverted(ctx, run, undone)
	if err != nil {
		s.emit(ctx, run, EvError,
			fmt.Sprintf("undid %d change(s); the rest could not be undone", len(undone)),
			map[string]any{"elementIds": undone, "partial": true})
		return nil, err
	}
	if len(gone) > 0 {
		// The row the person clicked names an element somebody destroyed. Said
		// out loud, because the alternative is a click that appears to do
		// nothing — the shape of this bug before JN20 was a click that appeared
		// to do nothing and had in fact half-written the board.
		s.emit(ctx, run, EvError, permanentlyGoneReason(len(gone)),
			map[string]any{"elementIds": gone, "gone": true, "partial": true})
	}
	if len(undone) == 0 {
		return s.redactFor(run, p.Sub), nil
	}
	s.emit(ctx, run, EvReverted, fmt.Sprintf("reverted %d change(s)", len(undone)),
		map[string]any{"elementIds": undone, "partial": true})
	s.auditReverted(ctx, p, run, len(undone), len(gone), true)

	// Once nothing this run did is still standing, the run itself is reverted.
	// Leaving it COMPLETED would offer an Undo that has nothing left to undo.
	//
	// A run that FAILED with work standing has no edge to REVERTED in the
	// transition table, so the label cannot follow it there — but the board is
	// already back, which is the half that matters, and revertable() reads the
	// effects rather than the state, so the button correctly stops offering
	// itself either way.
	whole := allReverted(run)
	if whole && CanTransition(run.State, StateReverted) {
		if err := s.transition(ctx, run, StateReverted, ""); err != nil {
			s.log.Warn("elements reverted but the run could not be marked reverted",
				zap.String("run", run.ID), zap.String("state", string(run.State)), zap.Error(err))
		}
	}
	// A per-action undo is the SHARPEST signal of the lot: they kept four rows
	// and took one back, which names the wrong row rather than the wrong run.
	s.learnFromRejection(ctx, run, nil, undone, whole)
	return s.redactFor(run, p.Sub), nil
}

// learnFromRejection turns an undo or a discard into supervision.
//
// Two things happen, in this order and only after the terminal state is durably
// written. First the typed corrections go on the run, which is what LP1's rule
// compiler reads — a revert is the cleanest preference signal the system will
// ever have ("these four were right, that one was wrong", attributed,
// timestamped, with the plan beside it) and it was being spent on a
// strikethrough. Then the model is asked, once, what standing rule would have
// prevented it, and the answer lands in ProposedRule where the outcome card
// already renders it.
//
// It must not reopen the run: Reflect takes the Run rather than staging state
// precisely so it can be called from here. It self-skips on a quarantined run
// and on one with nothing undone, so no caller-side guard is needed — and the
// run that WAS corrected was previously barred from proposing a rule about it,
// because `remember` is only offered mid-flight and a revert happens after a
// terminal state. This is the path that reaches it.
//
// Affordable because rejections are rare: the cost scales with the failure rate
// rather than the run rate.
//
// `source` is the run as it was JUDGED — normally `run` itself, but Discard
// passes a snapshot taken before it strips the rejected prose, because a
// reflection over a plan whose titles have been blanked would reason about
// nothing and propose a rule to match.
func (s *Service) learnFromRejection(ctx context.Context, run, source *Run, revertedIDs []string, wholeRun bool) {
	if run == nil {
		return
	}
	if source == nil {
		source = run
	}
	now := time.Now().UTC()
	if labels := RejectionCorrections(source, revertedIDs, wholeRun, now); len(labels) > 0 {
		_ = s.persist(ctx, run, "rejection corrections", func(fresh *Run) {
			fresh.Corrections = append(fresh.Corrections, labels...)
			fresh.UpdatedAt = now
			run.Corrections = fresh.Corrections
		})
	}
	if s.provider == nil {
		return
	}
	text, err := Reflect(ctx, s.provider, source, revertedIDs, wholeRun)
	if err != nil || text == "" {
		// A reflection that could not be had is not a failure of the undo. The
		// board is already back; this was the optional half.
		return
	}
	_ = s.persist(ctx, run, "proposed rule", func(fresh *Run) {
		if fresh.Plan == nil {
			return
		}
		fresh.Plan.ProposedRule = text
		fresh.UpdatedAt = now
		if run.Plan != nil {
			run.Plan.ProposedRule = text
		}
	})
}

// expandWithinRun grows a set of element ids to include everything the SAME RUN
// parented beneath them, skipping anything already undone.
func expandWithinRun(plan *Plan, seeds, already []string) map[string]bool {
	done := map[string]bool{}
	for _, id := range already {
		done[id] = true
	}
	want := map[string]bool{}
	for _, id := range seeds {
		if id != "" && !done[id] {
			want[id] = true
		}
	}
	if plan == nil {
		return want
	}
	// Repeat until stable: a note inside a column inside a board the run made
	// is three levels from the seed.
	for changed := true; changed; {
		changed = false
		for _, a := range plan.Actions {
			if a.ParentID == "" || want[a.ElementID] || done[a.ElementID] {
				continue
			}
			if want[a.ParentID] {
				want[a.ElementID] = true
				changed = true
			}
		}
	}
	return want
}

// allReverted reports whether every element this run's plan touched has been
// individually undone.
func allReverted(run *Run) bool {
	if run.Plan == nil || len(run.Plan.Actions) == 0 {
		return false
	}
	done := map[string]bool{}
	for _, id := range run.RevertedElementIDs {
		done[id] = true
	}
	for _, a := range run.Plan.Actions {
		if !done[a.ElementID] {
			return false
		}
	}
	return true
}

// revertOps replays the inverse of just the ops that touched `targets`.
//
// Like the whole-run revert, the ops come from the JOURNAL rather than from a
// recompiled plan: after an apply the cards live inside the new columns, so a
// recompiled scope would no longer find them.
//
// On failure it returns what it MANAGED to undo alongside the error, rather
// than a bare error and an empty set. Discarding the progress was the sharp
// end of partial-failure here: the person pressed Undo, saw a toast, and the
// list still showed every row as standing.
func (s *Service) revertOps(ctx context.Context, p *domain.Principal, run *Run, targets map[string]bool) ([]string, []string, error) {
	undone := map[string]bool{}
	var gone []string
	for i := len(run.TransactionIDs) - 1; i >= 0; i-- {
		txn, err := s.txnRepo.Get(ctx, run.TransactionIDs[i])
		if err != nil {
			return sortedKeys(undone), gone, fmt.Errorf("cannot read this run's changes to reverse them: %w", err)
		}
		var mine []domain.Op
		for _, op := range txn.Ops {
			if targets[op.ElementID] {
				mine = append(mine, op)
			}
		}
		// Revert re-checks what commit checked.
		mine, skipped, missing := s.keepFresh(ctx, run, txn.CreatedAt, mine)
		if len(skipped) > 0 {
			s.reportStaleSkips(ctx, run, skipped)
		}
		gone = append(gone, missing...)
		inverse := InvertOps(mine)
		if len(inverse) == 0 {
			continue
		}
		// A distinct id per (transaction, revert) pair: the whole-run revert
		// already claims txn.ID+"r", and a partial revert can happen more than
		// once against the same transaction.
		meta := service.TxnMeta{
			TxnID:      fmt.Sprintf("%sp%d", txn.ID, len(run.RevertedElementIDs)+len(undone)),
			Origin:     domain.OriginAgent,
			AgentRunID: run.ID,
		}
		if _, err := s.txns.ApplyWithMeta(ctx, p, run.Task.RootBoardID, "", inverse, meta); err != nil {
			return sortedKeys(undone), gone, err
		}
		for _, op := range mine {
			undone[op.ElementID] = true
		}
	}
	return sortedKeys(undone), gone, nil
}

// staleGrace absorbs the clock skew between a transaction's stamp and the
// UpdatedAt the same write set on its elements. They are written microseconds
// apart by different code; without a margin every element the run touched would
// look edited-since and a revert would undo nothing at all.
const staleGrace = 2 * time.Second

// keepFresh drops the ops whose targets somebody has changed since the run
// wrote them.
//
// Revert is the only write path in the product that did NOT re-check what
// commit checked: the inverse ops were computed at commit time against a board
// that no longer exists, and were replayed with no staleness comparison at all
// while the commit path runs CheckFingerprint. Single-player that is nearly
// harmless. With two people it is destruction: the agent creates a column, a
// colleague spends an hour filling it, the original requester presses Undo on
// the outcome card, and the column plus everything in it goes to trash.
//
// Two rules, both conservative — when in doubt, leave the element alone:
//
//   - Edited since the commit: excluded. The person who edited it is the
//     authority on its current content, not a plan from an hour ago.
//   - A container that gained children this run did not create: its inverse
//     DELETE is dropped so the container is emptied-but-kept rather than
//     trashed with a colleague's work inside it. Its other inverses still run.
//
// Returns the ops to replay, the ids left alone, and the ids that can no longer
// be reached at all (JN20) — all three reported by the caller rather than
// swallowed: "I will leave those alone" is the sentence that turns a silent
// partial undo into a decision the person made, and "those three are gone for
// good" is the one that stops a button being offered that cannot work.
func (s *Service) keepFresh(ctx context.Context, run *Run, at time.Time, ops []domain.Op) (kept []domain.Op, stale, gone []string) {
	mine := map[string]bool{}
	if run.Plan != nil {
		for _, a := range run.Plan.Actions {
			mine[a.ElementID] = true
		}
	}
	cutoff := at.Add(staleGrace)

	kept = make([]domain.Op, 0, len(ops))
	for _, op := range ops {
		el, err := s.elements.Get(ctx, op.ElementID)
		if err != nil || el == nil {
			// JN20's pre-flight. This branch used to read "already gone — the
			// inverse is a no-op or a restore; either way, let it run", and that
			// sentence was wrong about all three inverses. `applyDelete` and
			// `applyRestore` both open with a fetch, and `MergePatch` needs a
			// document to patch, so every one of them fails on an element that
			// was hard-deleted rather than trashed.
			//
			// The failure had teeth because of WHERE it happened: the apply loop
			// does not roll back and the journal row is written afterwards, so an
			// op that exploded 30 ops into a 40-op inverse left the first 30
			// applied, no transaction row describing them, and the run still
			// reading COMPLETED with an Undo button that would half-revert it
			// again. Two operations the product actively encourages get you
			// there — Empty trash is a first-class button and revert is the
			// central trust promise — and the error named nothing.
			//
			// So the unreachable ops are partitioned out BEFORE anything is
			// written, and the caller reports them: "undid 37 of 40; 3 items were
			// permanently deleted". Partial-and-honest, which is the same shape
			// the stale-element skip beside it already takes.
			gone = append(gone, op.ElementID)
			continue
		}
		if el.UpdatedAt.After(cutoff) {
			stale = append(stale, op.ElementID)
			continue
		}
		if op.Action == domain.ActionCreate && s.gainedForeignChildren(ctx, op.ElementID, mine) {
			stale = append(stale, op.ElementID)
			continue
		}
		kept = append(kept, op)
	}
	return kept, stale, gone
}

// gainedForeignChildren reports whether a container the run created now holds
// anything the run did not put there.
func (s *Service) gainedForeignChildren(ctx context.Context, id string, mine map[string]bool) bool {
	kids, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: id})
	if err != nil {
		// Unknown. Refusing to trash is the recoverable mistake; trashing a
		// colleague's work is not.
		return true
	}
	for _, k := range kids {
		if k.IsDeleted() {
			continue
		}
		if !mine[k.ID] {
			return true
		}
	}
	return false
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sortStrings(out)
	return out
}

// revertGrant mints a grant for the compensating write. It is the original
// grant plus delete (a revert must remove the columns the run created), a fresh
// expiry, and — the part that matters once revert became a BOARD permission —
// it acts for whoever pressed Undo.
//
// The grant used to carry the original run's OnBehalfOf, which the write path
// checks against the calling principal. So the moment an owner could reach a
// collaborator's run, every such revert 403'd inside the transaction service
// with no message: the authority came from the actor's edit rights on the
// board, and the grant still claimed to be acting for somebody else. A grant is
// an attenuation of the person USING it, never of the person who first minted
// one.
func (s *Service) revertGrant(run *Run, actor *domain.Principal) *domain.Delegation {
	d := run.Delegation
	d.Capabilities = append([]domain.Capability{domain.CapElementDelete}, d.Capabilities...)
	d.Consequence = domain.ConsequenceDestructive
	d.ExpiresAt = time.Now().UTC().Add(5 * time.Minute)
	if actor != nil && actor.Sub != "" {
		d.OnBehalfOf = actor.Sub
	}
	// The approval that authorises THIS write is the click that just happened.
	d.RequiresApproval = false
	d.ApprovedBy = d.OnBehalfOf
	return &d
}

// revertTransactions replays the inverse of everything the run committed.
//
// The ops are read back from the JOURNAL, not recompiled from the proposal.
// That distinction is load-bearing: after a successful apply the cards live
// inside the new columns, so recompiling the scope would no longer find them
// and the revert would silently do nothing. The transaction records what
// actually happened, and each op already carries its own inverse.
// It returns the element ids it undid, and returns them WITH the error when it
// stops partway — a revert that half-completes must leave a record of which
// half, or the person retries the part that already succeeded.
func (s *Service) revertTransactions(ctx context.Context, p *domain.Principal, run *Run) ([]string, []string, error) {
	if len(run.TransactionIDs) == 0 {
		return nil, nil, nil
	}
	if p.Delegation == nil {
		p.Delegation = s.revertGrant(run, p)
	}

	undone := map[string]bool{}
	var gone []string
	// Reverse chronological order: the last change applied is the first undone.
	for i := len(run.TransactionIDs) - 1; i >= 0; i-- {
		txn, err := s.txnRepo.Get(ctx, run.TransactionIDs[i])
		if err != nil {
			return sortedKeys(undone), gone, fmt.Errorf("cannot read this run's changes to reverse them: %w", err)
		}
		// Revert re-checks what commit checked: elements a colleague has edited
		// since the run wrote them are left alone rather than rolled over, and
		// elements somebody hard-deleted are dropped rather than replayed into a
		// fetch that fails a third of the way through the batch (JN20).
		fresh, skipped, missing := s.keepFresh(ctx, run, txn.CreatedAt, txn.Ops)
		if len(skipped) > 0 {
			s.reportStaleSkips(ctx, run, skipped)
		}
		gone = append(gone, missing...)
		inverse := InvertOps(fresh)
		if len(inverse) == 0 {
			continue
		}
		if _, err := s.txns.ApplyWithMeta(ctx, p, run.Task.RootBoardID, "", inverse, service.TxnMeta{
			TxnID:      txn.ID + "r",
			Origin:     domain.OriginAgent,
			AgentRunID: run.ID,
		}); err != nil {
			return sortedKeys(undone), gone, err
		}
		// The element graph is only half of what a commit did. The bells it rang
		// are the other half, and they were outside the invertible set: undo a
		// run that assigned four tasks and four colleagues keep an inbox item for
		// work that no longer exists — while the outcome card says the board is
		// back as it was. InvertOps cannot reach them because they are not ops;
		// the transaction records their ids for exactly this.
		s.txns.RetractNotifications(ctx, txn)
		for _, op := range fresh {
			undone[op.ElementID] = true
		}
	}
	return sortedKeys(undone), gone, nil
}

// reportStaleSkips records, on the run and in the journal, what the revert
// deliberately left standing.
//
// Surfaced as information rather than as a refusal: "reverting will undo 24
// changes; 3 items have been edited since, and I will leave those alone" is a
// decision the person can act on, where a bare refusal is a dead end and a
// silent skip is a lie about what Undo did.
func (s *Service) reportStaleSkips(ctx context.Context, run *Run, skipped []string) {
	s.emit(ctx, run, EvError,
		fmt.Sprintf("left %d item(s) alone — they have been edited since this run wrote them", len(skipped)),
		map[string]any{"elementIds": skipped, "stale": true})
	now := time.Now().UTC()
	_ = s.persist(ctx, run, "stale element ids", func(fresh *Run) {
		seen := map[string]bool{}
		for _, id := range fresh.StaleElementIDs {
			seen[id] = true
		}
		for _, id := range skipped {
			if seen[id] {
				continue
			}
			seen[id] = true
			fresh.StaleElementIDs = append(fresh.StaleElementIDs, id)
		}
		fresh.UpdatedAt = now
		run.StaleElementIDs = fresh.StaleElementIDs
	})
}
