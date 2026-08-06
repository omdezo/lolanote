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

// Holding a clone is not authority over what it points at.
//
// verifyOpScope let an ActionUpdate that failed the board-scope check through
// whenever ANY clone instance of the target sat on the declared board — and
// content.cloneSourceId is never validated when a CLONE is created, only its
// parent. So the whole attack was two ordinary writes:
//
//  1. create a CLONE on your own board naming any element id in the database;
//  2. update that id, declaring your own board.
//
// Scope passed on the strength of the clone, and no ACL was consulted anywhere
// on the path. A cross-tenant write to a board the attacker cannot read.
//
// The clone relationship decides SCOPE. It never decided PERMISSION, and now
// it cannot: editing through a clone requires what editing the source requires.
func TestClone_CannotBeUsedToWriteSomebodyElsesBoard(t *testing.T) {
	elements := memory.NewElementRepo()
	access := NewAccessResolver(elements)
	n := 0
	svc := NewTransactionService(elements, memory.NewTransactionRepo(), access, nil,
		IDGenerator(func() string { n++; return time_hex(300 + n) }), zap.NewNop())

	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(id, typ, parent, owner string, content domain.Content) {
		el := &domain.Element{
			ID: id, Type: domain.ElementType(typ),
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:   content,
			CreatedBy: owner, CreatedAt: now, UpdatedAt: now,
		}
		if typ == "BOARD" {
			el.ACL = &domain.ACL{OwnerID: owner, Editors: []string{}}
		}
		if err := elements.Insert(ctx, el); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	// Alice's private board and a card on it. Mallory is on neither.
	mk("aaaaaaaaaaaaaaaaaaaaaa01", "BOARD", "", "alice", domain.Content{"title": "Alice private"})
	mk("aaaaaaaaaaaaaaaaaaaaaa02", "CARD", "aaaaaaaaaaaaaaaaaaaaaa01", "alice",
		domain.Content{"textPreview": "Alice's confidential note"})

	// Mallory's own board, and the one write she is allowed: a CLONE she
	// parents to her own board, pointing at Alice's card. Nothing validates
	// the target of a clone — only that its PARENT is hers, which it is.
	mk("bbbbbbbbbbbbbbbbbbbbbb01", "BOARD", "", "mallory", domain.Content{"title": "Mallory"})
	mk("bbbbbbbbbbbbbbbbbbbbbb02", "CLONE", "bbbbbbbbbbbbbbbbbbbbbb01", "mallory",
		domain.Content{"cloneSourceId": "aaaaaaaaaaaaaaaaaaaaaa02"})

	mallory := &domain.Principal{Sub: "mallory"}
	_, err := svc.Apply(ctx, mallory, "bbbbbbbbbbbbbbbbbbbbbb01", "c1", []domain.Op{{
		Action:    domain.ActionUpdate,
		ElementID: "aaaaaaaaaaaaaaaaaaaaaa02", // Alice's card, on Alice's board
		Changes:   domain.Content{"content": map[string]any{"textPreview": "OVERWRITTEN BY MALLORY"}},
	}})

	if err == nil {
		t.Fatal("a stranger wrote to a private board through a clone they created")
	}
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("rejected with %v; the refusal should be a permission failure", err)
	}

	victim, gerr := elements.Get(ctx, "aaaaaaaaaaaaaaaaaaaaaa02")
	if gerr != nil {
		t.Fatalf("get victim: %v", gerr)
	}
	if got, _ := victim.Content["textPreview"].(string); got != "Alice's confidential note" {
		t.Fatalf("Alice's card now reads %q", got)
	}
}

// And the legitimate case still works: a synced note edited from a board the
// person can actually edit. Closing the hole must not close the feature.
func TestClone_TheOwnerStillEditsThroughTheirOwnClone(t *testing.T) {
	elements := memory.NewElementRepo()
	access := NewAccessResolver(elements)
	n := 0
	svc := NewTransactionService(elements, memory.NewTransactionRepo(), access, nil,
		IDGenerator(func() string { n++; return time_hex(400 + n) }), zap.NewNop())

	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(id, typ, parent string, content domain.Content) {
		el := &domain.Element{
			ID: id, Type: domain.ElementType(typ),
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:   content,
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}
		if typ == "BOARD" {
			el.ACL = &domain.ACL{OwnerID: "alice", Editors: []string{}}
		}
		if err := elements.Insert(ctx, el); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	mk("cccccccccccccccccccccc01", "BOARD", "", domain.Content{"title": "Source board"})
	mk("cccccccccccccccccccccc02", "CARD", "cccccccccccccccccccccc01", domain.Content{"textPreview": "Shared note"})
	mk("cccccccccccccccccccccc03", "BOARD", "", domain.Content{"title": "Second board"})
	mk("cccccccccccccccccccccc04", "CLONE", "cccccccccccccccccccccc03",
		domain.Content{"cloneSourceId": "cccccccccccccccccccccc02"})

	alice := &domain.Principal{Sub: "alice"}
	if _, err := svc.Apply(ctx, alice, "cccccccccccccccccccccc03", "c1", []domain.Op{{
		Action:    domain.ActionUpdate,
		ElementID: "cccccccccccccccccccccc02",
		Changes:   domain.Content{"content": map[string]any{"textPreview": "Edited from the clone"}},
	}}); err != nil {
		t.Fatalf("the owner could not edit their own synced note: %v", err)
	}

	src, _ := elements.Get(ctx, "cccccccccccccccccccccc02")
	if got, _ := src.Content["textPreview"].(string); got != "Edited from the clone" {
		t.Errorf("the synced edit did not reach the source: %q", got)
	}
}
