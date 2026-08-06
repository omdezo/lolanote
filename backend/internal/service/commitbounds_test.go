package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The three bounds and the claim, from the outside: what a caller gets back.

func TestCommit_RefusesMoreOpsThanAnyRealGestureProduces(t *testing.T) {
	elements := memory.NewElementRepo()
	seedOwnedBoard(t, elements, "cd00000000000000000ba001")
	svc, journal := partialWriteFixture(t, elements)

	ops := make([]domain.Op, maxOpsPerTransaction+1)
	for i := range ops {
		ops[i] = createOp(hexID(1000+i), "cd00000000000000000ba001")
	}
	_, err := svc.Apply(context.Background(), &domain.Principal{Sub: "alice"},
		"cd00000000000000000ba001", "", ops)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("err = %v, want a validation refusal", err)
	}
	// The refusal has to say what the limit is, or the caller can only guess how
	// much smaller to make the batch.
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("refusal = %q, want it to name the limit", err.Error())
	}
	if journal.Count() != 0 {
		t.Error("a refused batch still wrote a journal row")
	}
	if els, _ := elements.Children(context.Background(),
		domain.ElementFilter{ParentID: "cd00000000000000000ba001"}); len(els) != 0 {
		t.Errorf("%d elements were created by a batch that was refused", len(els))
	}
}

// Mongo's hard per-document limit is 16 MB and the journal row carries every
// op's changes AND its inverse. A batch the router accepted at 64 MB was
// therefore applied in full and then failed to journal: no inverse, no
// broadcast, collaborators silently diverged. Refusing before the first write is
// the only outcome that leaves the board in a state somebody can reason about.
func TestCommit_RefusesABatchTooLargeToJournal(t *testing.T) {
	elements := memory.NewElementRepo()
	seedOwnedBoard(t, elements, "cd00000000000000000ba001")
	svc, journal := partialWriteFixture(t, elements)

	// One sketch-sized element is enough; two of them cross the ceiling because
	// an op carries the new value and the old one.
	blob := strings.Repeat("x", 5<<20)
	ops := []domain.Op{
		{ElementID: hexID(1), Action: domain.ActionUpdate,
			Changes:     domain.Content{"content": map[string]any{"strokes": blob}},
			UndoChanges: domain.Content{"content": map[string]any{"strokes": blob}}},
	}
	_, err := svc.Apply(context.Background(), &domain.Principal{Sub: "alice"},
		"cd00000000000000000ba001", "", ops)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("err = %v, want a size refusal", err)
	}
	if !strings.Contains(err.Error(), "MB") {
		t.Errorf("refusal = %q, want it to name the size", err.Error())
	}
	if journal.Count() != 0 {
		t.Error("an oversized batch reached the journal")
	}
}

// The claim is an INSERT, not a lookup. A lookup leaves the window it is trying
// to close — two retries in flight both read "absent" and both apply the batch.
func TestCommit_AClaimedIDIsHeldWhileTheWorkRuns(t *testing.T) {
	elements := memory.NewElementRepo()
	seedOwnedBoard(t, elements, "cd00000000000000000ba001")
	svc, journal := partialWriteFixture(t, elements)
	ctx := context.Background()

	// Stand in for the first commit still being in flight: the row exists in the
	// applying state and nothing has settled it.
	if err := journal.Insert(ctx, &domain.Transaction{
		ID: "txn-inflight", BoardID: "cd00000000000000000ba001", UserID: "alice",
		State: domain.TxnApplying,
	}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	_, err := svc.ApplyWithMeta(ctx, &domain.Principal{Sub: "alice"},
		"cd00000000000000000ba001", "", []domain.Op{createOp(hexID(2), "cd00000000000000000ba001")},
		TxnMeta{TxnID: "txn-inflight"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want a conflict — the batch is already being applied", err)
	}
	if els, _ := elements.Children(ctx,
		domain.ElementFilter{ParentID: "cd00000000000000000ba001"}); len(els) != 0 {
		t.Errorf("the second attempt applied %d ops anyway", len(els))
	}
}

// An Idempotency-Key comes from a client, so the id it pins is attacker-chosen.
// Handing back whatever row already holds that id would be a way to read
// somebody else's transaction — its ops, its content — by guessing a key.
func TestCommit_AClaimNeverResolvesToSomebodyElsesCommit(t *testing.T) {
	elements := memory.NewElementRepo()
	seedOwnedBoard(t, elements, "cd00000000000000000ba001")
	svc, journal := partialWriteFixture(t, elements)
	ctx := context.Background()

	if err := journal.Insert(ctx, &domain.Transaction{
		ID: "shared-key", BoardID: "cd00000000000000000ba002", UserID: "bob",
		Ops: []domain.Op{{ElementID: "bobs-card", Action: domain.ActionUpdate,
			Changes: domain.Content{"content": map[string]any{"text": "bob's private note"}}}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := svc.ApplyWithMeta(ctx, &domain.Principal{Sub: "alice"},
		"cd00000000000000000ba001", "", []domain.Op{createOp(hexID(3), "cd00000000000000000ba001")},
		TxnMeta{TxnID: "shared-key"})
	if err == nil {
		t.Fatalf("alice was handed transaction %+v", got)
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want a conflict rather than another user's row", err)
	}
}

// A claim whose work did not stand must be released, or the next attempt looks
// up a commit that never happened and reports success having done nothing.
func TestCommit_AFullyCompensatedFailureReleasesItsClaim(t *testing.T) {
	elements := memory.NewElementRepo()
	seedOwnedBoard(t, elements, "cd00000000000000000ba001")
	svc, journal := partialWriteFixture(t, elements)
	ctx := context.Background()
	alice := &domain.Principal{Sub: "alice"}
	board := "cd00000000000000000ba001"

	// Second op is malformed, so op 1 lands and is then taken back.
	ops := []domain.Op{
		createOp(hexID(4), board),
		{ElementID: hexID(5), Action: domain.ActionCreate,
			Changes: domain.Content{"type": "CARD"}}, // no location: refused in applyCreate
	}
	if _, err := svc.ApplyWithMeta(ctx, alice, board, "", ops, TxnMeta{TxnID: "txn-failed"}); err == nil {
		t.Fatal("the malformed batch succeeded")
	}
	if _, err := journal.Get(ctx, "txn-failed"); err == nil {
		t.Fatal("the claim outlived a batch that left nothing behind")
	}

	// And the retry is a real retry.
	if _, err := svc.ApplyWithMeta(ctx, alice, board, "",
		[]domain.Op{createOp(hexID(4), board)}, TxnMeta{TxnID: "txn-failed"}); err != nil {
		t.Fatalf("the retry was blocked by a stale claim: %v", err)
	}
}

// Descendants had no depth or size cap at all — the read side of the same
// hazard the op count has on the write side. Duplicate, Export, the delete
// cascade and the account purge each pull a whole subtree into one request, and
// a workspace is a tree a person grows without ever being told there is a limit.
func TestSubtree_RefusesRatherThanReturningPartOfIt(t *testing.T) {
	ctx := context.Background()
	elements := memory.NewElementRepo()
	seedOwnedBoard(t, elements, "cd00000000000000000ba001")
	for i := 0; i < maxSubtree+5; i++ {
		if err := elements.Insert(ctx, &domain.Element{
			ID: fmt.Sprintf("%024x", i), Type: domain.TypeCard,
			Location: domain.Location{ParentID: "cd00000000000000000ba001"},
			Content:  domain.Content{"textPreview": "x"},
		}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	if _, err := subtreeOf(ctx, elements, "cd00000000000000000ba001", false); err == nil {
		t.Fatal("an oversized subtree came back as a success; a truncated duplicate is a board missing cards nobody can name")
	} else if !strings.Contains(err.Error(), "5000") {
		t.Errorf("refusal = %q, want it to name the limit", err.Error())
	}
}

func hexID(n int) string { return time_hex(n) }
