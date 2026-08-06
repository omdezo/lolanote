package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qomranote/backend/internal/agent/cognition"
)

// Learning from what people took back.
//
// Two halves of the same finding. The first is that a per-action revert is the
// cleanest preference data the system will ever have — "these four were right,
// that one was wrong", attributed, timestamped, with the plan beside it — and it
// was being spent on a strikethrough. The second is that the run which WAS
// corrected is structurally barred from proposing a rule about it: `remember` is
// only offered when the run was corrected mid-flight, and a revert or a discard
// happens AFTER a terminal state, so the strongest correction signal in the
// product could never reach the one channel built to capture it.

// AttachLearnedRules compiles this board's correction history into enforceable
// rules and hangs them on the scope.
//
// The four TRACE stages land as four cheap steps over data that already exists:
// DETECT is Run.Corrections, written at commit where both plans are in hand;
// GENERALIZE is a pure grouping by correction shape; VALIDATE replays each
// candidate against the plans this board actually APPLIED and kept; ENFORCE is a
// refusal at the tool boundary that quotes the person's own words back.
//
// Validation is what makes this safe to ship without a review queue. A rule
// generalized from two removals is a hypothesis, and one applied-and-kept action
// it would have refused proves it over-broad — which also subsumes retirement:
// a rule the person has since overridden simply stops being compiled, rather
// than needing a hits/misses counter to notice.
func AttachLearnedRules(scope *BoardScope, prior []*Run) {
	if scope == nil {
		return
	}
	var corrections []Correction
	var kept []*Plan
	for _, r := range prior {
		if r == nil {
			continue
		}
		corrections = append(corrections, StampRun(r.ID, r.Corrections)...)
		if r.Plan == nil {
			continue
		}
		switch r.State {
		case StateCompleted, StatePartial:
			// What they applied AND left standing. An action they applied and
			// then undid is evidence FOR the rule, not against it, so the
			// reverted subset is removed before the plan counts as a keep.
			kept = append(kept, planWithout(r.Plan, r.RevertedElementIDs))
		}
	}
	for _, cand := range GeneralizeCorrections(corrections) {
		if !ValidateRule(cand, kept) {
			continue
		}
		scope.LearnedRules = append(scope.LearnedRules, cand)
	}
}

// planWithout drops the actions whose element the person later undid.
func planWithout(p *Plan, revertedIDs []string) *Plan {
	if len(revertedIDs) == 0 {
		return p
	}
	gone := make(map[string]bool, len(revertedIDs))
	for _, id := range revertedIDs {
		gone[id] = true
	}
	out := *p
	out.Actions = nil
	for _, a := range p.Actions {
		if !gone[a.ElementID] {
			out.Actions = append(out.Actions, a)
		}
	}
	return &out
}

// RejectionCorrections turns a revert into correction records.
//
// A per-action undo is the same statement an adjustment makes, made later and
// with more information — the person lived with the change and then took it
// back — so it weighs more, not less. Derived from the PROPOSED plan rather than
// the effective one, so the seq numbers line up with what the person reviewed.
func RejectionCorrections(run *Run, revertedIDs []string, wholeRun bool, now time.Time) []Correction {
	if run == nil {
		return nil
	}
	plan := run.ProposedPlan
	if plan == nil {
		plan = run.Plan
	}
	if plan == nil {
		return nil
	}
	undone := make(map[string]bool, len(revertedIDs))
	for _, id := range revertedIDs {
		undone[id] = true
	}
	var out []Correction
	for _, a := range plan.Actions {
		if !wholeRun && !undone[a.ElementID] {
			continue
		}
		c := Correction{
			Kind: CorrectRevert, Seq: a.Seq, ActionKind: a.Kind,
			Target: CorrectionTarget(a), Outcome: OutcomeReverted, At: now,
			RunID: run.ID,
		}
		if !a.Kind.Creates() {
			c.ElementID = a.ElementID
		}
		out = append(out, c)
	}
	return out
}

// reflectionTool is the one shape a reflection call may return.
var reflectionTool = cognition.ToolDef{
	Name: "propose_rule",
	Description: "State, in ONE sentence, the convention this run got wrong — phrased as a " +
		"standing rule for this board. The person approves it before it is saved.",
	Schema: map[string]any{
		"type":     "object",
		"required": []string{"text"},
		"properties": map[string]any{
			"text": map[string]any{"type": "string",
				"description": "One sentence, e.g. \"Columns are pipeline stages — never add one.\""},
		},
	},
}

// Reflect asks, after a rejection, what the rule should have been.
//
// Fired on the transition to REVERTED or DISCARDED, which is why it takes the
// run rather than the staging state: it must run AFTER a terminal state without
// reopening the run. It lands in the existing ProposedRule field, so the outcome
// card renders it unchanged on the person's next visit and no new surface is
// needed.
//
// Affordable precisely because rejections are rare: the cost scales with the
// failure rate rather than with the run rate, which is the opposite of every
// other proposed model call in this area.
func Reflect(ctx context.Context, provider cognition.Provider, run *Run, revertedIDs []string, wholeRun bool) (string, error) {
	if provider == nil || run == nil {
		return "", cognition.ErrNotConfigured
	}
	plan := run.ProposedPlan
	if plan == nil {
		plan = run.Plan
	}
	rejected := RejectedShape(plan, revertedIDs, wholeRun)
	if len(rejected) == 0 {
		return "", nil
	}
	// A quarantined run does not get to write memory, and a proposed rule is the
	// most enforcement-adjacent thing it could write. The reflection is skipped
	// entirely rather than sanitized, because the material it would reason over
	// is the injected content itself.
	if plan != nil && plan.Quarantined {
		return "", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "They asked: %q\n", truncate(run.Task.Intent, 400))
	b.WriteString("The plan was applied and then taken back. What they undid:\n")
	for _, line := range rejected {
		b.WriteString("- " + line + "\n")
	}
	b.WriteString("\nName the convention this got wrong, as one standing rule for this board. " +
		"If the undo does not imply a durable convention — they simply changed their " +
		"mind — call propose_rule with an empty text rather than inventing a rule.")

	resp, err := provider.Complete(ctx, cognition.Request{
		System: "You are reviewing one rejected plan to work out what standing rule would " +
			"have prevented it. One sentence, about this board, in the person's own terms. " +
			"Never a rule about your own behaviour in general.",
		Messages:  []cognition.Message{{Role: "user", Text: b.String()}},
		Tools:     []cognition.ToolDef{reflectionTool},
		ForceTool: reflectionTool.Name,
		MaxTokens: 300,
		Label:     "reflect.rejection",
		// One forced call over a rejected plan: transcription, not judgement.
		// Paying authoring rates for it is what made a per-rejection reflection
		// look expensive when the failure rate is what bounds its cost.
		Tier: cognition.TierFast,
	})
	if err != nil {
		return "", err
	}
	for _, call := range resp.Calls {
		if call.Name != reflectionTool.Name {
			continue
		}
		var out struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(call.Input, &out); err != nil {
			return "", nil
		}
		text := out.Text
		// Sanitized on the WRITE, not on every future read. The material this
		// call reasoned over is board content, so its output is board content
		// with an extra step — and a payload cleaned only on read is one that
		// survives until the first reader forgets.
		clean, ok := MemoryWritable(false, text)
		if !ok {
			return "", nil
		}
		return truncate(clean, 200), nil
	}
	return "", nil
}
