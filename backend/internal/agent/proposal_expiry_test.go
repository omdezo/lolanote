package agent_test

import (
	"context"
	"testing"
	"time"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
)

// A previewed plan that nobody answers used to lock its board's agent forever.
//
// PROPOSED is non-terminal, so it holds the board's single-run slot; Reconcile
// deliberately skipped it; nothing expired it; and the composer only ever listed
// COMPLETED runs, so the proposal was invisible from every screen in the
// product. Preview once, close the tab, and that board could never run the agent
// again — for anyone, including the person who left it there.
func TestProposal_StopsHoldingTheBoardOnceItExpires(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Abandoned"}),
		}},
		finish("One column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	first, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Organize", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.awaitState(t, first.ID, agent.StateProposed)

	// While the proposal is live it legitimately holds the slot.
	if proposed.ProposalExpiresAt == nil {
		t.Fatal("a proposal carries no deadline, so nothing can ever free the board")
	}
	if _, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Something else", Autonomy: agent.AutonomyPreview,
	}); err == nil {
		t.Fatal("a second run started while a proposal was awaiting an answer")
	}

	// Now the person never comes back. Wind the deadline into the past exactly
	// as time would, and sweep.
	past := time.Now().UTC().Add(-time.Minute)
	proposed.ProposalExpiresAt = &past
	if err := h.runs.Update(ctx, proposed, proposed.Rev); err != nil {
		t.Fatalf("stage the expiry: %v", err)
	}
	h.svc.Reconcile(ctx)

	after, err := h.svc.Get(ctx, h.principal, first.ID)
	if err != nil {
		t.Fatalf("read the run back: %v", err)
	}
	if after.State != agent.StateDiscarded {
		t.Fatalf("state = %s, want DISCARDED once the proposal expired", after.State)
	}
	if after.Reason == "" {
		t.Error("the run was closed with no reason, so the audit cannot say why")
	}

	// The whole point: the board works again.
	if _, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Try again", Autonomy: agent.AutonomyPreview,
	}); err != nil {
		t.Fatalf("the board is still locked after the proposal expired: %v", err)
	}
}

// A proposal written before the field existed has no deadline. It must not be
// discarded on sight — somebody may be looking at it — but it must not be
// exempt either, or every board deadlocked before this change stays deadlocked.
func TestProposal_LegacyProposalsAreAdoptedRatherThanKilled(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Legacy"}),
		}},
		finish("One column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Organize", Autonomy: agent.AutonomyPreview,
	})
	proposed := h.awaitState(t, run.ID, agent.StateProposed)

	// Exactly what a row written by the previous version looks like.
	proposed.ProposalExpiresAt = nil
	if err := h.runs.Update(ctx, proposed, proposed.Rev); err != nil {
		t.Fatalf("stage the legacy row: %v", err)
	}

	h.svc.Reconcile(ctx)

	adopted, err := h.svc.Get(ctx, h.principal, run.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if adopted.State != agent.StateProposed {
		t.Fatalf("state = %s; a legacy proposal was destroyed rather than adopted", adopted.State)
	}
	if adopted.ProposalExpiresAt == nil {
		t.Fatal("the sweep left it without a deadline, so it is still immortal")
	}

	// And the second sweep, once that deadline passes, does free the board.
	past := time.Now().UTC().Add(-time.Minute)
	adopted.ProposalExpiresAt = &past
	if err := h.runs.Update(ctx, adopted, adopted.Rev); err != nil {
		t.Fatalf("wind the clock: %v", err)
	}
	h.svc.Reconcile(ctx)

	final, _ := h.svc.Get(ctx, h.principal, run.ID)
	if final.State != agent.StateDiscarded {
		t.Errorf("state = %s, want DISCARDED on the second sweep", final.State)
	}
}

// Answering a proposal clears the deadline — a run that moved on must not carry
// a stale expiry that a later sweep could act on.
func TestProposal_DeadlineIsClearedWhenTheRunMovesOn(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Answered"}),
		}},
		finish("One column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Organize", Autonomy: agent.AutonomyPreview,
	})
	h.awaitState(t, run.ID, agent.StateProposed)

	applied, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.ProposalExpiresAt != nil {
		t.Errorf("an applied run still carries a proposal deadline (%v)", applied.ProposalExpiresAt)
	}
}
