package agent

import (
	"fmt"
	"sort"
	"strings"
)

// Two shapes, and letting the person pick.
//
// The prompt's own worked example — one column per scene versus Act I/II/III —
// is a decision with two defensible answers, and the system's answer was to pick
// one before it had seen either. The person then reviewed a finished forty-action
// structure whose only alternative was "discard and retype".
//
// `ask` cannot cover this. It must fire BEFORE anything is staged, so the model
// would have to describe two shapes it has not built, in words, to somebody who
// has not seen them — the least informative moment in the run. And a whiteboard
// is the one medium where two alternatives can be judged in two seconds.
//
// One run, one digest, K compiled plans. The reset primitive already exists as
// `undo_staged`; this is the same operation applied to the whole staged list,
// with the snapshot kept.

// maxVariants caps how many shapes one run may offer.
//
// Three, because the review surface is a segmented control and a fourth option
// turns a two-second judgement into a comparison exercise — which is the cost
// this feature exists to avoid, reintroduced at the review.
const maxVariants = 3

// variantFloor is the plan size below which alternatives are not worth their
// cost. Twelve actions is roughly "a structure" rather than "a few cards": below
// it there is rarely a second defensible shape, and offering one anyway teaches
// people to click through the picker without looking.
const variantFloor = 12

// Variant is one complete alternative shape the run built.
//
// Actions is a whole plan's worth, independently laid out and ordered —
// LayoutPlan, LabelDestinations and OrderPlan are each a pass over one action
// slice, so they parameterize rather than change.
type Variant struct {
	// Label is the model's short name for the shape — "one column per scene".
	Label string `bson:"label" json:"label"`
	// Rationale is why somebody would choose this one.
	Rationale string   `bson:"rationale,omitempty" json:"rationale,omitempty"`
	Actions   []Action `bson:"actions" json:"actions"`
	// Measured is the SERVER's one-line statement of fact about this shape —
	// "6 containers, 2 of them empty" — not the model's account of its own work.
	//
	// The difference decides whether the picker informs or sells: a model asked
	// to describe two things it authored will describe both favourably, and the
	// person is choosing between two pieces of advocacy.
	Measured string `bson:"measured,omitempty" json:"measured,omitempty"`
}

// SnapshotVariant lifts the currently staged actions into a variant and clears
// staging for the next pass.
//
// Ids are NOT re-minted across variants: ActionID is derived from the run and
// the sequence, so variant B's third action carries the same id variant A's
// third action did. That is correct and deliberate — exactly one variant is ever
// applied, so the ids never coexist, and a retried apply of the chosen variant
// still collides on its own id rather than creating a duplicate.
func (s *staging) SnapshotVariant(label, rationale string) (int, error) {
	if len(s.plan.Actions) == 0 {
		return 0, fmt.Errorf("there is nothing staged to keep as an alternative")
	}
	if len(s.plan.Actions) < variantFloor {
		return 0, fmt.Errorf(
			"alternatives are for STRUCTURAL decisions — a second shape is only worth a "+
				"person's attention when the first one is a structure. This plan has %d changes; "+
				"below %d, propose the one you think is right and say why in your summary",
			len(s.plan.Actions), variantFloor)
	}
	if len(s.plan.Variants) >= maxVariants {
		return 0, fmt.Errorf("%d alternatives is already more than anybody compares at a glance; "+
			"finish with the ones you have", maxVariants)
	}
	s.plan.Variants = append(s.plan.Variants, Variant{
		Label:     truncate(sanitizeText(label), 60),
		Rationale: truncate(sanitizeBody(rationale), 300),
		Actions:   append([]Action(nil), s.plan.Actions...),
	})
	// A full reset, matching undo_staged's semantics over the whole list: the
	// next pass is a fresh shape, not an edit of the last one.
	s.plan.Actions = nil
	s.created = map[string]ActionKind{}
	s.placedThisRun = map[string]bool{}
	s.movedThisRun = map[string]bool{}
	return len(s.plan.Variants), nil
}

// SealVariants folds the still-staged actions in as the final variant and makes
// the FIRST one the plan proper.
//
// Every consumer downstream — the compiler, the fingerprint, the review list,
// revert — reads Plan.Actions, and none of them needs to know this run offered a
// choice. Variants[0] is what they get; the alternatives ride alongside as data
// the review surface may offer and nothing else has to understand.
func SealVariants(p *Plan) {
	if p == nil || len(p.Variants) == 0 {
		return
	}
	if len(p.Actions) > 0 {
		p.Variants = append(p.Variants, Variant{
			Label:   "alternative " + fmt.Sprint(len(p.Variants)+1),
			Actions: append([]Action(nil), p.Actions...),
		})
	}
	// One shape is not a choice. A run that snapshotted once and then staged
	// nothing has produced exactly one plan, and offering a picker over it would
	// be a control that does nothing.
	if len(p.Variants) < 2 {
		if len(p.Actions) == 0 && len(p.Variants) == 1 {
			p.Actions = p.Variants[0].Actions
		}
		p.Variants = nil
		return
	}
	p.Actions = append([]Action(nil), p.Variants[0].Actions...)
}

// MeasureVariants states the difference between the shapes as FACT.
//
// "4 columns, 2 of them empty" versus "6 columns, none empty" is the server
// speaking. The model describing its own two shapes would describe both
// favourably — it authored them — and a picker over two pieces of advocacy is
// worse than no picker, because it looks like information.
func MeasureVariants(p *Plan, scope *BoardScope, budget Budget) {
	if p == nil {
		return
	}
	for i := range p.Variants {
		v := &p.Variants[i]
		sub := &Plan{Actions: v.Actions, RunID: p.RunID}
		q := MeasurePlan(sub, scope, budget)
		parts := []string{fmt.Sprintf("%d changes", len(v.Actions))}
		if q.Containers > 0 {
			line := fmt.Sprintf("%d container(s)", q.Containers)
			if q.Empty > 0 {
				line += fmt.Sprintf(", %d empty", q.Empty)
			} else {
				line += ", none empty"
			}
			parts = append(parts, line)
		}
		if q.Content > 0 {
			parts = append(parts, fmt.Sprintf("%d card(s) of content", q.Content))
		}
		if q.Reused > 0 {
			parts = append(parts, fmt.Sprintf("%d existing element(s) refiled", q.Reused))
		}
		v.Measured = strings.Join(parts, " · ")
	}
}

// ---------------------------------------------------------------------------
// LP13 — the disagreement IS the question
// ---------------------------------------------------------------------------

// VariantDisagreement is a typed distance over compiled plans.
//
// W7 shipped a word count, and word count is not ambiguity: "complete" with a
// clear previous run is unambiguous, and "reorganise this board the way we
// discussed last week" is fourteen words and hopeless. Two shapes the same run
// built, compared field by field, are a real uncertainty signal — and they are
// free, because the shapes already exist.
//
// Computed server-side from compiled artifacts rather than by asking the model
// to introspect. That is the same division of labour as MEASURED: the model
// builds, the server states facts about what was built.
type VariantDisagreement struct {
	// SameDestinations is true when both shapes file into the same containers.
	SameDestinations bool
	// SameContainerCount is true when both build the same number of containers.
	SameContainerCount bool
	// SameVerbs is true when both use the same set of action kinds.
	SameVerbs bool
}

// Concentrated reports that the shapes agree about everything except ONE thing —
// which is exactly the case where a question has a good answer, because the
// person is being asked to decide one thing rather than to arbitrate.
func (d VariantDisagreement) Concentrated() bool {
	agree := 0
	for _, ok := range []bool{d.SameDestinations, d.SameContainerCount, d.SameVerbs} {
		if ok {
			agree++
		}
	}
	return agree == 2
}

// Agree reports that the shapes are the same answer in different words, in which
// case the run proceeds silently and the picker is not offered.
func (d VariantDisagreement) Agree() bool {
	return d.SameDestinations && d.SameContainerCount && d.SameVerbs
}

// CompareVariants measures how far apart two shapes are.
func CompareVariants(a, b Variant) VariantDisagreement {
	return VariantDisagreement{
		SameDestinations:   sameSet(destinationsOf(a), destinationsOf(b)),
		SameContainerCount: containerCount(a) == containerCount(b),
		SameVerbs:          sameSet(verbsOf(a), verbsOf(b)),
	}
}

func destinationsOf(v Variant) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range v.Actions {
		if a.ParentID == "" || seen[a.ParentID] {
			continue
		}
		seen[a.ParentID] = true
		out = append(out, a.ParentID)
	}
	sort.Strings(out)
	return out
}

func verbsOf(v Variant) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range v.Actions {
		if seen[string(a.Kind)] {
			continue
		}
		seen[string(a.Kind)] = true
		out = append(out, string(a.Kind))
	}
	sort.Strings(out)
	return out
}

func containerCount(v Variant) int {
	n := 0
	for _, a := range v.Actions {
		if a.Kind.Container() {
			n++
		}
	}
	return n
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// AskWhichShape turns a concentrated disagreement into the one question worth
// asking, with the shapes themselves as the options.
//
// The person picks a STRUCTURE rather than answering an abstraction — which is
// the whole reason this waited for CG14 rather than shipping as another
// heuristic over the request's wording. Silence when the shapes agree: two
// phrasings of one answer is not a decision, and asking about it is the noise
// that teaches people to ignore the question card.
func AskWhichShape(p *Plan) *Question {
	if p == nil || len(p.Variants) < 2 {
		return nil
	}
	d := CompareVariants(p.Variants[0], p.Variants[1])
	if d.Agree() {
		return nil
	}
	var subject string
	switch {
	case !d.SameContainerCount && d.SameDestinations && d.SameVerbs:
		subject = "how coarsely to group this"
	case !d.SameDestinations && d.SameContainerCount && d.SameVerbs:
		subject = "where this material belongs"
	case !d.SameVerbs && d.SameDestinations && d.SameContainerCount:
		subject = "whether to restructure or to rearrange"
	default:
		// Disagreement everywhere is not one decision, and pretending it is
		// produces a question whose options do not answer it. W7's prose ask is
		// the honest fallback and it already exists.
		return nil
	}
	options := make([]string, 0, len(p.Variants))
	for _, v := range p.Variants {
		label := v.Label
		if label == "" {
			label = fmt.Sprintf("%d changes", len(v.Actions))
		}
		if v.Measured != "" {
			label += " — " + v.Measured
		}
		options = append(options, truncate(label, 120))
	}
	return &Question{
		Text:    "There are two defensible shapes here and they differ on " + subject + ". Which do you want?",
		Options: options,
	}
}
