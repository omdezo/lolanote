package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
)

// JN20 and JN8's honesty clause, held together because they are one failure.
//
// Every inverse op opens with a fetch of the element it is undoing —
// `applyDelete` and `applyRestore` literally do, and a merge patch needs a
// document to patch. `keepFresh` used to wave a missing element through on the
// reasoning that "the inverse is a no-op or a restore; either way, let it run",
// and all three inverses fail on an element that was HARD-deleted rather than
// trashed.
//
// The failure had teeth because of where it happened. The apply loop does not
// roll back and the journal row is written afterwards, so an op that exploded
// thirty ops into a forty-op inverse left the first thirty applied, no
// transaction row describing them, and the run still reading COMPLETED with an
// Undo button that would half-revert it again. Two operations the product
// actively encourages get you there: Empty trash is a first-class button and
// revert is the central trust promise.

// hardDelete removes an element the way "Delete forever" and the 90-day sweep
// do — no trash row, no journal, nothing to fetch.
func hardDelete(t *testing.T, h *mpHarness, id string) {
	t.Helper()
	if err := h.elements.HardDelete(context.Background(), []string{id}); err != nil {
		t.Fatalf("hard delete %s: %v", id, err)
	}
}

func TestRevert_APermanentlyDeletedTargetIsReportedNotReplayed(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	col := mpParent[:20] + "f00a"
	run := h.seedForeignRun(t, "editor", "owner", col, time.Now().UTC().Add(-time.Hour))

	// Somebody tidied their trash in month two. This is the whole trigger.
	hardDelete(t, h, col)

	ctx := context.Background()
	_, err := h.svc.Revert(ctx, h.editor, run.ID)
	if err == nil {
		t.Fatal("a revert with nothing left to reach reported success")
	}
	// The refusal has to name the cause. "conflict" is what this said before,
	// and a guarantee that fails identically every time it is pressed costs
	// more trust than one that was never offered.
	if !strings.Contains(err.Error(), "permanently deleted") {
		t.Fatalf("the refusal does not say why:\n%v", err)
	}

	fresh, err := h.runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	// JN8: the ledger reads this field and draws the reason where the button
	// was. Without it the row keeps offering an Undo that 500s.
	if fresh.RevertBlockedReason == "" {
		t.Fatal("the run records no reason, so the ledger will keep offering the button")
	}
	// The state must NOT move to REVERTED. Nothing was undone, and a run
	// labelled reverted is a second lie on top of the first.
	if fresh.State == StateReverted {
		t.Fatal("a run that undid nothing was marked reverted")
	}
	// And the second press costs a comparison, not another walk of the journal.
	if err := revertable(fresh); err == nil {
		t.Fatal("revertable() still offers a run whose targets are gone")
	}
}

func TestRevert_WhatIsStillReachableIsStillUndone(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	gone := mpParent[:20] + "f00b"
	kept := mpParent[:20] + "f00c"
	h.seedForeignRun(t, "editor", "owner", gone, time.Now().UTC().Add(-time.Hour))
	run := h.seedForeignRun(t, "editor", "owner", kept, time.Now().UTC().Add(-time.Hour))
	// One run, both transactions: the shape a forty-op inverse has when three
	// of its targets have been destroyed.
	run.TransactionIDs = append([]string{"txn" + gone}, run.TransactionIDs...)
	ctx := context.Background()
	if err := h.runs.Update(ctx, run, run.Rev); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	hardDelete(t, h, gone)

	if _, err := h.svc.Revert(ctx, h.editor, run.ID); err != nil {
		t.Fatalf("a partially-reachable revert refused whole: %v", err)
	}
	// Partial-and-report, not all-or-nothing: the reachable column really came
	// back, which is the half the person can still have.
	el, err := h.elements.Get(ctx, kept)
	if err != nil {
		t.Fatalf("read element: %v", err)
	}
	if !el.IsDeleted() {
		t.Fatal("the reachable half of the run was not undone")
	}
	// A run that DID undo something is not blocked — the button stays live for
	// whatever else it still holds.
	fresh, err := h.runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if fresh.RevertBlockedReason != "" {
		t.Fatalf("a partially successful revert marked the run permanently blocked: %q",
			fresh.RevertBlockedReason)
	}
}

// The pre-flight is the point: nothing may be written until every target has
// been resolved, because a batch that fails halfway leaves no journal row and
// therefore no inverse of its own.
func TestKeepFresh_PartitionsGoneFromStaleFromReachable(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)

	reachable := mpParent[:20] + "f00d"
	edited := mpParent[:20] + "f00e"
	for _, id := range []string{reachable, edited} {
		if err := h.elements.Insert(ctx, &domain.Element{
			ID: id, Type: domain.TypeCard,
			Location:  domain.Location{ParentID: mpParent, Section: domain.SectionCanvas},
			Content:   domain.Content{"textPreview": "x"},
			CreatedBy: "editor", CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// Edited by a colleague since the run wrote it.
	if _, err := h.elements.MergePatch(ctx, edited, domain.Content{
		"content": map[string]any{"textPreview": "their hour of work"},
	}); err != nil {
		t.Fatalf("colleague edit: %v", err)
	}

	ops := []domain.Op{
		{ElementID: reachable, Action: domain.ActionCreate},
		{ElementID: edited, Action: domain.ActionCreate},
		{ElementID: mpParent[:20] + "dead", Action: domain.ActionCreate}, // never existed
	}
	kept, stale, missing := h.svc.keepFresh(ctx, &Run{}, at, ops)

	if len(kept) != 1 || kept[0].ElementID != reachable {
		t.Fatalf("the reachable op was not kept: %+v", kept)
	}
	if len(stale) != 1 || stale[0] != edited {
		t.Fatalf("the colleague-edited op was not reported stale: %v", stale)
	}
	// The one that matters: an unreachable target is partitioned OUT rather
	// than passed to a write that will fail a third of the way through.
	if len(missing) != 1 {
		t.Fatalf("the unreachable op was not partitioned out: %v", missing)
	}
}
