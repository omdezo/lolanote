package agent

import (
	"context"
	"strings"
	"testing"
)

// DF7 — delivery is where independent films die, and it is the one film artefact
// that is natively a checklist, so the product's create_todo is exactly the
// right container. But the list is not one list: a festival DCP, a broadcaster,
// a streamer and a self-release share a core and then diverge completely, and a
// single flat list is wrong for all four.

func TestDelivery_TheListDiffersByWhereTheFilmIsGoing(t *testing.T) {
	s := reviseStaging()
	ctx := context.Background()

	festival := s.runFilmSpec(ctx, &toolArgs{Artefact: "deliverables", Destination: "festival"},
		call(s, toolFilmSpec))
	if festival.IsError {
		t.Fatalf("film_spec refused a festival delivery list: %s", festival.Content)
	}
	streamer := s.runFilmSpec(ctx, &toolArgs{Artefact: "deliverables", Destination: "streamer"},
		call(s, toolFilmSpec))
	if streamer.IsError {
		t.Fatalf("film_spec refused a streamer delivery list: %s", streamer.Content)
	}

	// The item that stops a screening, and the item a platform will not take
	// delivery without. Neither belongs on the other's list.
	if !strings.Contains(festival.Content, "SMPTE ST 428-7") {
		t.Errorf("a festival list with no conformant DCP subtitle spec:\n%s", festival.Content)
	}
	if !strings.Contains(streamer.Content, "IMSC1") || !strings.Contains(streamer.Content, "E&O") {
		t.Errorf("a streamer list missing captions or the E&O gate:\n%s", streamer.Content)
	}
	if festival.Content == streamer.Content {
		t.Fatal("the destination changed nothing, which is the flat list this item exists to end")
	}
	// The core is common and stated once. Two overlapping full lists is how a
	// model builds both.
	if !strings.Contains(festival.Content, "ON TOP OF") {
		t.Errorf("the destination list does not say it is additive, so it reads as a "+
			"replacement:\n%s", festival.Content)
	}
	if !strings.Contains(festival.Content, "M&E") {
		t.Errorf("the core list — where M&E lives, the item everyone forgets — was dropped "+
			"when a destination was named:\n%s", festival.Content)
	}
}

// If nobody said where it is going, the honest move is the menu and a nudge to
// ask — not a guess, because the four lists are far enough apart that a guess is
// forty wrong items.
func TestDelivery_AnUnknownDestinationAsksRatherThanGuessing(t *testing.T) {
	s := reviseStaging()
	out := s.runFilmSpec(context.Background(),
		&toolArgs{Artefact: "deliverables", Destination: "cinema chain"}, call(s, toolFilmSpec))
	if !out.IsError {
		t.Fatal("a destination nobody has a list for was answered anyway")
	}
	if !strings.Contains(out.Content, "festival") || !strings.Contains(out.Content, "ASK") {
		t.Errorf("the refusal neither carries the menu nor says to ask:\n%s", out.Content)
	}
}

// A destination on a document that has none must be named, not swallowed. A
// silently dropped argument is how a model concludes it asked correctly and got
// the wrong answer.
func TestDelivery_ADestinationOnTheWrongDocumentIsRefused(t *testing.T) {
	s := reviseStaging()
	out := s.runFilmSpec(context.Background(),
		&toolArgs{Artefact: "shot-list", Destination: "streamer"}, call(s, toolFilmSpec))
	if !out.IsError {
		t.Error("a shot list quietly accepted a delivery destination")
	}
}

// The schema's enum is read off the artefact table, so a destination added to
// one and forgotten in the other is a value the model can never send.
func TestDelivery_TheSchemaEnumTracksTheData(t *testing.T) {
	spec, ok := artefactFor("deliverables")
	if !ok {
		t.Fatal("no deliverables spec")
	}
	keys := deliveryDestinationKeys()
	if len(keys) != len(spec.Destinations) {
		t.Fatalf("the tool offers %d destination(s) and the spec has %d", len(keys),
			len(spec.Destinations))
	}
	for _, d := range spec.Destinations {
		if !containsStr(keys, d.Key) {
			t.Errorf("%q is in the delivery spec and not in the tool's enum, so nothing can "+
				"ever ask for it", d.Key)
		}
	}
}

// DF8, DF29, DF34 — three recipes the product had every part of and no worked
// example for. Asserted as prompt text because they are CONTENT, and content is
// what gets tidied away by somebody who does not know M&E is the item everyone
// forgets.
func TestDelivery_ThePlaybookCarriesThePostGraphTheDPRAndTheMediaRule(t *testing.T) {
	for _, want := range []string{
		// DF8: the direction is what a professional reads first.
		"picture lock BLOCKS conform",
		"M&E DEPENDS ON the mix",
		// DF29: the DPR is derived, never invented.
		"Write today's production report",
		"DERIVED",
		// DF34: "back up the footage" is the failure the discipline prevents.
		"3-2-1 with VERIFIED",
		"CLEAR-TO-FORMAT",
		"there is no DIT",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("the playbook no longer says %q", want)
		}
	}
}
