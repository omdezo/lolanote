package agent

import (
	"context"
	"testing"

	"qomranote/backend/internal/agent/cognition"
)

// Telemetry lied about the one number that mattered.
//
// Every run ever written reported usage.calls:1 — a fourteen-turn run and a
// one-turn run were indistinguishable in the record, so the step starvation that
// cut "make a film" in half was invisible in exactly the field somebody
// inspecting the run would have looked at first.

// Three turns, three calls. The run is deliberately cut by its own step budget
// rather than finishing: a finish costs a review turn, and the point here is to
// count provider calls, not to model a tidy ending.
func TestTelemetry_AThreeTurnRunReportsThreeCalls(t *testing.T) {
	scope, repo := starvedScope(t)
	board := scope.Board.ID

	// Cache reads ride the same accumulator. A fixture that carries them proves
	// they are not zeroed on the way out along with the call count.
	turn := func(title string) cognition.ScriptedStep {
		return cognition.ScriptedStep{
			Tools: []cognition.ScriptedCall{{Name: "create_column",
				Input: map[string]any{"parentId": board, "title": title}}},
			Usage: cognition.Usage{CachedTokens: 10},
		}
	}
	provider := cognition.NewScripted(turn("Casting"), turn("Locations"), turn("Editing"))

	task := TaskSpec{
		Intent: "set up the stages", Owner: "alice", RootBoardID: board, Scope: ScopeBoard,
		Budget: Budget{MaxSteps: 3, MaxActions: 60, MaxTokens: 4000, MaxCostUSD: 1},
	}
	_, usage, err := NewPlanner(provider, repo, nil, nil, nil, nil).
		Run(context.Background(), scope, task, "run-telemetry", func(EventType, string, map[string]any) {}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if usage.Calls != 3 {
		t.Errorf("a three-turn run reported %d call(s), want 3", usage.Calls)
	}
	if usage.CachedTokens != 30 {
		t.Errorf("cached tokens = %d, want 30 — the accumulator dropped them", usage.CachedTokens)
	}

	// And the service-side fold, which is where the count used to collapse: the
	// planner's whole total is added to the run exactly once.
	var run Usage
	run.Add(usage)
	if run.Calls != 3 {
		t.Errorf("the run recorded %d call(s) after one service-side Add, want 3 — "+
			"Add is counting itself rather than the calls it folds in", run.Calls)
	}
}

// A total folded into a total keeps both counts. This is the shape the service
// uses and the one the old `u.Calls++` got wrong.
func TestTelemetry_AddAccumulatesCallsRatherThanCountingItself(t *testing.T) {
	var total Usage
	total.Add(Usage{Calls: 4, InputTokens: 100, CachedTokens: 40})
	total.Add(Usage{Calls: 2, InputTokens: 50, CachedTokens: 10})
	if total.Calls != 6 {
		t.Errorf("calls = %d, want 6", total.Calls)
	}
	if total.InputTokens != 150 || total.CachedTokens != 50 {
		t.Errorf("tokens = in:%d cached:%d, want in:150 cached:50", total.InputTokens, total.CachedTokens)
	}
}
