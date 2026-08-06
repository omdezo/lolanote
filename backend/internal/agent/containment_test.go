package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// Asked to improve a film workflow, the agent put "Screenplay" and
// "Pre-Production" INSIDE the existing "Development" column, did the same in two
// more, and filed six cards into them. Every id resolved, every parent existed,
// the plan passed validation — and the result was an arrangement the canvas
// cannot draw, because a column is a list of cards.
//
// The only question ever asked was "is the parent a container?", and a column
// answers yes.
func TestContainment_AColumnCannotHoldAColumn(t *testing.T) {
	if CanHold(domain.TypeColumn, domain.TypeColumn) {
		t.Error("a column may hold another column")
	}
	if CanHold(domain.TypeColumn, domain.TypeBoard) {
		t.Error("a column may hold a board")
	}
	// What a column is actually for.
	for _, child := range []domain.ElementType{
		domain.TypeCard, domain.TypeLink, domain.TypeTaskList,
		domain.TypeImage, domain.TypeTable,
	} {
		if !CanHold(domain.TypeColumn, child) {
			t.Errorf("a column may not hold a %s, which is what columns are for", child)
		}
	}
}

func TestContainment_ABoardHoldsAnything(t *testing.T) {
	for _, child := range []domain.ElementType{
		domain.TypeColumn, domain.TypeBoard, domain.TypeCard, domain.TypeLine,
	} {
		if !CanHold(domain.TypeBoard, child) {
			t.Errorf("a board may not hold a %s", child)
		}
	}
}

func TestContainment_ATodoListHoldsOnlyTasks(t *testing.T) {
	if !CanHold(domain.TypeTaskList, domain.TypeTask) {
		t.Error("a to-do list may not hold a task")
	}
	if CanHold(domain.TypeTaskList, domain.TypeCard) {
		t.Error("a to-do list may hold a card")
	}
}

// Something that is not a container holds nothing, whatever is asked.
func TestContainment_ACardHoldsNothing(t *testing.T) {
	if CanHold(domain.TypeCard, domain.TypeCard) {
		t.Error("a card may hold a card")
	}
}

// The refusal has to name the right home. A bare "not allowed" gets retried
// with the same parent.
func TestContainment_RefusalSaysWhereItShouldGo(t *testing.T) {
	err := containmentError(domain.TypeColumn, domain.TypeColumn, "col-1")
	if !strings.Contains(err.Error(), "board") {
		t.Errorf("the refusal does not say where a column belongs: %v", err)
	}
}

// The second gate: a plan that got past the tool boundary must not reach the
// board. This is the exact plan the run produced.
func TestContainment_PreconditionsRefuseTheRealPlan(t *testing.T) {
	development := &domain.Element{
		ID: "col-dev", Type: domain.TypeColumn,
		Content:  domain.Content{"title": "Development"},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
	}
	card := &domain.Element{
		ID: "card-1", Type: domain.TypeCard,
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
	}
	scope := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{
			"col-dev": development, "card-1": card,
		},
	}
	p := &Plan{Actions: []Action{
		// A column inside a column, exactly as it happened.
		{Seq: 0, Kind: ActCreateColumn, ElementID: "new-col", ParentID: "col-dev", Title: "Screenplay"},
		{Seq: 1, Kind: ActMove, ElementID: "card-1", ParentID: "new-col"},
	}}

	v := Preconditions(p, scope, TaskSpec{Budget: Budget{MaxActions: 20}, Autonomy: AutonomyPreview})
	if v.Passed {
		t.Fatal("a plan nesting a column inside a column passed validation")
	}
	found := false
	for _, c := range v.Criteria {
		if c.Name == "containment.legal" && !c.Passed {
			found = true
			if !strings.Contains(c.Detail, "COLUMN") {
				t.Errorf("the failure does not say what went where: %s", c.Detail)
			}
		}
	}
	if !found {
		t.Errorf("no containment failure recorded; criteria = %+v", v.Criteria)
	}
}

// And the ordinary shape — columns on a board, cards in the columns — still
// passes. A rule that also refuses the correct arrangement is worse than none.
func TestContainment_TheCorrectShapeStillPasses(t *testing.T) {
	card := &domain.Element{
		ID: "card-1", Type: domain.TypeCard,
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
	}
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{"card-1": card},
	}
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Screenplay"},
		{Seq: 1, Kind: ActMove, ElementID: "card-1", ParentID: "c1"},
		{Seq: 2, Kind: ActCreateNote, ElementID: "n1", ParentID: "c1", Text: "a step"},
	}}

	v := Preconditions(p, scope, TaskSpec{Budget: Budget{MaxActions: 20}, Autonomy: AutonomyPreview})
	for _, c := range v.Criteria {
		if c.Name == "containment.legal" && !c.Passed {
			t.Fatalf("the correct arrangement was refused: %s", c.Detail)
		}
	}
}
