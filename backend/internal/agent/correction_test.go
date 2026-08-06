package agent

import (
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
)

// The correction record is the only supervision signal the product can honestly
// learn from, and until now it was destroyed by the act of using it.

func correctionScope() *BoardScope {
	return &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{
			"col-keep": {ID: "col-keep", Type: domain.TypeColumn,
				Content:  domain.Content{"title": "Research"},
				Location: domain.Location{ParentID: "b1"}},
		},
	}
}

// One click on a container removes the container AND everything the plan put
// inside it. Counting those children as separate human decisions is how a rule
// miner infers a durable preference from a single gesture — the most common
// correction shape in the product carrying the largest amplification error.
func TestCorrections_ACascadeIsNotADecision(t *testing.T) {
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", Title: "Ideas", ParentID: "b1"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", Text: "one", ParentID: "c1"},
		{Seq: 2, Kind: ActCreateNote, ElementID: "n2", Text: "two", ParentID: "c1"},
		{Seq: 3, Kind: ActCreateNote, ElementID: "n3", Text: "three", ParentID: "c1"},
		{Seq: 4, Kind: ActCreateColumn, ElementID: "c2", Title: "Keep", ParentID: "b1"},
	}}
	adjustments := []Adjustment{{Kind: AdjustDrop, Seq: 0}}

	effective, drops := ApplyAdjustmentsDetailed(plan, adjustments, correctionScope())
	if len(effective.Actions) != 1 {
		t.Fatalf("the cascade did not run: %d actions survived, want 1", len(effective.Actions))
	}
	if len(drops) != 4 {
		t.Fatalf("drop provenance covers %d rows, want 4", len(drops))
	}
	explicit := 0
	for _, d := range drops {
		if d.Cause == DropExplicit {
			explicit++
			continue
		}
		if d.ParentSeq != 0 {
			t.Errorf("cascade of action %d blamed on seq %d, want the click at 0", d.Seq, d.ParentSeq)
		}
	}
	if explicit != 1 {
		t.Fatalf("%d rows recorded as decisions the person made, want 1", explicit)
	}

	got := DiffCorrections(plan, adjustments, drops, OutcomeApplied, time.Now())
	if len(got) != 1 {
		t.Fatalf("one click produced %d correction records — a miner would learn a 4x lie:\n%+v",
			len(got), got)
	}
	if got[0].Children != 3 {
		t.Errorf("the drop's real blast radius is missing: children = %d, want 3", got[0].Children)
	}
	if got[0].Target != "ideas" {
		t.Errorf("target = %q, want the normalized title so two runs' corrections collide", got[0].Target)
	}
}

// A grandchild dies with the click, not with the row between them.
func TestCorrections_CascadeAttributesToTheClickNotTheIntermediateRow(t *testing.T) {
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateBoard, ElementID: "bd", Title: "Season 2", ParentID: "b1"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c1", Title: "Ideas", ParentID: "bd"},
		{Seq: 2, Kind: ActCreateNote, ElementID: "n1", Text: "one", ParentID: "c1"},
	}}
	_, drops := ApplyAdjustmentsDetailed(plan, []Adjustment{{Kind: AdjustDrop, Seq: 0}}, correctionScope())
	for _, d := range drops {
		if d.Cause == DropCascade && d.ParentSeq != 0 {
			t.Errorf("action %d blamed on seq %d; the person clicked seq 0", d.Seq, d.ParentSeq)
		}
	}
}

// Every adjustment kind must survive as a label, with the value the person put
// in place — a retitle that recorded no new title teaches nothing.
func TestCorrections_EveryAdjustmentKindBecomesALabel(t *testing.T) {
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", Title: "Ideas", ParentID: "b1"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", Text: "budget", ParentID: "b1"},
		{Seq: 2, Kind: ActMove, ElementID: "col-keep", Title: "Research", ParentID: "b1"},
	}}
	adjustments := []Adjustment{
		{Kind: AdjustRetitle, Seq: 0, Value: "Backlog"},
		{Kind: AdjustRetext, Seq: 1, Value: "the budget"},
		{Kind: AdjustReparent, Seq: 2, Value: "b1"},
	}
	_, drops := ApplyAdjustmentsDetailed(plan, adjustments, correctionScope())
	got := DiffCorrections(plan, adjustments, drops, OutcomeRefined, time.Now())
	if len(got) != 3 {
		t.Fatalf("got %d corrections, want one per adjustment:\n%+v", len(got), got)
	}
	byKind := map[CorrectionKind]Correction{}
	for _, c := range got {
		byKind[c.Kind] = c
		if c.Outcome != OutcomeRefined {
			t.Errorf("%s carries outcome %q — the weight of the label is the outcome", c.Kind, c.Outcome)
		}
	}
	if byKind[CorrectRetitle].Value != "Backlog" {
		t.Errorf("the retitle recorded no replacement: %+v", byKind[CorrectRetitle])
	}
	if byKind[CorrectReparent].ElementID != "col-keep" {
		t.Errorf("an edit's correction lost the element it was aimed at: %+v", byKind[CorrectReparent])
	}
}

// The whole point of splitting the field: applying corrections must not destroy
// the proposal they were made against.
func TestRun_TheProposalSurvivesItsOwnCorrection(t *testing.T) {
	proposed := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", Title: "Ideas", ParentID: "b1"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c2", Title: "Keep", ParentID: "b1"},
	}}
	run := &Run{ID: "r1", Plan: proposed}
	run.FreezeProposal()

	adjustments := []Adjustment{{Kind: AdjustDrop, Seq: 0}}
	effective, drops := ApplyAdjustmentsDetailed(run.Plan, adjustments, correctionScope())
	run.RecordCorrections(adjustments, drops, OutcomeApplied, time.Now())
	run.Plan = effective // exactly what commit does

	if run.ProposedPlan == nil || len(run.ProposedPlan.Actions) != 2 {
		t.Fatalf("the plan the person reviewed was destroyed by applying their own edits: %+v",
			run.ProposedPlan)
	}
	if len(run.Plan.Actions) != 1 {
		t.Fatalf("effective plan = %d actions, want 1", len(run.Plan.Actions))
	}
	if len(run.Corrections) != 1 || run.Corrections[0].Target != "ideas" {
		t.Fatalf("the correction was not recorded on the run: %+v", run.Corrections)
	}
}

// PROPOSED is reachable more than once — the refine edge and the apply-retry
// edge both return there. A re-freeze would quietly adopt the CORRECTED plan as
// the thing originally proposed, which is the same data loss with extra steps.
func TestRun_FreezingIsIdempotent(t *testing.T) {
	run := &Run{ID: "r1", Plan: &Plan{Actions: []Action{{Seq: 0, Kind: ActCreateColumn, Title: "Ideas"}}}}
	run.FreezeProposal()
	run.Plan = &Plan{Actions: []Action{{Seq: 0, Kind: ActCreateColumn, Title: "Something else"}}}
	run.FreezeProposal()
	if run.ProposedPlan.Actions[0].Title != "Ideas" {
		t.Errorf("the second freeze overwrote the original proposal with the corrected one")
	}
}

// Re-asking is the operational definition of a capability that does not work,
// and it is detectable only if two phrasings of one request collide.
func TestIntentKey_TwoPhrasingsOfOneRequestCollide(t *testing.T) {
	a := IntentKey("Please tidy up the board")
	b := IntentKey("tidy the board up!")
	if a == "" {
		t.Fatal("a real request produced no key, so no re-ask can ever be detected")
	}
	if a != b {
		t.Errorf("two phrasings of one request hashed differently (%s vs %s) — "+
			"re-ask clustering would report zero", a, b)
	}
	if same := IntentKey("draw the budget table"); same == a {
		t.Errorf("two different requests collided, which would report re-asks that never happened")
	}
	// Content-free by construction: the key must not be reversible into words.
	if strings.Contains(a, "tidy") || strings.Contains(a, "board") {
		t.Errorf("the key carries the request text (%q), so retaining it is retaining content", a)
	}
}

// A one-content-word intent groups every "organise" ever typed, so it earns no
// key at all.
func TestIntentKey_TooShortToClusterOn(t *testing.T) {
	if k := IntentKey("organise"); k != "" {
		t.Errorf("a single word produced key %q — every such run would cluster together", k)
	}
}
