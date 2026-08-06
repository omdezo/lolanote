package cli

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/agentmem"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
	repo "qomranote/backend/internal/repository/mongo"
	"qomranote/backend/internal/service"
)

// The Service-backed harness tier.
//
// The corpus drove agent.NewPlanner(...).Run directly, so agent.Service, the
// RunStore, the state machine, Apply, adjustments, Discard, Revert and the
// journal were never instantiated. That harness proves the PLANNER and was
// being read as proving the product — and every artefact of the learning loop
// (an adjustment, a discard, a state timestamp, a journal row, a correction
// record) belongs to the layer it skipped. A real-world failure whose defining
// property is "the person dropped four of these rows and fixed two by hand" was
// not expressible as a probe at all.
//
// It also skipped the write envelope. The one commit path in the corpus used a
// bare principal with no Delegation, and TransactionService gates its entire
// delegation block on that field being non-nil — so expiry, root containment,
// MaxOps and the per-op capability check were all silently off. The corpus
// never left the un-delegated world, which is precisely the axis the agent's
// safety architecture lives on.
//
// agentmem already implements RunStore and EventStore for exactly this purpose,
// so this tier is wiring rather than invention.

// evalOwner is the board's owner in every fixture — the principal the ACL names
// and the sub the seeds stamp on CreatedBy.
const evalOwner = "eval"

// evalCollaborator is the SECOND principal: an editor on a shared board who is
// not its owner.
//
// The corpus contained no ACL, no Editors and no second principal anywhere, so
// the entire multiplayer surface was unmeasured — every probe measured the solo
// world the product leaves the moment anybody presses Share.
const evalCollaborator = "eval-b"

// evalHarness is one throwaway product: repos, the write path, and a real
// agent.Service over them.
type evalHarness struct {
	elements *memory.ElementRepo
	runs     *agentmem.RunRepo
	events   *agentmem.EventRepo
	svc      *agent.Service
}

func newEvalHarness(provider cognition.Provider) *evalHarness {
	elements := memory.NewElementRepo()
	txnRepo := memory.NewTransactionRepo()
	access := service.NewAccessResolver(elements)
	txns := service.NewTransactionService(elements, txnRepo, access, nil,
		service.IDGenerator(repo.NewID), zap.NewNop())
	runs := agentmem.NewRunRepo()
	events := agentmem.NewEventRepo()
	return &evalHarness{
		elements: elements, runs: runs, events: events,
		svc: agent.NewService(agent.Config{
			Elements: elements, Txns: txns, TxnRepo: txnRepo, Access: access,
			Labels: memory.NewLabelRepo(),
			Runs:   runs, Events: events, Provider: provider,
			NewID: agent.IDGenerator(repo.NewID), Log: zap.NewNop(),
		}),
	}
}

// runProbeThroughService runs one probe through the whole loop and returns the
// same evalResult shape the plan-only tier returns, plus the durable record.
func runProbeThroughService(ctx context.Context, provider cognition.Provider, p probe) evalResult {
	h := newEvalHarness(provider)
	if p.Seed != nil {
		p.Seed(ctx, h.elements, evalBoardID)
	}

	actor := p.As
	if actor == "" {
		actor = evalOwner
	}
	principal := &domain.Principal{Sub: actor, Name: actor}
	h.seedHistory(ctx, p, actor)

	start := time.Now()
	run, err := h.svc.Create(ctx, principal, agent.CreateRequest{
		BoardID:  evalBoardID,
		Intent:   p.Prompt,
		Scope:    agent.ScopeBoard,
		Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		// Admission refusing is a RESULT on this tier, not a harness fault: a
		// probe about a collaborator who may not start a run is measuring
		// exactly this.
		return evalResult{Err: err, Elapsed: time.Since(start), Board: h.elements}
	}

	settled := h.await(ctx, run.ID, agent.StateProposed)
	res := evalResult{
		Elapsed: time.Since(start), Board: h.elements, Run: settled,
		Journal: h.journal(ctx, run.ID),
	}
	if settled == nil {
		res.Err = fmt.Errorf("run %s never settled", run.ID)
		return res
	}
	res.Usage = settled.Usage
	res.Plan = settled.Plan
	if settled.Verdict != nil {
		res.Verdict = *settled.Verdict
	}
	for _, ev := range res.Journal {
		if len(ev.Type) > 9 && ev.Type[:9] == "security." {
			res.Security = append(res.Security, ev.Message)
		}
	}
	if res.Plan == nil {
		return res
	}
	// Scope is recompiled for the graders that read it. The run compiled its
	// own; this is the same call against the same repo, and the graders only
	// ever read it.
	if scope, serr := agent.CompileScope(ctx, h.elements, settled.Task); serr == nil {
		res.Scope = scope
		res.Quality = agent.MeasurePlan(res.Plan, scope, settled.Task.Budget)
	}

	if p.Apply && settled.State == agent.StateProposed && len(res.Plan.Actions) > 0 {
		// nil variant: a probe applies the shape the run led with, which is what
		// every probe asserted against before alternatives existed.
		applied, aerr := h.svc.Apply(ctx, principal, run.ID, p.Adjustments, nil)
		res.ApplyErr = aerr
		// Applied means the board actually moved, which is not the same as the
		// call returning: a run that fails its postconditions is auto-reverted
		// and comes back without an error.
		res.Applied = aerr == nil && applied != nil &&
			(applied.State == agent.StateCompleted || applied.State == agent.StatePartial)
		if applied != nil {
			res.Run = applied
			res.Plan = applied.Plan
		}
		res.Journal = h.journal(ctx, run.ID)
	}
	return res
}

// seedHistory writes the probe's prior runs into the store as terminal runs,
// which is where the Service reads history from.
//
// The plan-only tier could hand a PriorRun list straight to the scope. Here it
// has to go through the store, and that is the point: history is fetched per
// TENANT, so a run seeded as the owner's is genuinely invisible to a
// collaborator, exactly as it is in the product.
func (h *evalHarness) seedHistory(ctx context.Context, p probe, actor string) {
	author := p.HistoryBy
	if author == "" {
		author = actor
	}
	for i, prior := range p.History {
		unmet := make([]agent.Unmet, 0, len(prior.Unmet))
		for _, u := range prior.Unmet {
			unmet = append(unmet, agent.Unmet{Request: u, Why: "the run was stopped with it staged"})
		}
		when := time.Now().UTC().Add(-time.Duration(i+2) * time.Minute)
		_ = h.runs.Insert(ctx, &agent.Run{
			ID:     fmt.Sprintf("%sp%02d", probeRunID(p.ID)[:22], i),
			Tenant: author,
			Task: agent.TaskSpec{
				Intent: prior.Intent, Owner: author, RootBoardID: evalBoardID,
				Scope: agent.ScopeBoard, Autonomy: agent.AutonomyPreview,
			},
			State:     agent.StateCompleted,
			Plan:      &agent.Plan{Summary: prior.Summary, Unmet: unmet},
			CreatedAt: when, UpdatedAt: when,
		})
	}
}

// await polls until the run reaches the wanted state or a terminal one. Runs
// execute in their own goroutine, so a probe observes the state machine rather
// than the call that started it.
func (h *evalHarness) await(ctx context.Context, runID string, want agent.RunState) *agent.Run {
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		run, err := h.runs.Get(ctx, runID)
		if err == nil && (run.State == want || run.State.Terminal()) {
			return run
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(25 * time.Millisecond):
		}
	}
	return nil
}

func (h *evalHarness) journal(ctx context.Context, runID string) []*agent.Event {
	events, err := h.events.List(ctx, runID, 0, 1000)
	if err != nil {
		return nil
	}
	return events
}
