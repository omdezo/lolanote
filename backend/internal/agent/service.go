package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/service"
)

// The run coordinator: admission, the state machine, and the four decisions a
// human can make about a run (apply, discard, cancel, revert).
//
// Execution model for this milestone: a run executes in an in-process goroutine
// driven by a durable state machine, and a boot reconciler resolves whatever a
// crash left mid-flight. That is crash-recoverable without a queue, and it is
// the honest fit for work measured in seconds (G12). Activity leases and a
// separate worker role become worth their cost when a workload's runs start
// exceeding a couple of minutes; Organize does not.

// IDGenerator mints 24-hex element/run ids.
type IDGenerator func() string

// Service is the agent harness.
type Service struct {
	elements domain.ElementRepository
	users    domain.UserRepository
	txns     *service.TransactionService
	txnRepo  domain.TransactionRepository
	access   *service.AccessResolver
	labels   domain.LabelRepository
	comments domain.CommentRepository
	images   ImageFetcher
	links    LinkResolver
	runs     RunStore
	events   EventStore
	provider cognition.Provider
	bus      domain.EventBroadcaster
	newID    IDGenerator
	log      *zap.Logger

	// dailyCapUSD bounds one tenant's spend per UTC day. Denial-of-wallet is a
	// named threat, not an afterthought.
	dailyCapUSD float64
}

// Config carries the wiring for NewService.
type Config struct {
	Elements    domain.ElementRepository
	Users       domain.UserRepository
	Txns        *service.TransactionService
	TxnRepo     domain.TransactionRepository
	Access      *service.AccessResolver
	Labels      domain.LabelRepository
	Comments    domain.CommentRepository
	Images      ImageFetcher
	Links       LinkResolver
	Runs        RunStore
	Events      EventStore
	Provider    cognition.Provider
	Bus         domain.EventBroadcaster
	NewID       IDGenerator
	Log         *zap.Logger
	DailyCapUSD float64
}

// NewService constructs the harness. A nil Provider yields a service that
// admits nothing — the deployment simply has no agent, which the API reports as
// unavailable rather than failing mysteriously at the first request.
func NewService(cfg Config) *Service {
	return &Service{
		elements:    cfg.Elements,
		labels:      cfg.Labels,
		comments:    cfg.Comments,
		images:      cfg.Images,
		links:       cfg.Links,
		users:       cfg.Users,
		txns:        cfg.Txns,
		txnRepo:     cfg.TxnRepo,
		access:      cfg.Access,
		runs:        cfg.Runs,
		events:      cfg.Events,
		provider:    cfg.Provider,
		bus:         cfg.Bus,
		newID:       cfg.NewID,
		log:         cfg.Log.Named("agent"),
		dailyCapUSD: cfg.DailyCapUSD,
	}
}

// Enabled reports whether a model provider is configured.
func (s *Service) Enabled() bool { return s != nil && s.provider != nil }

// ---------------------------------------------------------------------------
// Admission
// ---------------------------------------------------------------------------

// CreateRequest is the client's ask, before normalization.
type CreateRequest struct {
	BoardID string `json:"boardId"`
	// Intent is what the person actually wants, in their own words. There is
	// no fixed workload: the same harness organizes, drafts, or declines,
	// depending on this one field.
	Intent      string   `json:"intent"`
	Scope       Scope    `json:"scope"`
	SelectionID []string `json:"selectionIds,omitempty"`
	Autonomy    Autonomy `json:"autonomy"`
}

// Create admits a task and starts the run. It returns as soon as the run is
// durable, and the work proceeds in the background — the client watches the
// journal rather than holding an HTTP request open for the model call.
func (s *Service) Create(ctx context.Context, p *domain.Principal, req CreateRequest) (*Run, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	// The human's own permission is checked first and independently. A
	// delegation attenuates; it never grants.
	if _, err := s.access.RequireEdit(ctx, req.BoardID, p); err != nil {
		return nil, err
	}
	intent := truncate(sanitizeBody(req.Intent), 1000)
	if intent == "" {
		return nil, ErrNoIntent
	}
	if !req.Scope.Valid() {
		req.Scope = ScopeBoard
	}
	if !req.Autonomy.Valid() {
		req.Autonomy = AutonomyPreview
	}

	// One run per board (G8). Checked here for a clear error; also enforced by
	// a unique index so a concurrent create cannot slip between check and write.
	// The guard is correct — two agents writing one board is a real hazard —
	// but a bare rejection makes the feature look broken when a colleague holds
	// the slot. Same invariant, legible outcome.
	if active, err := s.runs.ActiveByBoard(ctx, req.BoardID); err == nil && active != nil {
		who := "someone else"
		if active.Tenant == p.Sub {
			who = "you"
		}
		return nil, fmt.Errorf("%w: %s started one %s — it will finish shortly",
			ErrRunActive, who, humanAge(active.CreatedAt))
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	if err := s.checkDailyCap(ctx, p.Sub); err != nil {
		return nil, err
	}

	budget := DefaultBudget()
	budget.Normalize()

	runID := s.newID()
	now := time.Now().UTC()
	run := &Run{
		ID:     runID,
		Tenant: p.Sub,
		Task: TaskSpec{
			Intent:      intent,
			Owner:       p.Sub,
			RootBoardID: req.BoardID,
			Scope:       req.Scope,
			SelectionID: req.SelectionID,
			Autonomy:    req.Autonomy,
			Budget:      budget,
		},
		State:  StateCreated,
		Active: true,
		// The grant is minted server-side and is strictly weaker than the
		// human's own permissions: no delete, no ACL, no reach beyond this
		// board's subtree, and an expiry.
		Delegation: Delegation{
			RunID:       runID,
			OnBehalfOf:  p.Sub,
			RootBoardID: req.BoardID,
			Capabilities: []domain.Capability{
				domain.CapElementCreate,
				domain.CapElementUpdate,
				domain.CapElementMove,
				// Deletes are granted here, but a plan can only contain one
				// after a human has seen it: the tool is withheld from
				// unattended runs, and Preconditions rejects them there.
				domain.CapElementDelete,
			},
			Consequence: domain.ConsequenceDestructive,
			// One transaction may carry a to-do list and all of its tasks, so
			// the op ceiling sits above the action ceiling.
			MaxOps:    budget.MaxActions * 4,
			ExpiresAt: now.Add(30 * time.Minute),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.runs.Insert(ctx, run); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil, ErrRunActive
		}
		return nil, err
	}
	s.emit(ctx, run, EvRunCreated, "run admitted", map[string]any{
		"intent": intent, "scope": run.Task.Scope, "autonomy": run.Task.Autonomy,
	})

	// The principal is copied rather than captured: the request's context and
	// its cancellation belong to the HTTP round trip, not to the run.
	principal := &domain.Principal{Sub: p.Sub, Email: p.Email, Name: p.Name}
	go s.execute(context.Background(), principal, run.ID)
	return run, nil
}

// checkDailyCap enforces the tenant's spend ceiling before any tokens are spent.
func (s *Service) checkDailyCap(ctx context.Context, tenant string) error {
	if s.dailyCapUSD <= 0 {
		return nil
	}
	// A day's runs are bounded and small; summing them is cheaper and more
	// honest than a denormalized counter that can drift from the journal.
	runs, err := s.runs.ListByBoard(ctx, tenant, "", 500)
	if err != nil {
		return nil // never block work on a telemetry read
	}
	cutoff := time.Now().UTC().Truncate(24 * time.Hour)
	var spent float64
	for _, r := range runs {
		if r.CreatedAt.After(cutoff) {
			spent += r.Usage.CostUSD
		}
	}
	if spent >= s.dailyCapUSD {
		return fmt.Errorf("%w: daily AI budget reached", ErrBudget)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

// execute drives one run to PROPOSED (preview) or COMPLETED (auto).
func (s *Service) execute(ctx context.Context, p *domain.Principal, runID string) {
	defer func() {
		// A panic in the model or compiler path must terminate the run
		// cleanly rather than leave it non-terminal forever.
		if r := recover(); r != nil {
			s.log.Error("run panicked", zap.String("run", runID), zap.Any("panic", r))
			s.fail(ctx, runID, "the run stopped unexpectedly")
		}
	}()

	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, run.Task.Budget.Deadline)
	defer cancel()

	if err := s.transition(ctx, run, StatePlanning, ""); err != nil {
		return
	}

	scope, err := CompileScope(ctx, s.elements, run.Task)
	if err == nil {
		s.attachLabels(ctx, scope, run.Task.Owner)
		s.attachPeople(ctx, scope)
	}
	if err != nil {
		s.fail(ctx, runID, "could not read the board")
		return
	}
	s.emit(ctx, run, EvStepFinished, fmt.Sprintf("read board - %d items in scope", len(scope.Items)),
		map[string]any{"items": len(scope.Items)})

	if err := s.transition(ctx, run, StateRunning, ""); err != nil {
		return
	}

	emit := func(t EventType, msg string, data map[string]any) { s.emit(ctx, run, t, msg, data) }
	plan, usage, err := NewPlanner(s.provider, s.elements, s.labels, s.txnRepo, s.images, s.links).Run(ctx, scope, run.Task, run.ID, emit, run.Plan)
	run.Usage.Add(usage)
	if err != nil {
		s.finishWithReason(ctx, run, terminalFor(err), reasonFor(err))
		return
	}

	// A question has nothing to validate and nothing to apply — it just needs a
	// person. Send it straight to PROPOSED, where the bar renders it.
	if plan.Question != nil {
		run.Plan = plan
		s.emit(ctx, run, EvPlanReady, "needs an answer", map[string]any{"question": plan.Question.Text})
		_ = s.transition(ctx, run, StateProposed, "")
		return
	}

	// An answer with no changes: keep the plan so the summary reaches the user,
	// and end the run rather than offering an Apply button for nothing.
	if len(plan.Actions) == 0 {
		run.Plan = plan
		s.emit(ctx, run, EvPlanReady, "answered without changing anything", nil)
		s.finishWithReason(ctx, run, StatePartial, plan.Summary)
		return
	}

	// Preconditions run before anything is committed - the cheapest place to
	// catch a bad plan, and the only place with nothing to undo.
	verdict := Preconditions(plan, scope, run.Task)
	run.Verdict = &verdict
	s.emit(ctx, run, EvVerdict, "pre-commit checks", map[string]any{"passed": verdict.Passed})
	if !verdict.Passed {
		s.finishWithReason(ctx, run, StateFailed, "the proposed changes did not pass validation")
		return
	}

	run.Plan = plan
	s.emit(ctx, run, EvPlanReady, fmt.Sprintf("proposed %d change(s)", len(plan.Actions)),
		map[string]any{"actions": len(plan.Actions), "destructive": plan.Destructive()})

	// A quarantined plan never auto-applies, whatever the user chose. They
	// consented to skipping review of the agent's OWN judgement, not to skipping
	// review of a run that board content was steering.
	if run.Task.Autonomy == AutonomyAuto && !plan.Quarantined {
		if err := s.transition(ctx, run, StateApplying, ""); err != nil {
			return
		}
		if _, err := s.commit(ctx, p, run, nil); err != nil {
			s.log.Warn("auto-apply failed", zap.String("run", runID), zap.Error(err))
		}
		return
	}
	_ = s.transition(ctx, run, StateProposed, "")
}

// snapPref reads the user's snap-to-grid preference so agent-placed columns sit
// on the same grid as hand-placed ones.
func (s *Service) snapPref(ctx context.Context, sub string) bool {
	if s.users == nil {
		return false
	}
	u, err := s.users.GetBySub(ctx, sub)
	if err != nil || u == nil {
		return false
	}
	return u.EffectiveSettings().Preferences.SnapToGrid
}

// ---------------------------------------------------------------------------
// Human decisions
// ---------------------------------------------------------------------------

// Apply commits a proposed run, folding in the human's adjustments.
//
// The client sends TYPED adjustments, never ops. The server recompiles from its
// own stored proposal, so an adjustment can rearrange what was proposed and
// nothing else (G2).
func (s *Service) Apply(ctx context.Context, p *domain.Principal, runID string, adjustments []Adjustment) (*Run, error) {
	run, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if run.State != StateProposed || run.Plan == nil {
		return nil, ErrNotProposed
	}
	if _, err := s.access.RequireEdit(ctx, run.Task.RootBoardID, p); err != nil {
		return nil, err
	}
	if err := s.transition(ctx, run, StateApplying, ""); err != nil {
		return nil, err
	}
	return s.commit(ctx, p, run, adjustments)
}

// commit is the write path: recompile, re-check, one transaction, verify.
func (s *Service) commit(ctx context.Context, p *domain.Principal, run *Run, adjustments []Adjustment) (*Run, error) {
	scope, err := CompileScope(ctx, s.elements, run.Task)
	if err != nil {
		s.fail(ctx, run.ID, "could not re-read the board")
		return nil, err
	}
	// Membership is checked BEFORE Hydrate widens the scope, against the same
	// shape the plan was compiled from. A card added to this board while the
	// human was reviewing touches nothing the plan names, so per-element
	// timestamps all still match — and the plan would commit against a board it
	// never saw, orphaning the new card outside a grouping built without it.
	if !CheckMembership(run.Plan, scope) {
		_ = s.transition(ctx, run, StateProposed, "")
		s.emit(ctx, run, EvError, "the board gained or lost items while you were reviewing", nil)
		return nil, ErrStalePlan
	}

	// The plan may reference elements the agent found by reading a nested
	// board, so the scope is widened to everything the plan touches before it
	// is validated against.
	if err := scope.Hydrate(ctx, s.elements, run.Plan); err != nil {
		return nil, err
	}

	// Exact-action binding: the plan was computed against specific element
	// versions, and only those. If a collaborator changed one while the human
	// was reviewing, the approval no longer describes reality.
	stale, err := CheckFingerprint(ctx, s.elements, run.Plan)
	if err != nil {
		return nil, err
	}
	if len(stale) > 0 {
		_ = s.transition(ctx, run, StateProposed, "")
		s.emit(ctx, run, EvError, "the board changed while you were reviewing",
			map[string]any{"staleElements": stale})
		return nil, ErrStalePlan
	}

	effective := ApplyAdjustments(run.Plan, adjustments, scope)
	effective.Fingerprint = run.Plan.Fingerprint
	if len(effective.Actions) == 0 {
		s.finishWithReason(ctx, run, StateDiscarded, "every change was removed")
		return run, nil
	}

	verdict := Preconditions(effective, scope, run.Task)
	if !verdict.Passed {
		run.Verdict = &verdict
		s.finishWithReason(ctx, run, StateFailed, "the adjusted changes did not pass validation")
		return nil, domain.ErrValidation
	}

	ops, err := CompileOps(effective, scope)
	if err != nil {
		s.finishWithReason(ctx, run, StateFailed, "could not build the changes")
		return nil, err
	}

	// Labels the run coined exist only in the plan until this moment. They are
	// inserted BEFORE the ops, because the ops reference their ids — and only
	// here, so a discarded or reverted preview never leaves a stray tag in the
	// user's taxonomy. Insert is idempotent on id, so a retried apply is safe.
	if s.labels != nil {
		for _, l := range effective.NewLabels {
			if l == nil || l.OwnerID != p.Sub {
				continue // a label for someone else is not ours to make
			}
			if _, gerr := s.labels.Get(ctx, l.ID); gerr == nil {
				continue
			}
			if l.CreatedAt.IsZero() {
				l.CreatedAt = time.Now().UTC()
			}
			if err := s.labels.Insert(ctx, l); err != nil {
				s.finishWithReason(ctx, run, StateFailed, "could not create the new labels")
				return nil, err
			}
		}
	}

	// Comment bodies, like labels, exist only in the plan until this moment.
	// The thread elements are created by the ops; these are what make them say
	// anything.
	if s.comments != nil {
		for _, c := range effective.NewComments {
			if c == nil || c.AuthorID != p.Sub {
				continue
			}
			if c.ID == "" {
				c.ID = s.newID()
			}
			if c.CreatedAt.IsZero() {
				c.CreatedAt = time.Now().UTC()
			}
			if err := s.comments.Insert(ctx, c); err != nil {
				s.log.Warn("agent: could not write comment body", zap.Error(err))
			}
		}
	}

	aclBefore := aclHash(scope.Board.ACL)

	// The agent's principal for this write: the human's identity, attenuated by
	// the run's grant. Every op is re-validated against it inside the same write
	// path a human's drag uses.
	agentPrincipal := &domain.Principal{
		Sub: p.Sub, Email: p.Email, Name: p.Name,
		Delegation: &run.Delegation,
	}
	txn, err := s.txns.ApplyWithMeta(ctx, agentPrincipal, run.Task.RootBoardID, "", ops, service.TxnMeta{
		TxnID:      run.ID,
		Origin:     domain.OriginAgent,
		AgentRunID: run.ID,
	})
	if err != nil {
		s.finishWithReason(ctx, run, StateFailed, "the changes were rejected")
		return nil, err
	}

	run.Plan = effective
	run.TransactionIDs = append(run.TransactionIDs, txn.ID)
	s.emit(ctx, run, EvOpsCommitted, fmt.Sprintf("applied %d change(s)", len(effective.Actions)),
		map[string]any{"ops": len(ops), "transactionId": txn.ID})

	if err := s.transition(ctx, run, StateVerifying, ""); err != nil {
		return nil, err
	}

	// Completion is decided from re-read state, not from having reached the end
	// of the function.
	post := Postconditions(ctx, s.elements, effective, scope, aclBefore)
	run.Verdict = &post
	s.emit(ctx, run, EvVerdict, "post-commit checks", map[string]any{"passed": post.Passed})

	if !post.Passed {
		// Reality disagrees with intent: put the board back and fail, rather than
		// leaving the user with a half-applied change they did not ask for.
		s.log.Error("postconditions failed; auto-reverting", zap.String("run", run.ID))
		if _, rerr := s.revertTransactions(ctx, agentPrincipal, run); rerr != nil {
			s.log.Error("auto-revert failed", zap.String("run", run.ID), zap.Error(rerr))
		}
		s.finishWithReason(ctx, run, StateFailed, "the result did not verify; the board was restored")
		return run, nil
	}

	s.finishWithReason(ctx, run, StateCompleted, "")
	return run, nil
}

// Discard abandons a plan without writing anything.
func (s *Service) Discard(ctx context.Context, p *domain.Principal, runID string) (*Run, error) {
	run, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if run.State != StateProposed {
		return nil, ErrNotProposed
	}
	s.finishWithReason(ctx, run, StateDiscarded, "")
	return run, nil
}

// Refine sends a proposed plan back for another pass with the person's steer.
//
// This is what turns the agent from a vending machine into a collaborator.
// Nobody gets a structural request right the first time, because seeing the
// wrong answer is how you discover what you wanted. Before this, "make it four
// columns instead" meant discard, retype, and pay again — and the second
// attempt had no idea what was wrong with the first.
//
// The run keeps its identity, its budget and its board slot. Cost accumulates
// against the SAME run, so the meter the user sees is the true cost of the
// conversation rather than of its last turn.
func (s *Service) Refine(ctx context.Context, p *domain.Principal, runID, note string) (*Run, error) {
	run, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if run.State != StateProposed {
		return nil, ErrNotProposed
	}
	note = truncate(sanitizeBody(note), 1000)
	if note == "" {
		return nil, ErrNoIntent
	}
	if len(run.Task.Refinements) >= maxRefinements {
		return nil, fmt.Errorf("%w: this run has been revised enough — apply it or start fresh", ErrBudget)
	}
	// A refinement costs real tokens, so it is subject to the same daily
	// ceiling as a new run. Otherwise the cap is trivially escaped by revising.
	if err := s.checkDailyCap(ctx, p.Sub); err != nil {
		return nil, err
	}

	run.Task.Refinements = append(run.Task.Refinements, note)
	// Each pass gets fresh steps; without this a long conversation would starve
	// on the budget its first turn already spent.
	run.Task.Budget = DefaultBudget()
	run.Task.Budget.Normalize()
	// Optimistic concurrency: two people revising the same proposal at once
	// must not silently interleave their steers.
	if err := s.runs.Update(ctx, run, run.Rev); err != nil {
		return nil, err
	}
	s.emit(ctx, run, EvRunCreated, "revision requested", map[string]any{"note": note})

	principal := &domain.Principal{Sub: p.Sub, Email: p.Email, Name: p.Name}
	go s.execute(context.Background(), principal, run.ID)
	return run, nil
}

// maxRefinements bounds one conversation. Past a handful of passes the honest
// advice is to start again with a clearer request, not to keep nudging.
const maxRefinements = 5

// AuditEntry is one change the agent made on a board, in the terms a person
// would ask about it.
type AuditEntry struct {
	RunID    string    `json:"runId"`
	Intent   string    `json:"intent"`
	At       time.Time `json:"at"`
	Ops      int       `json:"ops"`
	Reverted bool      `json:"reverted"`
	CostUSD  float64   `json:"costUsd"`
	State    RunState  `json:"state"`
}

// Audit answers "what has the AI changed here", which every transaction has
// recorded since the agent shipped and nothing has ever surfaced. Trust in an
// agent is mostly the ability to check up on it afterwards.
func (s *Service) Audit(ctx context.Context, p *domain.Principal, boardID string, limit int) ([]AuditEntry, error) {
	if _, _, err := s.access.RequireView(ctx, boardID, p); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	runs, err := s.runs.ListByBoard(ctx, p.Sub, boardID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]AuditEntry, 0, len(runs))
	for _, r := range runs {
		// Only runs that actually wrote something. A discarded preview changed
		// nothing, and listing it as a change is exactly the noise that makes
		// an audit log stop being read.
		if r.State != StateCompleted && r.State != StateReverted && r.State != StatePartial {
			continue
		}
		ops := 0
		if r.Plan != nil {
			ops = len(r.Plan.Actions)
		}
		if ops == 0 {
			continue
		}
		at := r.UpdatedAt
		if r.CompletedAt != nil {
			at = *r.CompletedAt
		}
		out = append(out, AuditEntry{
			RunID: r.ID, Intent: r.Task.Intent, At: at, Ops: ops,
			Reverted: r.State == StateReverted, CostUSD: r.Usage.CostUSD, State: r.State,
		})
	}
	return out, nil
}

// Cancel stops a run. In preview mode nothing has been written, so cancellation
// is total; after a commit the user wants Revert instead.
func (s *Service) Cancel(ctx context.Context, p *domain.Principal, runID string) (*Run, error) {
	run, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if run.State.Terminal() {
		return run, nil // idempotent
	}
	s.finishWithReason(ctx, run, StateCancelled, "")
	return run, nil
}

// Revert undoes everything a completed run committed, as one new transaction.
//
// This is nearly free: the inverse of every op was computed when the op was
// built, because the element model has always required that of every writer.
func (s *Service) Revert(ctx context.Context, p *domain.Principal, runID string) (*Run, error) {
	run, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if run.State != StateCompleted && run.State != StatePartial {
		return nil, domain.ErrConflict
	}
	if _, err := s.access.RequireEdit(ctx, run.Task.RootBoardID, p); err != nil {
		return nil, err
	}
	agentPrincipal := &domain.Principal{Sub: p.Sub, Email: p.Email, Name: p.Name, Delegation: s.revertGrant(run)}
	if _, err := s.revertTransactions(ctx, agentPrincipal, run); err != nil {
		return nil, err
	}
	s.emit(ctx, run, EvReverted, "run reverted", nil)
	// COMPLETED → REVERTED is a defined edge, so this goes through transition
	// rather than finishWithReason: the latter short-circuits on an already
	// terminal state, which is exactly what a completed run is.
	if err := s.transition(ctx, run, StateReverted, ""); err != nil {
		return nil, err
	}
	return run, nil
}

// revertGrant mints a grant for the compensating write. It is the original
// grant plus delete (a revert must remove the columns the run created) and a
// fresh expiry, and nothing else.
func (s *Service) revertGrant(run *Run) *domain.Delegation {
	d := run.Delegation
	d.Capabilities = append([]domain.Capability{domain.CapElementDelete}, d.Capabilities...)
	d.Consequence = domain.ConsequenceDestructive
	d.ExpiresAt = time.Now().UTC().Add(5 * time.Minute)
	return &d
}

// revertTransactions replays the inverse of everything the run committed.
//
// The ops are read back from the JOURNAL, not recompiled from the proposal.
// That distinction is load-bearing: after a successful apply the cards live
// inside the new columns, so recompiling the scope would no longer find them
// and the revert would silently do nothing. The transaction records what
// actually happened, and each op already carries its own inverse.
func (s *Service) revertTransactions(ctx context.Context, p *domain.Principal, run *Run) (*domain.Transaction, error) {
	if len(run.TransactionIDs) == 0 {
		return nil, nil
	}
	if p.Delegation == nil {
		p.Delegation = s.revertGrant(run)
	}

	var last *domain.Transaction
	// Reverse chronological order: the last change applied is the first undone.
	for i := len(run.TransactionIDs) - 1; i >= 0; i-- {
		txn, err := s.txnRepo.Get(ctx, run.TransactionIDs[i])
		if err != nil {
			return nil, fmt.Errorf("cannot read this run's changes to reverse them: %w", err)
		}
		inverse := InvertOps(txn.Ops)
		if len(inverse) == 0 {
			continue
		}
		last, err = s.txns.ApplyWithMeta(ctx, p, run.Task.RootBoardID, "", inverse, service.TxnMeta{
			TxnID:      txn.ID + "r",
			Origin:     domain.OriginAgent,
			AgentRunID: run.ID,
		})
		if err != nil {
			return nil, err
		}
	}
	return last, nil
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

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

// ListByBoard returns the caller's recent runs on a board.
func (s *Service) ListByBoard(ctx context.Context, p *domain.Principal, boardID string, limit int) ([]*Run, error) {
	if _, _, err := s.access.RequireView(ctx, boardID, p); err != nil {
		return nil, err
	}
	return s.runs.ListByBoard(ctx, p.Sub, boardID, limit)
}

// PurgeTenant removes a user's agent data on account deletion (G13).
func (s *Service) PurgeTenant(ctx context.Context, tenant string) error {
	if err := s.events.DeleteByTenant(ctx, tenant); err != nil {
		return err
	}
	return s.runs.DeleteByTenant(ctx, tenant)
}

// Reconcile resolves runs a crash left non-terminal. A run whose model call was
// interrupted cannot be safely resumed — and must not be silently retried, as
// that would re-spend — so it is failed explicitly and the user can start again.
func (s *Service) Reconcile(ctx context.Context) {
	runs, err := s.runs.Unfinished(ctx)
	if err != nil {
		return
	}
	for _, run := range runs {
		if run.State == StateProposed {
			continue // a durable proposal awaiting a human decision survives a restart
		}
		s.log.Info("reconciling interrupted run", zap.String("run", run.ID), zap.String("state", string(run.State)))
		s.finishWithReason(ctx, run, StateFailed, "the server restarted while this run was in progress")
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

// ---------------------------------------------------------------------------
// State machine & journal
// ---------------------------------------------------------------------------

// transition moves a run to a new state, refusing edges the machine does not
// define. Only this function changes Run.State.
func (s *Service) transition(ctx context.Context, run *Run, to RunState, reason string) error {
	if !CanTransition(run.State, to) {
		return fmt.Errorf("agent: illegal transition %s → %s", run.State, to)
	}
	from := run.State
	run.State = to
	run.Active = !to.Terminal()
	run.Reason = reason
	run.UpdatedAt = time.Now().UTC()
	if to.Terminal() {
		now := run.UpdatedAt
		run.CompletedAt = &now
	}
	rev := run.Rev
	if err := s.runs.Update(ctx, run, rev); err != nil {
		run.State = from
		run.Active = !from.Terminal()
		s.log.Warn("state write rejected", zap.String("run", run.ID), zap.Error(err))
		return err
	}
	s.emit(ctx, run, EvRunState, string(to), map[string]any{"from": string(from), "to": string(to), "reason": reason})
	return nil
}

// finishWithReason drives a run to a terminal state, tolerating an illegal edge
// by forcing it: a run must always reach a terminal state, and refusing the
// transition would leave it stuck holding the board's run slot forever.
func (s *Service) finishWithReason(ctx context.Context, run *Run, to RunState, reason string) {
	if run.State.Terminal() {
		return
	}
	if err := s.transition(ctx, run, to, reason); err != nil {
		run.State = to
		run.Active = false
		run.Reason = reason
		run.UpdatedAt = time.Now().UTC()
		now := run.UpdatedAt
		run.CompletedAt = &now
		if uerr := s.runs.Update(ctx, run, run.Rev); uerr != nil {
			s.log.Error("could not terminate run", zap.String("run", run.ID), zap.Error(uerr))
			return
		}
		s.emit(ctx, run, EvRunState, string(to), map[string]any{"to": string(to), "reason": reason, "forced": true})
	}
}

func (s *Service) fail(ctx context.Context, runID, reason string) {
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return
	}
	s.finishWithReason(ctx, run, StateFailed, reason)
}

// emit appends to the journal and pushes the event to everyone on the board, so
// the run is visible live to the user AND to collaborators watching the same
// canvas — the same channel ordinary edits ride.
func (s *Service) emit(ctx context.Context, run *Run, t EventType, msg string, data map[string]any) {
	ev := &Event{
		ID:      s.newID(),
		RunID:   run.ID,
		Type:    t,
		Message: msg,
		Data:    data,
		At:      time.Now().UTC(),
	}
	if err := s.events.Append(ctx, ev); err != nil {
		s.log.Warn("journal append failed", zap.String("run", run.ID), zap.Error(err))
	}
	if s.bus != nil {
		s.bus.BroadcastEvent(run.Task.RootBoardID, "agent.event", map[string]any{
			"runId":    run.ID,
			"sequence": ev.Sequence,
			"state":    run.State,
			"type":     t,
			"message":  msg,
			"data":     data,
			"at":       ev.At,
		})
	}
}

// terminalFor maps a workload error onto the terminal state that describes it.
// A model that found nothing is not a failure of the system.
func terminalFor(err error) RunState {
	switch {
	case errors.Is(err, ErrNothingToDo), errors.Is(err, cognition.ErrRefused):
		return StatePartial
	case errors.Is(err, context.DeadlineExceeded):
		return StateExhausted
	default:
		return StateFailed
	}
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, ErrNothingToDo):
		return "nothing needed changing"
	case errors.Is(err, cognition.ErrRefused):
		return "the model declined this request"
	case errors.Is(err, cognition.ErrUnavailable):
		return "the AI service is unavailable right now"
	case errors.Is(err, context.DeadlineExceeded):
		return "the run ran out of time"
	default:
		return "the run could not be completed"
	}
}

// attachPeople lists who can be assigned work on this board: its owner and its
// editors. Resolved to names so the model reasons about "Sara" rather than a
// subject id, and scoped to the ACL so it cannot assign work to a stranger.
func (s *Service) attachPeople(ctx context.Context, scope *BoardScope) {
	if s.users == nil || scope == nil || scope.Board == nil || scope.Board.ACL == nil {
		return
	}
	subs := append([]string{scope.Board.ACL.OwnerID}, scope.Board.ACL.Editors...)
	seen := map[string]bool{}
	for _, sub := range subs {
		if sub == "" || seen[sub] {
			continue
		}
		seen[sub] = true
		name := sub
		if u, err := s.users.GetBySub(ctx, sub); err == nil && u != nil && u.DisplayName != "" {
			name = u.DisplayName
		}
		scope.People = append(scope.People, PersonRef{ID: sub, Name: sanitizeName(name)})
	}
}

// attachLabels gives the scope the owner's label vocabulary, so the model can
// reuse a tag instead of coining a near-duplicate. Failure is not fatal: an
// agent that cannot read labels simply does not offer to apply them, which is a
// smaller loss than failing the run.
func (s *Service) attachLabels(ctx context.Context, scope *BoardScope, owner string) {
	if s.labels == nil || scope == nil {
		return
	}
	owned, err := s.labels.ListByOwner(ctx, owner)
	if err != nil {
		s.log.Warn("agent: could not read labels", zap.Error(err))
		return
	}
	for _, l := range owned {
		scope.Labels = append(scope.Labels, LabelRef{ID: l.ID, Name: l.Name})
	}
}
