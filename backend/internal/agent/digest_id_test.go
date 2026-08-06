package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// Every create tool requires a parentId, and the compiled context never said
// what the board's id WAS. On a board with existing cards the model could copy
// one and infer the parent; on an empty board it had nothing, so it guessed —
// and every guess was rejected as an out-of-scope reference.
//
// Measured across the corpus before this: 18 wasted changes on one run, 9 on
// another, 2–5 on most. One run produced nothing at all and said so plainly:
// "create_note failed to recognize the current board's ID as a valid parent."
func TestDigest_StatesTheBoardID(t *testing.T) {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "5eed0000000000000000b0a1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Occupied: Rect{Empty: true},
	}
	out := scope.Render("")

	if !strings.Contains(out, "5eed0000000000000000b0a1") {
		t.Fatalf("the board's own id appears nowhere in the compiled context:\n%s", out)
	}
	// And it must say what to DO with it: an id with no instruction is one more
	// string among many.
	if !strings.Contains(out, "parentId") {
		t.Errorf("the context never says the board id is what parentId takes:\n%s", out)
	}
}
