package agent

import (
	"testing"

	"qomranote/backend/internal/domain"
)

func columnAt(id string, x, y float64) *domain.Element {
	return &domain.Element{
		ID: id, Type: domain.TypeColumn,
		Content: domain.Content{"title": id},
		Location: domain.Location{
			ParentID: "board-1", Section: domain.SectionCanvas,
			Position: domain.Point{X: x, Y: y}, Width: ColumnWidth, Height: 400,
		},
	}
}

func scopeWithColumns(cols ...*domain.Element) *BoardScope {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "board-1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Occupied: Rect{Empty: true},
	}
	for _, c := range cols {
		scope.Elements[c.ID] = c
		scope.Occupied = scope.Occupied.include(c)
	}
	return scope
}

func threeNewColumns() *Plan {
	return &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "n1", ParentID: "board-1", Title: "To do"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "n2", ParentID: "board-1", Title: "Doing"},
		{Seq: 2, Kind: ActCreateColumn, ElementID: "n3", ParentID: "board-1", Title: "Done"},
	}}
}

// Asking for three more columns on a board that already has some put them in a
// NEW ROW UNDERNEATH the existing ones, because layout always opened a fresh
// band below everything on the canvas.
//
// On a kanban board that reads exactly as the complaint: the columns stack
// instead of lining up. New columns belong at the END OF THE ROW they join —
// that is what a column is for.
func TestLayout_NewColumnsExtendAnExistingRow(t *testing.T) {
	existing := columnAt("old-1", 0, 0)
	scope := scopeWithColumns(existing)

	p := threeNewColumns()
	LayoutPlan(p, scope)

	for _, a := range p.Actions {
		if a.Position == nil {
			t.Fatalf("column %q got no position", a.Title)
		}
		if a.Position.Y != existing.Location.Position.Y {
			t.Errorf("column %q is at y=%v; the row it joins is at y=%v — a new column belongs "+
				"beside the others, not in a band beneath them",
				a.Title, a.Position.Y, existing.Location.Position.Y)
		}
		if a.Position.X <= existing.Location.Position.X {
			t.Errorf("column %q is at x=%v, not after the existing column at x=%v",
				a.Title, a.Position.X, existing.Location.Position.X)
		}
	}

	// And they must not land on each other.
	seen := map[float64]bool{}
	for _, a := range p.Actions {
		if seen[a.Position.X] {
			t.Errorf("two columns share x=%v", a.Position.X)
		}
		seen[a.Position.X] = true
	}
}

// Several existing columns: the new ones continue past the LAST of them.
func TestLayout_ContinuesPastTheWholeRow(t *testing.T) {
	scope := scopeWithColumns(
		columnAt("old-1", 0, 0),
		columnAt("old-2", 344, 0),
		columnAt("old-3", 688, 0),
	)
	p := threeNewColumns()
	LayoutPlan(p, scope)

	rightEdge := 688.0 + ColumnWidth
	for _, a := range p.Actions {
		if a.Position.X < rightEdge {
			t.Errorf("column %q at x=%v overlaps the existing row, which ends at x=%v",
				a.Title, a.Position.X, rightEdge)
		}
	}
}

// A board whose canvas content is NOT a row of columns — loose cards, a sketch —
// keeps the old behaviour: a fresh band below, which is right, because there is
// no row to join.
func TestLayout_StillOpensANewBandWhenThereIsNoRowToJoin(t *testing.T) {
	card := &domain.Element{
		ID: "card-1", Type: domain.TypeCard,
		Location: domain.Location{
			ParentID: "board-1", Section: domain.SectionCanvas,
			Position: domain.Point{X: 0, Y: 0}, Width: CardWidth, Height: 120,
		},
	}
	scope := scopeWithColumns()
	scope.Elements[card.ID] = card
	scope.Occupied = scope.Occupied.include(card)

	p := threeNewColumns()
	LayoutPlan(p, scope)

	if p.Actions[0].Position.Y <= card.Location.Position.Y {
		t.Errorf("new columns at y=%v collide with the loose content at y=%v",
			p.Actions[0].Position.Y, card.Location.Position.Y)
	}
}

// An empty board starts at the origin.
func TestLayout_EmptyBoardStartsAtTheOrigin(t *testing.T) {
	p := threeNewColumns()
	LayoutPlan(p, scopeWithColumns())

	if p.Actions[0].Position.X != 0 || p.Actions[0].Position.Y != 0 {
		t.Errorf("first column at (%v,%v), want the origin",
			p.Actions[0].Position.X, p.Actions[0].Position.Y)
	}
	if p.Actions[1].Position.Y != p.Actions[2].Position.Y {
		t.Error("three columns on an empty board must share a row")
	}
}

// The failure a user hit: "make 3 columns for film production" produced a new
// board with all three columns stacked at the origin.
//
// LayoutPlan only ever laid out the ROOT board's canvas, so anything the plan
// created inside a board the SAME PLAN created got no position and no width —
// which is not a bad arrangement, it is no arrangement. Nesting is the normal
// way the agent answers "set up X", so this hit the common case.
func TestLayout_PositionsEachCanvasThePlanCreates(t *testing.T) {
	scope := scopeWithColumns()
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateBoard, ElementID: "nb", ParentID: "board-1", Title: "Film Production"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c1", ParentID: "nb", Title: "Pre-Production"},
		{Seq: 2, Kind: ActCreateColumn, ElementID: "c2", ParentID: "nb", Title: "Production"},
		{Seq: 3, Kind: ActCreateColumn, ElementID: "c3", ParentID: "nb", Title: "Post-Production"},
	}}

	LayoutPlan(p, scope)

	for _, a := range p.Actions {
		if a.Position == nil {
			t.Fatalf("%q got no position at all", a.Title)
		}
		if a.Position.Width <= 0 {
			t.Errorf("%q got width %v", a.Title, a.Position.Width)
		}
	}

	// The three inside the new board must sit side by side, not on each other.
	inside := []*Action{&p.Actions[1], &p.Actions[2], &p.Actions[3]}
	for i, a := range inside {
		for _, b := range inside[i+1:] {
			if a.Position.X == b.Position.X && a.Position.Y == b.Position.Y {
				t.Errorf("%q and %q are stacked at the same point (%v,%v)",
					a.Title, b.Title, a.Position.X, a.Position.Y)
			}
		}
	}
	for _, a := range inside {
		if a.Position.Y != inside[0].Position.Y {
			t.Errorf("%q is on a different row from the others", a.Title)
		}
	}
}

// A nested board's canvas starts empty, so its contents begin at its own origin
// rather than inheriting the parent board's occupied space.
func TestLayout_NestedCanvasStartsAtItsOwnOrigin(t *testing.T) {
	far := cardAt("far", 4000, 4000, 280, 400)
	scope := scopeWithColumns()
	scope.Elements[far.ID] = far
	scope.Occupied = scope.Occupied.include(far)

	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateBoard, ElementID: "nb", ParentID: "board-1", Title: "New"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "nb", Text: "inside"},
	}}
	LayoutPlan(p, scope)

	if got := p.Actions[1].Position.Y; got > 1000 {
		t.Errorf("a note inside a brand-new board was placed at y=%v, "+
			"inheriting the parent board's occupied space", got)
	}
}
