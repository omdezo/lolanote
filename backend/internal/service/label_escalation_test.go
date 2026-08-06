package service

import (
	"context"
	"errors"
	"testing"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// SEC1 — THE ATTACK: stamp a stranger's private label onto your own content.
//
// A label is private to whoever coined it: LabelService.List only ever returns
// ListByOwner(you), and both the create op (labelsForCreate) and the attach
// endpoint (LabelService.Attach) refuse an id that is not yours. The UPDATE op
// refused nothing. `labelIds` is one of the three roots a merge patch may write,
// so this went from the request body into the repository untouched:
//
//	POST /api/v1/transactions
//	{"ops":[{"elementId":"<a card I may edit>","action":"update",
//	         "changes":{"labelIds":["<a label id belonging to someone else>"]}}]}
//
// Mallory needs no access to Bob's account and no access to Bob's boards. She
// needs one label id, which she gets from any element on any board she shares
// with him — GET /elements/:id returns labelIds verbatim. What she gets is her
// own content filed under his private vocabulary: it appears when he filters by
// that label, and his usage count moves from outside his account.
//
// It is the same shape as the CLONE escalation and as the clone read bypass
// before it — a check that exists on one door and not on the identical door
// beside it. The create-side test (Create_RefusesSomebodyElsesLabel) has passed
// since the day it was written, which is why nobody looked at the update side.
func labelEscalationFixture(t *testing.T) (*TransactionService, *memory.ElementRepo, domain.LabelRepository, context.Context) {
	t.Helper()
	svc, elements, labels, ctx := createFieldsFixture(t)

	// One card of Mallory's own, on a board she may edit, already wearing one of
	// her own tags — the realistic starting state, and the one that proves the
	// fix keys on the DELTA rather than on the array.
	card := &domain.Element{
		ID: "cf00000000000000000bc900", Type: domain.TypeCard,
		Location:  domain.Location{ParentID: "cf00000000000000000ba001", Section: domain.SectionCanvas},
		Content:   domain.Content{"textPreview": "mallory's note"},
		LabelIDs:  []string{"lab-alice-q3"},
		CreatedBy: "alice",
	}
	if err := elements.Insert(ctx, card); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	if err := labels.IncrementUsage(ctx, "lab-alice-q3", 1); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	return svc, elements, labels, ctx
}

func labelPatch(ids ...string) []domain.Op {
	raw := make([]any, 0, len(ids))
	for _, id := range ids {
		raw = append(raw, id)
	}
	return []domain.Op{{
		ElementID: "cf00000000000000000bc900",
		Action:    domain.ActionUpdate,
		Changes:   domain.Content{"labelIds": raw},
	}}
}

// The attack itself. Alice owns the board and the card; lab-bob-secret is Bob's.
// Before the fix this returned a transaction and the element came back wearing
// Bob's label.
func TestUpdate_RefusesSomebodyElsesLabel(t *testing.T) {
	svc, elements, labels, ctx := labelEscalationFixture(t)

	_, err := svc.Apply(ctx, &domain.Principal{Sub: "alice"},
		"cf00000000000000000ba001", "", labelPatch("lab-alice-q3", "lab-bob-secret"))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an update attached another person's label: err = %v", err)
	}

	el, gerr := elements.Get(ctx, "cf00000000000000000bc900")
	if gerr != nil {
		t.Fatalf("read back: %v", gerr)
	}
	for _, id := range el.LabelIDs {
		if id == "lab-bob-secret" {
			t.Fatalf("the element carries %q — the write landed despite the refusal", id)
		}
	}
	// The refusal has to happen before the counter moves, or a rejected attack
	// still leaves a visible mark in the victim's label list.
	bobs, gerr := labels.Get(ctx, "lab-bob-secret")
	if gerr != nil {
		t.Fatalf("read label: %v", gerr)
	}
	if bobs.UsageCount != 0 {
		t.Errorf("Bob's usage count = %d, want 0: a refused attach still moved his counter", bobs.UsageCount)
	}

	// Why the refusal above is load-bearing rather than incidental: the storage
	// layer will happily do it. `labelIds` is in patchableRoots in BOTH
	// repositories, so the pre-fix applyOp — which handed op.Changes to
	// MergePatch unexamined — wrote a stranger's label with no check anywhere in
	// the stack. Delete authorizeLabelPatch and this is what the endpoint does
	// again; asserting it here keeps the test honest about what it is defending.
	if _, perr := elements.MergePatch(ctx, "cf00000000000000000bc900",
		domain.Content{"labelIds": []any{"lab-bob-secret"}}); perr != nil {
		t.Fatalf("repository patch: %v", perr)
	}
	raw, _ := elements.Get(ctx, "cf00000000000000000bc900")
	if len(raw.LabelIDs) != 1 || raw.LabelIDs[0] != "lab-bob-secret" {
		t.Fatalf("labels = %v: the repository is expected to accept this — the "+
			"service is the only thing that refuses it, which is the point", raw.LabelIDs)
	}
}

// The agent reaches the identical door — ActApplyLabel compiles to exactly this
// op — and verifyDelegation inspects `acl` and every content key while never
// looking at labelIds at all. A delegated principal must be refused for the same
// reason and by the same guard, not by luck about which tool happens to emit it.
func TestUpdate_ADelegatedRunCannotAttachSomebodyElsesLabelEither(t *testing.T) {
	svc, _, _, ctx := labelEscalationFixture(t)

	agentPrincipal := &domain.Principal{
		Sub: "alice", Name: "Alice",
		Delegation: &domain.Delegation{
			RunID: "r-sec1", OnBehalfOf: "alice", RootBoardID: "cf00000000000000000ba001",
			Capabilities: []domain.Capability{domain.CapElementUpdate},
			Consequence:  domain.ConsequenceReversibleWrite,
			MaxOps:       10,
		},
	}
	_, err := svc.Apply(ctx, agentPrincipal,
		"cf00000000000000000ba001", "", labelPatch("lab-bob-secret"))
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a delegated write attached another person's label: err = %v", err)
	}
}

// The guard must key on what the patch ADDS, or it breaks ordinary editing.
//
// labelIds is replace-semantics, so adding one tag means sending the array that
// is already on the element. If the check looked at the whole list rather than
// the delta, every legitimate tagging write on a shared board would 403 the
// moment a teammate's label was already on the card.
func TestUpdate_CarriesTheCallersOwnLabelAndMovesTheCount(t *testing.T) {
	svc, elements, labels, ctx := labelEscalationFixture(t)

	if err := labels.Insert(ctx, &domain.Label{ID: "lab-alice-blocked", OwnerID: "alice", Name: "Blocked"}); err != nil {
		t.Fatalf("seed label: %v", err)
	}
	if _, err := svc.Apply(ctx, &domain.Principal{Sub: "alice"},
		"cf00000000000000000ba001", "", labelPatch("lab-alice-q3", "lab-alice-blocked")); err != nil {
		t.Fatalf("a legitimate tagging write was refused: %v", err)
	}

	el, _ := elements.Get(ctx, "cf00000000000000000bc900")
	if len(el.LabelIDs) != 2 {
		t.Fatalf("labels = %v, want both", el.LabelIDs)
	}
	// The count is the half that used to drift silently: applyCreate incremented
	// and the update path did neither, so every tag applied after creation was
	// invisible to the filter list's usage ordering.
	added, _ := labels.Get(ctx, "lab-alice-blocked")
	if added.UsageCount != 1 {
		t.Errorf("newly attached label usage = %d, want 1", added.UsageCount)
	}
	kept, _ := labels.Get(ctx, "lab-alice-q3")
	if kept.UsageCount != 1 {
		t.Errorf("an untouched label's usage = %d, want 1: keeping a label counted as attaching it", kept.UsageCount)
	}
}

// Removing a tag is ordinary editing and stays allowed — including a label that
// is not the caller's, because it is genuinely on an element they may edit and
// leaving its count high would strand a phantom in the owner's filter forever.
func TestUpdate_DetachingStillWorksAndTakesTheCountWithIt(t *testing.T) {
	svc, elements, labels, ctx := labelEscalationFixture(t)

	if _, err := svc.Apply(ctx, &domain.Principal{Sub: "alice"},
		"cf00000000000000000ba001", "", labelPatch()); err != nil {
		t.Fatalf("clearing the labels was refused: %v", err)
	}
	el, _ := elements.Get(ctx, "cf00000000000000000bc900")
	if len(el.LabelIDs) != 0 {
		t.Fatalf("labels = %v, want none", el.LabelIDs)
	}
	l, _ := labels.Get(ctx, "lab-alice-q3")
	if l.UsageCount != 0 {
		t.Errorf("usage after detach = %d, want 0", l.UsageCount)
	}
}

// An op that says nothing about labels must not pay for this check — no element
// read, no label read. A 240-op drag batch goes through applyOp too.
func TestUpdate_AnOpWithoutLabelsIsUntouched(t *testing.T) {
	svc, elements, _, ctx := labelEscalationFixture(t)

	if _, err := svc.Apply(ctx, &domain.Principal{Sub: "alice"},
		"cf00000000000000000ba001", "", []domain.Op{{
			ElementID: "cf00000000000000000bc900",
			Action:    domain.ActionMove,
			Changes:   domain.Content{"location": map[string]any{"index": 3.0}},
		}}); err != nil {
		t.Fatalf("an ordinary move was refused: %v", err)
	}
	el, _ := elements.Get(ctx, "cf00000000000000000bc900")
	if len(el.LabelIDs) != 1 || el.LabelIDs[0] != "lab-alice-q3" {
		t.Fatalf("labels = %v — a move rewrote them", el.LabelIDs)
	}
}
