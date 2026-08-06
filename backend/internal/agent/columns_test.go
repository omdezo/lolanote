package agent_test

import (
	"context"
	"testing"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// "Make three columns" is about the most common thing anyone asks, and they
// came out stacked on top of each other rather than side by side.
//
// This runs the whole path — plan, compile, commit — and reads the positions
// back off the elements, because every layer between the layout pass and the
// canvas is a place the geometry can be dropped.
func TestColumns_LandSideBySideNotStacked(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "To do"}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Doing"}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Done"}),
		}},
		finish("Three columns."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Make three columns", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.awaitState(t, run.ID, agent.StateProposed)

	// The plan itself must already say where they go: the preview a person
	// approves has to be positioned identically to what commits.
	seenX := map[float64]bool{}
	for _, a := range proposed.Plan.Actions {
		if a.Kind != agent.ActCreateColumn {
			continue
		}
		if a.Position == nil {
			t.Fatalf("column %q has no position in the plan", a.Title)
		}
		if seenX[a.Position.X] {
			t.Errorf("column %q shares x=%v with another — they will stack", a.Title, a.Position.X)
		}
		seenX[a.Position.X] = true
	}
	if len(seenX) != 3 {
		t.Fatalf("got %d distinct x positions for 3 columns", len(seenX))
	}

	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// And the geometry has to survive the compile and the commit.
	kids, err := h.elements.Children(ctx, domain.ElementFilter{ParentID: boardID})
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	var cols []*domain.Element
	for _, el := range kids {
		if el.Type == domain.TypeColumn {
			cols = append(cols, el)
		}
	}
	if len(cols) != 3 {
		t.Fatalf("got %d columns on the board, want 3", len(cols))
	}

	for i, a := range cols {
		if a.Location.Width <= 0 {
			t.Errorf("column %q committed with width %v", a.Title(), a.Location.Width)
		}
		for _, b := range cols[i+1:] {
			if a.Location.Position.X == b.Location.Position.X &&
				a.Location.Position.Y == b.Location.Position.Y {
				t.Errorf("columns %q and %q committed to the same point (%v,%v)",
					a.Title(), b.Title(), a.Location.Position.X, a.Location.Position.Y)
			}
			// Side by side means they do not overlap horizontally on a shared row.
			if a.Location.Position.Y == b.Location.Position.Y {
				gap := b.Location.Position.X - a.Location.Position.X
				if gap < 0 {
					gap = -gap
				}
				if gap < a.Location.Width {
					t.Errorf("columns %q and %q overlap: %v apart but %v wide",
						a.Title(), b.Title(), gap, a.Location.Width)
				}
			}
		}
	}
}
