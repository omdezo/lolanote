package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// The quality floor used to be budget-dependent. reviewTurn fires at most once
// and never with fewer than two steps left, and it was the only caller of
// MeasurePlan/CritiqueFor, expectation.Mismatch and shellCritique — so the
// step-starved run that ships a half-answer, and the run that reviews at step 3
// and then works for fifteen more, were the two runs guaranteed never to be
// measured on what they finally built.

func auditScope() *BoardScope {
	return &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Film"}},
		Elements: map[string]*domain.Element{
			"card-1": {ID: "card-1", Type: domain.TypeCard,
				Content:  domain.Content{"textPreview": "Harbour interview"},
				Location: domain.Location{ParentID: "b1"}},
		},
	}
}

func auditNotes(p *Plan) []string {
	var out []string
	for _, n := range p.Notes {
		if strings.HasPrefix(n, "Looking at the finished plan:") {
			out = append(out, n)
		}
	}
	return out
}

// The acceptance case: a run that never reviewed still gets measured.
func TestFinalAudit_FiresWithoutAReviewTurn(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Casting"},
		{Seq: 2, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Locations"},
		{Seq: 3, Kind: ActCreateColumn, ElementID: "c3", ParentID: "b1", Title: "Schedule"},
		{Seq: 4, Kind: ActCreateNote, ElementID: "n1", ParentID: "c1", Text: "Call the agency"},
	}}
	auditPlan(p, auditScope(), TaskSpec{Intent: "set up pre-production", Budget: Budget{MaxActions: 60}})

	notes := auditNotes(p)
	if len(notes) == 0 {
		t.Fatalf("the finished plan is three containers holding one card and nothing "+
			"was said about it:\n%v", p.Notes)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "3 containers between 1 card") {
		t.Errorf("the wide-and-flat finding never reached the person:\n%s", joined)
	}
}

// The mismatch check is the one the person most needs and the one reviewTurn was
// least likely to run: a question answered with edits to the board.
func TestFinalAudit_ReportsAnswersThatRearrangedTheBoard(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 1, Kind: ActMove, ElementID: "card-1", ParentID: "b1"},
		{Seq: 2, Kind: ActRename, ElementID: "card-1", Title: "Harbour"},
	}}
	auditPlan(p, auditScope(), TaskSpec{Intent: "what is missing from this board?", Budget: Budget{MaxActions: 60}})

	joined := strings.Join(auditNotes(p), "\n")
	if !strings.Contains(joined, "A QUESTION was asked") {
		t.Fatalf("a question was answered with two edits and the note says nothing:\n%v", p.Notes)
	}
	// The critiques are shared with the review turn, where they address a model
	// that can still act. A person reading an outcome card has no undo_staged.
	if strings.Contains(joined, "undo_staged") {
		t.Errorf("the note tells the person to call a tool only the model has:\n%s", joined)
	}
}

// discloseHollow already states emptiness in `unmet`, in the agent's voice and
// with the containers named. Saying it twice on one outcome card teaches the
// reader to skim both.
func TestFinalAudit_DoesNotRepeatTheHollowDisclosure(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Casting"},
		{Seq: 2, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Locations"},
	}}
	discloseHollow(p)
	auditPlan(p, auditScope(), TaskSpec{Intent: "set up pre-production", Budget: Budget{MaxActions: 60}})

	joined := strings.Join(auditNotes(p), "\n")
	if strings.Contains(joined, "hold nothing") {
		t.Errorf("emptiness is reported in both `unmet` and `notes`:\n%s", joined)
	}
	if len(p.Unmet) == 0 {
		t.Error("the hollow disclosure was lost — it is the more important of the two")
	}
}

// A plan with nothing wrong with it says nothing. A floor that always speaks is
// one people stop reading.
func TestFinalAudit_SilentOnAPlanWithNothingWrong(t *testing.T) {
	actions := []Action{
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Casting"},
	}
	for i := 0; i < 12; i++ {
		actions = append(actions, Action{
			Seq: i + 2, Kind: ActCreateNote, ElementID: "n" + string(rune('a'+i)),
			ParentID: "c1", Text: "a real step",
		})
	}
	p := &Plan{Actions: actions}
	auditPlan(p, auditScope(), TaskSpec{Intent: "set up casting", Budget: Budget{MaxActions: 60}})

	if notes := auditNotes(p); len(notes) > 0 {
		t.Errorf("a sound plan was criticised anyway:\n%s", strings.Join(notes, "\n"))
	}
}
