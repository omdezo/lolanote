package service

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// A line renders only where both of its endpoints resolve. Moving one of them
// into a nested board therefore erases the arrow without deleting anything:
// four of these sat on one real board after a single organizing run, joining
// columns that had ended up in different sub-boards.
//
// The delete cascade covered the case where an endpoint stops existing. Moving
// an endpoint somewhere else is the same loss and was covered by nothing.
func movedConnectorFixture(t *testing.T) (*TransactionService, *memory.ElementRepo, map[string]string) {
	t.Helper()
	elements := memory.NewElementRepo()
	access := NewAccessResolver(elements)
	n := 0
	svc := NewTransactionService(elements, memory.NewTransactionRepo(), access, nil,
		IDGenerator(func() string { n++; return time_hex(300 + n) }), zap.NewNop())

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

	mk("root", "dddddddddddddddddddddd01", "BOARD", "", domain.Content{"title": "Film"})
	mk("nested", "dddddddddddddddddddddd02", "BOARD", ids["root"], domain.Content{"title": "Pre-Production"})
	mk("scripting", "dddddddddddddddddddddd03", "COLUMN", ids["root"], domain.Content{"title": "Scripting"})
	mk("casting", "dddddddddddddddddddddd04", "COLUMN", ids["root"], domain.Content{"title": "Casting"})
	// The connector between the two columns, drawn on the canvas they share.
	mk("arrow", "dddddddddddddddddddddd05", "LINE", ids["root"], domain.Content{
		"fromId": ids["scripting"], "toId": ids["casting"], "label": "then",
	})
	// A card in each column, and an arrow between THOSE, so a move inside one
	// canvas can be told apart from a move across canvases.
	mk("beat", "dddddddddddddddddddddd06", "CARD", ids["scripting"], domain.Content{"textPreview": "Lock the script"})
	mk("audition", "dddddddddddddddddddddd07", "CARD", ids["casting"], domain.Content{"textPreview": "Auditions"})
	mk("cardline", "dddddddddddddddddddddd08", "LINE", ids["root"], domain.Content{
		"fromId": ids["beat"], "toId": ids["audition"],
	})
	return svc, elements, ids
}

func liveLine(t *testing.T, repo *memory.ElementRepo, id string) *domain.Element {
	t.Helper()
	el, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return el
}

// One endpoint goes into a nested board: the arrow can no longer be drawn on
// any canvas, so it must not stay on the board pretending to exist.
func TestMove_OneEndpointIntoANestedBoard_TrashesTheStrandedLine(t *testing.T) {
	svc, repo, ids := movedConnectorFixture(t)
	alice := &domain.Principal{Sub: "alice"}

	_, err := svc.Apply(context.Background(), alice, ids["root"], "c1", []domain.Op{
		{ElementID: ids["scripting"], Action: domain.ActionMove, Changes: domain.Content{
			"location": map[string]any{"parentId": ids["nested"], "section": "CANVAS", "index": 1.0},
		}},
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	arrow := liveLine(t, repo, ids["arrow"])
	if !arrow.IsDeleted() {
		t.Errorf("the line still sits on %s joining two different canvases — it draws nothing and stays in the graph",
			arrow.Location.ParentID)
	}
	// Its own batch: restoring the column must not drag back a connection that
	// still cannot be drawn.
	if arrow.TrashBatchID != arrow.ID {
		t.Errorf("stranded line was trashed in batch %q, not its own", arrow.TrashBatchID)
	}
	// The card-to-card arrow crosses the same two canvases now, because one of
	// its endpoints rode along inside the column.
	if cardline := liveLine(t, repo, ids["cardline"]); !cardline.IsDeleted() {
		t.Error("the arrow between the cards inside those columns crosses canvases too, and survived")
	}
}

// Both endpoints go into the same board: the relationship survived the move, so
// the arrow follows it rather than being thrown away.
func TestMove_BothEndpointsTogether_TakesTheLineWithThem(t *testing.T) {
	svc, repo, ids := movedConnectorFixture(t)
	alice := &domain.Principal{Sub: "alice"}

	_, err := svc.Apply(context.Background(), alice, ids["root"], "c1", []domain.Op{
		{ElementID: ids["scripting"], Action: domain.ActionMove, Changes: domain.Content{
			"location": map[string]any{"parentId": ids["nested"], "section": "CANVAS", "index": 1.0},
		}},
		{ElementID: ids["casting"], Action: domain.ActionMove, Changes: domain.Content{
			"location": map[string]any{"parentId": ids["nested"], "section": "CANVAS", "index": 2.0},
		}},
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	arrow := liveLine(t, repo, ids["arrow"])
	if arrow.IsDeleted() {
		t.Fatal("both columns landed on the same canvas and the arrow between them was trashed anyway")
	}
	if arrow.Location.ParentID != ids["nested"] {
		t.Errorf("the arrow stayed on %s while both its endpoints moved to %s", arrow.Location.ParentID, ids["nested"])
	}
	if cardline := liveLine(t, repo, ids["cardline"]); cardline.Location.ParentID != ids["nested"] {
		t.Errorf("the arrow between the cards stayed on %s", cardline.Location.ParentID)
	}
}

// Moving a card between two columns on the SAME board changes nothing about
// where its connectors can be drawn. A sweep that trashed those would make the
// ordinary drag destructive.
func TestMove_WithinOneCanvas_LeavesConnectorsAlone(t *testing.T) {
	svc, repo, ids := movedConnectorFixture(t)
	alice := &domain.Principal{Sub: "alice"}

	_, err := svc.Apply(context.Background(), alice, ids["root"], "c1", []domain.Op{
		{ElementID: ids["beat"], Action: domain.ActionMove, Changes: domain.Content{
			"location": map[string]any{"parentId": ids["casting"], "section": "CANVAS", "index": 3.0},
		}},
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	if cardline := liveLine(t, repo, ids["cardline"]); cardline.IsDeleted() {
		t.Error("a card moved between columns on one board, and its connector was trashed")
	}
	if arrow := liveLine(t, repo, ids["arrow"]); arrow.IsDeleted() {
		t.Error("an unrelated connector was trashed")
	}
}

// The drag-onto-a-board-tile gesture writes its moves directly rather than
// through applyOp, so it strands connectors by its own separate route.
func TestMoveAcrossBoards_TrashesTheStrandedLine(t *testing.T) {
	svc, repo, ids := movedConnectorFixture(t)
	alice := &domain.Principal{Sub: "alice"}

	if err := svc.MoveAcrossBoards(context.Background(), alice,
		[]string{ids["scripting"]}, ids["nested"]); err != nil {
		t.Fatalf("move across boards: %v", err)
	}

	if arrow := liveLine(t, repo, ids["arrow"]); !arrow.IsDeleted() {
		t.Error("dragging a column onto a board tile left the arrow behind on the old canvas")
	}
}
