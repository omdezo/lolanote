package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
)

// The acceptance line for the review loop, run through the whole harness: a
// plan that files empty columns into a nested board is shown the STOP nudge, and
// the review it takes to see it is a visible step of the run.
//
// The "complete" run staged eighteen near-duplicate empty columns into
// sub-boards and shipped them. The guard for exactly that plan existed; the run
// never reached it.
func TestReviewReach_EmptyColumnsInANestedBoardAreStopped(t *testing.T) {
	// Ids are deterministic from the run, so the fixture can name what an
	// earlier step creates — exactly as a real model would, from tool output.
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	nested := agent.ActionID(runIDGuess, 0)

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_board", map[string]any{"parentId": boardID, "title": "Pre-Production"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": nested, "title": "Casting"}),
			call("create_column", map[string]any{"parentId": nested, "title": "Locations"}),
			call("create_column", map[string]any{"parentId": nested, "title": "Permits"}),
		}},
		finish("Set up pre-production."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note", "another note")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "set up film production", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if run.ID != runIDGuess {
		t.Fatalf("run id assumption broken: got %s", run.ID)
	}
	proposed := h.awaitState(t, run.ID, agent.StateProposed)

	var nudged bool
	for _, req := range h.provider.Calls {
		for _, m := range req.Messages {
			if strings.Contains(m.Text, "STOP —") {
				nudged = true
			}
		}
	}
	if !nudged {
		t.Error("a plan of three empty columns inside a nested board was never told to stop")
	}

	var reviewed bool
	for _, ev := range h.events.All() {
		if ev.Type == agent.EvStepFinished && strings.Contains(ev.Message, "reviewing the plan") {
			reviewed = true
		}
	}
	if !reviewed {
		t.Error("the review step does not appear in the events stream")
	}

	// And the person is told, since the run finished hollow anyway.
	if len(proposed.Plan.Unmet) == 0 {
		t.Error("three empty columns shipped with nothing said about them")
	}
}

// Nothing on the root canvas at all: content created inside a column that
// already exists. This is the plan shape the gate silently dropped — no
// coordinates anywhere, so no arrangement, so no review of any kind.
func TestReviewReach_FilingIntoAnExistingColumnStillGetsReviewed(t *testing.T) {
	column := fmt.Sprintf("%024x", 0xc0100000)
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_note", map[string]any{"parentId": column, "text": "lock the script"}),
			call("create_note", map[string]any{"parentId": column, "text": "book the crew"}),
		}},
		finish("Two cards into the backlog."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	h.seedColumn(t, boardID, column, "Backlog")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "add the first two tasks", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.awaitState(t, run.ID, agent.StateProposed)

	var reviewed bool
	for _, req := range h.provider.Calls {
		for _, m := range req.Messages {
			if strings.Contains(m.Text, "Before this reaches the user") {
				reviewed = true
			}
		}
	}
	if !reviewed {
		t.Error("a plan that files everything into an existing column was never reviewed")
	}
}
