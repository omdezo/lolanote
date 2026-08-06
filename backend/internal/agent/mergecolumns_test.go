package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The agent could make duplicate structure and never repair it. One real board
// came out of an organizing run holding `Dev & Scoping` twice and `Editing`
// twice, and every verb on offer made it worse: delete takes the cards with the
// shelf, and moving the cards by hand leaves the empty shelf standing.
//
// Built over a real repository because the whole point of the tool is that it
// reads what is ACTUALLY inside the column being dissolved. A fixture answering
// from scope would prove the opposite of what the code has to do.
func mergeStaging(t *testing.T) (*staging, *memory.ElementRepo) {
	t.Helper()
	repo := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	put := func(id string, typ domain.ElementType, parent, title string, index float64) {
		t.Helper()
		content := domain.Content{"title": title}
		if typ == domain.TypeCard {
			content = domain.Content{"textPreview": title}
		}
		if err := repo.Insert(ctx, &domain.Element{
			ID: id, Type: typ, Content: content,
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas, Index: index},
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	put("root-board", domain.TypeBoard, "", "Film", 0)
	put("keep-col", domain.TypeColumn, "root-board", "Editing", 1)
	put("keep-card", domain.TypeCard, "keep-col", "assembly cut", 3)
	put("drop-col", domain.TypeColumn, "root-board", "Editing", 2)
	put("drop-a", domain.TypeCard, "drop-col", "picture lock", 1)
	put("drop-b", domain.TypeCard, "drop-col", "sound mix", 2)
	put("sub-board", domain.TypeBoard, "root-board", "Post", 3)

	scope, err := CompileScope(ctx, repo, TaskSpec{
		Owner: "alice", RootBoardID: "root-board", Scope: ScopeBoard,
	})
	if err != nil {
		t.Fatalf("compile scope: %v", err)
	}
	return &staging{
		runID: "run-merge", scope: scope, elements: repo,
		task:        TaskSpec{Autonomy: AutonomyPreview, Budget: Budget{MaxActions: 60}},
		plan:        &Plan{},
		created:     map[string]ActionKind{},
		failedCalls: map[string]int{},
		quotas:      newQuotas(),
		emit:        func(EventType, string, map[string]any) {},
	}, repo
}

// The acceptance line: children re-parented in order, shell trashed, inverse
// restores. Asserted on the compiled OPS, because the location the renderer
// reads is the only place "in order" is either true or not.
func TestMergeColumns_MovesTheChildrenAndTrashesTheShell(t *testing.T) {
	s, _ := mergeStaging(t)

	out := s.runMergeColumns(context.Background(),
		&toolArgs{KeepID: "keep-col", DropID: "drop-col"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("merge refused: %s", out.Content)
	}

	OrderPlan(s.plan, s.scope)
	ops, err := CompileOps(s.plan, s.scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("compiled %d ops, want two moves and a delete: %+v", len(ops), ops)
	}

	var lastIndex float64
	for i, want := range []string{"drop-a", "drop-b"} {
		op := ops[i]
		if op.ElementID != want || op.Action != domain.ActionMove {
			t.Fatalf("op %d = %s %s, want a move of %s", i, op.Action, op.ElementID, want)
		}
		loc, _ := op.Changes["location"].(map[string]any)
		if loc["parentId"] != "keep-col" {
			t.Errorf("%s lands in %v, not the surviving column", want, loc["parentId"])
		}
		index, _ := loc["index"].(float64)
		// After what the surviving column already holds — its last card sits at
		// index 3 — and in the order they were staged.
		if index <= 3 || index <= lastIndex {
			t.Errorf("%s takes index %v, which lands it on top of what is already in the column", want, index)
		}
		lastIndex = index
	}

	shell := ops[2]
	if shell.ElementID != "drop-col" || shell.Action != domain.ActionDelete {
		t.Fatalf("last op is %s %s, want the emptied column trashed", shell.Action, shell.ElementID)
	}

	// The inverse puts the board back exactly: the shell restored, both cards
	// returned to it. A revert that restored the column and left the cards in
	// the other one would be a second reorganisation.
	inverse := InvertOps(ops)
	if inverse[0].ElementID != "drop-col" || inverse[0].Action != domain.ActionRestore {
		t.Fatalf("the revert does not restore the column first: %+v", inverse[0])
	}
	for _, inv := range inverse[1:] {
		loc, _ := inv.Changes["location"].(map[string]any)
		if loc["parentId"] != "drop-col" {
			t.Errorf("reverting %s puts it back under %v, not the column it came from", inv.ElementID, loc["parentId"])
		}
	}
}

// The digest elides a container's contents past a handful, and an id outside
// the compiled scope is refused by both the compiler and the preconditions. A
// merge that only moved what the model happened to be SHOWN would trash the
// rest with the shell.
func TestMergeColumns_MovesChildrenTheDigestNeverShowed(t *testing.T) {
	s, _ := mergeStaging(t)
	// What budget exhaustion leaves behind: the card is on the board and not in
	// the scope the model was given.
	delete(s.scope.Elements, "drop-b")

	out := s.runMergeColumns(context.Background(),
		&toolArgs{KeepID: "keep-col", DropID: "drop-col"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("merge refused: %s", out.Content)
	}
	if _, err := CompileOps(s.plan, s.scope); err != nil {
		t.Fatalf("a child read live was never registered in scope: %v", err)
	}
	moved := 0
	for _, a := range s.plan.Actions {
		if a.Kind == ActMove {
			moved++
		}
	}
	if moved != 2 {
		t.Errorf("moved %d card(s); the one the digest elided would have been trashed with the shell", moved)
	}
}

func TestMergeColumns_RefusesWhatIsNotTwoColumns(t *testing.T) {
	cases := []struct{ name, keep, drop, want string }{
		{"same column twice", "keep-col", "keep-col", "itself"},
		{"a card", "keep-col", "keep-card", "merge_notes"},
		{"a board", "sub-board", "keep-col", "merge_columns joins two columns"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := mergeStaging(t)
			out := s.runMergeColumns(context.Background(),
				&toolArgs{KeepID: tc.keep, DropID: tc.drop}, &reply{staging: s})
			if !out.IsError {
				t.Fatalf("allowed: %s", out.Content)
			}
			if !strings.Contains(out.Content, tc.want) {
				t.Errorf("refusal reads %q, which does not say %q", out.Content, tc.want)
			}
			if len(s.plan.Actions) != 0 {
				t.Errorf("staged %d action(s) despite refusing", len(s.plan.Actions))
			}
		})
	}
}

// It trashes a column, so it is offered on the same terms as delete: only where
// a person sees the plan before it commits.
func TestMergeColumns_RefusedWhenTheRunMayNotTrash(t *testing.T) {
	s, _ := mergeStaging(t)
	s.task.Autonomy = AutonomyAuto

	out := s.runMergeColumns(context.Background(),
		&toolArgs{KeepID: "keep-col", DropID: "drop-col"}, &reply{staging: s})
	if !out.IsError {
		t.Fatalf("an unattended run dissolved a column: %s", out.Content)
	}
	if len(s.plan.Actions) != 0 {
		t.Errorf("staged %d action(s) despite refusing", len(s.plan.Actions))
	}
}

// And the model can only see it where it can use it.
func TestMergeColumns_OfferedOnlyWithDeletes(t *testing.T) {
	has := func(tools []cognition.ToolDef, name string) bool {
		for _, tl := range tools {
			if tl.Name == name {
				return true
			}
		}
		return false
	}
	if !has(ToolCatalogue(true, false), toolMergeColumns) {
		t.Error("a preview run is not offered merge_columns")
	}
	if has(ToolCatalogue(false, false), toolMergeColumns) {
		t.Error("an unattended run is shown a tool it will always be refused")
	}
}
