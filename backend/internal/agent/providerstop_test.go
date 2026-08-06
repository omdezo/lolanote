package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
)

// awaitTerminal polls until the run stops, whatever it stops as. The existing
// awaitState fails the moment a run goes terminal, which is exactly the state
// this test is about.
func awaitTerminal(t *testing.T, h *harness, runID string) *agent.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := h.svc.Get(context.Background(), h.principal, runID)
		if err == nil && run.State.Terminal() {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s never finished", runID)
	return nil
}

// A run cut by the provider used to return NOTHING.
//
// The cost cap broke out of the loop and came back as a reviewable prefix with
// a Continue button; the deadline and a provider outage returned early and threw
// away every staged action along with the money already spent producing them.
// From the person's side the two runs were identical — same request, same board,
// same eight minutes — and one of them produced a red card and no work.
//
// This asserts the plan survives the outage, and that its summary names the
// provider rather than a budget nobody reached.
func TestProviderStop_APartialPlanSurvivesAnOutage(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Casting"}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Locations"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Sound"}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Editing"}),
		}},
		// The provider dies on the next turn, with four changes already staged.
		cognition.ScriptedStep{Err: errors.New("provider returned 503")},
	)
	h.seedBoard(t, boardID, "a note")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Build the production structure", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	done := awaitTerminal(t, h, run.ID)
	if done.Plan == nil {
		t.Fatalf("the outage threw the plan away: state=%s reason=%q", done.State, done.Reason)
	}
	if len(done.Plan.Actions) != 4 {
		t.Fatalf("staged work was lost: %d action(s) kept, want 4", len(done.Plan.Actions))
	}
	// The summary has to name what actually stopped it. "Ran out of room" for an
	// outage sends somebody to raise a limit they never approached.
	if !strings.Contains(strings.ToLower(done.Plan.Summary), "ai service") {
		t.Errorf("the summary does not name the provider: %q", done.Plan.Summary)
	}
	// And the prefix is continuable, which is the whole point of keeping it.
	if len(done.Plan.Unmet) == 0 {
		t.Errorf("a truncated plan with nothing unmet cannot be continued: %+v", done.Plan)
	}
}
