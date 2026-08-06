package agent_test

import (
	"context"
	"testing"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
)

// The loop forces a REVIEW turn after every finish: the model is shown its own
// arrangement and, finding nothing worth changing, finishes again. That second
// finish is asked to look at the layout. It is not asked what the plan left
// undone, and it is not asked for a standing rule.
//
// The handler overwrote both fields with whatever the second call carried, which
// was nothing — so on every run that took the review turn, which is every run,
// `unmet` and `remember` came out empty. Neither field had a single assertion
// anywhere in the harness, so the failure was invisible, and it ran downstream of
// both: the rule card never rendered on any run, and "LEFT UNDONE" — which the
// digest's own comment calls the most actionable sentence it can carry — was
// blank for every continuation that inherited it.

func TestReviewTurn_DoesNotEraseWhatTheFirstFinishReported(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			{Name: "finish", Input: map[string]any{
				"summary": "Made a column.",
				"unmet": []map[string]any{
					{"request": "fill the columns", "why": "ran out of room"},
				},
			}},
		}},
		// The review turn: a bare summary, exactly what a real run produces.
		confirm(),
	)
	h.seedBoard(t, boardID, "a card")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Organize", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	final := h.awaitState(t, run.ID, agent.StateProposed, agent.StateCompleted, agent.StatePartial)
	if final.Plan == nil {
		t.Fatal("no plan")
	}
	if len(final.Plan.Unmet) != 1 || final.Plan.Unmet[0].Request != "fill the columns" {
		t.Fatalf("the review turn erased what the run said it had not done: %+v", final.Plan.Unmet)
	}
	if final.Plan.Unmet[0].Why != "ran out of room" {
		t.Errorf("the reason was lost: %+v", final.Plan.Unmet[0])
	}
}

// The other direction still has to work: a later finish that DOES speak replaces
// what the earlier one said, because the review turn can legitimately discover
// that something it had listed is now handled.
func TestReviewTurn_ASecondAnswerStillReplacesTheFirst(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			{Name: "finish", Input: map[string]any{
				"summary": "Made a column.",
				"unmet":   []map[string]any{{"request": "fill the columns", "why": "ran out of room"}},
			}},
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			{Name: "finish", Input: map[string]any{
				"summary": "Reviewed.",
				"unmet":   []map[string]any{{"request": "connect the stages", "why": "not asked for"}},
			}},
		}},
	)
	h.seedBoard(t, boardID, "a card")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Organize", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	final := h.awaitState(t, run.ID, agent.StateProposed, agent.StateCompleted, agent.StatePartial)
	if len(final.Plan.Unmet) != 1 || final.Plan.Unmet[0].Request != "connect the stages" {
		t.Fatalf("a second, non-empty answer did not replace the first: %+v", final.Plan.Unmet)
	}
}
