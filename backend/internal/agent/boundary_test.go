package agent

import (
	"os"
	"strings"
	"testing"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

func scriptedCall(name string) cognition.ToolCall { return cognition.ToolCall{ID: "c1", Name: name} }

// The tool boundary must refuse an illegal parent WHILE THE MODEL CAN STILL
// ACT. Preconditions is the backstop, and reaching it means the whole run
// fails — the person gets nothing instead of a plan minus one bad call.
//
// A live run staged a CARD inside a CARD and a COLUMN inside a COLUMN, and only
// Preconditions caught them. Something in the boundary is not doing its job.
func TestBoundary_RefusesIllegalParentsAtStagingTime(t *testing.T) {
	s := reviseStaging()

	col, err := s.add(Action{Kind: ActCreateColumn, ParentID: "b1", Title: "Alpha"})
	if err != nil {
		t.Fatalf("seed column: %v", err)
	}
	note, err := s.add(Action{Kind: ActCreateNote, ParentID: col, Text: "a card"})
	if err != nil {
		t.Fatalf("seed note: %v", err)
	}

	cases := []struct{ name, parent, tool string }{
		{"column inside a column", col, toolCreateColumn},
		{"card inside a card", note, toolCreateNote},
		{"column inside a card", note, toolCreateColumn},
		{"board inside a column", col, toolCreateBoard},
	}
	for _, tc := range cases {
		out := s.runCreateBoard(nil,
			&toolArgs{ParentID: tc.parent, Title: "Nope", Text: "nope"},
			&reply{staging: s, call: scriptedCall(tc.tool)})
		if !out.IsError {
			t.Errorf("%s was ACCEPTED at the tool boundary: %s", tc.name, out.Content)
			continue
		}
		if !strings.Contains(out.Content, "cannot hold") && !strings.Contains(out.Content, "cannot") {
			t.Errorf("%s: refusal does not explain: %s", tc.name, out.Content)
		}
	}
}

// And the legal shapes still work, or the guard is worse than the bug.
func TestBoundary_AllowsTheOrdinaryShapes(t *testing.T) {
	s := reviseStaging()
	col, _ := s.add(Action{Kind: ActCreateColumn, ParentID: "b1", Title: "Alpha"})

	for _, tc := range []struct{ name, parent, tool string }{
		{"column on the board", "b1", toolCreateColumn},
		{"card in a column", col, toolCreateNote},
		{"card on the board", "b1", toolCreateNote},
		{"board on the board", "b1", toolCreateBoard},
	} {
		out := s.runCreateBoard(nil,
			&toolArgs{ParentID: tc.parent, Title: "Fine", Text: "fine"},
			&reply{staging: s, call: scriptedCall(tc.tool)})
		if out.IsError {
			t.Errorf("%s was refused: %s", tc.name, out.Content)
		}
	}
	_ = domain.TypeCard
}

// Every handler that takes a parent from the model must check it.
//
// One did not, and one checked for the wrong type: a generic edit put the TABLE
// check into runClone and left runCreateTable with none at all. The live corpus
// showed the result — a CARD inside a CARD reaching Preconditions, which fails
// the WHOLE run rather than one call, so the person gets nothing instead of a
// plan minus a bad idea.
//
// Reading the source is the only way to catch a MISSING call: a handler that
// never checks simply never fails, and a test per handler is a list somebody
// forgets to extend.
func TestBoundary_EveryParentedCreateIsChecked(t *testing.T) {
	src, err := os.ReadFile("toolhandlers.go")
	if err != nil {
		t.Fatalf("read toolhandlers.go: %v", err)
	}
	body := string(src)

	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "in.ParentID") || strings.Contains(line, "resolveParentFor(") {
			continue
		}
		t.Errorf("a handler takes in.ParentID without the containment check:\n  %s",
			strings.TrimSpace(line))
	}

	// And the child type must not be a copy-paste from elsewhere: the table
	// check sitting inside runClone is exactly how this shipped.
	for _, want := range []struct{ fn, childType string }{
		{"runClone", "domain.TypeClone"},
		{"runCreateTable", "domain.TypeTable"},
	} {
		at := strings.Index(body, "func (s *staging) "+want.fn+"(")
		if at < 0 {
			t.Errorf("%s not found", want.fn)
			continue
		}
		rest := body[at+1:]
		if end := strings.Index(rest, "\nfunc "); end > 0 {
			rest = rest[:end]
		}
		if !strings.Contains(rest, want.childType) {
			t.Errorf("%s does not check for %s — it is checking for something else",
				want.fn, want.childType)
		}
	}
}
