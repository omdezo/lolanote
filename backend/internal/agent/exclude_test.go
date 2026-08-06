package agent

import (
	"context"
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// There was no way to tell the agent not to read something, at any layer.
//
// Cast medical notes, a distributor's numbers, an unsigned contract and a
// private note about a crew member sit on the same board as the shot list, and
// the only way to keep them out of a model context was to keep them out of the
// product. Scope narrowing did not help: it is a per-REQUEST choice made by
// whoever starts the run, so it protects nothing from the next run or from a
// collaborator's.
//
// Enforcement is at scope compilation, so the assertion is about SCOPE, not
// about any one tool refusing.
func TestExclude_PrivateItemsNeverEnterTheScope(t *testing.T) {
	ctx := context.Background()
	els := memory.NewElementRepo()
	seedExcluded(t, els)

	scope, err := CompileScope(ctx, els, TaskSpec{RootBoardID: "b1", Scope: ScopeBoard, Owner: "alice"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if _, ok := scope.Elements["private-1"]; ok {
		t.Error("a card marked private is addressable by the run")
	}
	if _, ok := scope.Elements["public-1"]; !ok {
		t.Error("an ordinary card was dropped along with the private one")
	}
	// A private CONTAINER takes its whole subtree with it: the point is not to
	// hide a heading, it is to hide what is under it.
	if _, ok := scope.Elements["private-col"]; ok {
		t.Error("a column marked private is addressable")
	}
	if _, ok := scope.Elements["inside-private"]; ok {
		t.Error("the contents of a private column reached the run")
	}

	// The rendered page must SAY the hole is there. Silence lets the model
	// answer "is anything missing?" confidently and wrongly.
	page := scope.Render("")
	if !strings.Contains(page, "PRIVATE:") {
		t.Errorf("the digest hides the exclusion entirely:\n%s", page)
	}
	// And it must not leak what it excluded.
	if strings.Contains(page, "Cast medical notes") {
		t.Errorf("the private text is in the prompt:\n%s", page)
	}
}

// read_board reads children live from the repository rather than from the
// compiled scope, so it is a second door into the addressable set — and it was
// the way round the exclusion.
func TestExclude_ReadBoardDoesNotRouteAroundIt(t *testing.T) {
	ctx := context.Background()
	els := memory.NewElementRepo()
	seedExcluded(t, els)

	scope, err := CompileScope(ctx, els, TaskSpec{RootBoardID: "b1", Scope: ScopeBoard, Owner: "alice"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	s := &staging{
		scope: scope, elements: els, plan: &Plan{},
		created: map[string]ActionKind{}, failedCalls: map[string]int{},
		quotas: newQuotas(), emit: func(EventType, string, map[string]any) {},
		task: TaskSpec{Budget: Budget{MaxActions: 60}, Owner: "alice"},
	}

	digest, err := s.readBoard(ctx, "b1")
	if err != nil {
		t.Fatalf("read_board: %v", err)
	}
	if strings.Contains(digest, "private-1") || strings.Contains(digest, "Cast medical notes") {
		t.Errorf("read_board printed material marked private:\n%s", digest)
	}
	if _, ok := s.scope.Elements["private-1"]; ok {
		t.Error("read_board made a private element addressable")
	}
	if !strings.Contains(digest, "marked private") {
		t.Errorf("read_board came back looking exhaustive:\n%s", digest)
	}
}

func seedExcluded(t *testing.T, els *memory.ElementRepo) {
	t.Helper()
	ctx := context.Background()
	put := func(el *domain.Element) {
		if err := els.Insert(ctx, el); err != nil {
			t.Fatalf("seed %s: %v", el.ID, err)
		}
	}
	put(&domain.Element{ID: "b1", Type: domain.TypeBoard,
		Content: domain.Content{"title": "Film"},
		ACL:     &domain.ACL{OwnerID: "alice"}})
	put(&domain.Element{ID: "public-1", Type: domain.TypeCard,
		Content:  domain.Content{"textPreview": "Harbour interview"},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}})
	put(&domain.Element{ID: "private-1", Type: domain.TypeCard,
		Content: domain.Content{
			"textPreview":   "Cast medical notes — do not circulate",
			agentExcludeKey: true,
		},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}})
	put(&domain.Element{ID: "private-col", Type: domain.TypeColumn,
		Content:  domain.Content{"title": "Deal terms", agentExcludeKey: true},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}})
	put(&domain.Element{ID: "inside-private", Type: domain.TypeCard,
		Content:  domain.Content{"textPreview": "Distributor advance: 1.2m"},
		Location: domain.Location{ParentID: "private-col", Section: domain.SectionCanvas}})
}
