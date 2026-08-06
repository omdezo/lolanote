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

// Deleting an account with shared boards is refused with "transfer or unshare
// them first" — and transfer did not exist anywhere in the tree. The product
// instructed a person to perform an action it had no way to perform, at exactly
// the moment their collaborators' work was at stake.
func transferFixture(t *testing.T) (*ShareService, *AccountService, *memory.ElementRepo, context.Context) {
	t.Helper()
	elements := memory.NewElementRepo()
	access := NewAccessResolver(elements)
	ctx := context.Background()
	now := time.Now().UTC()

	board := &domain.Element{
		ID: "70000000000000000000ba01", Type: domain.TypeBoard,
		Location:  domain.Location{Section: domain.SectionCanvas},
		Content:   domain.Content{"title": "Feature film"},
		ACL:       &domain.ACL{OwnerID: "alice", Editors: []string{"bob"}},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}
	if err := elements.Insert(ctx, board); err != nil {
		t.Fatalf("seed: %v", err)
	}
	audit, _ := testAudit()
	return NewShareService(elements, nil, nil, access, audit),
		NewAccountService(nil, elements, nil, nil, nil, nil, audit, zap.NewNop()), elements, ctx
}

func TestTransfer_HandsTheBoardToAnEditor(t *testing.T) {
	share, _, elements, ctx := transferFixture(t)

	st, err := share.TransferOwnership(ctx, &domain.Principal{Sub: "alice"},
		"70000000000000000000ba01", "bob")
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if st == nil {
		t.Fatal("no share state returned")
	}

	board, _ := elements.Get(ctx, "70000000000000000000ba01")
	if board.ACL.OwnerID != "bob" {
		t.Fatalf("owner is %q, want bob", board.ACL.OwnerID)
	}
	// The new owner leaves the editor set — it is the set of people who are NOT
	// the owner, and leaving them in makes every membership check answer twice.
	for _, e := range board.ACL.Editors {
		if e == "bob" {
			t.Error("the new owner is still listed as an editor")
		}
	}
	// And the previous owner keeps access: handing over is not leaving.
	var aliceKept bool
	for _, e := range board.ACL.Editors {
		if e == "alice" {
			aliceKept = true
		}
	}
	if !aliceKept {
		t.Error("the previous owner was cut out of their own work")
	}
}

// The safety story in one test: a board cannot be pushed onto somebody who
// never accepted it.
func TestTransfer_RefusesSomeoneWhoIsNotAnEditor(t *testing.T) {
	share, _, elements, ctx := transferFixture(t)

	_, err := share.TransferOwnership(ctx, &domain.Principal{Sub: "alice"},
		"70000000000000000000ba01", "mallory")
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("transferred to a stranger: %v", err)
	}
	// The refusal has to say WHY, or it is a dead end like the one it replaces.
	var authored interface{ AuthoredMessage() string }
	if !errors.As(err, &authored) {
		t.Fatal("the refusal carries no message for the person reading it")
	}
	board, _ := elements.Get(ctx, "70000000000000000000ba01")
	if board.ACL.OwnerID != "alice" {
		t.Error("ownership moved anyway")
	}
}

func TestTransfer_OnlyTheOwnerMayHandItOver(t *testing.T) {
	share, _, _, ctx := transferFixture(t)

	// Bob is an editor, not the owner.
	_, err := share.TransferOwnership(ctx, &domain.Principal{Sub: "bob"},
		"70000000000000000000ba01", "bob")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an editor gave themselves the board: %v", err)
	}
}

// The whole point: the sentence the deletion refusal prints is now a sentence a
// person can act on. Refused while shared; transfer; and the board is no longer
// theirs to block it.
func TestTransfer_MakesTheDeletionRefusalFollowable(t *testing.T) {
	share, account, elements, ctx := transferFixture(t)
	alice := &domain.Principal{Sub: "alice"}

	if err := account.DeleteAccount(ctx, alice); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a shared board did not block the delete: %v", err)
	}

	if _, err := share.TransferOwnership(ctx, alice, "70000000000000000000ba01", "bob"); err != nil {
		t.Fatalf("the instruction the refusal gives could not be carried out: %v", err)
	}

	board, _ := elements.Get(ctx, "70000000000000000000ba01")
	if board.ACL.OwnerID == "alice" {
		t.Fatal("alice still owns the board")
	}
}
