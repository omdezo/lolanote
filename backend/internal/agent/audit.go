package agent

import "strings"

// The final audit: the plan-quality checks, run over the plan that actually
// shipped, for the PERSON rather than for the model.
//
// Four of the five checks used to live inside reviewTurn and nowhere else, and
// reviewTurn is doubly gated: once per run, and never with fewer than two steps
// left. Both guards are individually right — never pay for insight you cannot
// act on, never pay twice — and together they produce the opposite of the
// coverage they were written for. A step-starved run has its last two steps
// consumed, so it is never reviewed; a run that reviews at step 3 and then works
// for fifteen more is never reviewed on what it finally built. The runs that
// most need the check are precisely the ones that cannot get it.
//
// The way out is that all five checks are PURE FUNCTIONS OVER THE FINISHED
// PLAN. The gates exist because a review costs a model turn — that cost applies
// to the model's copy of the answer, not to the person's. So the person gets
// theirs unconditionally, after the loop, with zero model calls, written into
// Plan.Notes exactly as discloseHollow already writes the fifth check into
// `unmet`.

// maxAuditNotes bounds the block. Three findings tell a reviewer what is wrong
// with a plan; eight is a wall they skim past, which is the same as none.
const maxAuditNotes = 3

// auditPlan measures the finished plan and records what is weak about it where
// the person will read it.
//
// Notes is the harness speaking, so these lines state measurements and their
// implication and stop there. The verdict stays with the reader: a plan of four
// changes is thin for "set up pre-production" and complete for "fix the
// spelling", and this cannot tell which it was looking at.
func auditPlan(p *Plan, scope *BoardScope, task TaskSpec) {
	if p == nil || len(p.Actions) == 0 {
		return
	}
	quality := MeasurePlan(p, scope, task.Budget)
	want := expectationOf(task.Intent)

	// The empty-container line is dropped here and only here. discloseHollow
	// states the same fact in `unmet`, in the agent's own voice and with the
	// containers named — and a person meeting one finding twice on one outcome
	// card learns to skim both.
	measured := quality
	measured.Empty = 0

	var findings []string
	findings = append(findings, measured.CritiqueFor(want)...)
	if mismatch := want.Mismatch(p, quality); mismatch != "" {
		findings = append(findings, mismatch)
	}
	findings = append(findings, shellCritique(p, scope)...)
	// House-style violations reach the FINISHED plan too, not only the review
	// turn. A step-starved run never gets a review, and a run that reviewed at
	// step 3 was never measured on what it built afterwards — which is exactly
	// the run whose last four columns are named in the wrong language.
	findings = append(findings, ConformanceCritique(p, scope)...)
	if len(findings) == 0 {
		return
	}
	if len(findings) > maxAuditNotes {
		findings = findings[:maxAuditNotes]
	}
	for _, f := range findings {
		p.Notes = append(p.Notes, "Looking at the finished plan: "+auditVoice(f))
	}
}

// auditVoice turns a line written AT the model into a line written ABOUT the
// plan.
//
// The critiques are shared with reviewTurn, where they are read by the run that
// can still act on them and are phrased accordingly — "withdraw them with
// undo_staged", "you are allowed 60". Handed unchanged to a person looking at a
// finished proposal, that reads as instructions addressed to them for tools they
// do not have.
func auditVoice(s string) string {
	for _, cut := range []string{
		" Withdraw them with undo_staged.",
		" Withdraw those edits with undo_staged.",
		"Withdraw those edits with undo_staged.",
	} {
		s = strings.ReplaceAll(s, cut, "")
	}
	s = strings.ReplaceAll(s, "You are allowed", "The run was allowed")
	s = strings.ReplaceAll(s, "you have staged", "the run staged")
	s = strings.ReplaceAll(s, "and you have used", "and it used")
	s = strings.ReplaceAll(s, "have used", "used")
	return strings.TrimSpace(s)
}
