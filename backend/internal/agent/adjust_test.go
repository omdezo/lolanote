package agent

import (
	"reflect"
	"testing"

	"qomranote/backend/internal/domain"
)

// Adjusting a plan edits its ACTIONS. Everything else about the plan must
// survive unchanged.
//
// The old implementation rebuilt the plan from an enumerated list of fields, so
// every field added afterwards was silently discarded the moment a user dropped
// a single row. That already cost NewLabels once (apply compiled ops pointing
// at labels that were never created) and was still dropping Quarantined — which
// means an adjusted plan forgot that board content had tried to steer the run,
// and became eligible to auto-apply.
//
// This test walks the struct by reflection rather than naming fields, so a new
// field is covered the day it is added instead of the day it breaks something.
func TestApplyAdjustments_PreservesEveryFieldExceptActions(t *testing.T) {
	scope := &BoardScope{Board: &domain.Element{ID: "b1"}, Elements: map[string]*domain.Element{}}

	before := &Plan{
		Actions: []Action{
			{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", Title: "Pricing", ParentID: "b1"},
			{Seq: 1, Kind: ActCreateColumn, ElementID: "c2", Title: "Launch", ParentID: "b1"},
		},
		Summary:     "Two columns.",
		Notes:       []string{"Ignored 1 reference to an item that is not on this board."},
		Fingerprint: map[string]string{"b1": "v1"},
		NewLabels:   []*domain.Label{{ID: "l1", Name: "urgent"}},
		NewComments: []*domain.Comment{{ID: "cm1", Body: "why this shape"}},
		// Load-bearing: a quarantined plan must never become un-quarantined by
		// the user removing one row from it.
		Quarantined:  true,
		Destinations: []string{"other-board"},
		Unmet:        []Unmet{{Request: "also file the invoices", Why: "no invoices on this board"}},
	}

	after := ApplyAdjustments(before, []Adjustment{{Kind: AdjustDrop, Seq: 1}}, scope)

	if len(after.Actions) != 1 || after.Actions[0].ElementID != "c1" {
		t.Fatalf("actions = %+v, want only c1", after.Actions)
	}

	bv, av := reflect.ValueOf(*before), reflect.ValueOf(*after)
	typ := bv.Type()
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if name == "Actions" {
			continue // the one field an adjustment is allowed to change
		}
		if !reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) {
			t.Errorf("field %s was lost or altered by an adjustment: %v → %v",
				name, bv.Field(i).Interface(), av.Field(i).Interface())
		}
	}
}

// And a plan carrying nothing but an answer still adjusts without panicking.
func TestApplyAdjustments_EmptyPlan(t *testing.T) {
	scope := &BoardScope{Board: &domain.Element{ID: "b1"}, Elements: map[string]*domain.Element{}}
	out := ApplyAdjustments(&Plan{Question: &Question{Text: "which board?"}}, nil, scope)
	if out.Actions == nil {
		t.Error("actions must serialize as [] and not null")
	}
	if out.Question == nil || out.Question.Text != "which board?" {
		t.Error("the question did not survive")
	}
}
