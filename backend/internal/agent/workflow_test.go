package agent_test

import (
	"context"
	"fmt"
	"testing"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// Designing a process, end to end.
//
// Before this the agent could create cards and draw arrows between them and had
// no idea what shape that made: five connected steps were packed into rows like
// any other five cards, so the picture said nothing the text did not. A process
// is a shape, and the shape is the point.
func TestWorkflow_DesignsAProcessLeftToRight(t *testing.T) {
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	// design_as is action-free, so the ids follow the creates in order.
	step := func(i int) string { return agent.ActionID(runIDGuess, i) }

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("design_as", map[string]any{"shape": "flow"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_note", map[string]any{"parentId": boardID, "text": "Draft"}),
			call("create_note", map[string]any{"parentId": boardID, "text": "Review"}),
			call("create_note", map[string]any{"parentId": boardID, "text": "Publish"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("connect", map[string]any{"fromId": step(0), "toId": step(1), "relation": "leads_to"}),
			call("connect", map[string]any{"fromId": step(1), "toId": step(2), "relation": "leads_to"}),
		}},
		finish("A three-step process."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Design our publishing process", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.awaitState(t, run.ID, agent.StateProposed)

	if proposed.Plan.Shape != agent.LayoutFlow {
		t.Fatalf("plan shape = %q, want flow", proposed.Plan.Shape)
	}

	// The three steps must advance to the right, each strictly after the last.
	pos := map[string]*agent.ColumnBox{}
	for _, a := range proposed.Plan.Actions {
		if a.Kind == agent.ActCreateNote {
			if a.Position == nil {
				t.Fatalf("step %q got no position", a.Text)
			}
			pos[a.Text] = a.Position
		}
	}
	for _, pair := range [][2]string{{"Draft", "Review"}, {"Review", "Publish"}} {
		before, after := pos[pair[0]], pos[pair[1]]
		if before == nil || after == nil {
			t.Fatalf("missing a step: %v", pos)
		}
		if after.X <= before.X {
			t.Errorf("%q is at x=%v and %q at x=%v — a process must advance to the right",
				pair[0], before.X, pair[1], after.X)
		}
		if after.X < before.X+before.Width {
			t.Errorf("%q overlaps %q horizontally", pair[1], pair[0])
		}
	}
	// A straight chain is one row: nothing branches, so nothing should be
	// pushed off the line.
	if pos["Draft"].Y != pos["Publish"].Y {
		t.Errorf("an unbranched chain was split across rows (y %v vs %v)",
			pos["Draft"].Y, pos["Publish"].Y)
	}

	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// And the connectors commit as real, styled lines.
	kids, _ := h.elements.Children(ctx, domain.ElementFilter{ParentID: boardID})
	lines := 0
	for _, el := range kids {
		if el.Type != domain.TypeLine {
			continue
		}
		lines++
		if el.Content["fromId"] == "" || el.Content["toId"] == "" {
			t.Error("a connector committed with a loose end")
		}
		if el.Content["endArrow"] != true {
			t.Error("a leads_to connector committed without a forward arrowhead")
		}
	}
	if lines != 2 {
		t.Fatalf("got %d connectors on the board, want 2", lines)
	}
}

// A blocker has to be visible without reading it. Every arrow used to be the
// same grey arrow, so the one relationship that explains why nothing is moving
// looked exactly like the ones that were fine.
func TestWorkflow_RelationDecidesHowTheLineIsDrawn(t *testing.T) {
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	step := func(i int) string { return agent.ActionID(runIDGuess, i) }

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_note", map[string]any{"parentId": boardID, "text": "Legal sign-off"}),
			call("create_note", map[string]any{"parentId": boardID, "text": "Launch"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("connect", map[string]any{
				"fromId": step(0), "toId": step(1),
				"relation": "blocks", "label": "waiting",
			}),
		}},
		finish("One blocker."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Show what is blocking launch", Autonomy: agent.AutonomyPreview,
	})
	h.awaitState(t, run.ID, agent.StateProposed)
	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	kids, _ := h.elements.Children(ctx, domain.ElementFilter{ParentID: boardID})
	var line *domain.Element
	for _, el := range kids {
		if el.Type == domain.TypeLine {
			line = el
		}
	}
	if line == nil {
		t.Fatal("no connector was committed")
	}
	plain := agent.StyleForTest(agent.RelationLeadsTo)
	if line.Content["color"] == plain.Color {
		t.Error("a blocker is drawn identically to an ordinary step, so it cannot be seen")
	}
	if w, _ := line.Content["weight"].(int); w <= plain.Weight {
		t.Errorf("blocker weight %v is not heavier than an ordinary connector", line.Content["weight"])
	}
	if line.Content["label"] != "waiting" {
		t.Errorf("label = %v, want it carried through", line.Content["label"])
	}
}

// A shape declared but never connected must not silently produce a diagram of
// nothing — it falls back to ordinary packing.
func TestWorkflow_UnconnectedShapeFallsBackToPacking(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("design_as", map[string]any{"shape": "flow"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_note", map[string]any{"parentId": boardID, "text": "One"}),
			call("create_note", map[string]any{"parentId": boardID, "text": "Two"}),
		}},
		finish("Two loose notes."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Two notes", Autonomy: agent.AutonomyPreview,
	})
	proposed := h.awaitState(t, run.ID, agent.StateProposed)

	for _, a := range proposed.Plan.Actions {
		if a.Kind == agent.ActCreateNote && a.Position == nil {
			t.Fatalf("note %q got no position when the shape could not be drawn", a.Text)
		}
	}
}

// DA3: arrange could not see what the same run had just created.
//
// resolveExisting already fell through to the staged overlay, so every other
// id-taking tool worked on a card staged a moment earlier — and ComputeArrangement
// read scope.Elements directly and answered "%s is not on this board". So
// "create five steps then arrange them" half worked: the cards appeared, the
// connectors appeared, and the layout silently did not, which is more confusing
// than a refusal would have been.
//
// Asserted as a COLUMN rather than a row, because the default shelf pack already
// lays creates out left to right: a row assertion would pass just as happily
// against an arrange that failed and was ignored.
func TestArrange_ReachesElementsThisRunCreated(t *testing.T) {
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	step := func(i int) string { return agent.ActionID(runIDGuess, i) }

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_note", map[string]any{"parentId": boardID, "text": "First"}),
			call("create_note", map[string]any{"parentId": boardID, "text": "Second"}),
			call("create_note", map[string]any{"parentId": boardID, "text": "Third"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("arrange", map[string]any{
				"elementIds": []any{step(0), step(1), step(2)}, "layout": "column",
			}),
		}},
		finish("Three notes, stacked."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Add three notes and stack them", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.awaitState(t, run.ID, agent.StateProposed)

	pos := map[string]*agent.ColumnBox{}
	for _, a := range proposed.Plan.Actions {
		if a.Kind == agent.ActCreateNote {
			if a.Position == nil {
				t.Fatalf("note %q got no position at all", a.Text)
			}
			pos[a.Text] = a.Position
		}
	}
	if len(pos) != 3 {
		t.Fatalf("got %d created notes, want 3", len(pos))
	}
	for _, pair := range [][2]string{{"First", "Second"}, {"Second", "Third"}} {
		above, below := pos[pair[0]], pos[pair[1]]
		if below.Y <= above.Y {
			t.Errorf("%q is at y=%v and %q at y=%v — a column stacks downwards",
				pair[0], above.Y, pair[1], below.Y)
		}
		if above.X != below.X {
			t.Errorf("a column must share one x: %q at %v, %q at %v",
				pair[0], above.X, pair[1], below.X)
		}
	}

	// And it commits: the arranger writing onto the create action means one op
	// per card, not a create followed by a move that the review list counts twice.
	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	kids, _ := h.elements.Children(ctx, domain.ElementFilter{ParentID: boardID})
	stacked := map[string]domain.Point{}
	for _, el := range kids {
		if el.Type == domain.TypeCard && el.Location.Section == domain.SectionCanvas {
			stacked[fmt.Sprint(el.Content["textPreview"])] = el.Location.Position
		}
	}
	if len(stacked) != 3 {
		t.Fatalf("committed %d notes, want 3", len(stacked))
	}
	if stacked["First"].X != stacked["Third"].X || stacked["Third"].Y <= stacked["First"].Y {
		t.Fatalf("the arrangement did not survive the commit: %v", stacked)
	}
}
