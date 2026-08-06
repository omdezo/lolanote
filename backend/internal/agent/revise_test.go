package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

func reviseStaging() *staging {
	return &staging{
		runID: "r1",
		scope: &BoardScope{
			Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
			Elements: map[string]*domain.Element{},
		},
		task:        TaskSpec{Budget: Budget{MaxActions: 60}},
		plan:        &Plan{},
		created:     map[string]ActionKind{},
		failedCalls: map[string]int{},
		quotas:      newQuotas(),
		emit:        func(EventType, string, map[string]any) {},
	}
}

// "Set up a complete production plan for a short documentary" produced THIRTEEN
// columns for a five-stage plan: the model staged a set, thought again, staged
// a better set, and could not withdraw the first. Eight duplicates reached the
// board, most of them empty, and it read as chaos.
//
// The content was good. The harness could only ever ADD.
func TestRevise_RefusesADuplicateSibling(t *testing.T) {
	s := reviseStaging()
	if _, err := s.add(Action{
		Kind: ActCreateColumn, ParentID: "b1", Title: "Pre-Production",
	}); err != nil {
		t.Fatalf("first column: %v", err)
	}

	twin := s.duplicateSibling("b1", "Pre-Production", ActCreateColumn)
	if twin == "" {
		t.Fatal("a second column with the same name was not recognised as a duplicate")
	}
	// Case and spacing are not what make two names different.
	if s.duplicateSibling("b1", "  pre-production  ", ActCreateColumn) == "" {
		t.Error("a duplicate escaped by differing only in case and spacing")
	}
	// A genuinely different name is fine.
	if s.duplicateSibling("b1", "Production", ActCreateColumn) != "" {
		t.Error("a distinct column was called a duplicate")
	}
	// And a different parent is a different place.
	if s.duplicateSibling("other", "Pre-Production", ActCreateColumn) != "" {
		t.Error("a column in another board was called a duplicate")
	}
}

// Only containers. Two cards with the same text are a legitimate thing to want.
func TestRevise_DuplicateGuardOnlyAppliesToContainers(t *testing.T) {
	s := reviseStaging()
	if _, err := s.add(Action{Kind: ActCreateNote, ParentID: "b1", Title: "Same"}); err != nil {
		t.Fatalf("first note: %v", err)
	}
	if s.duplicateSibling("b1", "Same", ActCreateNote) != "" {
		t.Error("two notes with the same title were refused; only structure needs to be unique")
	}
}

// Withdrawing takes the contents with it: a card in a column that is no longer
// being created has nowhere to be.
func TestRevise_UnstageCascadesToContents(t *testing.T) {
	s := reviseStaging()
	col, _ := s.add(Action{Kind: ActCreateColumn, ParentID: "b1", Title: "Wrong cut"})
	s.add(Action{Kind: ActCreateNote, ParentID: col, Text: "inside it"})
	keep, _ := s.add(Action{Kind: ActCreateColumn, ParentID: "b1", Title: "Right cut"})
	s.add(Action{Kind: ActCreateNote, ParentID: keep, Text: "keeps"})

	if len(s.plan.Actions) != 4 {
		t.Fatalf("staged %d actions, want 4", len(s.plan.Actions))
	}

	out := s.runUnstage(nil, &toolArgs{ElementID: col}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("unstage failed: %s", out.Content)
	}
	if len(s.plan.Actions) != 2 {
		t.Fatalf("%d actions remain, want the 2 belonging to the kept column", len(s.plan.Actions))
	}
	for _, a := range s.plan.Actions {
		if a.ElementID == col || a.ParentID == col {
			t.Errorf("the withdrawn column or its contents survived: %+v", a)
		}
	}
	// Sequence must stay positional — Preconditions checks it.
	for i, a := range s.plan.Actions {
		if a.Seq != i {
			t.Errorf("action %d carries seq %d; the plan is no longer in sequence", i, a.Seq)
		}
	}
	// And the withdrawn id is no longer a legal parent.
	if _, still := s.created[col]; still {
		t.Error("the withdrawn column is still offered as a parent")
	}
}

// A connector whose endpoint is withdrawn goes with it — a line to nothing is
// worse than no line.
func TestRevise_UnstageRemovesDanglingConnectors(t *testing.T) {
	s := reviseStaging()
	a, _ := s.add(Action{Kind: ActCreateNote, ParentID: "b1", Text: "one"})
	b, _ := s.add(Action{Kind: ActCreateNote, ParentID: "b1", Text: "two"})
	s.add(Action{Kind: ActConnect, ParentID: "b1", FromID: a, ToID: b})

	s.runUnstage(nil, &toolArgs{ElementID: b}, &reply{staging: s})

	for _, act := range s.plan.Actions {
		if act.Kind == ActConnect {
			t.Errorf("a connector survived the loss of its endpoint: %+v", act)
		}
	}
}

// It removes YOUR staged changes and nothing else. Deleting something already
// on the board is a different capability, gated on review for good reason.
func TestRevise_UnstageCannotTouchTheBoard(t *testing.T) {
	s := reviseStaging()
	s.scope.Elements["existing"] = &domain.Element{ID: "existing", Type: domain.TypeCard}

	out := s.runUnstage(nil, &toolArgs{ElementID: "existing"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("undo_staged removed something that was already on the board")
	}
	if !strings.Contains(out.Content, "staged") {
		t.Errorf("the refusal does not explain the boundary: %s", out.Content)
	}
}
