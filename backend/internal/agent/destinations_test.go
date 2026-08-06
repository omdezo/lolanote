package agent

import (
	"testing"

	"qomranote/backend/internal/domain"
)

// The review list is the only place a person can check WHERE thirty cards are
// about to go, and it did not say. A move's summary is the card's own text, and
// anything filed into a container has no coordinate to draw as a ghost — so the
// canonical request, "file these into columns", was un-reviewable.
func TestLabelDestinations(t *testing.T) {
	existingColumn := &domain.Element{
		ID: "col-existing", Type: domain.TypeColumn,
		Content: domain.Content{"title": "Research"},
	}
	scope := &BoardScope{
		Board:    &domain.Element{ID: "board-1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{"col-existing": existingColumn},
	}

	plan := &Plan{Actions: []Action{
		// A column this plan is about to create.
		{Seq: 0, Kind: ActCreateColumn, ElementID: "col-new", ParentID: "board-1", Title: "Pricing"},
		// Filed into it. The container does not exist yet, so only a pass over
		// the finished plan can name it.
		{Seq: 1, Kind: ActMove, ElementID: "card-a", ParentID: "col-new"},
		// Filed into one that was already there.
		{Seq: 2, Kind: ActMove, ElementID: "card-b", ParentID: "col-existing"},
		// Straight onto the canvas: the ghost shows exactly where, so a label
		// would be noise on every row.
		{Seq: 3, Kind: ActCreateNote, ElementID: "note-1", ParentID: "board-1"},
		// The Unsorted tray IS a destination — it is the one place on the board
		// that is not the canvas.
		{Seq: 4, Kind: ActMove, ElementID: "card-c", ParentID: "board-1",
			Section: string(domain.SectionUnsorted)},
	}}

	LabelDestinations(plan, scope)

	want := map[int]string{0: "", 1: "Pricing", 2: "Research", 3: "", 4: "Unsorted"}
	for seq, expect := range want {
		if got := plan.Actions[seq].Destination; got != expect {
			t.Errorf("action %d destination = %q, want %q", seq, got, expect)
		}
	}
}

// Re-labelling must be idempotent and must clear what is no longer true: a plan
// is labelled again after every adjustment, and a row whose parent was dropped
// would otherwise keep pointing at a column that is no longer being made.
func TestLabelDestinations_ClearsStaleLabels(t *testing.T) {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "board-1"},
		Elements: map[string]*domain.Element{},
	}
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActMove, ElementID: "card-a", ParentID: "board-1",
			Destination: "a column that is no longer in this plan"},
	}}

	LabelDestinations(plan, scope)

	if plan.Actions[0].Destination != "" {
		t.Errorf("destination = %q; a stale label survived a re-label",
			plan.Actions[0].Destination)
	}
}

func TestLabelDestinations_ToleratesNothing(t *testing.T) {
	// Called on every plan including the empty ones — an answer, or a question.
	LabelDestinations(nil, nil)
	LabelDestinations(&Plan{}, nil)
	LabelDestinations(&Plan{}, &BoardScope{})
}
