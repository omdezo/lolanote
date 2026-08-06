package agent

import (
	"fmt"
	"strings"
)

// A structure with nothing in it.
//
// The failure this exists for: "set up film production" produced a board, three
// columns named Pre-Production / Production / Post-Production, and not one card
// inside any of them. Four changes, a tidy result, and no answer to the question
// — the person could have typed those three words themselves in less time than
// the run took.
//
// It is a planning failure, not a geometry one, and it has a shape the server
// can recognise: containers created, nothing put in them. Naming a category is
// the easy half of organising; deciding what belongs in it is the work.
//
// The response is graduated rather than a refusal. Creating ONE empty column is
// a legitimate request — "add a Done column" — so the check only fires on a plan
// whose containers are empty as a group. When it fires the model is made to look
// again; if it finishes hollow anyway, the person is told plainly rather than
// handed a shell and left to discover it.

// hollowContainers returns the titles of containers this plan creates and never
// fills. Empty when the plan is not hollow.
//
// A container counts as filled by anything parented to it — a card created
// there, or an element moved in. Both are content; only the total matters.
func hollowContainers(p *Plan) []string { return emptyContainers(p, minHollowContainers) }

// emptyContainers is hollowContainers with the floor as an argument.
//
// The floor exists to keep "add a Done column" out of the guard, and that is the
// right call while the run is still choosing. It is the wrong call once the run
// has been CUT: an exhausted run with exactly one container left empty knows
// precisely why it is empty, and that container is the thing the next run has
// to finish. Under the floor of two it reported nothing at all.
func emptyContainers(p *Plan, floor int) []string {
	if p == nil {
		return nil
	}
	filled := map[string]bool{}
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.ParentID == "" {
			continue
		}
		if a.Kind.Creates() || a.Kind == ActMove {
			filled[a.ParentID] = true
		}
	}

	var hollow []string
	for i := range p.Actions {
		a := &p.Actions[i]
		if !a.Kind.Creates() || !a.Kind.Container() {
			continue
		}
		if filled[a.ElementID] || carriesItsOwnContent(a) {
			continue
		}
		name := a.Title
		if name == "" {
			name = a.Summary
		}
		hollow = append(hollow, name)
	}

	// Counted across the EMPTY ones only, never as a fraction of all containers.
	//
	// The first version required every container to be empty before it would
	// complain, and that let the reported case straight through: a board holding
	// three columns is itself full, so "one container was filled, the run must
	// know what it is doing" was true — of the board, and irrelevant to the
	// three empty columns that were the entire answer. What matters is how many
	// LEAVES are empty, not whether their parent counts as occupied.
	if len(hollow) < floor {
		return nil
	}
	return hollow
}

// carriesItsOwnContent reports whether a container arrives already full.
//
// A to-do list and a table hold their items INLINE on the action rather than as
// separate child actions, so nothing is ever parented to them and the plain
// "has children" test calls a perfectly full checklist empty.
func carriesItsOwnContent(a *Action) bool {
	// A duplicate is the same shape: its whole subtree rides on the action as
	// Copies, so a copied column full of cards would otherwise read as an
	// empty column and trip the "all shelves, no contents" stop.
	return len(a.Tasks) > 0 || len(a.Rows) > 0 || len(a.Copies) > 1
}

// minHollowContainers is where "add a column" stops and "here is an empty filing
// cabinet" begins.
const minHollowContainers = 2

// hollowNudge is what the model is told when its plan is all shelves and no
// contents. Directive rather than advisory: the observation already existed in
// the self-view as a remark, and a remark is something a model finishing a turn
// reads past.
func hollowNudge(names []string) string {
	return fmt.Sprintf(
		"STOP — this plan creates %s and puts nothing in any of them.\n\n"+
			"Naming the categories is the easy half; deciding what belongs in each is the work, "+
			"and it is the half that was asked for. A person can type three column headings "+
			"faster than this run takes.\n\n"+
			"Either fill them from what is already on this board, or create the cards that "+
			"belong in each one — the real steps, deliverables or items, not more headings. "+
			"If the material genuinely does not exist yet, say so in `unmet` when you finish, "+
			"so the person is told rather than handed empty boxes.",
		strings.Join(quoteAll(names), ", "))
}

func quoteAll(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, fmt.Sprintf("%q", n))
	}
	return out
}

// discloseHollow records, for the person, that the structure arrived empty.
//
// The last line of defence. The model was told and had a turn to act; if the
// plan is still a shell, the person is told plainly rather than handed labelled
// boxes and left to notice for themselves.
func discloseHollow(p *Plan) {
	names := hollowContainers(p)
	if len(names) == 0 {
		return
	}
	addUnmet(p, Unmet{
		Request: "filling " + strings.Join(names, ", "),
		Why:     "the structure was created but nothing was put inside it",
	})
}

// addUnmet records an unmet request once.
//
// A run that was cut off with empty containers is BOTH exhausted and hollow, and
// both disclosures name the same containers. Listing them twice reads as two
// separate failures on a plan that had one, and the second entry — "the
// structure was created but nothing was put inside it" — is the weaker of the
// two explanations once the first has said the run was stopped mid-flow.
func addUnmet(p *Plan, u Unmet) {
	for _, already := range p.Unmet {
		if already.Request == u.Request {
			return
		}
	}
	p.Unmet = append(p.Unmet, u)
}

// Refusing a shell OUTRIGHT was tried and reverted.
//
// A finish-time refusal broke sixteen fixtures, and they were not artificial:
// "add two columns" is a shell and is a perfectly ordinary request. A guard
// that blocks the common legitimate case to catch an occasional bad one is a
// guard that gets switched off, and it would have refused real users.
//
// So the graduated response stands: the review turn leads with it while the
// model can still act, and discloseHollow tells the person if it ships anyway.
// Some runs will still come back thin. They will come back thin VISIBLY, which
// is the difference that matters.

// promoteSilentSummary makes a no-op run's reason reach the person.
//
// A run that changes nothing puts its reason in plan.Summary — and the outcome
// card renders "Nothing to do · No changes were needed" over the top of it,
// showing unmet and notes but never the summary. So asked to fill in budget
// figures that are not on the board, the agent declined for exactly the right
// reason and explained itself into a field nobody reads. From the person's
// side that is indistinguishable from the agent doing nothing at all.
//
// Only when there is genuinely nothing else: a run that DID something shows its
// changes, and copying the summary into unmet there would read as a failure
// report on a successful run.
func promoteSilentSummary(p *Plan, intent string) {
	if p == nil || len(p.Actions) > 0 || len(p.Unmet) > 0 || p.Question != nil {
		return
	}
	why := strings.TrimSpace(p.Summary)
	if why == "" {
		return
	}
	p.Unmet = append(p.Unmet, Unmet{Request: truncate(intent, 120), Why: why})
}
