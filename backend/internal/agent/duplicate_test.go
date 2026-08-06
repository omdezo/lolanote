package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The guard folded case and whitespace and nothing else, so "Concept" beside
// "Concept & Premise" was two shelves for one idea — and the "complete" run
// produced eighteen of those in a single plan.
//
// The pairs that must NOT match are the load-bearing half. Every seeded film
// board in this product has Pre-Production and Production side by side, and a
// substring test calls the second a duplicate of the first.
func TestDuplicateNames_MeanTheSameThingOrDoNot(t *testing.T) {
	same := [][2]string{
		{"Editing", "editing"},
		{"Editing", "  EDITING  "},
		{"Concept", "Concept & Premise"},
		{"Concept & Premise", "Concept and Premise"},
		{"Dev & Scoping", "Dev Scoping"},
		{"Pre-Production", "Pre Production"},
		{"To Do", "Todo"},
		{"Sound", "Sound Design"},
	}
	for _, p := range same {
		if !sameStructureName(p[0], p[1]) {
			t.Errorf("%q and %q name the same shelf and the guard says otherwise", p[0], p[1])
		}
	}

	different := [][2]string{
		// The one that matters: two stages of a film, not one misspelled twice.
		{"Production", "Pre-Production"},
		{"Production", "Post-Production"},
		{"Pre-Production", "Post-Production"},
		// A ratio on letters calls these an 80% match. The token that differs is
		// the entire point of the name.
		{"Week 1", "Week 2"},
		{"Scene 3", "Scene 4"},
		{"Act I", "Act II"},
		{"Casting", "Cast"},
		{"Budget", "Schedule"},
	}
	for _, p := range different {
		if sameStructureName(p[0], p[1]) {
			t.Errorf("%q and %q are different shelves and the guard merged them", p[0], p[1])
		}
	}
}

// dupStaging builds a run over a real repository, because emptiness is a fact
// about the board and not about the scope: the redirect asks the store whether
// a column has anything in it, and a fixture that answers from memory alone
// would prove the wrong thing.
func dupStaging(t *testing.T, seed func(put func(id string, typ domain.ElementType, parent, title string))) *staging {
	t.Helper()
	repo := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	put := func(id string, typ domain.ElementType, parent, title string) {
		t.Helper()
		content := domain.Content{"title": title}
		if typ == domain.TypeCard {
			content = domain.Content{"textPreview": title}
		}
		if err := repo.Insert(ctx, &domain.Element{
			ID: id, Type: typ, Content: content,
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	put("root-board", domain.TypeBoard, "", "Film")
	put("sub-board", domain.TypeBoard, "root-board", "Post")
	seed(put)

	scope, err := CompileScope(ctx, repo, TaskSpec{
		Owner: "alice", RootBoardID: "root-board", Scope: ScopeBoard,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return &staging{
		runID: "run-dup", scope: scope, elements: repo,
		task:        TaskSpec{Budget: Budget{MaxActions: 60}},
		plan:        &Plan{},
		created:     map[string]ActionKind{},
		failedCalls: map[string]int{},
		quotas:      newQuotas(),
		emit:        func(EventType, string, map[string]any) {},
	}
}

// The W2 acceptance line, first half. An empty twin is not a conflict — it is
// what the model was reaching for. Refusing is how "complete" became eighteen
// new empty columns beside eighteen empty ones: the model wanted somewhere to
// put cards and a refusal gave it nowhere, so it built its own.
func TestCreateColumn_RedirectsIntoAnEmptyTwin(t *testing.T) {
	s := dupStaging(t, func(put func(string, domain.ElementType, string, string)) {
		put("col-editing", domain.TypeColumn, "sub-board", "Editing")
	})

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Title: "Editing"},
		&reply{staging: s, call: scriptedCall(toolCreateColumn)})

	if out.IsError {
		t.Fatalf("an empty twin was refused rather than offered: %s", out.Content)
	}
	if len(s.plan.Actions) != 0 {
		t.Errorf("staged %d action(s); the whole point is that it stages none", len(s.plan.Actions))
	}
	if !strings.Contains(out.Content, "col-editing") {
		t.Errorf("the outcome does not hand back the id to use: %s", out.Content)
	}
	if !strings.Contains(out.Content, "did not create a second one") {
		t.Errorf("the outcome does not say what happened: %s", out.Content)
	}

	// And the id it handed back has to WORK as a parent on the next call, or the
	// redirect is a dead end dressed as a success.
	fill := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "col-editing", Text: "conform the cut"},
		&reply{staging: s, call: scriptedCall(toolCreateNote)})
	if fill.IsError {
		t.Fatalf("the id the redirect handed back is not usable as a parent: %s", fill.Content)
	}
	if got := lastAction(t, s).ParentID; got != "col-editing" {
		t.Errorf("the note landed under %q, not in the existing column", got)
	}
}

// The second half. A twin with things in it is a real collision: handing back
// its id would silently merge two different piles of work under one name.
func TestCreateColumn_RefusesAndNamesANonEmptyTwin(t *testing.T) {
	s := dupStaging(t, func(put func(string, domain.ElementType, string, string)) {
		put("col-editing", domain.TypeColumn, "sub-board", "Editing")
		put("card-cut", domain.TypeCard, "col-editing", "rough cut")
	})

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Title: "Editing"},
		&reply{staging: s, call: scriptedCall(toolCreateColumn)})

	if !out.IsError {
		t.Fatalf("a second Editing was allowed beside a full one: %s", out.Content)
	}
	if len(s.plan.Actions) != 0 {
		t.Errorf("staged %d action(s) despite refusing", len(s.plan.Actions))
	}
	if !strings.Contains(out.Content, "col-editing") {
		t.Errorf("the refusal does not name the column it means: %s", out.Content)
	}
}

// The near-miss the normalizer exists for, driven end to end: an empty
// "Concept & Premise" absorbs a create_column called "Concept".
func TestCreateColumn_RedirectsOnANormalizedMatch(t *testing.T) {
	s := dupStaging(t, func(put func(string, domain.ElementType, string, string)) {
		put("col-concept", domain.TypeColumn, "sub-board", "Concept & Premise")
	})

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Title: "Concept"},
		&reply{staging: s, call: scriptedCall(toolCreateColumn)})

	if out.IsError || len(s.plan.Actions) != 0 {
		t.Fatalf("near-duplicate not absorbed: err=%v actions=%d content=%s",
			out.IsError, len(s.plan.Actions), out.Content)
	}
	if !strings.Contains(out.Content, "col-concept") {
		t.Errorf("the outcome does not name the column to use: %s", out.Content)
	}
}

// Pre-Production and Production are two stages, and the guard must let the
// second one through. This is the false positive a substring rule would create,
// and it would block the most ordinary structure this product builds.
func TestCreateColumn_DistinctStagesAreNotDuplicates(t *testing.T) {
	s := dupStaging(t, func(put func(string, domain.ElementType, string, string)) {
		put("col-pre", domain.TypeColumn, "sub-board", "Pre-Production")
	})

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Title: "Production"},
		&reply{staging: s, call: scriptedCall(toolCreateColumn)})

	if out.IsError {
		t.Fatalf("Production was refused because Pre-Production exists: %s", out.Content)
	}
	if len(s.plan.Actions) != 1 {
		t.Fatalf("actions = %d, want the column to be staged", len(s.plan.Actions))
	}
}

// The sibling only became visible with the subtree walk. Before it, an
// exact-name Editing beside the existing Editing inside a nested board passed
// straight through, because the scope stopped at the board tile.
func TestCreateColumn_GuardSeesSiblingsInsideNestedBoards(t *testing.T) {
	s := dupStaging(t, func(put func(string, domain.ElementType, string, string)) {
		put("col-editing", domain.TypeColumn, "sub-board", "Editing")
		put("card-cut", domain.TypeCard, "col-editing", "rough cut")
	})
	if _, ok := s.scope.Elements["col-editing"]; !ok {
		t.Fatal("the column inside the nested board is not in scope at all")
	}
	if twin := s.duplicateSibling("sub-board", "Editing", ActCreateColumn); twin != "col-editing" {
		t.Errorf("duplicateSibling = %q, want col-editing", twin)
	}
}

// A twin the run staged moments ago and has already started filling is not a
// redirect target: handing its id back would lose the second name entirely
// while the first is already carrying different content.
func TestCreateColumn_StagedTwinInProgressIsNotEmpty(t *testing.T) {
	s := dupStaging(t, func(put func(string, domain.ElementType, string, string)) {})

	first := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Title: "Sound"},
		&reply{staging: s, call: scriptedCall(toolCreateColumn)})
	if first.IsError {
		t.Fatalf("first create failed: %s", first.Content)
	}
	staged := lastAction(t, s).ElementID

	// Empty so far, so the second call is offered the first one.
	again := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Title: "Sound"},
		&reply{staging: s, call: scriptedCall(toolCreateColumn)})
	if again.IsError || !strings.Contains(again.Content, staged) {
		t.Fatalf("an empty staged twin was not offered back: err=%v %s", again.IsError, again.Content)
	}

	// Now put something in it, and the same call must refuse rather than merge.
	if fill := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: staged, Text: "spot the mix"},
		&reply{staging: s, call: scriptedCall(toolCreateNote)}); fill.IsError {
		t.Fatalf("filling the staged column failed: %s", fill.Content)
	}
	third := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Title: "Sound"},
		&reply{staging: s, call: scriptedCall(toolCreateColumn)})
	if !third.IsError {
		t.Errorf("a staged column with content was offered as an empty one: %s", third.Content)
	}
}
