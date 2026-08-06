package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// A create op was not a full element write, and nothing said so: labelIds and
// acl were dropped without a word, so a caller got a 201 and an untagged
// element. The same silent-drop class as the content.cells and content.hex
// bugs — and it meant create and update had different undocumented field sets,
// which is exactly the asymmetry a compiler branch gets wrong.
func createFieldsFixture(t *testing.T) (*TransactionService, *memory.ElementRepo, domain.LabelRepository, context.Context) {
	t.Helper()
	elements := memory.NewElementRepo()
	labels := memory.NewLabelRepo()
	ctx := context.Background()
	now := time.Now().UTC()

	board := &domain.Element{
		ID: "cf00000000000000000ba001", Type: domain.TypeBoard,
		Location:  domain.Location{Section: domain.SectionCanvas},
		Content:   domain.Content{"title": "Board"},
		ACL:       &domain.ACL{OwnerID: "alice", Editors: []string{}},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}
	if err := elements.Insert(ctx, board); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	for _, l := range []*domain.Label{
		{ID: "lab-alice-q3", OwnerID: "alice", Name: "Q3"},
		{ID: "lab-bob-secret", OwnerID: "bob", Name: "Bob's tag"},
	} {
		if err := labels.Insert(ctx, l); err != nil {
			t.Fatalf("seed label: %v", err)
		}
	}

	svc, _ := partialWriteFixture(t, elements)
	svc.AttachLabels(labels)
	return svc, elements, labels, ctx
}

func createOpWith(extra map[string]any) domain.Op {
	changes := domain.Content{
		"type": string(domain.TypeCard),
		"location": map[string]any{
			"parentId": "cf00000000000000000ba001", "section": "CANVAS",
		},
		"content": map[string]any{"textPreview": "a note"},
	}
	for k, v := range extra {
		changes[k] = v
	}
	return domain.Op{ElementID: "cf00000000000000000bc001", Action: domain.ActionCreate, Changes: changes}
}

func TestCreate_CarriesTheCallersOwnLabels(t *testing.T) {
	svc, elements, labels, ctx := createFieldsFixture(t)

	if _, err := svc.Apply(ctx, &domain.Principal{Sub: "alice"},
		"cf00000000000000000ba001", "", []domain.Op{
			createOpWith(map[string]any{"labelIds": []any{"lab-alice-q3"}}),
		}); err != nil {
		t.Fatalf("a labelled create was refused: %v", err)
	}

	el, err := elements.Get(ctx, "cf00000000000000000bc001")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(el.LabelIDs) != 1 || el.LabelIDs[0] != "lab-alice-q3" {
		t.Fatalf("the element came back with labels %v — the create dropped them", el.LabelIDs)
	}
	// The taxonomy has to stay honest about itself.
	l, _ := labels.Get(ctx, "lab-alice-q3")
	if l.UsageCount != 1 {
		t.Errorf("usage count = %d, want 1", l.UsageCount)
	}
}

// A label is private to whoever coined it. A create must not become the one
// door through which someone else's vocabulary can be attached.
func TestCreate_RefusesSomebodyElsesLabel(t *testing.T) {
	svc, elements, _, ctx := createFieldsFixture(t)

	_, err := svc.Apply(ctx, &domain.Principal{Sub: "alice"},
		"cf00000000000000000ba001", "", []domain.Op{
			createOpWith(map[string]any{"labelIds": []any{"lab-bob-secret"}}),
		})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a create attached another person's label: %v", err)
	}
	if _, gerr := elements.Get(ctx, "cf00000000000000000bc001"); gerr == nil {
		t.Error("the element was created anyway")
	}
}

// The reject-vs-ignore decision is the whole point. Sharing is not a create-time
// concern, and silently ignoring an acl is worse than refusing it: the caller
// walks away believing they shared something.
func TestCreate_RefusesAnACLRatherThanIgnoringIt(t *testing.T) {
	svc, elements, _, ctx := createFieldsFixture(t)

	_, err := svc.Apply(ctx, &domain.Principal{Sub: "alice"},
		"cf00000000000000000ba001", "", []domain.Op{
			createOpWith(map[string]any{"acl": map[string]any{
				"ownerId": "mallory", "editors": []any{"mallory"},
			}}),
		})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a create carrying an acl was answered with %v — silently "+
			"ignoring it would tell the caller they shared something", err)
	}
	if _, gerr := elements.Get(ctx, "cf00000000000000000bc001"); gerr == nil {
		t.Error("the element was created anyway")
	}
}

// The ordinary create is untouched, or the guard is a wall.
func TestCreate_WithoutEitherFieldStillWorks(t *testing.T) {
	svc, elements, _, ctx := createFieldsFixture(t)

	if _, err := svc.Apply(ctx, &domain.Principal{Sub: "alice"},
		"cf00000000000000000ba001", "", []domain.Op{createOpWith(nil)}); err != nil {
		t.Fatalf("an ordinary create broke: %v", err)
	}
	if _, err := elements.Get(ctx, "cf00000000000000000bc001"); err != nil {
		t.Fatalf("the element was not created: %v", err)
	}
}
