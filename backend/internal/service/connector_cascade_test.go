package service

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// A connector to something that no longer exists is not a connector.
//
// Deleting a card used to leave its lines behind pointing at a gone id. They
// render as nothing — the endpoint no longer resolves — so a board silently
// accumulated invisible elements that still occupied the graph and still came
// back on a restore. Seven had built up on one real board.
func connectorFixture(t *testing.T) (*TransactionService, *memory.ElementRepo, map[string]string) {
	t.Helper()
	elements := memory.NewElementRepo()
	access := NewAccessResolver(elements)
	n := 0
	svc := NewTransactionService(elements, memory.NewTransactionRepo(), access, nil,
		IDGenerator(func() string { n++; return time_hex(100 + n) }), zap.NewNop())

	now := time.Now().UTC()
	ids := map[string]string{}
	mk := func(key, id, typ, parent string, content domain.Content) {
		el := &domain.Element{
			ID: id, Type: domain.ElementType(typ),
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:   content,
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}
		if typ == "BOARD" {
			el.ACL = &domain.ACL{OwnerID: "alice", Editors: []string{}}
		}
		if err := elements.Insert(context.Background(), el); err != nil {
			t.Fatalf("insert %s: %v", key, err)
		}
		ids[key] = id
	}

	mk("board", "cccccccccccccccccccccc01", "BOARD", "", domain.Content{})
	mk("column", "cccccccccccccccccccccc02", "COLUMN", ids["board"], domain.Content{"title": "Pre"})
	// Two cards nested in the column — so the connector does NOT live in the
	// same container as the things it joins. That is the case the first
	// version of this cascade missed.
	mk("scout", "cccccccccccccccccccccc03", "CARD", ids["column"], domain.Content{"textPreview": "Scout"})
	mk("shoot", "cccccccccccccccccccccc04", "CARD", ids["column"], domain.Content{"textPreview": "Shoot"})
	mk("line", "cccccccccccccccccccccc05", "LINE", ids["board"], domain.Content{
		"fromId": ids["scout"], "toId": ids["shoot"], "label": "then",
	})
	// An unrelated line between two other things must survive untouched.
	mk("keep-a", "cccccccccccccccccccccc06", "CARD", ids["board"], domain.Content{"textPreview": "A"})
	mk("keep-b", "cccccccccccccccccccccc07", "CARD", ids["board"], domain.Content{"textPreview": "B"})
	mk("other", "cccccccccccccccccccccc08", "LINE", ids["board"], domain.Content{
		"fromId": ids["keep-a"], "toId": ids["keep-b"],
	})
	return svc, elements, ids
}

func deleted(t *testing.T, repo *memory.ElementRepo, id string) bool {
	t.Helper()
	el, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return el.IsDeleted()
}

func TestDelete_TrashesTheConnectorsThatPointedAtIt(t *testing.T) {
	svc, repo, ids := connectorFixture(t)
	alice := &domain.Principal{Sub: "alice"}

	_, err := svc.Apply(context.Background(), alice, ids["board"], "c1", []domain.Op{
		{Action: domain.ActionDelete, ElementID: ids["scout"]},
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	if !deleted(t, repo, ids["line"]) {
		t.Error("the line still points at a deleted card — it renders as nothing but stays in the graph")
	}
	if deleted(t, repo, ids["other"]) {
		t.Error("an unrelated connector was trashed")
	}
	if deleted(t, repo, ids["shoot"]) {
		t.Error("the connector's OTHER endpoint was trashed; only the line should follow")
	}
}

// Deleting the container: the cascade already trashes the cards inside it, and
// the connectors between those cards must go with them.
func TestDelete_ContainerTakesTheConnectorsBetweenItsChildren(t *testing.T) {
	svc, repo, ids := connectorFixture(t)
	alice := &domain.Principal{Sub: "alice"}

	_, err := svc.Apply(context.Background(), alice, ids["board"], "c1", []domain.Op{
		{Action: domain.ActionDelete, ElementID: ids["column"]},
	})
	if err != nil {
		t.Fatalf("delete column: %v", err)
	}

	if !deleted(t, repo, ids["line"]) {
		t.Error("both endpoints were trashed with the column, but the line survived")
	}
	if deleted(t, repo, ids["other"]) {
		t.Error("a connector elsewhere on the board was trashed")
	}
}

// Restoring must bring the relationships back. They ride the same batch id, so
// an undo returns the card AND what it was connected to — anything else quietly
// loses the structure the person drew.
func TestDelete_RestoreBringsTheConnectorsBack(t *testing.T) {
	svc, repo, ids := connectorFixture(t)
	alice := &domain.Principal{Sub: "alice"}
	ctx := context.Background()

	if _, err := svc.Apply(ctx, alice, ids["board"], "c1", []domain.Op{
		{Action: domain.ActionDelete, ElementID: ids["scout"]},
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Apply(ctx, alice, ids["board"], "c1", []domain.Op{
		{Action: domain.ActionRestore, ElementID: ids["scout"]},
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if deleted(t, repo, ids["scout"]) {
		t.Fatal("the card did not come back")
	}
	if deleted(t, repo, ids["line"]) {
		t.Error("the card came back without its connection")
	}
}
