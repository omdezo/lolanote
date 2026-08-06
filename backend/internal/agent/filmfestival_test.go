package agent

import (
	"context"
	"strings"
	"testing"
)

// DF28 — festival submission is the only part of a filmmaker's work that is a
// genuine optimisation problem with IRREVERSIBLE moves: premiere status is a
// resource you spend once, and submitting to the wrong festival first closes
// every A-list door permanently. The tracker existed as a shape and carried none
// of the rules or dates that make the shape worth having.

func TestFestival_ThePremiereRuleAndTheRegionalCalendarArrive(t *testing.T) {
	s := reviseStaging()
	out := s.runFilmSpec(context.Background(), &toolArgs{Artefact: "festivals"},
		call(s, toolFilmSpec))
	if out.IsError {
		t.Fatalf("film_spec refused the festival tracker: %s", out.Content)
	}
	for _, want := range []string{
		"WORLD premiere", // the irreversible move
		"Berlinale",      // the rule set that is actually written down
		"Red Sea",        // the regional ladder this user climbs
		"Muscat",         // the rung with an Omani category
		"2 Aug 2026",     // a real window, not "check their site"
	} {
		if !strings.Contains(out.Content, want) {
			t.Errorf("the festival spec never mentions %q:\n%s", want, out.Content)
		}
	}
}

// A deadline is the most perishable knowledge in the pack. Every one of these
// has to arrive cited and dated, or an agent stating it is indistinguishable
// from an agent inventing it — and it goes stale silently.
func TestFestival_EveryDatedFactCarriesItsSourceAndDate(t *testing.T) {
	spec, ok := artefactFor("festivals")
	if !ok {
		t.Fatal("no festival spec")
	}
	if len(spec.Facts) == 0 {
		t.Fatal("the festival tracker carries no dated facts at all, which is the state this " +
			"item exists to end")
	}
	for _, f := range spec.Facts {
		if f.Source == "" || f.AsOf == "" {
			t.Errorf("%q is asserted with source %q and date %q — a deadline without both is "+
				"a fabrication as far as anybody reading can tell", f.Topic, f.Source, f.AsOf)
		}
	}
	rendered := spec.Render()
	if !strings.Contains(rendered, "as of "+factsAsOf) {
		t.Errorf("the rendered spec does not date its facts:\n%s", rendered)
	}
	if !strings.Contains(rendered, "a deadline moves") {
		t.Errorf("nothing tells the model these dates expire, so it will state them as "+
			"permanent:\n%s", rendered)
	}
}

// The perishable half rides the artefact, not the always-on block: a run that is
// not building a festival tracker should not be paying for a festival calendar.
func TestFestival_TheCalendarDoesNotRideEveryFilmBoard(t *testing.T) {
	block := DomainPackText()
	if strings.Contains(block, "Red Sea") || strings.Contains(block, "2 Aug 2026") {
		t.Errorf("the festival calendar is inlined into the always-on domain block — that is " +
			"the most perishable knowledge in the pack charged to every production run")
	}
}
