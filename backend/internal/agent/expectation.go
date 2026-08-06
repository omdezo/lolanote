package agent

import (
	"fmt"
	"strings"
)

// Checking the plan against the REQUEST, not just against itself.
//
// The review turn measured the plan — how many changes, how many empty
// containers — and never asked the only question that finally matters: does
// this answer what was asked?
//
// Two failures survived every prose instruction because of it. "What is missing
// from this plan?" produced fourteen useful notes AND moved two cards and
// renamed one: the notes were the answer, the edits were noise the person had
// to review. "Break down the crew structure" produced eight columns and no
// hierarchy at all. In both the prompt already said the right thing, in words,
// and words were not enough.
//
// So the intent is read for what KIND of answer it wants, and the plan is
// checked against that. Reading English by keyword is crude and it is honest
// about being crude: it only ever raises a question the model can overrule, and
// it fires on the phrasings people actually use.

// expectation is what the request's wording implies the answer should be.
type expectation struct {
	// Reporting: the answer is words, and the board is not touched.
	Reporting bool
	// Relational: the answer has a shape — a process, a hierarchy, a breakdown.
	Relational bool
	// Impossible: the request is for something outside what the agent can do at
	// all — sharing, permissions, account settings.
	Impossible bool
}

// questionOpeners are how English requests for an ANSWER begin. A request that
// starts this way wants to be told something, not to have the board rearranged
// while it is being told.
var questionOpeners = []string{
	"what ", "which ", "why ", "who ", "where ", "when ",
	"is ", "are ", "do ", "does ", "should ", "could ", "how ",
}

// relationalWords are the requests whose answer is a shape rather than a list.
var relationalWords = []string{
	"break down", "breakdown", "map how", "map the", "map out",
	"flow", "process", "pipeline", "structure", "hierarchy",
	"depends", "dependency", "sequence", "steps from", "workflow",
	"who reports", "org chart", "what leads to",
}

// impossibleWords name the capabilities the agent does not have. Asked for one
// of these it must decline and STOP — not decline and then build something else
// to have produced a result.
// Phrases, not fragments. "make this public" also matches "make this
// public-facing copy clearer", which is ordinary editing work — a substring
// match on a common word turns a useful check into one that refuses real
// requests, and a check that refuses real requests gets switched off.
var impossibleWords = []string{
	"share this board", "share the board", "share it with",
	"make it public", "make the board public", "publish this board",
	"permissions", "give access", "grant access", "sharing settings",
	"invite the team", "invite everyone", "add collaborators",
	"delete my account", "change my password", "billing",
}

// expectationOf reads the request for the kind of answer it implies.
func expectationOf(intent string) expectation {
	s := strings.ToLower(strings.TrimSpace(intent))
	var e expectation
	for _, opener := range questionOpeners {
		if strings.HasPrefix(s, opener) {
			e.Reporting = true
			break
		}
	}
	// A question mark anywhere is the same signal, wherever the sentence starts.
	if strings.Contains(s, "?") {
		e.Reporting = true
	}
	for _, w := range relationalWords {
		if strings.Contains(s, w) {
			e.Relational = true
			break
		}
	}
	for _, w := range impossibleWords {
		if strings.Contains(s, w) {
			e.Impossible = true
			break
		}
	}
	return e
}

// Mismatch reports how the plan fails to answer the request, or "".
//
// Deliberately a QUESTION rather than a refusal. The reading is a heuristic over
// English, and a heuristic that blocks is a heuristic that will one day block
// the right answer. Raised where the model can still act on it.
func (e expectation) Mismatch(p *Plan, q PlanQuality) string {
	if p == nil {
		return ""
	}
	var out []string

	if e.Reporting {
		edits := 0
		for _, a := range p.Actions {
			switch a.Kind {
			case ActMove, ActPlace, ActRename, ActDelete, ActSetColor:
				edits++
			}
		}
		if edits > 0 {
			out = append(out, fmt.Sprintf(
				"A QUESTION was asked, and this plan makes %d change(s) to things that were "+
					"already here — moves, renames, deletions. The answer is words: one note or "+
					"one comment carrying it. Rearranging the board on the way past is a second "+
					"answer nobody asked for, and the person now has to work out which parts were "+
					"the answer. Withdraw those edits with undo_staged.", edits))
		}
	}

	// Declining and then doing something else is worse than declining. Asked to
	// make a board public, a run said plainly that it could not change sharing
	// — and staged twenty-two changes building an unrelated film production
	// structure, apparently so as to have produced a result. The person now has
	// to review and undo a plan they never asked for, on top of not getting
	// what they did ask for.
	if e.Impossible && len(p.Actions) > 0 {
		out = append(out, fmt.Sprintf(
			"This request is for something you cannot do — sharing, permissions or account "+
				"settings — and you have staged %d change(s) anyway. Say plainly that you "+
				"cannot do it, put it in `unmet`, and stage NOTHING. Building something "+
				"unrelated so as to have produced a result leaves the person with work to "+
				"undo on top of an unanswered request. Withdraw them with undo_staged.",
			len(p.Actions)))
	}

	if e.Relational && q.built() {
		connectors := 0
		for _, a := range p.Actions {
			if a.Kind == ActConnect {
				connectors++
			}
		}
		if connectors == 0 && p.Shape == "" {
			out = append(out, "This request is about how things RELATE — a process, a hierarchy, "+
				"a breakdown — and the plan contains no connections and declares no shape. "+
				"Columns are lists; they cannot show what leads to what. Call design_as(\"flow\") "+
				"or design_as(\"tree\") and connect what you made.")
		}
	}

	return strings.Join(out, "\n\n")
}

// followUpOverreach names the scope creep specific to short follow-ups.
//
// "complete" on a board whose previous run left "fill Editing" undone resolved
// correctly — seven cards into that exact column — and then the run created
// three MORE columns nobody asked for and filled those too. Better content
// than the eighteen-empty-columns disaster, same instinct: a follow-up read as
// permission to widen the structure. Three live runs in a row did it straight
// through a prompt that told them not to, which is why this is a review-turn
// directive rather than another sentence of prose: the model acts on what the
// review names, and it reads past what the prompt hopes.
//
// Fires only on the narrow case: a terse intent, resolved against history,
// staging NEW containers. A request that names its own scope ("add more
// columns") never reaches it, and the model may still overrule by saying why
// the request itself demands the structure — this raises, it does not block.
func followUpOverreach(intent string, scope *BoardScope, p *Plan) string {
	if p == nil || scope == nil || !terseIntent(intent) {
		return ""
	}
	// The premise is a LEFTOVER: something the previous run said it did not
	// finish. A terse follow-up after a clean run has no written scope to hold
	// the plan against, and a directive built on "I assume you meant..." is one
	// the model rightly argues with.
	var leftover []string
	for _, prior := range scope.History {
		if len(prior.Unmet) > 0 {
			leftover = prior.Unmet
			break
		}
	}
	if len(leftover) == 0 {
		return ""
	}
	var made []string
	for _, a := range p.Actions {
		if a.Kind.Creates() && a.Kind.Container() && a.Kind.ElementType() != "" {
			name := a.Title
			if name == "" {
				name = a.Summary
			}
			made = append(made, fmt.Sprintf("%q", name))
		}
	}
	if len(made) == 0 {
		return ""
	}
	// The leftover is QUOTED, not alluded to. "What that run left undone is the
	// whole scope" was an assertion the model argued with two runs in five —
	// justifying its extra columns in the summary and keeping them. Set against
	// the previous run's own words, the gap between what was asked and what was
	// built stops being a matter of opinion.
	return fmt.Sprintf(
		"THIS LOOKS LIKE SCOPE CREEP ON A FOLLOW-UP. The request is a few words, so its "+
			"meaning comes from what the previous run left undone — and that run wrote down "+
			"exactly what that was:\n  %s\n\n"+
			"That list is the whole scope. You created %s; the list names none of them. "+
			"Withdraw them with undo_staged (their contents go with them) and put the material "+
			"into what already exists.\n\n"+
			"Only keep them if THE REQUEST demands them — which a one-word follow-up cannot. "+
			"\"A film also needs these stages\" is your judgement, not their ask: put it in "+
			"your summary as a suggestion, and let the person say so with their next request.",
		strings.Join(leftover, "\n  "), strings.Join(made, ", "))
}
