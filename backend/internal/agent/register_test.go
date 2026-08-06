package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// The prompt was written entirely for ONE register: editing a board that
// already has material on it. "You are arranging a space", "keep columns
// comparable", "every card should be somewhere it belongs" — and the worked
// example sorts 64 cards that already exist.
//
// Asked to AUTHOR something ("show me the best organised workflow for a drama
// film"), the agent had no guidance at all, so it fell back to what it knew:
// six column headings and moving the twelve cards already there. It spent 12 of
// 60 allowed changes and under 3% of its cost budget. Not constrained —
// unbriefed.
func TestPrompt_DistinguishesAuthoringFromOrganising(t *testing.T) {
	for _, want := range []string{"ORGANISING", "AUTHORING", "REPORTING"} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("the prompt never names the %s register", want)
		}
	}

	// The tells, so the model can place a request rather than guess.
	for _, tell := range []string{"set up", "design a", "from your imagination", "tidy this"} {
		if !strings.Contains(systemPrompt, tell) {
			t.Errorf("no example of a request in either register: %q", tell)
		}
	}

	// A worked example of what thin looks like, and what real looks like. The
	// organising register has had one since it was written; authoring had none,
	// which is most of why the answers were thin.
	for _, marker := range []string{"Thin answer", "Real answer"} {
		if !strings.Contains(systemPrompt, marker) {
			t.Errorf("the authoring register has no worked example (%q missing)", marker)
		}
	}

	// And a stated bar. "Be thorough" is not actionable; a number is.
	if !strings.Contains(systemPrompt, "SIXTY changes") {
		t.Error("the prompt never says how much room a run actually has")
	}
	if !strings.Contains(systemPrompt, "fewer than ten") {
		t.Error("the prompt gives no floor for an authoring answer")
	}
}

// The bar has to match the real budget, or it is telling the model something
// false about what it may spend.
func TestPrompt_StatedBudgetMatchesTheRealOne(t *testing.T) {
	if DefaultBudget().MaxActions != 60 {
		t.Fatalf("MaxActions is %d but the prompt promises sixty — one of them is lying",
			DefaultBudget().MaxActions)
	}
}

// The self-review asked only about arrangement, so a plan answering a quarter
// of the question passed every time by being neatly arranged.
func TestReviewTurn_AsksAboutSufficiencyBeforeArrangement(t *testing.T) {
	s := &staging{
		scope: &BoardScope{
			Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
			Elements: map[string]*domain.Element{},
			Occupied: Rect{Empty: true},
		},
		plan: &Plan{Actions: []Action{
			{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Alpha"},
			{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "c1", Text: "real work"},
		}},
	}
	turn := s.reviewTurn(4)
	if turn == "" {
		t.Fatal("no review turn")
	}
	complete := strings.Index(turn, "complete answer")
	weakest := strings.Index(turn, "weakest grouping")
	if complete < 0 {
		t.Fatal("the review never asks whether the answer is complete")
	}
	if weakest < 0 {
		t.Fatal("the review stopped asking about the arrangement")
	}
	if complete > weakest {
		t.Error("arrangement is asked about before sufficiency; a quarter-answer that " +
			"is neatly arranged passes")
	}
}
