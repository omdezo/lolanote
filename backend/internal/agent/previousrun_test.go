package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// No memory between runs.
//
// "Complete" arrived as a fresh, context-free request at a board whose previous
// run had created eighteen columns and put nothing in any of them. That run had
// already written the exact instruction its successor needed — the structure was
// created and nothing was put inside it — and the digest carried a single word,
// "applied", instead.

func memoryScope(history ...PriorRun) *BoardScope {
	return &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard, Content: domain.Content{"title": "Film"}},
		Elements: map[string]*domain.Element{},
		History:  history,
	}
}

// The acceptance line: what the last run said it did not finish reaches the next
// run's context verbatim.
func TestPreviousRun_TheUnmetListReachesTheNextRunsContext(t *testing.T) {
	out := memoryScope(PriorRun{
		Intent:  "make a film production plan",
		Outcome: "applied",
		When:    "2 minutes ago",
		Summary: "This run ran out of room at step 14 of 14.",
		Unmet:   []string{"fill columns A, B — the run was stopped with these staged and nothing inside them"},
	}).Render("")

	if !strings.Contains(out, "PREVIOUS RUN") {
		t.Errorf("the digest has no previous-run block at all:\n%s", out)
	}
	if !strings.Contains(out, "fill columns A, B") {
		t.Errorf("the previous run's unmet list never reached the digest:\n%s", out)
	}
	if !strings.Contains(out, "ran out of room at step 14") {
		t.Errorf("the previous run's summary never reached the digest:\n%s", out)
	}
	if !strings.Contains(out, "make a film production plan") {
		t.Errorf("the unmet list arrived with no request attached to it:\n%s", out)
	}
	// The block is memory, not orders. A past request rendered as a live one is
	// how a run ends up redoing last week's work.
	if !strings.Contains(out, "not an instruction") {
		t.Errorf("nothing marks the block as history rather than as a request:\n%s", out)
	}
}

// Detail for the last run or two; one line each for the rest. An archival dump
// of every request ever made is what the digest already had.
func TestPreviousRun_OlderRunsKeepTheOneLineForm(t *testing.T) {
	runs := make([]PriorRun, 0, 3)
	for _, name := range []string{"newest", "middle", "oldest"} {
		runs = append(runs, PriorRun{
			Intent: name, Outcome: "applied", When: "an hour ago",
			Summary: name + " summary", Unmet: []string{name + " gap"},
		})
	}
	out := memoryScope(runs...).Render("")

	if !strings.Contains(out, "newest gap") || !strings.Contains(out, "middle gap") {
		t.Errorf("the two most recent runs are not rendered in full:\n%s", out)
	}
	if strings.Contains(out, "oldest gap") {
		t.Errorf("a third run spent the memory budget on itself:\n%s", out)
	}
	if !strings.Contains(out, "EARLIER REQUESTS") || !strings.Contains(out, `"oldest"`) {
		t.Errorf("the older run vanished instead of keeping its one line:\n%s", out)
	}
	if len(out) > 6000 {
		t.Errorf("the digest grew to %d chars on three remembered runs", len(out))
	}
}

// A run with nothing to add beyond its outcome word is exactly what the old
// one-line list was for. Promoting it to a block would spend the budget on
// "applied" with nothing after it.
func TestPreviousRun_ARunWithNothingToSayStaysInTheList(t *testing.T) {
	out := memoryScope(PriorRun{
		Intent: "organize this", Outcome: "applied", When: "yesterday",
	}).Render("")

	if strings.Contains(out, "PREVIOUS RUN") {
		t.Errorf("an outcome-only run was promoted to a memory block:\n%s", out)
	}
	if !strings.Contains(out, "EARLIER REQUESTS") || !strings.Contains(out, "organize this") {
		t.Errorf("the run was dropped from the digest entirely:\n%s", out)
	}
}

// A board with no history renders neither heading. The empty case is the one
// that ships untested and takes a stray "PREVIOUS RUN:" header with it.
func TestPreviousRun_AFreshBoardCarriesNoMemoryBlock(t *testing.T) {
	out := memoryScope().Render("")
	if strings.Contains(out, "PREVIOUS RUN") || strings.Contains(out, "EARLIER REQUESTS") {
		t.Errorf("a board with no history rendered a memory heading:\n%s", out)
	}
}
