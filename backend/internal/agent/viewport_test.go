package agent

import (
	"testing"

	"qomranote/backend/internal/domain"
)

func cardAt(id string, x, y, w, h float64) *domain.Element {
	return &domain.Element{
		ID: id, Type: domain.TypeCard,
		Location: domain.Location{
			ParentID: "board-1", Section: domain.SectionCanvas,
			Position: domain.Point{X: x, Y: y}, Width: w, Height: h,
		},
	}
}

func scopeSeen(v *Viewport, els ...*domain.Element) *BoardScope {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "board-1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Occupied: Rect{Empty: true},
		Viewport: v,
	}
	for _, e := range els {
		scope.Elements[e.ID] = e
		scope.Occupied = scope.Occupied.include(e)
	}
	return scope
}

func twoNotes() *Plan {
	return &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateNote, ElementID: "n1", ParentID: "board-1", Text: "one"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n2", ParentID: "board-1", Text: "two"},
	}}
}

// On a large board, "below everything" means "thousands of pixels from the part
// being worked on". The person watches nothing appear and concludes the agent
// ignored them.
func TestViewport_NewContentLandsWhereThePersonIsLooking(t *testing.T) {
	// A wall of content far from the view.
	far := cardAt("far", 0, 0, 280, 4000)
	view := &Viewport{X: 6000, Y: 3000, Width: 1400, Height: 900}

	p := twoNotes()
	LayoutPlan(p, scopeSeen(view, far))

	for _, a := range p.Actions {
		if a.Position.X < view.X || a.Position.X > view.X+view.Width {
			t.Errorf("note at x=%v is outside the visible region [%v..%v]",
				a.Position.X, view.X, view.X+view.Width)
		}
		if a.Position.Y < view.Y || a.Position.Y > view.Y+view.Height {
			t.Errorf("note at y=%v is outside the visible region [%v..%v]",
				a.Position.Y, view.Y, view.Y+view.Height)
		}
	}
}

// Landing where they are looking must not mean landing ON what they are
// looking at.
func TestViewport_StepsBelowWhatIsAlreadyInView(t *testing.T) {
	inView := cardAt("seen", 6100, 3100, 280, 300)
	view := &Viewport{X: 6000, Y: 3000, Width: 1400, Height: 1600}

	p := twoNotes()
	LayoutPlan(p, scopeSeen(view, inView))

	bottom := inView.Location.Position.Y + inView.Location.Height
	for _, a := range p.Actions {
		if a.Position.Y < bottom {
			t.Errorf("note at y=%v overlaps the card occupying %v..%v",
				a.Position.Y, inView.Location.Position.Y, bottom)
		}
	}
}

// Content outside the view is irrelevant to where new content goes — that is
// the whole point of using the viewport.
func TestViewport_IgnoresContentOutsideTheView(t *testing.T) {
	elsewhere := cardAt("elsewhere", 0, 0, 280, 9000)
	view := &Viewport{X: 6000, Y: 200, Width: 1400, Height: 900}

	p := twoNotes()
	LayoutPlan(p, scopeSeen(view, elsewhere))

	if p.Actions[0].Position.Y > view.Y+view.Height {
		t.Errorf("y=%v; a tall element far to the left pushed new content out of view",
			p.Actions[0].Position.Y)
	}
}

// No viewport — an older client, or a run started from somewhere without a
// canvas — keeps the previous behaviour exactly.
func TestViewport_AbsentFallsBackToBelowEverything(t *testing.T) {
	existing := cardAt("c", 100, 100, 280, 200)
	p := twoNotes()
	LayoutPlan(p, scopeSeen(nil, existing))

	if p.Actions[0].Position.Y <= 300 {
		t.Errorf("y=%v, want below the existing content ending at 300", p.Actions[0].Position.Y)
	}
}

// A degenerate box — a client that measured before layout settled — is treated
// as no viewport at all rather than as a one-pixel target.
func TestViewport_RejectsNonsense(t *testing.T) {
	for _, v := range []*Viewport{
		{X: 0, Y: 0, Width: 0, Height: 0},
		{X: 0, Y: 0, Width: 10, Height: 10},
		{X: 1e9, Y: 1e9, Width: 1400, Height: 900},
	} {
		if v.Valid() {
			t.Errorf("viewport %+v was accepted", v)
		}
	}
	if !(&Viewport{X: -400, Y: -200, Width: 1400, Height: 900}).Valid() {
		t.Error("a legitimate negative-origin viewport was rejected; boards extend both ways")
	}
}

// A column still joins the row of columns wherever that row is. Where the
// person is looking decides where LOOSE content goes; a column has a stronger
// rule, and it wins.
func TestViewport_DoesNotOverrideTheColumnRow(t *testing.T) {
	existingCol := columnAt("old", 0, 0)
	view := &Viewport{X: 5000, Y: 5000, Width: 1400, Height: 900}
	scope := scopeSeen(view, existingCol)

	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "board-1", Title: "New"},
	}}
	LayoutPlan(p, scope)

	if p.Actions[0].Position.Y != existingCol.Location.Position.Y {
		t.Errorf("column at y=%v, want it in the existing row at y=%v",
			p.Actions[0].Position.Y, existingCol.Location.Position.Y)
	}
}
