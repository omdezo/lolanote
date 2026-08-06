package agent

import (
	"fmt"
	"strings"
)

// Measuring a plan, so "is this enough?" has an answer rather than an opinion.
//
// The self-review asked the model to "count what you staged" and judge whether
// that was a complete answer. Asking something to audit itself in prose is the
// weakest possible check: the run that produced six empty column headings and
// stopped was perfectly satisfied with six empty column headings.
//
// So the server counts instead, and hands the numbers back. A model shown
// "12 of 60 changes · 6 containers, 3 empty · 0 new cards" has something to
// disagree with. A model asked "is this complete?" says yes.
//
// Every metric here is a fact about the plan, not a judgement of taste. What
// counts as good differs by request — a rename is a complete answer to "fix the
// spelling" — so this reports and the model decides, with one exception: the
// floor for a plan that builds structure and puts nothing in it, which is
// wrong in every register.

// PlanQuality is what a plan measurably is.
type PlanQuality struct {
	// Actions staged, against what the run was allowed.
	Actions int
	Budget  int

	// Containers created, and how many of them hold anything.
	Containers int
	Empty      int

	// Content is the substance: cards, to-do lists, links, tables — the things
	// a person reads. Structure is the shelves that hold them.
	Content   int
	Structure int

	// Reused counts existing elements the plan files somewhere. A plan that is
	// all reuse is organising; all content is authoring; both is a rebuild.
	Reused int

	// Depth is how many levels of containment the plan creates.
	Depth int

	// WholeInOne marks a plan whose substance rides on a FEW actions rather
	// than many — a document is a page of prose in one action, a duplicate is a
	// whole subtree in one.
	//
	// Without it the thin-plan critique fires on plans that are correctly
	// minimal. Asked to write a treatment, a run staged exactly one document,
	// was told "1 change is a sketch, not a structure", and answered by writing
	// a SECOND treatment — a critique tuned for empty-column plans pushing a
	// correct answer into a worse one.
	WholeInOne bool

	// FixedShape names a document whose shape the trade decided, when that is
	// what the plan is building — or "" for ordinary structure.
	//
	// The same failure as WholeInOne, one axis over. "Most of them are close to
	// empty, either fewer groups or more of what goes in them" is right for a
	// board of ideas and WRONG for a shooting schedule, which legitimately has
	// one column per shooting day for eighteen to thirty days. Without the
	// exemption the product's own quality machinery tells a run off for
	// producing the right answer — and the run, which has been taught to take
	// the critique seriously, rebuilds it into three acts and destroys a
	// schedule.
	FixedShape string
}

// MeasurePlan computes the facts. Pure, so it can be asserted on in tests and
// shown to the model in the same shape.
func MeasurePlan(p *Plan, scope *BoardScope, budget Budget) PlanQuality {
	q := PlanQuality{Budget: budget.MaxActions}
	if p == nil {
		return q
	}
	q.Actions = len(p.Actions)
	q.Empty = len(hollowContainers(p))

	filled := map[string]bool{}
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.ParentID != "" && (a.Kind.Creates() || a.Kind == ActMove) {
			filled[a.ParentID] = true
		}
	}

	for i := range p.Actions {
		a := &p.Actions[i]
		switch {
		case a.Kind == ActMove || a.Kind == ActPlace:
			q.Reused++
		case a.Kind == ActWriteDocument:
			q.Content++
			q.WholeInOne = true
		case a.Kind == ActDuplicate:
			q.WholeInOne = true
			// One action, many elements. Counting it as a single empty
			// container told a run that had just copied a full column it had
			// "created shelves and nothing to put on them" — and the critique
			// pushes back on exactly the kind of plan that was correct.
			q.Containers++
			q.Structure++
			if n := len(a.Copies) - 1; n > 0 {
				q.Content += n
			}
		case a.Kind.Creates() && a.Kind.Container():
			q.Containers++
			q.Structure++
		case a.Kind.Creates():
			q.Content++
		}
	}
	q.Depth = plannedDepth(p, scope)
	q.FixedShape = plannedFixedShape(p)
	return q
}

// thinFloor is the number of changes below which a plan that BUILT something is
// almost certainly a set of headings rather than an answer. Not applied to
// plans that only edit: "fix the spelling" is complete in one action.
const thinFloor = 10

// Report renders the measurements for the model, in the order that matters.
//
// Deliberately numbers first and judgement second. "You have used a fifth of
// your room" is a fact it can act on; "be more thorough" is a mood.
func (q PlanQuality) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "MEASURED: %d of %d changes used", q.Actions, q.Budget)
	if q.Budget > 0 {
		fmt.Fprintf(&b, " (%d%%)", q.Actions*100/q.Budget)
	}
	if q.Containers > 0 {
		fmt.Fprintf(&b, " · %d container(s), %d empty", q.Containers, q.Empty)
	}
	fmt.Fprintf(&b, " · %d new card(s) of content · %d existing item(s) filed", q.Content, q.Reused)
	if q.Depth > 1 {
		fmt.Fprintf(&b, " · %d levels deep", q.Depth)
	}
	// Said out loud, because the model has to know WHY it is not being told off
	// for thirty columns. Silence would read as the check having missed it, and
	// the next turn would helpfully "fix" the schedule.
	if q.FixedShape != "" {
		fmt.Fprintf(&b, "\nRECOGNISED: this is %s, so the usual three-to-six-containers "+
			"guidance does not apply to it.", q.FixedShape)
	}
	return b.String()
}

// Critique names what is weak about the plan, or "" when nothing is.
//
// Each line is a specific fact with the action it implies. A critique that says
// "consider adding more detail" is one a model agrees with and ignores.
func (q PlanQuality) Critique() []string { return q.CritiqueFor(expectation{}) }

// CritiqueForIntent is the critique the MODEL was actually given for a request.
//
// Critique() takes no expectation, which makes it the wrong thing to report:
// the eval harness printed "4 changes is a sketch, not a structure" against a
// reporting run that was never told any such thing, and sent a whole
// investigation after a critique that had not fired.
//
// Exported because the harness lives in another package and reporting what the
// model saw is the entire point of a harness.
func (q PlanQuality) CritiqueForIntent(intent string) []string {
	return q.CritiqueFor(expectationOf(intent))
}

// CritiqueFor is Critique, told what kind of answer the request wanted.
//
// The size floor is nonsense for a question: "what is missing from this plan?"
// answered in one note is a GOOD answer, and telling it off for using 1% of the
// budget is the kind of noise that teaches people to ignore the whole check.
func (q PlanQuality) CritiqueFor(e expectation) []string {
	var out []string

	if q.Empty > 0 {
		out = append(out, fmt.Sprintf(
			"%d container(s) hold nothing. Naming a category is the easy half; "+
				"deciding what belongs in it is the half that was asked for.", q.Empty))
	}

	// Structure with no substance. The exact shape of the run that produced six
	// column headings and called it an answer.
	if q.Structure > 0 && q.Content == 0 && q.Reused == 0 {
		out = append(out, "This plan creates shelves and nothing to put on them. "+
			"Not one card of content, and nothing existing filed anywhere.")
	}

	if q.built() && q.Actions < thinFloor && !e.Reporting && !q.WholeInOne {
		out = append(out, fmt.Sprintf(
			"%d changes is a sketch, not a structure. You are allowed %d and have used "+
				"%d%% of them.", q.Actions, q.Budget, q.percent()))
	}

	// A wide, flat structure is a list of labels wearing a structure's clothes —
	// unless it is a document whose shape is not this product's to have an
	// opinion about.
	if q.Containers >= 3 && q.Content > 0 && q.Content < q.Containers && q.FixedShape == "" {
		out = append(out, fmt.Sprintf(
			"%d containers between %d cards — most of them are close to empty. "+
				"Either fewer groups, or more of what goes in them.", q.Containers, q.Content))
	}

	return out
}

// built reports whether the plan makes anything, as opposed to only editing
// what is there. "Fix the spelling" is a complete answer in one action, and a
// floor applied to it would be nonsense.
func (q PlanQuality) built() bool { return q.Structure > 0 || q.Content > 0 }

func (q PlanQuality) percent() int {
	if q.Budget <= 0 {
		return 0
	}
	return q.Actions * 100 / q.Budget
}
