package agent

import (
	"testing"

	"qomranote/backend/internal/domain"
)

func childAt(id, parent string, index float64) *domain.Element {
	return &domain.Element{
		ID: id, Type: domain.TypeCard,
		Content:  domain.Content{"textPreview": id},
		Location: domain.Location{ParentID: parent, Index: index},
	}
}

func orderScope(children ...*domain.Element) *BoardScope {
	scope := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{
			"col-a": {ID: "col-a", Type: domain.TypeColumn,
				Content:  domain.Content{"title": "Intro"},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
			"col-b": {ID: "col-b", Type: domain.TypeColumn,
				Content:  domain.Content{"title": "Process"},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
		},
	}
	for _, c := range children {
		scope.Elements[c.ID] = c
	}
	return scope
}

// Asked to expand a shot list, the agent added two shots to a column that
// already held three — and they landed ON TOP of them:
//
//	Intro   Shot 4 (1.5)  Shot 5 (2)  Shot 1 (3)  Shot 2 (4)  Shot 3 (5)
//
// The index was the action's position in the PLAN, so a running count produced
// numbers lower than what was already in the column.
func TestOrdering_NewCardsGoAfterWhatIsAlreadyThere(t *testing.T) {
	scope := orderScope(
		childAt("shot-1", "col-a", 3),
		childAt("shot-2", "col-a", 4),
		childAt("shot-3", "col-a", 5),
	)
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateNote, ElementID: "shot-4", ParentID: "col-a", Text: "Shot 4"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "shot-5", ParentID: "col-a", Text: "Shot 5"},
	}}

	OrderPlan(p, scope)

	for _, a := range p.Actions {
		if a.Index <= 5 {
			t.Errorf("%q got index %v; the column already goes up to 5, so it lands on top",
				a.Text, a.Index)
		}
	}
	if p.Actions[0].Index >= p.Actions[1].Index {
		t.Errorf("staging order was not preserved: %v then %v",
			p.Actions[0].Index, p.Actions[1].Index)
	}
}

// One column's contents must not decide another's ordering. The single running
// count meant a card's position depended on how many cards happened to be
// staged for a DIFFERENT column first.
func TestOrdering_EachContainerCountsForItself(t *testing.T) {
	scope := orderScope(childAt("old", "col-a", 7))
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateNote, ElementID: "a1", ParentID: "col-a", Text: "into A"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "b1", ParentID: "col-b", Text: "into B"},
		{Seq: 2, Kind: ActCreateNote, ElementID: "b2", ParentID: "col-b", Text: "also B"},
	}}

	OrderPlan(p, scope)

	if got := p.Actions[0].Index; got != 8 {
		t.Errorf("card into a column ending at 7 got index %v, want 8", got)
	}
	// col-b is empty, so it starts at 1 regardless of what happened in col-a.
	if got := p.Actions[1].Index; got != 1 {
		t.Errorf("first card into an empty column got index %v, want 1", got)
	}
	if got := p.Actions[2].Index; got != 2 {
		t.Errorf("second card into that column got index %v, want 2", got)
	}
}

// Moves are ordered too — the prompt has always promised "cards land in the
// order you stage them", and for a container that is what index means.
func TestOrdering_MovesFollowStagingOrder(t *testing.T) {
	scope := orderScope(
		childAt("loose-1", "b1", 0),
		childAt("loose-2", "b1", 0),
	)
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActMove, ElementID: "loose-2", ParentID: "col-a"},
		{Seq: 1, Kind: ActMove, ElementID: "loose-1", ParentID: "col-a"},
	}}

	OrderPlan(p, scope)

	if p.Actions[0].Index >= p.Actions[1].Index {
		t.Errorf("moves did not follow staging order: %v then %v",
			p.Actions[0].Index, p.Actions[1].Index)
	}
}

// A canvas places by coordinate, not by index. Numbering things dropped on a
// board would be meaningless and would fight the layout pass.
func TestOrdering_LeavesCanvasPlacementsAlone(t *testing.T) {
	scope := orderScope()
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "New"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "b1", Text: "loose"},
	}}

	OrderPlan(p, scope)

	for _, a := range p.Actions {
		if a.Index != 0 {
			t.Errorf("%q lands on the canvas but was given index %v", a.Title+a.Text, a.Index)
		}
	}
}

// A container this plan creates starts empty, whatever else is on the board.
func TestOrdering_NewContainerStartsAtOne(t *testing.T) {
	scope := orderScope(childAt("old", "col-a", 40))
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "fresh", ParentID: "b1", Title: "Fresh"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "fresh", Text: "first"},
	}}

	OrderPlan(p, scope)

	if got := p.Actions[1].Index; got != 1 {
		t.Errorf("first card in a brand-new column got index %v, want 1", got)
	}
}

func TestOrdering_ToleratesNothing(t *testing.T) {
	OrderPlan(nil, nil)
	OrderPlan(&Plan{}, nil)
	OrderPlan(&Plan{Actions: []Action{{Seq: 0, Kind: ActRename, ElementID: "x"}}}, orderScope())
}
