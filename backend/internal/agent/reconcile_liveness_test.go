package agent_test

import (
	"context"
	"testing"
	"time"

	"qomranote/backend/internal/agent"
)

// Reconcile force-failed every unfinished run on every tick, and the tick is
// five minutes. A run that was simply STILL WORKING when a sweep landed was
// killed mid-flight and told "the server restarted while this run was in
// progress" — which had not happened — while its goroutine carried on spending
// against a row that now read FAILED.
//
// At the old three-minute deadline that was a lottery. Raising the deadline to
// eight minutes to fix step starvation made it a certainty: any run that used
// its new budget crossed a tick. The timeout fix had made the timeout worse.
//
// Liveness is UpdatedAt, which the run loop stamps every step.
func TestReconcile_LeavesALiveRunAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.seedBoard(t, boardID, "a note")

	run := seedUnfinished(t, h, agent.StateRunning, time.Now().UTC())

	h.svc.Reconcile(ctx)

	after, err := h.runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.State != agent.StateRunning {
		t.Fatalf("a run that had just made progress was reconciled to %s — "+
			"the sweep cannot tell working from abandoned", after.State)
	}
}

// And the orphan it exists for: silent well past its own deadline, so the
// process that owned it is gone.
func TestReconcile_ClearsAnAbandonedRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.seedBoard(t, boardID, "a note")

	stale := time.Now().UTC().Add(-2 * time.Hour)
	run := seedUnfinished(t, h, agent.StateRunning, stale)

	h.svc.Reconcile(ctx)

	after, err := h.runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.State != agent.StateFailed {
		t.Fatalf("an abandoned run was left holding the board's run slot (%s)", after.State)
	}
	// And the reason must not claim a restart that may never have happened.
	if after.Plan != nil && after.Plan.Summary == "the server restarted while this run was in progress" {
		t.Error("the failure still asserts a server restart it cannot know about")
	}
}

func seedUnfinished(t *testing.T, h *harness, state agent.RunState, updated time.Time) *agent.Run {
	t.Helper()
	run := &agent.Run{
		ID:     "5a5a5a5a5a5a5a5a5a5a5a01",
		Tenant: h.principal.Sub,
		Task: agent.TaskSpec{
			Intent: "Organize", Owner: h.principal.Sub, RootBoardID: boardID,
			Scope: agent.ScopeBoard, Autonomy: agent.AutonomyPreview,
			Budget: agent.DefaultBudget(),
		},
		State:     state,
		Active:    true,
		CreatedAt: updated,
		UpdatedAt: updated,
	}
	if err := h.runs.Insert(context.Background(), run); err != nil {
		t.Fatalf("seed the run: %v", err)
	}
	return run
}
