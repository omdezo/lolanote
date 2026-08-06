package agent

// Reading runs back: the panel, the resumable event stream, the board's
// history, and the tenant-scoped deletes that account removal needs.
//
// Everything here is authorization plus a repository call. Kept apart from the
// write paths so that stays obvious.

import (
	"context"
	"sort"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/service"
)

// Get returns a run the caller owns.
func (s *Service) Get(ctx context.Context, p *domain.Principal, runID string) (*Run, error) {
	return s.load(ctx, p, runID)
}

// Events returns journal entries after a cursor, so a client that reconnects
// catches up instead of losing what it missed while disconnected.
func (s *Service) Events(ctx context.Context, p *domain.Principal, runID string, since int64) ([]*Event, error) {
	if _, err := s.load(ctx, p, runID); err != nil {
		return nil, err
	}
	return s.events.List(ctx, runID, since, 500)
}

// MaxWait bounds a long-poll. Above this the caller should be watching the
// journal, and holding an HTTP request open costs a connection per waiter.
const MaxWait = 60 * time.Second

// AwaitSettled blocks until a run stops being in flight, or the deadline passes.
//
// POST /agent/runs is correct for a browser holding a WebSocket and wrong for
// every machine caller: a cron job, a chat command or an MCP host had to poll
// an events endpoint with a cursor to learn whether the thing it asked for
// happened. That is the difference between having an API and having an API you
// would build on. The run object is already the complete answer — it carries
// Plan, Verdict, Usage and TransactionIDs — so this adds no new shape, only the
// ability to wait for it.
//
// A timeout is NOT an error: the run is still going, and the caller gets its
// current state and can ask again. Reporting "no" for "not yet" is how polling
// clients end up retrying work that is already running.
func (s *Service) AwaitSettled(ctx context.Context, p *domain.Principal, runID string, wait time.Duration) (*Run, error) {
	if wait > MaxWait {
		wait = MaxWait
	}
	deadline := time.Now().Add(wait)
	for {
		run, err := s.load(ctx, p, runID)
		if err != nil {
			return nil, err
		}
		// PROPOSED counts as settled: the machine asked for a plan and there is
		// one waiting. Blocking until a human answers would hold the connection
		// for the whole proposal lifetime.
		if run.State.Terminal() || run.State == StateProposed {
			return run, nil
		}
		if !time.Now().Before(deadline) {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return run, nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// ListByBoard returns the caller's recent runs on a board.
func (s *Service) ListByBoard(ctx context.Context, p *domain.Principal, boardID string, limit int) ([]*Run, error) {
	if _, _, err := s.access.RequireView(ctx, boardID, p); err != nil {
		return nil, err
	}
	return s.runs.ListByBoard(ctx, p.Sub, boardID, limit)
}

// PurgeTenant removes a user's agent data on account deletion (G13).
//
// Two axes, because a run is filed under the human who STARTED it and carries
// the content of the board it READ. A collaborator's run on your board holds
// your cards' titles in Plan.Actions, your words quoted in the intent, and your
// structure in every staged-action summary — all under their tenantSub. So you
// deleted your account, the elements were hard-deleted, and a verbatim partial
// copy of your board lived on indefinitely in somebody else's agent history,
// readable by them over GET /agent/runs/:id for as long as their account
// existed. A tenant-keyed purge cannot reach it by construction.
//
// The second axis is best-effort against adapters that predate the field: the
// assertion failing means the deployment keeps exactly the erasure it had, and
// says so in the log rather than reporting a clean purge it did not perform.
func (s *Service) PurgeTenant(ctx context.Context, tenant string) error {
	if purger, ok := s.events.(EventBoardOwnerPurger); ok {
		if err := purger.DeleteByBoardOwner(ctx, tenant); err != nil {
			return err
		}
	} else {
		s.log.Warn("journal purge cannot reach collaborators' runs on this person's boards",
			zap.String("tenant", tenant))
	}
	if err := s.events.DeleteByTenant(ctx, tenant); err != nil {
		return err
	}
	if purger, ok := s.runs.(RunBoardOwnerStore); ok {
		if err := purger.DeleteByBoardOwner(ctx, tenant); err != nil {
			return err
		}
	} else {
		s.log.Warn("run purge cannot reach collaborators' runs on this person's boards",
			zap.String("tenant", tenant))
	}
	return s.runs.DeleteByTenant(ctx, tenant)
}

// RunPublicView is the half of a run that belongs to the BOARD rather than to
// the person who started it.
//
// Tenancy was modelled as "a run is private to its requester", which is right
// for the intent text and wrong for the effects. An owner who watched forty
// elements appear from an editor's run had to ask that editor to undo it, and
// the audit view — the "what has the AI changed here" surface — showed them
// only their own runs on their own board.
//
// The split is the whole design: everything here is a fact about what happened
// on a shared canvas, and nothing here is anybody's private words. No intent,
// no refinements, no steers, no plan prose, no cost.
type RunPublicView struct {
	RunID string   `json:"runId"`
	State RunState `json:"state"`
	// Who the run acted for, by display name. The subject id stays server-side.
	Actor      string    `json:"actor"`
	BoardID    string    `json:"boardId"`
	Ops        int       `json:"ops"`
	At         time.Time `json:"at"`
	Revertible bool      `json:"revertible"`
	Reverted   bool      `json:"reverted"`
	// Mine says the reader is the person this run acted for, so a client can
	// offer the full object rather than this shadow of it.
	Mine bool `json:"mine"`
}

// publicView projects a run to what any board editor may see.
func (s *Service) publicView(ctx context.Context, r *Run, reader string) RunPublicView {
	ops := 0
	if r.Plan != nil {
		ops = len(r.Plan.Actions)
	}
	at := r.UpdatedAt
	if r.CompletedAt != nil {
		at = *r.CompletedAt
	}
	return RunPublicView{
		RunID: r.ID, State: r.State, Actor: s.displayName(ctx, r.Tenant),
		BoardID: r.Task.RootBoardID, Ops: ops, At: at,
		Revertible: revertable(r) == nil, Reverted: r.State == StateReverted,
		Mine: r.Tenant == reader,
	}
}

// displayName resolves a sub for a message a colleague will read. Falls back to
// a generic noun rather than to the raw subject id: the id is exactly the thing
// MP14 exists to keep out of anywhere it can be echoed.
func (s *Service) displayName(ctx context.Context, sub string) string {
	if s.users != nil && sub != "" {
		if u, err := s.users.GetBySub(ctx, sub); err == nil && u != nil && u.DisplayName != "" {
			return sanitizeName(u.DisplayName)
		}
	}
	return "a collaborator"
}

// BoardRuns lists what the assistant has done on a board, whoever ran it.
//
// Board-scoped rather than tenant-scoped, redacted to the public half. Per-run
// revert is the product's central trust promise and it was unavailable to the
// person with the most at stake.
func (s *Service) BoardRuns(ctx context.Context, p *domain.Principal, boardID string, limit int) ([]RunPublicView, error) {
	role, _, err := s.access.RequireView(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	runs, err := s.boardRuns(ctx, p, boardID, role, limit)
	if err != nil {
		return nil, err
	}
	out := make([]RunPublicView, 0, len(runs))
	for _, r := range runs {
		out = append(out, s.publicView(ctx, r, p.Sub))
	}
	return out, nil
}

// boardRuns reads every run on a board the caller may audit.
//
// An editor sees the board's runs; anyone weaker sees only their own, which is
// what shipped. The union is deduplicated because a person's own runs appear
// under both keys.
func (s *Service) boardRuns(ctx context.Context, p *domain.Principal, boardID string, role service.Role, limit int) ([]*Run, error) {
	mine, err := s.runs.ListByBoard(ctx, p.Sub, boardID, limit)
	if err != nil {
		return nil, err
	}
	if !role.CanEdit() {
		return mine, nil
	}
	owner, ok := s.runs.(RunBoardOwnerStore)
	if !ok {
		// The adapter predates the key. Narrower than promised, and saying so
		// beats an audit view that silently claims to be complete.
		return mine, nil
	}
	theirs, err := owner.ListByBoardOwner(ctx, s.boardOwnerOf(ctx, boardID), boardID, limit)
	if err != nil {
		return mine, nil
	}
	seen := map[string]bool{}
	out := make([]*Run, 0, len(mine)+len(theirs))
	for _, r := range append(append([]*Run{}, mine...), theirs...) {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	sortRunsNewestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// sortRunsNewestFirst is the one ordering every merged run list uses, so a
// union of two queries reads like a single sorted one.
func sortRunsNewestFirst(runs []*Run) {
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
}

func (s *Service) boardOwnerOf(ctx context.Context, boardID string) string {
	_, acl, err := s.access.BoardChain(ctx, boardID)
	if err != nil || acl == nil {
		return ""
	}
	return acl.OwnerID
}

// AccountRuns is the account-wide run query four surfaces were blocked on.
//
// ListAgentRuns refused without a boardId and the store had no tenant-wide
// list, so "what has the assistant done for me lately" and "which of my boards
// has a proposal waiting" had nowhere to be asked — while the spend cap worked
// around the same gap with an undeclared, unpaginated `ListByBoard(tenant, "")`.
func (s *Service) AccountRuns(ctx context.Context, p *domain.Principal, f RunFilter, limit int) ([]*Run, error) {
	if p == nil || p.Sub == "" {
		return nil, domain.ErrUnauthorized
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if wide, ok := s.runs.(RunAnalyticsStore); ok {
		return wide.ListByTenant(ctx, p.Sub, f, limit)
	}
	return s.runs.ListByBoard(ctx, p.Sub, f.BoardID, limit)
}

// AccountUsage answers "what am I spending" as a grouped query rather than as
// 500 documents unmarshalled in Go.
func (s *Service) AccountUsage(ctx context.Context, p *domain.Principal, f RunFilter) (UsageRollup, error) {
	if p == nil || p.Sub == "" {
		return UsageRollup{}, domain.ErrUnauthorized
	}
	if wide, ok := s.runs.(RunAnalyticsStore); ok {
		return wide.AggregateUsage(ctx, p.Sub, f)
	}
	runs, err := s.runs.ListByBoard(ctx, p.Sub, f.BoardID, 500)
	if err != nil {
		return UsageRollup{}, err
	}
	out := UsageRollup{ByState: map[RunState]int64{}}
	for _, r := range runs {
		if !f.Since.IsZero() && r.CreatedAt.Before(f.Since) {
			continue
		}
		out.Runs++
		out.CostUSD += r.Usage.CostUSD
		out.InTokens += int64(r.Usage.InputTokens)
		out.OutTokens += int64(r.Usage.OutputTokens)
		out.ByState[r.State]++
	}
	return out, nil
}

// ClearHistory lets a person erase their own agent history.
//
// The runs and events collections are a verbatim archive of every request typed
// into the composer, every refinement, every mid-run correction — including the
// plans the person explicitly pressed Discard on. Nothing in the product told
// them it existed and nothing let them clear it, in an app with a privacy tab
// and a download-my-data endpoint. The mechanism has been sitting here since
// account deletion needed it; this is the same call, reachable one step earlier.
//
// It refuses while a run is in flight rather than deleting a row the goroutine
// is about to write to, which would leave the board's slot held by a run that no
// longer exists.
func (s *Service) ClearHistory(ctx context.Context, p *domain.Principal) error {
	if p == nil || p.Sub == "" {
		return domain.ErrUnauthorized
	}
	recent, err := s.runs.ListByBoard(ctx, p.Sub, "", 500)
	if err != nil {
		return err
	}
	for _, r := range recent {
		if r.State.Active() {
			return ErrHistoryInFlight
		}
	}
	return s.PurgeTenant(ctx, p.Sub)
}

// ErrHistoryInFlight refuses a history wipe while something is still running.
var ErrHistoryInFlight = wrap(domain.ErrConflict,
	"the assistant is working right now — let it finish or stop it first")

// Reconcile resolves runs a crash left non-terminal. A run whose model call was
// interrupted cannot be safely resumed — and must not be silently retried, as
// that would re-spend — so it is failed explicitly and the user can start again.
// reconcileGrace is the margin past a run's own deadline before its row is
// treated as abandoned. One tick's worth, so a run that ends microseconds after
// its deadline is never racing the sweeper for its own completion.
const reconcileGrace = 5 * time.Minute

func (s *Service) Reconcile(ctx context.Context) {
	runs, err := s.runs.Unfinished(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, run := range runs {
		if run.State == StateProposed {
			// A proposal awaiting a human decision survives a restart — that is
			// the point of it being durable. What it must NOT do is survive
			// forever: PROPOSED is non-terminal, so an unanswered plan holds the
			// board's only run slot, and a person who previewed once and closed
			// the tab locked the agent for everyone on that board permanently.
			if run.ProposalExpiresAt == nil {
				// Written before this field existed. Give it one lifetime from
				// now rather than expiring it immediately, so a deploy does not
				// discard a plan somebody is looking at — and so boards already
				// deadlocked in production heal on the next sweep.
				deadline := now.Add(proposalLifetime)
				run.ProposalExpiresAt = &deadline
				if err := s.runs.Update(ctx, run, run.Rev); err != nil {
					s.log.Warn("stamping proposal deadline", zap.String("run", run.ID), zap.Error(err))
				}
				continue
			}
			if now.Before(*run.ProposalExpiresAt) {
				continue
			}
			s.log.Info("expiring an unanswered proposal", zap.String("run", run.ID))
			s.finishWithReason(ctx, run, StateDiscarded,
				"this plan was never answered, so it stopped holding the board")
			continue
		}
		// Only a run that has genuinely STOPPED, never one that is merely
		// unfinished.
		//
		// This swept every in-flight run on every tick, and the sweep runs
		// every five minutes. A run alive when a tick landed was force-failed
		// mid-flight, told "the server restarted" — which had not happened —
		// while its goroutine carried on spending against a run row that now
		// said FAILED. At the old three-minute deadline that was a lottery; the
		// deadline is now eight minutes, so any run using its budget crossed a
		// tick and the kill became a certainty. Raising the deadline without
		// this made the timeout fix worse than the bug it fixed.
		//
		// Liveness is UpdatedAt: the run loop stamps it every step. Past its
		// own deadline plus a grace margin, nothing is coming — the process
		// that owned it is gone, and the row is the orphan this exists to
		// clear. Inside that window it is somebody's run in progress.
		grace := run.Task.Budget.Deadline
		if grace <= 0 {
			grace = DefaultBudget().Deadline
		}
		if last := run.UpdatedAt; !last.IsZero() && now.Sub(last) < grace+reconcileGrace {
			continue
		}
		s.log.Info("reconciling interrupted run", zap.String("run", run.ID),
			zap.String("state", string(run.State)), zap.Duration("silent", now.Sub(run.UpdatedAt)))
		s.finishWithReason(ctx, run, StateFailed,
			"this run stopped without finishing — the server it was running on went away")
	}
}

func (s *Service) load(ctx context.Context, p *domain.Principal, runID string) (*Run, error) {
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	// Tenancy: a run is visible only to the principal it was admitted for.
	if run.Tenant != p.Sub {
		return nil, domain.ErrNotFound
	}
	return run, nil
}

// loadForBoard returns a run the caller may ACT on because of the board, not
// because of the run: their own, or anyone's on a board they can edit.
//
// The permission answer here is the board's, not the run's. If you can edit the
// board you can undo an agent transaction on it, because you could delete the
// same elements by hand — and the person with the most at stake, the owner, was
// the one person the tenant gate locked out.
//
// It returns the FULL run because the callers need the journal and the plan to
// compute inverses; the redaction happens on the way back out, in redactFor.
func (s *Service) loadForBoard(ctx context.Context, p *domain.Principal, runID string) (*Run, error) {
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Tenant == p.Sub {
		return run, nil
	}
	if _, err := s.access.RequireEdit(ctx, run.Task.RootBoardID, p); err != nil {
		// ErrNotFound, not ErrForbidden: a run id is not something a stranger
		// should be able to probe for existence.
		return nil, domain.ErrNotFound
	}
	return run, nil
}

// redactFor is the public half of a run, built by ALLOWLIST.
//
// A run has a private half — intent, refinements, steers, plan prose, cost —
// and a public half: that it happened, who for, when, how many ops, its
// transaction ids, whether it is revertible. Once revert became a board
// permission rather than a tenancy one, the return value of Revert started
// travelling to people who may not read the requester's own words, and a
// blank-the-fields denylist would have leaked the next field somebody added.
func (s *Service) redactFor(run *Run, reader string) *Run {
	if run == nil || run.Tenant == reader {
		return run
	}
	return &Run{
		ID:     run.ID,
		State:  run.State,
		Active: run.Active,
		Rev:    run.Rev,
		Task: TaskSpec{
			RootBoardID: run.Task.RootBoardID,
			Autonomy:    run.Task.Autonomy,
			Scope:       run.Task.Scope,
		},
		TransactionIDs:     run.TransactionIDs,
		RevertedElementIDs: run.RevertedElementIDs,
		StaleElementIDs:    run.StaleElementIDs,
		// Public by the same rule as Revertible above: "whether this can still
		// be undone" is a fact about the board every editor may act on, and the
		// reason carries no intent, no prose and no cost. Withholding it would
		// leave a colleague with the button and none of the explanation.
		RevertBlockedReason: run.RevertBlockedReason,
		CreatedAt:           run.CreatedAt,
		UpdatedAt:           run.UpdatedAt,
		CompletedAt:         run.CompletedAt,
	}
}
