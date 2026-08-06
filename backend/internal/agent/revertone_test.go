package agent_test

import (
	"context"
	"fmt"
	"testing"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// Revert used to be all-or-nothing. A run that got four things right and one
// thing wrong had to be thrown away whole, so in practice people kept the wrong
// change rather than lose the right ones.
func TestAgent_RevertOne(t *testing.T) {
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	keepColumn := agent.ActionID(runIDGuess, 0)
	dropColumn := agent.ActionID(runIDGuess, 1)

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Keep"}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Drop"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_note", map[string]any{"parentId": dropColumn, "text": "inside the doomed column"}),
			call("create_note", map[string]any{"parentId": keepColumn, "text": "inside the surviving one"}),
		}},
		finish("Two columns."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Two columns", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.awaitState(t, run.ID, agent.StateProposed)
	applied, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.State != agent.StateCompleted {
		t.Fatalf("state = %s (%s)", applied.State, applied.Reason)
	}

	after, err := h.svc.RevertOne(ctx, h.principal, run.ID, []string{dropColumn})
	if err != nil {
		t.Fatalf("revert one: %v", err)
	}

	if !gone(t, h, dropColumn) {
		t.Error("the reverted column is still on the board")
	}
	// The cascade: a note inside a reverted container cannot be left behind,
	// or it is stranded in something that no longer exists.
	for _, id := range childIDsOf(t, h, run, dropColumn) {
		if !gone(t, h, id) {
			t.Errorf("element %s was inside the reverted column and survived it", id)
		}
	}
	if gone(t, h, keepColumn) {
		t.Fatal("reverting one column took the other with it — the whole point was to keep it")
	}
	for _, id := range childIDsOf(t, h, run, keepColumn) {
		if gone(t, h, id) {
			t.Errorf("element %s was inside the surviving column and was removed", id)
		}
	}

	// The run is still COMPLETED: part of it stands, so there is still
	// something a whole-run Undo would act on.
	if after.State != agent.StateCompleted {
		t.Errorf("state = %s, want COMPLETED while part of the run still stands", after.State)
	}
	if len(after.RevertedElementIDs) == 0 {
		t.Error("the run does not record what was undone, so the UI cannot grey the row")
	}

	// Pressing it again is a no-op, not a second compensating write and not an
	// error. Double-click is the ordinary case, not the exotic one.
	txnsBefore := h.txns.Count()
	if _, err := h.svc.RevertOne(ctx, h.principal, run.ID, []string{dropColumn}); err != nil {
		t.Fatalf("second revert of the same element: %v", err)
	}
	if h.txns.Count() != txnsBefore {
		t.Errorf("reverting the same element twice wrote another transaction (%d → %d)",
			txnsBefore, h.txns.Count())
	}
}

// The case the cascade actually exists for.
//
// Deleting a container already takes its contents with it, so a note the run
// CREATED inside a reverted column disappears either way. An element that was
// already on the board and was only MOVED in is different: if its move is not
// also inverted, the column's delete cascades over it and a card the user has
// had for months lands in the trash. It has to be put back where it was.
func TestAgent_RevertOne_ReturnsMovedElementsRatherThanTrashingThem(t *testing.T) {
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	column := agent.ActionID(runIDGuess, 0)

	existing := cardIDs(boardID, 1)[0]
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Sorted"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("move_element", map[string]any{"elementId": existing, "parentId": column}),
		}},
		finish("Filed one card."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a card the user already had")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "File this", Autonomy: agent.AutonomyPreview,
	})
	h.awaitState(t, run.ID, agent.StateProposed)
	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	moved, err := h.elements.Get(ctx, existing)
	if err != nil || moved.Location.ParentID != column {
		t.Fatalf("fixture problem: the card was not moved into the column (%+v)", moved)
	}

	after, err := h.svc.RevertOne(ctx, h.principal, run.ID, []string{column})
	if err != nil {
		t.Fatalf("revert one: %v", err)
	}

	back, err := h.elements.Get(ctx, existing)
	if err != nil {
		t.Fatalf("the user's own card is no longer readable: %v", err)
	}
	if back.IsDeleted() {
		t.Fatal("undoing a column the agent made sent the user's own card to the trash")
	}
	if back.Location.ParentID != boardID {
		t.Errorf("parentId = %q, want the card returned to the board it came from", back.Location.ParentID)
	}
	// And the bookkeeping has to include it, or the run can never be considered
	// fully reverted.
	if !listHas(after.RevertedElementIDs, existing) {
		t.Errorf("revertedElementIds = %v, missing the moved card", after.RevertedElementIDs)
	}
}

func listHas(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Undoing the last standing piece of a run leaves nothing to undo, so the run
// itself must become REVERTED — otherwise the card keeps offering an Undo that
// does nothing.
func TestAgent_RevertOne_LastPieceRevertsTheRun(t *testing.T) {
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	only := agent.ActionID(runIDGuess, 0)

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Only"}),
		}},
		finish("One column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "One column", Autonomy: agent.AutonomyPreview,
	})
	h.awaitState(t, run.ID, agent.StateProposed)
	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	after, err := h.svc.RevertOne(ctx, h.principal, run.ID, []string{only})
	if err != nil {
		t.Fatalf("revert one: %v", err)
	}
	if after.State != agent.StateReverted {
		t.Errorf("state = %s, want REVERTED once nothing the run did is still standing", after.State)
	}
}

// An empty list means the whole run — every client that predates this sends no
// body at all, and that must keep meaning what it always meant.
func TestAgent_RevertOne_EmptyListRevertsEverything(t *testing.T) {
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	col := agent.ActionID(runIDGuess, 0)

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Anything"}),
		}},
		finish("One column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "One column", Autonomy: agent.AutonomyPreview,
	})
	h.awaitState(t, run.ID, agent.StateProposed)
	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	after, err := h.svc.RevertOne(ctx, h.principal, run.ID, nil)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if after.State != agent.StateReverted {
		t.Fatalf("state = %s, want REVERTED", after.State)
	}
	if !gone(t, h, col) {
		t.Error("the column survived a whole-run revert")
	}
}

// gone reports whether an element is no longer on the board.
func gone(t *testing.T, h *harness, id string) bool {
	t.Helper()
	el, err := h.elements.Get(context.Background(), id)
	return err != nil || el.IsDeleted()
}

// childIDsOf lists the elements this run's plan parented beneath one element.
func childIDsOf(t *testing.T, h *harness, run *agent.Run, parent string) []string {
	t.Helper()
	fresh, err := h.svc.Get(context.Background(), h.principal, run.ID)
	if err != nil || fresh.Plan == nil {
		t.Fatalf("read run: %v", err)
	}
	var out []string
	for _, a := range fresh.Plan.Actions {
		if a.ParentID == parent {
			out = append(out, a.ElementID)
		}
	}
	if len(out) == 0 {
		t.Fatalf("fixture problem: nothing in the plan is parented to %s", parent)
	}
	return out
}

var _ = domain.TypeColumn
