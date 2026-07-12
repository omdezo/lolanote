package service

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// fixture builds two boards owned by two different users, each with one card.
func fixture(t *testing.T) (*TransactionService, *memory.ElementRepo, map[string]*domain.Element) {
	t.Helper()
	elements := memory.NewElementRepo()
	txns := memory.NewTransactionRepo()
	access := NewAccessResolver(elements)
	counter := 0
	newID := IDGenerator(func() string {
		counter++
		return time_hex(counter)
	})
	svc := NewTransactionService(elements, txns, access, nil, newID, zap.NewNop())

	now := time.Now().UTC()
	mk := func(id, typ, parent, owner string) *domain.Element {
		el := &domain.Element{
			ID: id, Type: domain.ElementType(typ),
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:   domain.Content{},
			CreatedBy: owner, CreatedAt: now, UpdatedAt: now,
		}
		if typ == "BOARD" {
			el.ACL = &domain.ACL{OwnerID: owner, Editors: []string{}}
		}
		_ = elements.Insert(context.Background(), el)
		return el
	}

	items := map[string]*domain.Element{}
	items["boardA"] = mk("aaaaaaaaaaaaaaaaaaaaaaa1", "BOARD", "", "alice")
	items["cardA"] = mk("aaaaaaaaaaaaaaaaaaaaaaa2", "CARD", items["boardA"].ID, "alice")
	items["boardB"] = mk("bbbbbbbbbbbbbbbbbbbbbbb1", "BOARD", "", "bob")
	items["cardB"] = mk("bbbbbbbbbbbbbbbbbbbbbbb2", "CARD", items["boardB"].ID, "bob")
	return svc, elements, items
}

func time_hex(n int) string {
	// 24-hex ids for freshly created elements in tests.
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 24)
	for i := range out {
		out[i] = hexdigits[(n+i)%16]
	}
	return string(out)
}

// TestApply_IDOR_RejectsCrossBoardOp is the security-critical case: Alice,
// an editor of her own board, must NOT be able to mutate Bob's card by
// declaring her boardId while targeting his element.
func TestApply_IDOR_RejectsCrossBoardOp(t *testing.T) {
	svc, elements, items := fixture(t)
	alice := &domain.Principal{Sub: "alice"}

	op := domain.Op{
		ElementID: items["cardB"].ID, // Bob's card
		Action:    domain.ActionUpdate,
		Changes:   domain.Content{"content": map[string]any{"textPreview": "hacked"}},
	}
	// Alice claims her own board as the transaction scope.
	_, err := svc.Apply(context.Background(), alice, items["boardA"].ID, "client1", []domain.Op{op})
	if err != domain.ErrForbidden {
		t.Fatalf("expected ErrForbidden for cross-board op, got %v", err)
	}
	// Bob's card must be untouched.
	got, _ := elements.Get(context.Background(), items["cardB"].ID)
	if got.Content["textPreview"] == "hacked" {
		t.Fatal("IDOR: Bob's card was mutated across boards")
	}
}

// TestApply_AllowsInBoardOp confirms the legitimate path still works.
func TestApply_AllowsInBoardOp(t *testing.T) {
	svc, elements, items := fixture(t)
	alice := &domain.Principal{Sub: "alice"}

	op := domain.Op{
		ElementID: items["cardA"].ID,
		Action:    domain.ActionUpdate,
		Changes:   domain.Content{"content": map[string]any{"textPreview": "hello"}},
	}
	if _, err := svc.Apply(context.Background(), alice, items["boardA"].ID, "client1", []domain.Op{op}); err != nil {
		t.Fatalf("legit op rejected: %v", err)
	}
	got, _ := elements.Get(context.Background(), items["cardA"].ID)
	if got.Content["textPreview"] != "hello" {
		t.Fatal("legit op did not apply")
	}
}

// TestApply_PartialWriteGuard: if any op in a transaction is out of scope,
// NONE of the ops apply.
func TestApply_PartialWriteGuard(t *testing.T) {
	svc, elements, items := fixture(t)
	alice := &domain.Principal{Sub: "alice"}

	ops := []domain.Op{
		{ElementID: items["cardA"].ID, Action: domain.ActionUpdate, Changes: domain.Content{"content": map[string]any{"textPreview": "ok"}}},
		{ElementID: items["cardB"].ID, Action: domain.ActionUpdate, Changes: domain.Content{"content": map[string]any{"textPreview": "bad"}}},
	}
	if _, err := svc.Apply(context.Background(), alice, items["boardA"].ID, "c", ops); err != domain.ErrForbidden {
		t.Fatalf("expected rejection, got %v", err)
	}
	got, _ := elements.Get(context.Background(), items["cardA"].ID)
	if got.Content["textPreview"] == "ok" {
		t.Fatal("partial write: first op applied despite second being rejected")
	}
}

// TestApply_TrashCascade: deleting a board trashes its children under one
// batch, and restoring the board restores them.
func TestApply_TrashCascade(t *testing.T) {
	svc, elements, items := fixture(t)
	alice := &domain.Principal{Sub: "alice"}
	ctx := context.Background()

	// Nest a sub-board with a card under boardA.
	sub := &domain.Element{ID: "aaaaaaaaaaaaaaaaaaaaaaa3", Type: domain.TypeBoard,
		Location: domain.Location{ParentID: items["boardA"].ID, Section: domain.SectionCanvas},
		ACL:      &domain.ACL{OwnerID: "alice"}, Content: domain.Content{}, CreatedBy: "alice"}
	_ = elements.Insert(ctx, sub)
	nested := &domain.Element{ID: "aaaaaaaaaaaaaaaaaaaaaaa4", Type: domain.TypeCard,
		Location: domain.Location{ParentID: sub.ID, Section: domain.SectionCanvas},
		Content:  domain.Content{}, CreatedBy: "alice"}
	_ = elements.Insert(ctx, nested)

	del := domain.Op{ElementID: sub.ID, Action: domain.ActionDelete}
	if _, err := svc.Apply(ctx, alice, items["boardA"].ID, "c", []domain.Op{del}); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	// The nested card must now be trashed (won't leak into search).
	got, _ := elements.Get(ctx, nested.ID)
	if !got.IsDeleted() {
		t.Fatal("cascade: nested card not trashed with its board")
	}

	// Restoring the sub-board restores the nested card too.
	restore := domain.Op{ElementID: sub.ID, Action: domain.ActionRestore}
	if _, err := svc.Apply(ctx, alice, items["boardA"].ID, "c", []domain.Op{restore}); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	got, _ = elements.Get(ctx, nested.ID)
	if got.IsDeleted() {
		t.Fatal("cascade restore: nested card not restored with its board")
	}
}
