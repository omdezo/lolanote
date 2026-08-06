package agent

import (
	"fmt"
	"strings"
)

// What a run says when the loop stopped it mid-sentence.
//
// The observed failure: "make a film" was allowed 60 changes but only 14 model
// turns, staged about two changes a turn, and was cut by the step counter at the
// worst possible moment — the last column created and left empty. It shipped as
// COMPLETED with no summary and no unmet: a half-answer wearing a whole answer's
// clothes, and from the person's side there was nothing to distinguish it from a
// run that had finished and simply decided that was enough.
//
// The model never got the turn in which it would have said any of this, so the
// server says it instead. Every sentence here is computed from the plan, which
// is the only honest source available: the model's last words were about the
// step it was in the middle of, not about the shape of what it left behind.
//
// This also feeds the next run. A truncated run that names its unfilled
// containers hands its successor an actionable to-do; one that says nothing
// hands it amnesia, and "complete" arrives as a fresh, context-free request.

// stopReason is which of the four things that can cut a run actually did.
//
// It exists because the four used to behave as two. The step limit and the cost
// cap broke out of the loop and produced an honest prefix; the deadline and a
// provider outage returned early and produced NOTHING — the person waited eight
// minutes, was charged for every turn taken, and got a red card, for the same
// run that cut a different way came back reviewable with a Continue button.
// Naming the reason is what lets one exit path serve all four.
type stopReason int

const (
	// stopFinished is the model deciding it was done. Not a truncation.
	stopFinished stopReason = iota
	stopSteps
	stopCost
	stopDeadline
	stopProvider
)

// truncating reports whether this reason cut the run short.
func (r stopReason) truncating() bool { return r != stopFinished }

// discloseExhaustion tells the person what a truncated run could not.
//
// The reason is carried rather than guessed: naming the wrong budget sends
// somebody to raise a limit that was never reached, and reporting a provider
// outage as "ran out of room" is the version of that mistake that also hides an
// incident.
func discloseExhaustion(p *Plan, used, allowed int, why stopReason) {
	if p == nil {
		return
	}
	// The literal words matter more than the prose around them: this is the one
	// sentence that distinguishes a prefix from an answer, and it has to survive
	// being skimmed.
	cut := fmt.Sprintf(
		"This run ran out of room at step %d of %d — what is here is a prefix of the answer, not the whole of it.",
		used, allowed)
	switch why {
	case stopCost:
		cut = fmt.Sprintf(
			"This run ran out of its cost budget after %d step(s) — what is here is a prefix of the answer, not the whole of it.",
			used)
	case stopDeadline:
		cut = fmt.Sprintf(
			"This run ran out of time after %d step(s) — what is here is a prefix of the answer, not the whole of it.",
			used)
	case stopProvider:
		cut = fmt.Sprintf(
			"The AI service stopped responding after step %d — what is here is a prefix of the answer, not the whole of it.",
			used)
	}
	if p.Summary == "" {
		p.Summary = cut
	} else {
		// A summary can exist on a truncated run — a finish that the review turn
		// un-did, then no room left to finish again. Keeping it and leading with
		// the truncation is the honest order: what was claimed, qualified by what
		// actually happened.
		p.Summary = cut + " " + p.Summary
	}

	// No floor, unlike hollowContainers: see emptyContainers for why one empty
	// container is worth naming here and not there.
	names := emptyContainers(p, 1)
	if len(names) == 0 {
		return
	}
	cause := fmt.Sprintf(
		"the run was stopped at step %d of %d with these staged and nothing inside them yet", used, allowed)
	switch why {
	case stopCost:
		cause = fmt.Sprintf(
			"the run reached its cost limit after %d step(s) with these staged and nothing inside them yet", used)
	case stopDeadline:
		cause = fmt.Sprintf(
			"the run ran out of time after %d step(s) with these staged and nothing inside them yet", used)
	case stopProvider:
		cause = fmt.Sprintf(
			"the AI service stopped responding after step %d, with these staged and nothing inside them yet", used)
	}
	addUnmet(p, Unmet{Request: "filling " + strings.Join(names, ", "), Why: cause})
}
