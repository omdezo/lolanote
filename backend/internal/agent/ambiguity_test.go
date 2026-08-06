package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// "Complete" — one word, no referent — went straight to guessing, and guessed
// eighteen new empty columns beside the ones the person was asking it to fill.
// The ask tool existed the whole time and nothing ever reached for it.

func ambiguityScope(history ...PriorRun) *BoardScope {
	return &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard, Content: domain.Content{"title": "Film"}},
		Elements: map[string]*domain.Element{},
		History:  history,
	}
}

// Nothing to resolve it with: asking costs one turn, guessing costs the person a
// review and an undo.
func TestAmbiguity_AShortRequestWithNoHistoryIsPointedAtAsk(t *testing.T) {
	msg := openingMessage(ambiguityScope(), TaskSpec{Intent: "complete", Budget: DefaultBudget()})
	if !strings.Contains(msg, "ask()") {
		t.Errorf("a one-word request was never told the ask tool exists:\n%s", msg)
	}
	if !strings.Contains(msg, "BEFORE you stage anything") {
		t.Errorf("the nudge does not say when to ask, which is the only time it works:\n%s", msg)
	}
	// A nudge, never a gate. The model is told to proceed if the board settles
	// it — a heuristic that blocks is one that eventually blocks the right
	// answer.
	if !strings.Contains(msg, "proceed") {
		t.Errorf("the nudge reads as a refusal rather than a nudge:\n%s", msg)
	}
}

// With history the word usually is not ambiguous at all: "complete" two minutes
// after "make a film" means finish the film. Proceed — and say which reading was
// taken, so a wrong one is visible instead of silent.
func TestAmbiguity_AShortRequestWithHistoryProceedsAndDeclaresTheReading(t *testing.T) {
	scope := ambiguityScope(PriorRun{
		Intent: "make a film production plan", Outcome: "applied", When: "2 minutes ago",
	})
	msg := openingMessage(scope, TaskSpec{Intent: "complete", Budget: DefaultBudget()})

	if !strings.Contains(msg, "SAY WHICH READING YOU TOOK") {
		t.Errorf("the run may guess without ever declaring the guess:\n%s", msg)
	}
	if strings.Contains(msg, "there is no earlier run") {
		t.Errorf("the nudge ignored the history it was given:\n%s", msg)
	}
	// And the point of the whole item: finish what is there rather than build a
	// second copy of it beside the first.
	if !strings.Contains(msg, "completing what is already there") {
		t.Errorf("nothing tells the run to fill rather than rebuild:\n%s", msg)
	}
}

// A request that names its own subject is left alone. The cost of a false
// positive is telling somebody who asked a clear question that they were
// unclear, and that is worse than an occasional miss.
func TestAmbiguity_AClearRequestIsNotNudged(t *testing.T) {
	clear := []string{
		"group these cards by theme",
		"what is missing from this plan?",
		"tidy up the left cluster",
		"design a workflow for a drama shoot",
	}
	for _, intent := range clear {
		if terseIntent(intent) {
			t.Errorf("%q was treated as too short to act on", intent)
		}
		msg := openingMessage(ambiguityScope(), TaskSpec{Intent: intent, Budget: DefaultBudget()})
		if strings.Contains(msg, "THIS REQUEST IS VERY SHORT") {
			t.Errorf("%q was nudged:\n%s", intent, msg)
		}
	}
}

// Short, or long and empty. "Can you do all of that for me" is seven words that
// name nothing, and it reads to a model as a fuller request than it is.
func TestAmbiguity_CatchesShortAndPronounOnlyRequests(t *testing.T) {
	terse := []string{"complete", "fix it", "do that one", "can you do all of that for me"}
	for _, intent := range terse {
		if !terseIntent(intent) {
			t.Errorf("%q names nothing and was not caught", intent)
		}
	}
	// An empty intent is refused at admission; nudging it here would be a second
	// answer to a question already answered.
	if terseIntent("   ") {
		t.Error("an empty request was nudged rather than refused upstream")
	}
}
