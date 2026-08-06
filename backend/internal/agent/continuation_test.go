package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// Memory between runs, and the button that uses it.
//
// The film build was cut at thirty actions with its last column created and
// empty. There was no way to say "keep going" except typing a fresh prompt that
// knew nothing about the run before it — so the follow-up, "complete", built a
// second copy of the structure beside the first.

// seedFinishedRun writes a terminal run with an unmet list into the store, the
// way a truncated run leaves one behind.
func (h *harness) seedFinishedRun(t *testing.T, id, intent string, unmet ...agent.Unmet) *agent.Run {
	t.Helper()
	run := &agent.Run{
		ID:     id,
		Tenant: owner,
		Task: agent.TaskSpec{
			Intent: intent, Owner: owner, RootBoardID: boardID,
			Scope: agent.ScopeBoard, Autonomy: agent.AutonomyPreview,
		},
		State: agent.StateCompleted,
		Plan: &agent.Plan{
			Summary: "This run ran out of room at step 14 of 14 — what is here is a prefix of the answer.",
			Unmet:   unmet,
		},
		CreatedAt: time.Now().UTC().Add(-2 * time.Minute),
		UpdatedAt: time.Now().UTC().Add(-2 * time.Minute),
	}
	if err := h.runs.Insert(context.Background(), run); err != nil {
		t.Fatalf("seed prior run: %v", err)
	}
	return run
}

// W6, through the whole harness: what the last run left undone is in the next
// run's context before it stages anything.
func TestMemory_TheNextRunSeesWhatTheLastOneLeftUndone(t *testing.T) {
	h := newHarness(t, finish("Filled them."), confirm())
	h.seedBoard(t, boardID, "a note")
	h.seedFinishedRun(t, "prior000000000000000001", "make a film production plan",
		agent.Unmet{
			Request: "filling Editing, Sound",
			Why:     "the run was stopped at step 14 of 14 with these staged and nothing inside them yet",
		})

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "complete", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.awaitState(t, run.ID, agent.StateProposed, agent.StatePartial)

	var sawUnmet, sawBlock bool
	for _, req := range h.provider.Calls {
		for _, m := range req.Messages {
			if strings.Contains(m.Text, "filling Editing, Sound") {
				sawUnmet = true
			}
			if strings.Contains(m.Text, "PREVIOUS RUN") {
				sawBlock = true
			}
		}
	}
	if !sawBlock {
		t.Error("the run was never shown a previous-run block")
	}
	if !sawUnmet {
		t.Error("the previous run's unmet list never reached the model — " +
			"which is the whole of why 'complete' arrived context-free")
	}
}

// M2's acceptance line: the continuation's task carries the prior unmet as its
// intent, and the two runs link.
func TestContinuation_CarriesThePriorUnmetAndLinksBack(t *testing.T) {
	h := newHarness(t, finish("Filled them."), confirm())
	h.seedBoard(t, boardID, "a note")
	prior := h.seedFinishedRun(t, "prior000000000000000002", "make a film production plan",
		agent.Unmet{Request: "filling Editing, Sound", Why: "the run was stopped at step 14 of 14"})

	next, err := h.svc.Continue(context.Background(), h.principal, prior.ID)
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if !strings.Contains(next.Task.Intent, "filling Editing, Sound") {
		t.Errorf("the continuation does not carry what was left undone: %q", next.Task.Intent)
	}
	if !strings.Contains(next.Task.Intent, "make a film production plan") {
		t.Errorf("the continuation dropped the request the leftovers belong to: %q", next.Task.Intent)
	}
	// The instruction the whole item exists for: finish what is standing.
	if !strings.Contains(next.Task.Intent, "do not create a second set") {
		t.Errorf("nothing tells the continuation to fill rather than rebuild: %q", next.Task.Intent)
	}
	if next.ContinuesRunID != prior.ID {
		t.Errorf("continuesRunId = %q, want %q — the two runs are one job and "+
			"nothing afterwards can tell", next.ContinuesRunID, prior.ID)
	}
	if next.ID == prior.ID {
		t.Error("Continue resurrected the old run rather than starting a new one")
	}
	h.awaitState(t, next.ID, agent.StateProposed, agent.StatePartial)
}

// A run that finished everything has nothing to continue. Offering it anyway
// would spend a run's budget asking the model to redo work that is standing.
func TestContinuation_ARunWithNothingLeftIsRefused(t *testing.T) {
	h := newHarness(t)
	h.seedBoard(t, boardID, "a note")
	prior := h.seedFinishedRun(t, "prior000000000000000003", "make a film production plan")

	if _, err := h.svc.Continue(context.Background(), h.principal, prior.ID); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("continuing a finished run returned %v, want a validation refusal", err)
	}
}

// Continuing a run that has not stopped would mean two runs writing one board.
func TestContinuation_ARunStillInFlightCannotBeContinued(t *testing.T) {
	h := newHarness(t, cognition.ScriptedStep{Text: "thinking"})
	h.seedBoard(t, boardID, "a note")

	live := &agent.Run{
		ID: "live0000000000000000001", Tenant: owner,
		Task:  agent.TaskSpec{Intent: "make a film", Owner: owner, RootBoardID: boardID},
		State: agent.StateRunning, Active: true,
		Plan:      &agent.Plan{Unmet: []agent.Unmet{{Request: "filling Editing"}}},
		CreatedAt: time.Now().UTC(),
	}
	if err := h.runs.Insert(context.Background(), live); err != nil {
		t.Fatalf("seed live run: %v", err)
	}
	if _, err := h.svc.Continue(context.Background(), h.principal, live.ID); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("continuing a live run returned %v, want a conflict", err)
	}
}
