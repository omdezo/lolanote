package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

func qualityScope() *BoardScope {
	return &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{
			"old-1": {ID: "old-1", Type: domain.TypeCard},
			"old-2": {ID: "old-2", Type: domain.TypeCard},
		},
	}
}

var qualityBudget = Budget{MaxActions: 60}

// The plan the user actually got: six column headings, a handful of moves, and
// nothing written. It was measured at 2.6% of budget and reported as finished.
func TestQuality_MeasuresTheThinPlan(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Development"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Production"},
		{Seq: 2, Kind: ActCreateColumn, ElementID: "c3", ParentID: "b1", Title: "Post"},
		{Seq: 3, Kind: ActMove, ElementID: "old-1", ParentID: "c1"},
	}}
	q := MeasurePlan(p, qualityScope(), qualityBudget)

	if q.Actions != 4 || q.Budget != 60 {
		t.Errorf("actions/budget = %d/%d", q.Actions, q.Budget)
	}
	if q.Containers != 3 {
		t.Errorf("containers = %d, want 3", q.Containers)
	}
	if q.Content != 0 {
		t.Errorf("content = %d; this plan writes nothing", q.Content)
	}
	if q.Reused != 1 {
		t.Errorf("reused = %d, want 1", q.Reused)
	}
	if q.Empty != 2 {
		t.Errorf("empty = %d, want the two columns nothing was filed into", q.Empty)
	}

	// The report has to carry the numbers, because the numbers are the argument.
	report := q.Report()
	for _, want := range []string{"4 of 60", "6%", "3 container", "2 empty", "0 new card"} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q: %s", want, report)
		}
	}
}

// A critique that says "consider adding more detail" is one a model agrees with
// and ignores. Each line has to be a fact with an action implied.
func TestQuality_CritiquesTheThinPlanSpecifically(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Development"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Production"},
		{Seq: 2, Kind: ActCreateColumn, ElementID: "c3", ParentID: "b1", Title: "Post"},
	}}
	weak := MeasurePlan(p, qualityScope(), qualityBudget).Critique()
	if len(weak) == 0 {
		t.Fatal("three empty columns drew no criticism at all")
	}
	joined := strings.Join(weak, " ")
	if !strings.Contains(joined, "shelves") {
		t.Errorf("the structure-with-no-substance case is not named: %v", weak)
	}
	if !strings.Contains(joined, "sketch") {
		t.Errorf("the thinness is not named: %v", weak)
	}
}

// A real answer must draw no criticism, or the check is noise and gets ignored.
func TestQuality_SaysNothingAboutASubstantialPlan(t *testing.T) {
	actions := []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Pre-Production"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Production"},
	}
	// Five real cards in each — the shape of an answer rather than a heading.
	for i := 0; i < 10; i++ {
		parent := "c1"
		if i >= 5 {
			parent = "c2"
		}
		actions = append(actions, Action{
			Seq: 2 + i, Kind: ActCreateNote, ElementID: "n" + string(rune('a'+i)),
			ParentID: parent, Text: "a real step",
		})
	}
	q := MeasurePlan(&Plan{Actions: actions}, qualityScope(), qualityBudget)
	if weak := q.Critique(); len(weak) > 0 {
		t.Errorf("a filled two-stage structure was criticised: %v", weak)
	}
	if q.Content != 10 {
		t.Errorf("content = %d, want 10", q.Content)
	}
}

// "Fix the spelling" is complete in one action. A floor applied to editing
// would make the check absurd, and an absurd check is one people switch off.
func TestQuality_DoesNotDemandBulkFromAnEdit(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActRename, ElementID: "old-1", Title: "Corrected"},
	}}
	if weak := MeasurePlan(p, qualityScope(), qualityBudget).Critique(); len(weak) > 0 {
		t.Errorf("a one-line correction was told it was too small: %v", weak)
	}
}

// Many containers between few cards is a list of labels wearing a structure's
// clothes.
func TestQuality_NoticesAWideFlatStructure(t *testing.T) {
	actions := []Action{}
	for i := 0; i < 5; i++ {
		actions = append(actions, Action{
			Seq: i, Kind: ActCreateColumn, ElementID: "c" + string(rune('a'+i)),
			ParentID: "b1", Title: "Stage",
		})
	}
	// Two cards spread across five columns.
	actions = append(actions,
		Action{Seq: 5, Kind: ActCreateNote, ElementID: "n1", ParentID: "ca", Text: "x"},
		Action{Seq: 6, Kind: ActCreateNote, ElementID: "n2", ParentID: "cb", Text: "y"})

	weak := MeasurePlan(&Plan{Actions: actions}, qualityScope(), qualityBudget).Critique()
	if !strings.Contains(strings.Join(weak, " "), "close to empty") {
		t.Errorf("five columns holding two cards drew no comment: %v", weak)
	}
}

// The numbers reach the model. Prose asking it to count itself does not work —
// the run that produced the headings was satisfied with the headings.
func TestQuality_ReviewTurnCarriesTheNumbers(t *testing.T) {
	s := &staging{
		scope: qualityScope(),
		task:  TaskSpec{Budget: qualityBudget},
		plan: &Plan{Actions: []Action{
			{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Alpha"},
			{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "c1", Text: "real"},
		}},
	}
	turn := s.reviewTurn(4)
	if !strings.Contains(turn, "MEASURED") {
		t.Fatalf("the review turn carries no measurements:\n%s", turn)
	}
	if !strings.Contains(turn, "of 60 changes") {
		t.Error("the review turn never states the budget the model actually has")
	}
}
