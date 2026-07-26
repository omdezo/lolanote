package agent

import (
	"context"
	"fmt"
	"time"

	"qomranote/backend/internal/domain"
)

// Verification: completion is decided from re-read environment state, never
// from the model's claim that it finished.
//
// Two phases. Preconditions is a PURE function over the plan and the compiled
// scope, run before anything commits — it catches essentially every failure at
// zero cost and with nothing to undo. Postconditions re-reads the subtree
// afterwards and, if reality disagrees, the run auto-reverts and ends FAILED
// with the board restored — never PARTIAL.
//
// Both are written against ACTION KINDS rather than against one workload, so a
// capability added to the tool registry is verified the day it ships instead of
// silently escaping the checks.

const sectionUnsorted = domain.SectionUnsorted

// Preconditions checks a plan against the scope and budget before commit.
func Preconditions(p *Plan, scope *BoardScope, task TaskSpec) Verdict {
	var v Verdict

	if len(p.Actions) == 0 {
		v.Fail("plan.present", "the plan is empty", true)
		v.Settle()
		return v
	}
	v.Pass("plan.present")

	if len(p.Actions) > task.Budget.MaxActions {
		v.Fail("budget.actions", fmt.Sprintf("%d changes exceeds the limit of %d", len(p.Actions), task.Budget.MaxActions), true)
	} else {
		v.Pass("budget.actions")
	}

	// An unattended run must not destroy anything. The capability is withheld
	// from the tool catalogue in that mode, so reaching here means something
	// upstream went wrong — check anyway.
	if p.Destructive() && task.Autonomy != AutonomyPreview {
		v.Fail("consequence.reviewed", "a deletion cannot be applied without review", true)
	} else {
		v.Pass("consequence.reviewed")
	}

	// Scope containment and referential integrity. Every id is either an
	// element the server itself compiled, or one this plan creates earlier in
	// its own sequence. This is what makes a forged adjustment or an injected
	// id inert.
	created := map[string]ActionKind{}
	scopeOK, refOK, shapeOK := true, true, true
	for i, a := range p.Actions {
		if a.Seq != i {
			v.Fail("plan.ordered", "actions are not in sequence", true)
			shapeOK = false
		}
		if a.Kind.Creates() {
			if a.ElementID == "" {
				v.Fail("action.shape", fmt.Sprintf("action %d has no element id", a.Seq), true)
				shapeOK = false
			}
			created[a.ElementID] = a.Kind
		} else if _, ok := scope.Elements[a.ElementID]; !ok {
			v.Fail("scope.containment", fmt.Sprintf("action %d targets %s, which is not on this board", a.Seq, a.ElementID), true)
			scopeOK = false
		}

		if a.ParentID != "" && a.ParentID != scope.Board.ID {
			if _, staged := created[a.ParentID]; !staged {
				if _, exists := scope.Elements[a.ParentID]; !exists {
					v.Fail("parents.resolve", fmt.Sprintf("action %d parents to %s, which does not exist", a.Seq, a.ParentID), true)
					refOK = false
				}
			}
		}

		if err := shapeOf(a); err != nil {
			v.Fail("action.shape", fmt.Sprintf("action %d: %v", a.Seq, err), true)
			shapeOK = false
		}
	}
	if scopeOK {
		v.Pass("scope.containment")
	}
	if refOK {
		v.Pass("parents.resolve")
	}
	if shapeOK {
		v.Pass("plan.ordered")
		v.Pass("action.shape")
	}

	// Self-contradiction. Placing an element on the canvas and also filing it
	// into a container are mutually exclusive: whichever op lands second wins,
	// so the other was a wasted change the user was asked to approve. A model
	// composing and restructuring in the same pass produces exactly this, and it
	// reads as a plan that does not know what it wants.
	placed, reparented := map[string]int{}, map[string]int{}
	for _, a := range p.Actions {
		switch a.Kind {
		case ActPlace:
			placed[a.ElementID] = a.Seq
		case ActMove:
			reparented[a.ElementID] = a.Seq
		}
	}
	contradiction := ""
	for id, ps := range placed {
		if ms, ok := reparented[id]; ok {
			contradiction = fmt.Sprintf(
				"action %d positions %s on the canvas and action %d files it into a container — pick one",
				ps, id, ms)
			break
		}
	}
	if contradiction != "" {
		v.Fail("plan.coherent", contradiction, true)
	} else {
		v.Pass("plan.coherent")
	}

	// Nesting depth: an agent that can create boards inside boards could
	// otherwise bury content arbitrarily deep in a single plan.
	if depth := plannedDepth(p, scope); depth > 3 {
		v.Fail("nesting.bounded", fmt.Sprintf("this plan nests %d levels deep", depth), true)
	} else {
		v.Pass("nesting.bounded")
	}

	v.Settle()
	return v
}

// shapeOf checks that an action carries the fields its kind requires.
func shapeOf(a Action) error {
	switch a.Kind {
	case ActCreateBoard, ActCreateColumn:
		if a.Title == "" {
			return fmt.Errorf("%s needs a title", a.Kind)
		}
	case ActCreateNote:
		if a.Text == "" {
			return fmt.Errorf("a note needs text")
		}
	case ActCreateTodo:
		if a.Title == "" || len(a.Tasks) == 0 {
			return fmt.Errorf("a to-do list needs a title and tasks")
		}
	case ActCreateLink:
		if a.URL == "" {
			return fmt.Errorf("a link needs a URL")
		}
	case ActMove:
		if a.ParentID == "" {
			return fmt.Errorf("a move needs a destination")
		}
		if a.ParentID == a.ElementID {
			return fmt.Errorf("an element cannot contain itself")
		}
	case ActRename:
		if a.Title == "" {
			return fmt.Errorf("a rename needs a title")
		}
	case ActSetText:
		if a.Text == "" {
			return fmt.Errorf("a rewrite needs text")
		}
	case ActDelete:
		if a.ElementID == "" {
			return fmt.Errorf("a deletion needs a target")
		}
	case ActApplyLabel:
		if a.ElementID == "" || a.LabelID == "" {
			return fmt.Errorf("tagging needs an element and a label")
		}
	case ActSetColor:
		// An empty colour is the "default" swatch, so only the target is
		// required — clearing a colour is a legitimate edit.
		if a.ElementID == "" {
			return fmt.Errorf("colouring needs a target")
		}
	case ActSetTask:
		if a.ElementID == "" {
			return fmt.Errorf("ticking needs a target")
		}
	case ActSetAssignee:
		if a.ElementID == "" || a.AssigneeID == "" {
			return fmt.Errorf("assigning needs a task and a person")
		}
	case ActSetReminder:
		if a.ElementID == "" || a.RemindAt == "" {
			return fmt.Errorf("a reminder needs a task and a time")
		}
	case ActResize:
		if a.ElementID == "" || a.Position == nil || a.Position.Width <= 0 {
			return fmt.Errorf("resizing needs a target and a width")
		}
	case ActCreateHeading:
		if a.Text == "" {
			return fmt.Errorf("a heading needs text")
		}
	case ActPlace:
		if a.ElementID == "" || a.Position == nil {
			return fmt.Errorf("placing needs a target and a position")
		}
	case ActComment:
		if a.Text == "" {
			return fmt.Errorf("a comment needs something to say")
		}
	case ActCloneHere:
		if a.FromID == "" || a.ParentID == "" {
			return fmt.Errorf("a clone needs a source and a destination")
		}
	case ActConnect:
		if a.FromID == "" || a.ToID == "" {
			return fmt.Errorf("a connection needs both ends")
		}
		if a.FromID == a.ToID {
			return fmt.Errorf("an element cannot be connected to itself")
		}
	case ActCreateTable:
		if len(a.Rows) < 2 {
			return fmt.Errorf("a table needs a header row and at least one data row")
		}
		width := len(a.Rows[0])
		for i, r := range a.Rows {
			if len(r) != width {
				return fmt.Errorf("table row %d has %d cells, header has %d", i, len(r), width)
			}
		}
	default:
		return fmt.Errorf("unknown action %q", a.Kind)
	}
	return nil
}

// plannedDepth measures how deep the plan's own containment chains go.
func plannedDepth(p *Plan, scope *BoardScope) int {
	depth := map[string]int{scope.Board.ID: 0}
	max := 0
	for _, a := range p.Actions {
		if !a.Kind.Creates() {
			continue
		}
		d := depth[a.ParentID] + 1
		depth[a.ElementID] = d
		if d > max {
			max = d
		}
	}
	return max
}

// CheckFingerprint reports whether every targeted element is still at the
// version the plan was computed against.
//
// It deliberately covers ONLY targeted elements: a collaborator editing an
// untouched card elsewhere on the board must not invalidate a pending preview.
func CheckFingerprint(ctx context.Context, elements domain.ElementRepository, p *Plan) ([]string, error) {
	var stale []string
	for id, want := range p.Fingerprint {
		// The membership entry is checked by the caller against a freshly
		// compiled scope, not by reading an element that does not exist.
		if id == membershipKey {
			continue
		}
		el, err := elements.Get(ctx, id)
		if err != nil {
			stale = append(stale, id)
			continue
		}
		if el.IsDeleted() || el.UpdatedAt.UTC().Format(time.RFC3339Nano) != want {
			stale = append(stale, id)
		}
	}
	sortStrings(stale)
	return stale, nil
}

// CheckMembership reports whether the board gained or lost elements since the
// plan was made. Separate from CheckFingerprint because it compares against a
// recompiled scope rather than against individual elements.
func CheckMembership(p *Plan, fresh *BoardScope) bool {
	want, ok := p.Fingerprint[membershipKey]
	if !ok || fresh == nil {
		return true // plans made before this existed are not retroactively stale
	}
	return want == fresh.membershipHash()
}

// Postconditions re-reads the board after commit and checks that reality
// matches what the plan intended.
//
// aclBefore is the board's ACL hash captured before the transaction: an agent
// run must be provably incapable of changing who can see the board, and the way
// to prove it is to compare, not to assert.
func Postconditions(ctx context.Context, elements domain.ElementRepository, p *Plan, scope *BoardScope, aclBefore string) Verdict {
	var v Verdict

	board, err := elements.Get(ctx, scope.Board.ID)
	if err != nil {
		v.Fail("board.readable", err.Error(), true)
		v.Settle()
		return v
	}
	if aclHash(board.ACL) != aclBefore {
		v.Fail("acl.unchanged", "the board's sharing settings changed during the run", true)
	} else {
		v.Pass("acl.unchanged")
	}

	createdOK, editedOK, cycleOK := true, true, true
	touched := map[string]bool{}

	for _, a := range p.Actions {
		touched[a.ElementID] = true
		el, err := elements.Get(ctx, a.ElementID)

		switch {
		case a.Kind.Creates():
			if err != nil || el.IsDeleted() {
				v.Fail("created.exist", fmt.Sprintf("%s was not created", a.Summary), true)
				createdOK = false
				continue
			}
			if el.Type != a.Kind.ElementType() {
				v.Fail("created.exist", fmt.Sprintf("%s is a %s, not a %s", a.ElementID, el.Type, a.Kind.ElementType()), true)
				createdOK = false
			}
			if el.Location.ParentID != a.ParentID {
				v.Fail("created.exist", fmt.Sprintf("%s did not land where the plan said", a.ElementID), true)
				createdOK = false
			}

		case a.Kind == ActDelete:
			if err == nil && !el.IsDeleted() {
				v.Fail("edited.applied", fmt.Sprintf("%s was not trashed", a.ElementID), true)
				editedOK = false
			}

		default:
			if err != nil || el.IsDeleted() {
				v.Fail("edited.applied", fmt.Sprintf("%s is missing after the change", a.ElementID), true)
				editedOK = false
				continue
			}
			if a.Kind == ActMove && el.Location.ParentID != a.ParentID {
				v.Fail("edited.applied", fmt.Sprintf("%s did not move", a.ElementID), true)
				editedOK = false
			}
		}

		if err == nil && el != nil && el.Location.ParentID == el.ID {
			v.Fail("no.cycles", fmt.Sprintf("%s contains itself", el.ID), true)
			cycleOK = false
		}
	}
	if createdOK {
		v.Pass("created.exist")
	}
	if editedOK {
		v.Pass("edited.applied")
	}
	if cycleOK {
		v.Pass("no.cycles")
	}

	// Nothing outside the plan may have moved. The run touched a known set;
	// anything else that changed parent is a defect, not a nuance.
	untouched := true
	for id, before := range scope.Elements {
		if touched[id] {
			continue
		}
		after, err := elements.Get(ctx, id)
		if err != nil {
			continue
		}
		if after.Location.ParentID != before.Location.ParentID {
			v.Fail("scope.untouched", fmt.Sprintf("%s moved but was not part of the plan", id), true)
			untouched = false
		}
	}
	if untouched {
		v.Pass("scope.untouched")
	}

	v.Settle()
	return v
}

// aclHash renders an ACL into a comparable string. It is a change detector, not
// a security primitive — the security property is that the agent has no
// capability to write an ACL at all; this only proves it.
func aclHash(acl *domain.ACL) string {
	if acl == nil {
		return "-"
	}
	s := acl.OwnerID + "|"
	editors := make([]string, len(acl.Editors))
	copy(editors, acl.Editors)
	sortStrings(editors)
	for _, e := range editors {
		s += e + ","
	}
	s += "|" + acl.PublicEditLink
	if acl.ViewLink != nil {
		s += "|" + acl.ViewLink.Token
	}
	return s
}

// ApplyAdjustments folds the human's typed edits into a plan and returns the
// effective plan, resequenced.
//
// Every adjustment is addressed by an action's position in THIS plan, so an
// adjustment can only rearrange what was already proposed — it cannot introduce
// anything. Dropping a container also drops whatever the plan put inside it,
// because the children would otherwise have no parent.
func ApplyAdjustments(p *Plan, adjustments []Adjustment, scope *BoardScope) *Plan {
	dropped := map[int]bool{}
	retitle := map[int]string{}
	retext := map[int]string{}

	for _, adj := range adjustments {
		if adj.Seq < 0 || adj.Seq >= len(p.Actions) {
			continue
		}
		switch adj.Kind {
		case AdjustDrop:
			dropped[adj.Seq] = true
		case AdjustRetitle:
			// A human-supplied name is sanitized on exactly the same path as a
			// model-supplied one: it is about to become board content either way.
			if v := sanitizeName(adj.Value); v != "" {
				retitle[adj.Seq] = v
			}
		case AdjustRetext:
			if v := sanitizeBody(adj.Value); v != "" {
				retext[adj.Seq] = v
			}
		}
	}

	// Cascade: an action parented to a dropped create cannot survive it.
	deadParents := map[string]bool{}
	for seq := range dropped {
		if p.Actions[seq].Kind.Creates() {
			deadParents[p.Actions[seq].ElementID] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for i, a := range p.Actions {
			if dropped[i] {
				continue
			}
			if deadParents[a.ParentID] {
				dropped[i] = true
				if a.Kind.Creates() {
					deadParents[a.ElementID] = true
				}
				changed = true
			}
		}
	}

	// NewLabels rides along: dropping an action must not silently drop the
	// vocabulary its siblings still reference, or apply would compile ops
	// pointing at labels that were never created.
	out := &Plan{
		Summary: p.Summary, Notes: p.Notes,
		Fingerprint: p.Fingerprint, NewLabels: p.NewLabels, NewComments: p.NewComments,
	}
	for i, a := range p.Actions {
		if dropped[i] {
			continue
		}
		if v, ok := retitle[i]; ok {
			a.Title = v
			a.Summary = "Renamed to “" + v + "”"
		}
		if v, ok := retext[i]; ok {
			a.Text = v
			a.Summary = "Note: " + truncate(v, 60)
		}
		// Ids stay bound to the ORIGINAL sequence so a retried apply is still
		// idempotent; only the position in the list is renumbered.
		a.Seq = len(out.Actions)
		out.Actions = append(out.Actions, a)
	}
	LayoutPlan(out, scope)
	return out
}
