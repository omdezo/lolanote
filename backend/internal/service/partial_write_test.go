package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The write path applied ops one at a time with no rollback.
//
// The pre-validation loop's own comment called itself "a partial-write guard (a
// transaction is all-or-nothing in intent, so we never apply op 1 then fail on
// op 2)" — but it covers scope and delegation only. It cannot pre-empt a
// duplicate key or a validation failure inside applyCreate, and when one of
// those hit, the elements existed and NOTHING recorded them: no journal row, so
// no inverse; no broadcast, so the client's own undo stack never saw the ops.
// Per-run revert could not reach it, Ctrl+Z could not reach it, the audit view
// could not see it, and the outcome card said the changes were rejected.

// stubbornElements refuses to soft-delete, which is how a compensating write
// fails in the field: the row is there and the database will not take it back.
type stubbornElements struct {
	*memory.ElementRepo
	refuseDelete bool
}

func (s *stubbornElements) SoftDelete(ctx context.Context, ids []string, by, batch string, at time.Time) error {
	if s.refuseDelete {
		return errors.New("storage refused the delete")
	}
	return s.ElementRepo.SoftDelete(ctx, ids, by, batch, at)
}

func partialWriteFixture(t *testing.T, elements domain.ElementRepository) (*TransactionService, *memory.TransactionRepo) {
	t.Helper()
	access := NewAccessResolver(elements)
	journal := memory.NewTransactionRepo()
	n := 0
	svc := NewTransactionService(elements, journal, access, nil,
		IDGenerator(func() string { n++; return time_hex(900 + n) }), zap.NewNop())
	return svc, journal
}

func seedOwnedBoard(t *testing.T, elements domain.ElementRepository, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := elements.Insert(context.Background(), &domain.Element{
		ID: id, Type: domain.TypeBoard,
		Content:   domain.Content{"title": "Board"},
		ACL:       &domain.ACL{OwnerID: "alice", Editors: []string{}},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed board: %v", err)
	}
}

func createOp(id, board string) domain.Op {
	return domain.Op{
		ElementID: id, Action: domain.ActionCreate,
		Changes: domain.Content{
			"type":     string(domain.TypeCard),
			"location": map[string]any{"parentId": board, "section": string(domain.SectionCanvas)},
			"content":  map[string]any{"textPreview": "note " + id},
		},
	}
}

func TestApply_AFailedBatchTakesBackWhatItAlreadyWrote(t *testing.T) {
	elements := memory.NewElementRepo()
	seedOwnedBoard(t, elements, "1111111111111111111ab001")
	svc, journal := partialWriteFixture(t, elements)
	ctx := context.Background()
	alice := &domain.Principal{Sub: "alice"}

	// The second op collides on id with the first, which is exactly the class of
	// failure pre-validation cannot see.
	_, err := svc.Apply(ctx, alice, "1111111111111111111ab001", "c1", []domain.Op{
		createOp("1111111111111111111ab002", "1111111111111111111ab001"),
		createOp("1111111111111111111ab002", "1111111111111111111ab001"),
	})
	if err == nil {
		t.Fatal("a duplicate-id batch was accepted")
	}

	el, gerr := elements.Get(ctx, "1111111111111111111ab002")
	if gerr == nil && !el.IsDeleted() {
		t.Error("op 1's element is standing on the board after op 2 failed the batch")
	}
	if journal.Count() != 0 {
		t.Errorf("a fully compensated batch left %d journal row(s); nothing happened, so nothing should be recorded", journal.Count())
	}
}

// The case the whole item is really about: compensation itself fails. The change
// is standing, and the ONLY acceptable outcome is that it is journalled — the
// row carries its own inverse, so there is still a way back.
func TestApply_WhatCannotBeRolledBackIsStillJournalled(t *testing.T) {
	base := memory.NewElementRepo()
	elements := &stubbornElements{ElementRepo: base}
	seedOwnedBoard(t, elements, "1111111111111111111ac001")
	svc, journal := partialWriteFixture(t, elements)
	ctx := context.Background()
	alice := &domain.Principal{Sub: "alice"}

	elements.refuseDelete = true
	txn, err := svc.Apply(ctx, alice, "1111111111111111111ac001", "c1", []domain.Op{
		createOp("1111111111111111111ac002", "1111111111111111111ac001"),
		createOp("1111111111111111111ac002", "1111111111111111111ac001"),
	})
	if err == nil {
		t.Fatal("a duplicate-id batch was accepted")
	}
	if txn == nil {
		t.Fatal("a write that landed and could not be taken back produced no transaction — there is no way to undo it")
	}
	if journal.Count() != 1 {
		t.Fatalf("journal rows = %d, want the standing change recorded once", journal.Count())
	}
	if len(txn.Ops) != 1 || txn.Ops[0].ElementID != "1111111111111111111ac002" {
		t.Errorf("the journalled row does not describe what is standing: %+v", txn.Ops)
	}
	// And it is reachable: the row is what an undo replays from.
	stored, gerr := journal.Get(ctx, txn.ID)
	if gerr != nil {
		t.Fatalf("the recovery handle is not readable: %v", gerr)
	}
	if len(stored.Ops) != 1 {
		t.Errorf("stored ops = %d, want 1", len(stored.Ops))
	}
}

// Revert is not the inverse of commit while the write path does things outside
// the transaction it is committing. Grouping a board files columns into nested
// boards and leaves every connector between them behind — and those tidy-up
// writes were never appended to txn.Ops, so InvertOps could not see them and the
// undo copy promised a board that was not back as it was.
func TestApply_ConnectorTidyUpJoinsTheTransactionItBelongsTo(t *testing.T) {
	elements := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()

	seedOwnedBoard(t, elements, "2222222222222222222ab001")
	mk := func(id, typ, parent string, content domain.Content) {
		if err := elements.Insert(ctx, &domain.Element{
			ID: id, Type: domain.ElementType(typ),
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:   content,
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// Two cards on the board with an arrow between them, and a nested board to
	// file one of them into.
	mk("2222222222222222222ab002", "CARD", "2222222222222222222ab001", domain.Content{"textPreview": "a"})
	mk("2222222222222222222ab003", "CARD", "2222222222222222222ab001", domain.Content{"textPreview": "b"})
	mk("2222222222222222222ab004", "LINE", "2222222222222222222ab001", domain.Content{
		"fromId": "2222222222222222222ab002", "toId": "2222222222222222222ab003",
	})
	if err := elements.Insert(ctx, &domain.Element{
		ID: "2222222222222222222ab005", Type: domain.TypeBoard,
		Location:  domain.Location{ParentID: "2222222222222222222ab001", Section: domain.SectionCanvas},
		Content:   domain.Content{"title": "Nested"},
		ACL:       &domain.ACL{OwnerID: "alice", Editors: []string{}},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed nested: %v", err)
	}

	svc, _ := partialWriteFixture(t, elements)
	txn, err := svc.Apply(ctx, &domain.Principal{Sub: "alice"}, "2222222222222222222ab001", "c1",
		[]domain.Op{{
			ElementID: "2222222222222222222ab002", Action: domain.ActionMove,
			Changes: domain.Content{"location": map[string]any{
				"parentId": "2222222222222222222ab005", "section": string(domain.SectionCanvas),
			}},
			UndoChanges: domain.Content{"location": map[string]any{
				"parentId": "2222222222222222222ab001", "section": string(domain.SectionCanvas),
			}},
		}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	var sawLine bool
	for _, op := range txn.Ops {
		if op.ElementID == "2222222222222222222ab004" {
			sawLine = true
		}
	}
	if !sawLine {
		t.Fatalf("the connector was retired outside the transaction that caused it; ops = %+v", txn.Ops)
	}
}
