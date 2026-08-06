package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// Shell-in-shell: a board `Pre-Production` whose only content is a column
// called `Pre-Production`. Mechanically correct — the board is the right home,
// the column already had that name — and unreadable as design, because opening
// the board shows one box labelled with the room you just walked into.
func shellScope() *BoardScope {
	els := map[string]*domain.Element{
		"pre-board": {ID: "pre-board", Type: domain.TypeBoard,
			Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
			Content:  domain.Content{"title": "Pre-Production"}},
		"pre-col": {ID: "pre-col", Type: domain.TypeColumn,
			Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
			Content:  domain.Content{"title": "Pre Production"}},
		"casting-col": {ID: "casting-col", Type: domain.TypeColumn,
			Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
			Content:  domain.Content{"title": "Casting"}},
		"card1": {ID: "card1", Type: domain.TypeCard,
			Location: domain.Location{ParentID: "b1", Section: domain.SectionUnsorted},
			Content:  domain.Content{"textPreview": "Scout locations"}},
	}
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: els,
		Occupied: Rect{Empty: true},
	}
	for id, el := range els {
		scope.Items = append(scope.Items, Item{ID: id, Type: el.Type})
	}
	return scope
}

// The name match is normalized, so the hyphen the person typed on one and not
// the other does not hide the redundancy.
func TestShellCritique_NamesTheBoardHoldingItsOwnName(t *testing.T) {
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActMove, ElementID: "pre-col", ParentID: "pre-board"},
	}}
	got := shellCritique(plan, shellScope())
	if len(got) != 1 {
		t.Fatalf("want one critique line, got %v", got)
	}
	if !strings.Contains(got[0], "Pre-Production") || !strings.Contains(got[0], "own name") {
		t.Errorf("the line does not name the problem: %s", got[0])
	}
	// It has to say what to DO. "This is redundant" is a verdict the model
	// agrees with and does nothing about.
	if !strings.Contains(got[0], "CARDS") {
		t.Errorf("the critique offers no repair: %s", got[0])
	}
}

// A column that names something the board does not is the ordinary, correct
// case, and a critique that fired on it would be noise on every organizing run.
func TestShellCritique_SaysNothingAboutAProperColumn(t *testing.T) {
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActMove, ElementID: "casting-col", ParentID: "pre-board"},
		{Seq: 1, Kind: ActMove, ElementID: "card1", ParentID: "casting-col"},
	}}
	if got := shellCritique(plan, shellScope()); len(got) != 0 {
		t.Errorf("critiqued a column that names its own thing: %v", got)
	}
}

// The board is usually created by the same plan that fills it, so the name has
// to resolve through the staged action as well as through the board listing.
func TestShellCritique_ResolvesABoardThisPlanIsCreating(t *testing.T) {
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateBoard, ElementID: "new-board", ParentID: "b1", Title: "Pre-Production"},
		{Seq: 1, Kind: ActMove, ElementID: "pre-col", ParentID: "new-board"},
		// The same board twice: one line per board, not one per move.
		{Seq: 2, Kind: ActMove, ElementID: "casting-col", ParentID: "new-board"},
	}}
	got := shellCritique(plan, shellScope())
	if len(got) != 1 {
		t.Fatalf("want exactly one line for one board, got %v", got)
	}
}

// And it has to reach the model. The critique lives beside the measured ones in
// the review turn — the section the run is asked to act on.
func TestShellCritique_ReachesTheReviewTurn(t *testing.T) {
	s := &staging{
		scope: shellScope(),
		task:  TaskSpec{Intent: "group this board", Budget: DefaultBudget()},
		plan: &Plan{Actions: []Action{
			{Seq: 0, Kind: ActMove, ElementID: "pre-col", ParentID: "pre-board"},
			{Seq: 1, Kind: ActMove, ElementID: "card1", ParentID: "casting-col"},
		}},
	}
	turn := s.reviewTurn(4)
	if !strings.Contains(turn, "WHAT IS WEAK ABOUT THAT") {
		t.Fatalf("no critique section in the review:\n%s", turn)
	}
	if !strings.Contains(turn, "its own name") {
		t.Errorf("the shell-in-shell critique never reached the model:\n%s", turn)
	}
}
