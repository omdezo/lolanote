package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// Per-action revert produces the cleanest preference data the system will ever
// have — "these four were right, that one was wrong", attributed, timestamped,
// with the plan beside it. It was used for a strikethrough, and the next run
// inherited the string "applied, then undone by the user": a sentence that says
// a correction happened and not one thing about what it was.

func rejectedPlan() *Plan {
	return &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", Title: "Ideas",
			Summary: `Column "Ideas"`, ParentID: "b1"},
		{Seq: 1, Kind: ActMove, ElementID: "card-9", Summary: "Move 4 cards into Ideas", ParentID: "c1"},
		{Seq: 2, Kind: ActCreateColumn, ElementID: "c2", Title: "Keep",
			Summary: `Column "Keep"`, ParentID: "b1"},
	}}
}

func TestRejection_TheDigestCarriesTheShapeNotJustTheVerdict(t *testing.T) {
	rejected := RejectedShape(rejectedPlan(), []string{"c1", "card-9"}, false)
	if len(rejected) != 2 {
		t.Fatalf("rejected shape = %v, want the two actions they undid", rejected)
	}

	s := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard, Content: domain.Content{"title": "Film"}},
		Elements: map[string]*domain.Element{},
		History: []PriorRun{{
			Intent: "organise this", Outcome: "applied, then undone by the user",
			When: "3 days ago", Summary: "Grouped the loose cards.", Rejected: rejected,
		}},
	}
	out := s.Render("")
	if !strings.Contains(out, "THEY UNDID") {
		t.Fatalf("the next run inherited a verdict word and no shape:\n%s", out)
	}
	if !strings.Contains(out, `Column "Ideas"`) || !strings.Contains(out, "Move 4 cards") {
		t.Errorf("the rejected changes are not named:\n%s", out)
	}
	if !strings.Contains(out, "do not simply do these again") {
		t.Errorf("the block reports the rejection without saying what to do about it:\n%s", out)
	}
}

// A whole-run revert rejects everything; a partial one rejects only the named
// subset, and a run with nothing undone contributes nothing.
func TestRejection_WholeRunVersusPartial(t *testing.T) {
	if got := RejectedShape(rejectedPlan(), nil, true); len(got) != 3 {
		t.Errorf("a whole-run revert rejected %d of 3 actions", len(got))
	}
	if got := RejectedShape(rejectedPlan(), nil, false); got != nil {
		t.Errorf("a run nobody undid produced a rejection list: %v", got)
	}
}

// A revert is the same statement an adjustment makes, made later and with more
// information — so it becomes a correction record with the heaviest outcome.
func TestRejection_BecomesACorrectionRecord(t *testing.T) {
	run := &Run{ID: "r1", ProposedPlan: rejectedPlan()}
	got := RejectionCorrections(run, []string{"c1"}, false, time.Now())
	if len(got) != 1 {
		t.Fatalf("got %d corrections, want the one action they undid: %+v", len(got), got)
	}
	if got[0].Kind != CorrectRevert || got[0].Outcome != OutcomeReverted {
		t.Errorf("a revert was recorded as something else: %+v", got[0])
	}
	if got[0].Target != "ideas" {
		t.Errorf("target = %q, want the normalized subject so it can generalize", got[0].Target)
	}
	if got[0].RunID != "r1" {
		t.Error("the correction cannot name the run it came from, so a rule built on it " +
			"could not cite its evidence")
	}
}

// `remember` is offered only when the run was corrected mid-flight, and a revert
// happens AFTER a terminal state — so the most decisively corrected run in the
// product could never reach the one channel built to capture a rule.
func TestRejection_ReflectionProposesARuleAfterTheRunIsOver(t *testing.T) {
	provider := cognition.NewScripted(cognition.ScriptedStep{
		Tools: []cognition.ScriptedCall{{
			Name:  "propose_rule",
			Input: map[string]any{"text": "Columns are pipeline stages — never add one."},
		}},
	})
	run := &Run{ID: "r1", State: StateReverted,
		Task: TaskSpec{Intent: "organise this board"}, ProposedPlan: rejectedPlan()}

	got, err := Reflect(context.Background(), provider, run, []string{"c1"}, false)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if got != "Columns are pipeline stages — never add one." {
		t.Fatalf("the reflection produced %q", got)
	}
	// It must have been TOLD what was undone, or it is guessing.
	if len(provider.Calls) != 1 || !strings.Contains(provider.Calls[0].Messages[0].Text, `Column "Ideas"`) {
		t.Errorf("the reflection call did not carry the rejected shape: %+v", provider.Calls)
	}
	if provider.Calls[0].ForceTool != "propose_rule" {
		t.Error("the reflection was free to answer in prose, which lands nowhere")
	}
}

// A run that board content steered does not get to propose a rule about it: the
// material the reflection would reason over IS the injected content.
func TestRejection_AQuarantinedRunDoesNotReflect(t *testing.T) {
	plan := rejectedPlan()
	plan.Quarantined = true
	provider := cognition.NewScripted()
	run := &Run{ID: "r1", State: StateReverted, ProposedPlan: plan}
	got, err := Reflect(context.Background(), provider, run, nil, true)
	if err != nil || got != "" {
		t.Fatalf("a quarantined run reflected: %q err=%v", got, err)
	}
	if len(provider.Calls) != 0 {
		t.Error("the reflection was paid for on a run whose output must be discarded")
	}
}

// Nothing undone is nothing to learn from, and it must cost no model call.
func TestRejection_NothingUndoneCostsNothing(t *testing.T) {
	provider := cognition.NewScripted()
	run := &Run{ID: "r1", ProposedPlan: rejectedPlan()}
	if got, _ := Reflect(context.Background(), provider, run, nil, false); got != "" {
		t.Errorf("reflected on a run nobody corrected: %q", got)
	}
	if len(provider.Calls) != 0 {
		t.Error("a model call was made with nothing to reflect on")
	}
}
