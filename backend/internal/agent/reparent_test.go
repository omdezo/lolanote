package agent

import (
	"testing"

	"qomranote/backend/internal/domain"
)

func reparentScope() *BoardScope {
	return &BoardScope{
		Board: &domain.Element{ID: "board-1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{
			"col-a": {ID: "col-a", Type: domain.TypeColumn, Content: domain.Content{"title": "Alpha"}},
			"col-b": {ID: "col-b", Type: domain.TypeColumn, Content: domain.Content{"title": "Beta"}},
			// A card is not a container, so nothing may be filed into it.
			"card-x": {ID: "card-x", Type: domain.TypeCard},
			"card-a": {ID: "card-a", Type: domain.TypeCard},
		},
	}
}

func planFiling() *Plan {
	return &Plan{Actions: []Action{
		{Seq: 0, Kind: ActMove, ElementID: "card-a", ParentID: "col-a"},
	}}
}

// The right card in the wrong column used to be un-fixable: the only adjustment
// available was DROP, so a plan that was ninety percent correct got applied at
// ninety percent and finished by hand.
func TestReparent_SendsAnActionSomewhereElse(t *testing.T) {
	scope := reparentScope()
	out := ApplyAdjustments(planFiling(), []Adjustment{
		{Kind: AdjustReparent, Seq: 0, Value: "col-b"},
	}, scope)

	if len(out.Actions) != 1 {
		t.Fatalf("actions = %d, want the action kept, not dropped", len(out.Actions))
	}
	if out.Actions[0].ParentID != "col-b" {
		t.Errorf("parentId = %q, want col-b", out.Actions[0].ParentID)
	}
	// And the review says so, because the destination pass runs on the way out.
	if out.Actions[0].Destination != "Beta" {
		t.Errorf("destination = %q, want Beta — the row must say where it now goes",
			out.Actions[0].Destination)
	}
}

func TestReparent_CanSendSomethingBackToTheBoard(t *testing.T) {
	scope := reparentScope()
	out := ApplyAdjustments(planFiling(), []Adjustment{
		{Kind: AdjustReparent, Seq: 0, Value: "board-1"},
	}, scope)

	a := out.Actions[0]
	if a.ParentID != "board-1" {
		t.Fatalf("parentId = %q, want the board", a.ParentID)
	}
	if a.Section != string(domain.SectionCanvas) {
		t.Errorf("section = %q; something returned to the board belongs on the canvas", a.Section)
	}
	if a.Destination != "" {
		t.Errorf("destination = %q; the board is not a named destination", a.Destination)
	}
}

// Containment. The id comes from a client, so it is exactly as trustworthy as
// one the model proposed — which is to say, not at all until it is resolved
// against the scope this run compiled.
func TestReparent_RefusesDestinationsOutsideTheScope(t *testing.T) {
	scope := reparentScope()
	for _, dest := range []string{
		"ffffffffffffffffffffffff", // never seen
		"card-x",                   // real, but cannot hold anything
		"",                         // nothing
	} {
		out := ApplyAdjustments(planFiling(), []Adjustment{
			{Kind: AdjustReparent, Seq: 0, Value: dest},
		}, scope)
		if out.Actions[0].ParentID != "col-a" {
			t.Errorf("destination %q was accepted; parentId became %q",
				dest, out.Actions[0].ParentID)
		}
	}
}

// A container this same plan creates is a legal destination — it is exactly
// where a person will most often want to redirect something.
func TestReparent_AcceptsAContainerThePlanIsAboutToCreate(t *testing.T) {
	scope := reparentScope()
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "col-new", ParentID: "board-1", Title: "Later"},
		{Seq: 1, Kind: ActMove, ElementID: "card-a", ParentID: "col-a"},
	}}

	out := ApplyAdjustments(plan, []Adjustment{
		{Kind: AdjustReparent, Seq: 1, Value: "col-new"},
	}, scope)

	if out.Actions[1].ParentID != "col-new" {
		t.Fatalf("parentId = %q, want col-new", out.Actions[1].ParentID)
	}
	if out.Actions[1].Destination != "Later" {
		t.Errorf("destination = %q, want Later", out.Actions[1].Destination)
	}
}

// Redirecting into a container this plan creates, then dropping that container,
// must still cascade — otherwise the redirect strands the card in something
// that will not exist.
func TestReparent_StillCascadesWhenTheNewParentIsDropped(t *testing.T) {
	scope := reparentScope()
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "col-new", ParentID: "board-1", Title: "Later"},
		{Seq: 1, Kind: ActMove, ElementID: "card-a", ParentID: "col-a"},
	}}

	out := ApplyAdjustments(plan, []Adjustment{
		{Kind: AdjustReparent, Seq: 1, Value: "col-new"},
		{Kind: AdjustDrop, Seq: 0},
	}, scope)

	for _, a := range out.Actions {
		if a.ElementID == "card-a" {
			t.Fatalf("the card survived into a column that is no longer being created (parent %q)",
				a.ParentID)
		}
	}
}
