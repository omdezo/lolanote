package agent

import (
	"context"

	"qomranote/backend/internal/domain"
	"strings"
	"testing"
)

// "What is missing from this plan?" came back as fourteen useful notes AND two
// moves and a rename. The notes were the answer; the edits were noise the
// person had to review, undo, or live with. The prompt already forbade it in
// words, twice, and words were not enough.
func TestExpectation_AQuestionShouldNotRestructure(t *testing.T) {
	e := expectationOf("what is missing from this plan?")
	if !e.Reporting {
		t.Fatal("a question was not read as a request for an answer")
	}

	// The real plan's shape: an answer, plus edits nobody asked for.
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateNote, ElementID: "n1", ParentID: "b1", Text: "Missing: a budget"},
		{Seq: 1, Kind: ActMove, ElementID: "old-1", ParentID: "c1"},
		{Seq: 2, Kind: ActRename, ElementID: "old-2", Title: "Tidied"},
	}}
	msg := e.Mismatch(p, MeasurePlan(p, nil, Budget{MaxActions: 60}))
	if msg == "" {
		t.Fatal("a plan that answered a question AND rearranged the board drew no comment")
	}
	if !strings.Contains(msg, "undo_staged") {
		t.Errorf("the correction does not say how to withdraw the edits: %s", msg)
	}
}

// An answer in words alone is exactly right and must draw nothing.
func TestExpectation_AnAnswerInWordsIsFine(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActComment, ElementID: "cm1", ParentID: "b1", Text: "Three things are missing…"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "b1", Text: "…and here they are"},
	}}
	e := expectationOf("what is missing?")
	if msg := e.Mismatch(p, MeasurePlan(p, nil, Budget{MaxActions: 60})); msg != "" {
		t.Errorf("an answer written as notes was criticised: %s", msg)
	}
}

// "Break down the crew structure" produced eight columns and no hierarchy —
// columns are lists, and a list cannot show what reports to what.
func TestExpectation_ARelationalRequestNeedsAShape(t *testing.T) {
	e := expectationOf("break down the crew structure for a mid-size shoot")
	if !e.Relational {
		t.Fatal("a breakdown was not read as a request about relationships")
	}

	flat := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Camera"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "c1", Text: "DoP"},
	}}
	msg := e.Mismatch(flat, MeasurePlan(flat, nil, Budget{MaxActions: 60}))
	if msg == "" {
		t.Fatal("a hierarchy built as flat columns drew no comment")
	}
	if !strings.Contains(msg, "design_as") {
		t.Errorf("the correction does not name the tool that fixes it: %s", msg)
	}

	// Declaring the shape settles it.
	shaped := &Plan{Shape: LayoutTree, Actions: flat.Actions}
	if msg := e.Mismatch(shaped, MeasurePlan(shaped, nil, Budget{MaxActions: 60})); msg != "" {
		t.Errorf("a declared tree was still criticised: %s", msg)
	}
	// So does connecting things, even without declaring.
	connected := &Plan{Actions: append(append([]Action{}, flat.Actions...),
		Action{Seq: 2, Kind: ActConnect, ElementID: "l1", ParentID: "b1", FromID: "c1", ToID: "n1"})}
	if msg := e.Mismatch(connected, MeasurePlan(connected, nil, Budget{MaxActions: 60})); msg != "" {
		t.Errorf("a connected breakdown was still criticised: %s", msg)
	}
}

// The reading is crude on purpose, and must not fire on ordinary work.
func TestExpectation_LeavesOrdinaryRequestsAlone(t *testing.T) {
	for _, intent := range []string{
		"tidy this board",
		"organise these cards by owner",
		"set up a production plan",
		"add a Done column",
	} {
		e := expectationOf(intent)
		if e.Reporting {
			t.Errorf("%q was read as a question", intent)
		}
		p := &Plan{Actions: []Action{
			{Seq: 0, Kind: ActMove, ElementID: "x", ParentID: "c1"},
			{Seq: 1, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Done"},
		}}
		if msg := e.Mismatch(p, MeasurePlan(p, nil, Budget{MaxActions: 60})); msg != "" {
			t.Errorf("%q drew a mismatch it should not: %s", intent, msg)
		}
	}
}

// A question that only reads is untouched, and a plan with nothing in it cannot
// mismatch anything.
func TestExpectation_ToleratesTheEmptyCases(t *testing.T) {
	e := expectationOf("what should we do about the permits?")
	if !e.Reporting {
		t.Error("a question with a mid-sentence opener was missed")
	}
	if msg := e.Mismatch(nil, PlanQuality{}); msg != "" {
		t.Errorf("a nil plan produced a mismatch: %s", msg)
	}
	if msg := e.Mismatch(&Plan{}, PlanQuality{}); msg != "" {
		t.Errorf("an empty plan produced a mismatch: %s", msg)
	}
}

// A question answered in one note is a good answer. Telling it off for using
// 1% of the budget is the noise that teaches people to ignore the whole check.
func TestExpectation_NoSizeFloorForAQuestion(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateNote, ElementID: "n1", ParentID: "b1", Text: "Three things are missing…"},
	}}
	q := MeasurePlan(p, nil, Budget{MaxActions: 60})

	if len(q.Critique()) == 0 {
		t.Fatal("the size floor no longer fires at all")
	}
	for _, line := range q.CritiqueFor(expectationOf("what is missing?")) {
		if strings.Contains(line, "sketch") {
			t.Errorf("a one-note answer to a question was called a sketch: %s", line)
		}
	}
}

// Asked to make a board public, a run said plainly it could not change sharing
// — then staged 22 changes building an unrelated film production structure,
// apparently so as to have produced a result. The person is left with work to
// undo on top of an unanswered request.
func TestExpectation_DecliningThenBuildingSomethingElse(t *testing.T) {
	e := expectationOf("share this board with the whole team and make it public")
	if !e.Impossible {
		t.Fatal("a request for sharing was not read as outside what the agent can do")
	}

	busy := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateBoard, ElementID: "b2", ParentID: "b1", Title: "Film Production"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "b2", Text: "Concept & Story"},
	}}
	msg := e.Mismatch(busy, MeasurePlan(busy, nil, Budget{MaxActions: 60}))
	if msg == "" {
		t.Fatal("declining and then building something unrelated drew no comment")
	}
	if !strings.Contains(msg, "unmet") || !strings.Contains(msg, "undo_staged") {
		t.Errorf("the correction does not say what to do instead: %s", msg)
	}

	// Declining cleanly draws nothing.
	if msg := e.Mismatch(&Plan{}, PlanQuality{}); msg != "" {
		t.Errorf("a clean refusal was criticised: %s", msg)
	}
}

// And ordinary requests are not mistaken for impossible ones.
func TestExpectation_ImpossibleDoesNotOverreach(t *testing.T) {
	for _, intent := range []string{
		"share the workload across the columns",
		"make this public-facing copy clearer",
		"organise this board",
	} {
		if expectationOf(intent).Impossible {
			t.Errorf("%q was read as outside what the agent can do", intent)
		}
	}
}

// The review turn's closing questions are the LAST thing the model reads, and
// last is what gets acted on. They were written for authoring and appended
// unconditionally.
//
// Asked "what is missing from this plan?", a run left the right comment, read
// "a question was asked — withdraw those edits", and then two paragraphs later
// read "is this a complete answer or a sketch? go back and put the real work
// in". It created a column and moved two cards, and explained itself with
// "I previously only left a comment about this, rather than acting on it".
func TestClosingQuestions_DoNotContradictTheMismatch(t *testing.T) {
	reporting := closingQuestions(expectationOf("what is missing from this plan?"))

	for _, pushes := range []string{"sketch", "put the real work in"} {
		if strings.Contains(reporting, pushes) {
			t.Errorf("a reporting run is told %q — the same turn that told it to withdraw "+
				"its edits then asks for more of them", pushes)
		}
	}
	if !strings.Contains(reporting, "undo_staged") {
		t.Error("the reporting close does not repeat the one instruction that matters")
	}

	// And authoring keeps the push it needs: the empty-headings failure this
	// text exists to catch is still the common one.
	authoring := closingQuestions(expectationOf("set up a production plan"))
	if !strings.Contains(authoring, "sketch") {
		t.Error("the authoring close lost its floor — empty headings go back to passing")
	}
}

// finishStaging is the smallest thing runFinish needs.
func finishStaging(intent string) *staging {
	return &staging{
		runID: "r1",
		scope: &BoardScope{
			Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
			Elements: map[string]*domain.Element{},
		},
		task:        TaskSpec{Intent: intent, Budget: Budget{MaxActions: 60}},
		plan:        &Plan{},
		created:     map[string]ActionKind{},
		failedCalls: map[string]int{},
		quotas:      newQuotas(),
		emit:        func(EventType, string, map[string]any) {},
	}
}

const realAnswer = "The plan is missing specific tasks for the Production and " +
	"Post-Production phases, as well as overall planning elements like a budget, " +
	"a schedule, and defined deliverables."

// Asked "what is missing from this plan?", a run named the gaps correctly and
// staged nothing — the whole answer lived in the summary, which is run-panel
// text that disappears when the panel closes. The board was left unable to say
// what was found, and the person got a paragraph to copy somewhere by hand.
//
// The review turn cannot catch this: it returns early on an empty plan, so the
// run that stages nothing is exactly the run that is never reviewed.
func TestFinish_AnAnswerMustLandOnTheBoard(t *testing.T) {
	s := finishStaging("what is missing from this plan?")

	out := s.runFinish(context.Background(), &toolArgs{Summary: realAnswer}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("finished with the answer in the run panel and nothing on the board")
	}
	if !strings.Contains(out.Content, "comment") {
		t.Errorf("the refusal does not name a tool that would fix it: %s", out.Content)
	}
	if s.finished {
		t.Error("the run finished anyway")
	}

	// Once only. A model that means it finishes on the second call; a check
	// that can fire twice is a loop that burns the whole step budget.
	again := s.runFinish(context.Background(), &toolArgs{Summary: realAnswer}, &reply{staging: s})
	if again.IsError {
		t.Errorf("refused twice — this loops: %s", again.Content)
	}
	if !s.finished {
		t.Error("the second call did not finish")
	}
}

// Narrow on purpose. Three ways a legitimately empty plan must still finish.
func TestFinish_EmptyPlansThatAreCorrect(t *testing.T) {
	for _, tc := range []struct{ name, intent, summary string }{{
		name:    "nothing is missing is a short answer",
		intent:  "what is missing from this plan?",
		summary: "Nothing — every stage has its deliverables.",
	}, {
		name:   "a request the agent cannot carry out stages nothing legitimately",
		intent: "share this board with the whole team and make it public",
		summary: "I cannot change sharing or permissions from here. You can do it from " +
			"the board menu, under Share, which is the only place that setting lives.",
	}, {
		name:   "not a question at all",
		intent: "organise this board",
		summary: "Everything on this board is already filed where it belongs, so there " +
			"was nothing worth moving. The three columns each hold their own stage.",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			s := finishStaging(tc.intent)
			out := s.runFinish(context.Background(),
				&toolArgs{Summary: tc.summary}, &reply{staging: s})
			if out.IsError {
				t.Errorf("refused a legitimate empty plan: %s", out.Content)
			}
			if !s.finished {
				t.Error("the run did not finish")
			}
		})
	}
}

// "complete" resolved correctly against the previous run — seven cards into
// the named column — and then created three MORE columns nobody asked for.
// Three live runs straight through the prompt's prose, which is why the check
// is a review-turn directive: the model acts on what the review names.
func TestFollowUpOverreach_NamesTheUnaskedForContainers(t *testing.T) {
	scope := &BoardScope{
		Board:   &domain.Element{ID: "b1", Type: domain.TypeBoard},
		History: []PriorRun{{Intent: "make a film", Outcome: "completed", Unmet: []string{"filling Editing — stopped before it"}}},
	}
	p := &Plan{Actions: []Action{
		{Kind: ActCreateNote, ParentID: "col-editing", Text: "Rough cut"},
		{Kind: ActCreateColumn, ParentID: "b1", Title: "Visual Effects"},
		{Kind: ActCreateColumn, ParentID: "b1", Title: "Deliverables"},
	}}

	out := followUpOverreach("complete", scope, p)
	if out == "" {
		t.Fatal("a terse follow-up creating containers was not flagged")
	}
	for _, name := range []string{"Visual Effects", "Deliverables"} {
		if !strings.Contains(out, name) {
			t.Errorf("the directive does not name %q — the model cannot withdraw what is not named", name)
		}
	}
	if !strings.Contains(out, "undo_staged") {
		t.Error("the directive does not say how to withdraw")
	}
	// The leftover is QUOTED, not alluded to. As an assertion ("what that run
	// left undone is the whole scope") the model argued and kept its columns
	// two runs in five; set against the previous run's own words it complied
	// five in five. The quote is the mechanism.
	if !strings.Contains(out, "filling Editing") {
		t.Error("the directive does not quote the leftover it holds the plan against")
	}
}

// The narrow gate, three ways: no history means no follow-up to creep on; a
// self-naming request is not terse; and filling without new structure is
// exactly the wanted behaviour.
func TestFollowUpOverreach_StaysOutOfTheWay(t *testing.T) {
	history := []PriorRun{{Intent: "make a film", Outcome: "completed", Unmet: []string{"filling Editing — stopped before it"}}}
	col := Plan{Actions: []Action{{Kind: ActCreateColumn, ParentID: "b1", Title: "New"}}}
	fill := Plan{Actions: []Action{{Kind: ActCreateNote, ParentID: "col-1", Text: "x"}}}

	if out := followUpOverreach("complete", &BoardScope{}, &col); out != "" {
		t.Errorf("flagged with no history: %s", out)
	}
	if out := followUpOverreach(
		"set up a complete production plan for a short documentary",
		&BoardScope{History: history}, &col); out != "" {
		t.Errorf("flagged a request that names its own scope: %s", out)
	}
	if out := followUpOverreach("complete", &BoardScope{History: history}, &fill); out != "" {
		t.Errorf("flagged a pure fill — the exact behaviour this exists to protect: %s", out)
	}
}
