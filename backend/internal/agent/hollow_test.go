package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// "Set up film production" produced a board, three columns named
// Pre-Production / Production / Post-Production, and not one card inside any of
// them. Four changes, a tidy result, no answer — the person could have typed
// those three words faster than the run took.
func TestHollow_CatchesAStructureWithNothingInIt(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateBoard, ElementID: "nb", ParentID: "b1", Title: "Film Production"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c1", ParentID: "nb", Title: "Pre-Production"},
		{Seq: 2, Kind: ActCreateColumn, ElementID: "c2", ParentID: "nb", Title: "Production"},
		{Seq: 3, Kind: ActCreateColumn, ElementID: "c3", ParentID: "nb", Title: "Post-Production"},
	}}

	// The board itself IS filled — the columns are in it. The three columns are
	// not, and they are the whole answer.
	hollow := hollowContainers(p)
	if len(hollow) != 3 {
		t.Fatalf("hollow = %v, want the three columns", hollow)
	}
	for _, want := range []string{"Pre-Production", "Production", "Post-Production"} {
		if !contains(hollow, want) {
			t.Errorf("%q was not reported as empty", want)
		}
	}
}

// Content makes a container real, whether it was created there or moved in.
func TestHollow_FilledContainersAreNotReported(t *testing.T) {
	cases := map[string]Action{
		"created in": {Seq: 2, Kind: ActCreateNote, ElementID: "n1", ParentID: "c1", Text: "a step"},
		"moved in":   {Seq: 2, Kind: ActMove, ElementID: "existing", ParentID: "c1"},
		// A to-do list carries its items inline, so nothing is parented to it —
		// the naive "has children" test calls a full checklist empty.
		"a todo list": {Seq: 2, Kind: ActCreateTodo, ElementID: "t1", ParentID: "c1",
			Title: "Tasks", Tasks: []string{"Book venue", "Email finance"}},
	}
	for name, filler := range cases {
		p := &Plan{Actions: []Action{
			{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Alpha"},
			{Seq: 1, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Beta"},
			filler,
		}}
		// One of the two is filled, so the plan is clearly doing real work and
		// the other may well be deliberate. Not our call to make.
		if h := hollowContainers(p); len(h) != 0 {
			t.Errorf("%s: reported %v as hollow, but this plan fills a container", name, h)
		}
	}
}

// "Add a Done column" is a request people genuinely make.
func TestHollow_OneEmptyContainerIsLegitimate(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Done"},
	}}
	if h := hollowContainers(p); len(h) != 0 {
		t.Errorf("a single empty column was flagged: %v", h)
	}
}

// A plan with no containers at all cannot be hollow.
func TestHollow_IgnoresPlansThatBuildNoStructure(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateNote, ElementID: "n1", ParentID: "b1", Text: "a note"},
		{Seq: 1, Kind: ActRename, ElementID: "x", Title: "renamed"},
	}}
	if h := hollowContainers(p); len(h) != 0 {
		t.Errorf("reported %v on a plan that creates no containers", h)
	}
	if hollowContainers(nil) != nil {
		t.Error("nil plan reported as hollow")
	}
}

// The correction has to be actionable. "This is empty" is a remark; a model
// finishing a turn reads past a remark.
func TestHollow_NudgeSaysWhatToDo(t *testing.T) {
	nudge := hollowNudge([]string{"Pre-Production", "Production"})
	for _, want := range []string{"Pre-Production", "Production", "unmet"} {
		if !strings.Contains(nudge, want) {
			t.Errorf("the correction never mentions %q: %s", want, nudge)
		}
	}
	if !strings.Contains(nudge, "STOP") {
		t.Error("the correction opens softly enough to be skimmed past")
	}
}

// If the model was told, had a turn, and still finished with empty containers,
// the person is told rather than handed labelled boxes.
func TestHollow_DisclosesWhatWasNotFilled(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Alpha"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Beta"},
	}}
	discloseHollow(p)

	if len(p.Unmet) != 1 {
		t.Fatalf("unmet = %+v, want one disclosure", p.Unmet)
	}
	if !strings.Contains(p.Unmet[0].Request, "Alpha") {
		t.Errorf("the disclosure does not name what was left empty: %+v", p.Unmet[0])
	}
	if p.Unmet[0].Why == "" {
		t.Error("the disclosure gives no reason")
	}

	// And a plan that did its job says nothing.
	good := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Alpha"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "c1", Text: "real work"},
	}}
	discloseHollow(good)
	if len(good.Unmet) != 0 {
		t.Errorf("a filled plan was given a disclosure: %+v", good.Unmet)
	}
}

// A to-do list or a table created WITHOUT any items really is empty, and
// saying so is the point.
func TestHollow_AnItemlessChecklistIsStillEmpty(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateTodo, ElementID: "t1", ParentID: "b1", Title: "Tasks"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Alpha"},
	}}
	if h := hollowContainers(p); len(h) != 2 {
		t.Errorf("hollow = %v, want both the itemless list and the empty column", h)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// The observation has to reach the model where it will act on it. The self-view
// already remarked that containers "will be created empty" — as one line among
// several, which a model finishing a turn reads straight past.
func TestHollow_LeadsTheReviewTurn(t *testing.T) {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Occupied: Rect{Empty: true},
	}
	s := &staging{
		scope: scope,
		plan: &Plan{Actions: []Action{
			{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Alpha"},
			{Seq: 1, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Beta"},
		}},
	}

	turn := s.reviewTurn(4)
	if turn == "" {
		t.Fatal("no review turn for a hollow plan")
	}
	// It takes the LAST word rather than replacing the review, because last is
	// what a model finishing a turn acts on. It used to replace it, and the plan
	// with the worst numbers was therefore the one never shown any.
	if !strings.Contains(turn, "STOP") {
		t.Error("the hollow structure is not what the review turn ends on")
	}
	if strings.Index(turn, "STOP") < strings.Index(turn, "MEASURED") {
		t.Error("the correction comes before the measurements it is arguing from")
	}
	if !strings.Contains(turn, "Alpha") {
		t.Error("the review turn does not name the empty containers")
	}
	// And it stays ONE turn: a second stop would cost a step the plan should be
	// spending on the content it is missing.
	if s.reviewTurn(4) != "" {
		t.Error("the run would be sent back to look at itself twice")
	}
}

// A plan that filled what it built gets the ordinary review, not a lecture.
func TestHollow_FilledPlansGetTheOrdinaryReview(t *testing.T) {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Occupied: Rect{Empty: true},
	}
	s := &staging{
		scope: scope,
		plan: &Plan{Actions: []Action{
			{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Alpha"},
			{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "c1", Text: "real work"},
		}},
	}
	turn := s.reviewTurn(4)
	if strings.Contains(turn, "STOP") {
		t.Errorf("a filled plan was told off for being empty: %s", turn)
	}
}

// A run that changes nothing must still say why, through a channel the person
// sees. The reason lived in plan.Summary, and the outcome card renders
// "Nothing to do · No changes were needed" over the top of it — so asked for
// budget figures it did not have, the agent explained itself into a field
// nobody reads.
func TestNoOp_ExplanationReachesThePerson(t *testing.T) {
	p := &Plan{Summary: "The figures are not on this board and I will not invent them."}
	promoteSilentSummary(p, "fill in the actual budget numbers")

	if len(p.Unmet) != 1 {
		t.Fatalf("unmet = %+v, want the explanation promoted", p.Unmet)
	}
	if !strings.Contains(p.Unmet[0].Why, "not invent") {
		t.Errorf("the reason was lost: %+v", p.Unmet[0])
	}

	// A run that DID something keeps its summary where it is — the outcome card
	// shows the changes, and duplicating the summary into unmet would read as a
	// failure report on a successful run.
	did := &Plan{
		Summary: "Made three columns.",
		Actions: []Action{{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "A"}},
	}
	promoteSilentSummary(did, "organise this")
	if len(did.Unmet) != 0 {
		t.Errorf("a run that did something was given a failure note: %+v", did.Unmet)
	}

	// And one that already explained itself is not told twice.
	already := &Plan{
		Summary: "Nothing to do.",
		Unmet:   []Unmet{{Request: "x", Why: "y"}},
	}
	promoteSilentSummary(already, "organise this")
	if len(already.Unmet) != 1 {
		t.Errorf("the explanation was duplicated: %+v", already.Unmet)
	}
}
