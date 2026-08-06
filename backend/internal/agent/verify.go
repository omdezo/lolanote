package agent

import (
	"context"
	"fmt"
	"strings"
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
	//
	// The words come from ErrDestructiveNeedsReview rather than being retyped
	// here. That sentinel shipped declared and referenced nowhere while this
	// site — the one place in the codebase that actually detects the condition
	// it names — carried its own paraphrase, so the refusal existed twice and
	// the vocabulary entry existed for nothing.
	if p.Destructive() && task.Autonomy != AutonomyPreview {
		v.Fail("consequence.reviewed", ErrDestructiveNeedsReview.Error(), true)
	} else {
		v.Pass("consequence.reviewed")
	}

	// Scope containment and referential integrity. Every id is either an
	// element the server itself compiled, or one this plan creates earlier in
	// its own sequence. This is what makes a forged adjustment or an injected
	// id inert.
	created := map[string]ActionKind{}
	scopeOK, refOK, shapeOK := true, true, true
	templateOK, ruleOK, archivedOK := true, true, true
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

		// A parent that EXISTS is not automatically a parent that may hold this.
		// The tool boundary refuses these while the model can still act; this is
		// the gate that stops one reaching the board if it ever gets past there.
		// It shipped without: six columns went inside three other columns, and
		// six cards were filed into an arrangement the canvas cannot draw.
		if child := childTypeOf(a, scope); child != domain.TypeUnknown {
			if parent := parentTypeOf(a, created, scope); !CanHold(parent, child) {
				v.Fail("containment.legal", fmt.Sprintf(
					"action %d puts a %s inside a %s, which this board cannot show",
					a.Seq, child, parent), true)
				refOK = false
			}
		}

		if err := shapeOf(a); err != nil {
			v.Fail("action.shape", fmt.Sprintf("action %d: %v", a.Seq, err), true)
			shapeOK = false
		}

		// Writes into a stencil, and corrections the person already made. Both
		// are refused at the tool boundary while the model can still act on
		// them; this is the gate that stops one reaching the board if it ever
		// gets past there — including through an adjustment, which is the one
		// path that adds a parent nothing refused at staging time.
		if s := templateTargetOf(a, scope); s != "" {
			v.Fail("template.untouched", fmt.Sprintf("action %d writes into template %s", a.Seq, s), true)
			templateOK = false
		}
		// JN17's write refusal, at the same second gate and for the same
		// reason: an adjustment can hand a plan a parent that staging never
		// saw, and reorganising a wrapped production destroys the record of
		// how it was actually run.
		if s := archivedTargetOf(a, scope); s != "" {
			v.Fail("archived.untouched", fmt.Sprintf("action %d writes into archived board %s", a.Seq, s), true)
			archivedOK = false
		}
		for _, rule := range scope.LearnedRules {
			if rule.Matches(a) {
				v.Fail("memory.respected", fmt.Sprintf("action %d: %s", a.Seq, rule.Refusal()), true)
				ruleOK = false
				break
			}
		}
	}
	if archivedOK {
		v.Pass("archived.untouched")
	}
	if templateOK {
		v.Pass("template.untouched")
	}
	if ruleOK {
		v.Pass("memory.respected")
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
//
// The per-kind rules live on the ActionSpec registry, so validation cannot drift
// from compilation the way it did when both were hand-written switches. An
// unregistered kind is refused rather than defaulted: a kind nothing knows how
// to check is one nothing knows how to compile either.
func shapeOf(a Action) error {
	spec, ok := SpecFor(a.Kind)
	if !ok {
		return fmt.Errorf("unknown action %q", a.Kind)
	}
	if spec.Validate == nil {
		return nil
	}
	return spec.Validate(a)
}

// plannedDepth measures how deep the plan's own containment chains go.
func plannedDepth(p *Plan, scope *BoardScope) int {
	depth := map[string]int{}
	if scope != nil && scope.Board != nil {
		depth[scope.Board.ID] = 0
	}
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
		// Membership entries are checked by the caller against a freshly
		// compiled scope, not by reading an element that does not exist. There
		// is one per destination container now, so this is a prefix test rather
		// than an equality test against a single sentinel key.
		//
		// The prefix is the BARE word, without the separator, so it also catches
		// the single "__members" entry plans proposed before the partition
		// landed still carry. Matching only "__members:" would send that key
		// down to elements.Get, which cannot find it, and every plan left open
		// across the deploy would come back stale with a sentinel in its list of
		// changed elements.
		if strings.HasPrefix(id, membershipPrefix) {
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

// fingerprintFor narrows the original plan's fingerprint to what the adjusted
// plan still touches.
//
// The membership entry always survives: the board gaining or losing items
// invalidates any plan, whichever rows the person kept. Everything else is
// dropped along with the action that named it, which is what lets somebody
// remove the one row that went stale and apply the rest — before this, a single
// element moving under a plan made every subsequent Apply fail identically and
// the only exit was to discard the whole run.
func fingerprintFor(original, effective *Plan) map[string]string {
	if original == nil || len(original.Fingerprint) == 0 || effective == nil {
		return nil
	}
	keep := map[string]bool{}
	for _, id := range effective.TargetIDs() {
		keep[id] = true
	}
	out := make(map[string]string, len(keep)+1)
	for id, want := range original.Fingerprint {
		if keep[id] || strings.HasPrefix(id, membershipPrefix) {
			out[id] = want
		}
	}
	return out
}

// CheckMembership reports whether any container this plan writes into gained or
// lost elements since the plan was made. Separate from CheckFingerprint because
// it compares against a recompiled scope rather than against individual
// elements.
//
// Per destination, not per workspace: a create or delete somewhere else in the
// subtree cannot orphan a card inside a grouping this plan never touches, and
// treating it as staleness made a thirty-action plan on a shared board nearly
// impossible to apply.
func CheckMembership(p *Plan, fresh *BoardScope) bool {
	if p == nil || fresh == nil {
		return true
	}
	for key, want := range p.Fingerprint {
		if !strings.HasPrefix(key, membershipKey) {
			continue
		}
		parent := strings.TrimPrefix(key, membershipKey)
		// A destination this plan creates has no live child set yet, and a
		// container read at planning time but elided from the recompiled scope
		// would otherwise read as "emptied". Only a container present in both
		// compilations is comparable; anything else is checked per-element.
		if got, ok := fresh.MemberSets[parent]; ok && got != want {
			return false
		}
	}
	return true
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
			// Action.Type, not Kind.ElementType: place_file's output depends on
			// the attachment, and checking the kind's default would fail every
			// correctly-compiled FILE card.
			if el.Type != a.Type() {
				v.Fail("created.exist", fmt.Sprintf("%s is a %s, not a %s", a.ElementID, el.Type, a.Type()), true)
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

// ApplyAdjustmentsDetailed folds the human's typed edits into a plan and returns
// the effective plan, resequenced, alongside the PROVENANCE of every dropped row.
//
// Every adjustment is addressed by an action's position in THIS plan, so an
// adjustment can only rearrange what was already proposed — it cannot introduce
// anything. Dropping a container also drops whatever the plan put inside it,
// because the children would otherwise have no parent.
//
// The provenance return is what stops the correction miner learning a lie. The
// cascade used to write into the same `dropped` map as the person's own click,
// so ten missing actions came out of one gesture and nothing downstream could
// tell which one the human had actually made. It also makes the review list
// honest: dropping a container silently removed its nine children with no row
// saying so, which is why a reviewer's drop was a bigger act than they knew.
func ApplyAdjustmentsDetailed(p *Plan, adjustments []Adjustment, scope *BoardScope) (*Plan, []DropRecord) {
	dropped := map[int]bool{}
	// killedBy records which explicit drop each cascaded row died with, walking
	// up through intermediate cascades so a grandchild is attributed to the
	// click rather than to its parent row.
	killedBy := map[int]int{}
	retitle := map[int]string{}
	retext := map[int]string{}

	reparent := map[int]string{}
	for _, adj := range adjustments {
		if adj.Seq < 0 || adj.Seq >= len(p.Actions) {
			continue
		}
		switch adj.Kind {
		case AdjustReparent:
			// Containment, exactly as for a model-supplied parent: a person may
			// redirect a change to somewhere on this board, and nowhere else.
			// The id arrives from a client and is therefore no more trusted
			// than one the model proposed.
			if dest, ok := resolveDestination(adj.Value, p, scope); ok {
				reparent[adj.Seq] = dest
			}
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
	//
	// Computed against the EFFECTIVE parent, not the one the model proposed. A
	// person can now redirect a card into a column and then drop that column in
	// the same review, and reading the original parent here left the card
	// destined for a container that was no longer going to exist.
	parentOf := func(i int) string {
		if v, ok := reparent[i]; ok {
			return v
		}
		return p.Actions[i].ParentID
	}
	deadParents := map[string]int{} // created id -> the seq whose removal killed it
	for seq := range dropped {
		for _, id := range p.Actions[seq].CreatedIDs() {
			deadParents[id] = seq
		}
	}
	for changed := true; changed; {
		changed = false
		for i, a := range p.Actions {
			if dropped[i] {
				continue
			}
			killer, dead := deadParents[parentOf(i)]
			if !dead {
				continue
			}
			dropped[i] = true
			// Attribute to the CLICK, not to the intermediate row: a card inside
			// a column inside a dropped board is one decision deep, however many
			// levels of plan sit between them.
			if root, ok := killedBy[killer]; ok {
				killer = root
			}
			killedBy[i] = killer
			for _, id := range a.CreatedIDs() {
				deadParents[id] = i
			}
			changed = true
		}
	}

	drops := make([]DropRecord, 0, len(dropped))
	for i := range p.Actions {
		if !dropped[i] {
			continue
		}
		rec := DropRecord{Seq: i, Kind: p.Actions[i].Kind, Cause: DropExplicit, ParentSeq: -1}
		if parent, cascaded := killedBy[i]; cascaded {
			rec.Cause, rec.ParentSeq = DropCascade, parent
		}
		drops = append(drops, rec)
	}

	// Everything about the plan EXCEPT its actions survives an adjustment, and
	// it survives by being copied wholesale rather than field by field.
	//
	// This was an enumerated literal, and enumerations rot. It had already lost
	// NewLabels once — apply then compiled ops referencing labels that were
	// never created. Still missing when that was fixed: Quarantined (an adjusted
	// plan would forget that board content had tried to steer the run, and could
	// then auto-apply), Destinations (apply would not know to check the human's
	// rights on the other board), and Question. Copying the struct means the
	// next field added is carried without anyone remembering to come here.
	copied := *p
	out := &copied
	out.Actions = nil
	for i, a := range p.Actions {
		if dropped[i] {
			continue
		}
		if v, ok := retitle[i]; ok {
			a.Title = v
			a.Summary = "Renamed to “" + v + "”"
		}
		if v, ok := reparent[i]; ok {
			a.ParentID = v
			// A redirected element loses any canvas coordinate the layout pass
			// gave it: the two answers to "where" are mutually exclusive, and
			// LayoutPlan below recomputes whichever one still applies.
			a.Position = nil
			if v == scope.Board.ID {
				a.Section = string(domain.SectionCanvas)
			} else {
				a.Section = ""
			}
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
	LabelDestinations(out, scope)
	OrderPlan(out, scope)
	out.EnsureShape()
	return out, drops
}

// resolveDestination validates a human-chosen parent for a plan action.
//
// The same containment rule the model is held to: a destination is either an
// element the server itself compiled into scope, or a container this plan
// creates earlier in its own sequence, or the board itself. An id from anywhere
// else is inert — a person's client is not more trusted than the model, because
// the request that carries it is equally forgeable.
func resolveDestination(id string, p *Plan, scope *BoardScope) (string, bool) {
	if id == "" || scope == nil || scope.Board == nil {
		return "", false
	}
	if id == scope.Board.ID {
		return id, true
	}
	for _, a := range p.Actions {
		for _, made := range a.CreatedIDs() {
			if made != id {
				continue
			}
			if !a.Kind.Container() {
				return "", false // a note cannot hold a card
			}
			return id, true
		}
	}
	el, ok := scope.Elements[id]
	if !ok || !el.Type.IsContainer() {
		return "", false
	}
	return id, true
}
