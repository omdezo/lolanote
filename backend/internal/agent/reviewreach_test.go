package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// The quality loop was structurally unreachable on the runs that needed it.
//
// reviewTurn was gated on RenderSelfView, which describes GEOMETRY, and returns
// "" when nothing lands on the root canvas. Every run after the first organizing
// pass files into sub-boards and columns, so MEASURED, the critique, the
// register mismatch and the "STOP — shelves with nothing on them" nudge were
// skipped together — and the run burned its one allowed review on the empty
// string, so nothing could fire later either.

// nestedFilingScope is a root board holding a nested board holding a column
// holding nothing — the shape the product is in after one organizing run.
func nestedFilingScope() *BoardScope {
	sub := &domain.Element{
		ID: "sub", Type: domain.TypeBoard,
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
		Content:  domain.Content{"title": "Pre-Production"},
	}
	col := &domain.Element{
		ID: "col", Type: domain.TypeColumn,
		Location: domain.Location{ParentID: "sub", Section: domain.SectionCanvas},
		Content:  domain.Content{"title": "Casting"},
	}
	card := func(id string) *domain.Element {
		return &domain.Element{
			ID: id, Type: domain.TypeCard,
			Location: domain.Location{ParentID: "b1", Section: domain.SectionUnsorted},
			Content:  domain.Content{"textPreview": "a loose card"},
		}
	}
	els := map[string]*domain.Element{
		"sub": sub, "col": col, "card1": card("card1"), "card2": card("card2"),
	}
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: els,
		Occupied: Rect{Empty: true},
	}
	for id := range els {
		scope.Items = append(scope.Items, Item{ID: id, Type: els[id].Type})
	}
	return scope
}

// A plan of pure moves places nothing, so there is no arrangement to describe —
// and that used to mean no review at all. Filing thirty cards into columns is
// the single most common thing the agent does.
func TestReviewReach_FiresWhenNothingLandsOnACanvas(t *testing.T) {
	scope := nestedFilingScope()
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActMove, ElementID: "card1", ParentID: "col"},
		{Seq: 1, Kind: ActMove, ElementID: "card2", ParentID: "col"},
	}}
	if view := RenderSelfView(plan, scope); view != "" {
		t.Fatalf("the fixture is wrong: this plan was meant to place nothing:\n%s", view)
	}

	s := &staging{scope: scope, task: TaskSpec{Intent: "file these", Budget: DefaultBudget()}, plan: plan}
	turn := s.reviewTurn(4)
	if turn == "" {
		t.Fatal("a plan that files everything into a column got no review at all")
	}
	// The view degrades to containment, which is the axis that always exists.
	if !strings.Contains(turn, "Pre-Production › Casting") {
		t.Errorf("the review does not say where the plan files things:\n%s", turn)
	}
	// And the judgement rides regardless of which view rendered.
	if !strings.Contains(turn, "MEASURED") {
		t.Errorf("the measurements were dropped with the arrangement:\n%s", turn)
	}
}

// The review is a step of the run and costs a model turn, so it belongs in the
// journal. There was no emit here at all: the events stream showed the run go
// quiet and then produce a different plan.
func TestReviewReach_ShowsUpInTheEventsStream(t *testing.T) {
	var seen []string
	s := &staging{
		scope: nestedFilingScope(),
		task:  TaskSpec{Intent: "file these", Budget: DefaultBudget()},
		plan: &Plan{Actions: []Action{
			{Seq: 0, Kind: ActMove, ElementID: "card1", ParentID: "col"},
		}},
		emit: func(t EventType, msg string, _ map[string]any) {
			if t == EvStepFinished {
				seen = append(seen, msg)
			}
		},
	}
	if s.reviewTurn(4) == "" {
		t.Fatal("no review turn")
	}
	found := false
	for _, m := range seen {
		if strings.Contains(m, "reviewing the plan") {
			found = true
		}
	}
	if !found {
		t.Errorf("the review step is invisible in the journal: %v", seen)
	}
}

// The STOP nudge is the guard written for the eighteen-empty-columns plan, and
// the plan it was written for filed into nested boards — where the gate that
// carried it never opened.
func TestReviewReach_HollowNudgeSurvivesTheDegradedView(t *testing.T) {
	scope := nestedFilingScope()
	// Empty to-do lists cannot be staged through the tools, so this is the
	// hand-built equivalent: containers parented to a column, which places
	// nothing on any canvas.
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateTodo, ElementID: "t1", ParentID: "col", Title: "Auditions"},
		{Seq: 1, Kind: ActCreateTodo, ElementID: "t2", ParentID: "col", Title: "Callbacks"},
	}}
	if view := RenderSelfView(plan, scope); view != "" {
		t.Fatalf("the fixture is wrong: this plan was meant to place nothing:\n%s", view)
	}

	s := &staging{scope: scope, task: TaskSpec{Intent: "set up casting", Budget: DefaultBudget()}, plan: plan}
	turn := s.reviewTurn(4)
	if !strings.Contains(turn, "STOP") {
		t.Errorf("a plan of empty containers filed out of sight was never stopped:\n%s", turn)
	}
	if !strings.Contains(turn, "MEASURED") {
		t.Errorf("a hollow plan is shown no numbers to argue with:\n%s", turn)
	}
	// The nudge is what gets acted on, so it takes the last word.
	if strings.Index(turn, "MEASURED") > strings.Index(turn, "STOP") {
		t.Error("the measurements come after the correction; last is what gets acted on")
	}
}

// The tree has to say WHICH Casting: a plan filing into three sub-boards that
// each hold a column of the same name is otherwise three identical rows.
func TestReviewReach_ContainmentNamesTheWholeChain(t *testing.T) {
	scope := nestedFilingScope()
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActMove, ElementID: "card1", ParentID: "col"},
		// A container this plan creates and never fills is a row worth reading,
		// even though nothing points at it.
		{Seq: 1, Kind: ActCreateColumn, ElementID: "new", ParentID: "sub", Title: "Locations"},
	}}
	tree := RenderContainmentTree(plan, scope)

	for _, want := range []string{
		"Pre-Production › Casting (1 item(s))",
		"Pre-Production › Locations (empty)",
	} {
		if !strings.Contains(tree, want) {
			t.Errorf("containment tree missing %q:\n%s", want, tree)
		}
	}
	// An empty plan has no containment to describe.
	if got := RenderContainmentTree(&Plan{}, scope); got != "" {
		t.Errorf("an empty plan rendered a tree: %q", got)
	}
}

// Even a plan that neither places nor files — renames, retexts, colour changes —
// gets its numbers checked. There is no third axis to degrade to, so the review
// shows the decisions themselves rather than skipping.
func TestReviewReach_APlanOfPureEditsIsStillReviewed(t *testing.T) {
	s := &staging{
		scope: nestedFilingScope(),
		task:  TaskSpec{Intent: "fix the spelling", Budget: DefaultBudget()},
		plan: &Plan{Actions: []Action{
			{Seq: 0, Kind: ActRename, ElementID: "col", Title: "Casting & Crew"},
		}},
	}
	turn := s.reviewTurn(4)
	if !strings.Contains(turn, "MEASURED") {
		t.Errorf("an edit-only plan skipped the review entirely:\n%s", turn)
	}
	if !strings.Contains(turn, "Casting & Crew") {
		t.Errorf("the review does not say what the plan actually decided:\n%s", turn)
	}
}
