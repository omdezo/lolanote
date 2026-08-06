package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// Step starvation and the silent half-answer.
//
// "Make a film" was allowed 60 changes and 14 model turns. The model staged
// about two changes a turn out of politeness, so the real ceiling was under
// thirty — and the run was cut at step 14 mid-flow, its last column created and
// left empty, and shipped as COMPLETED with no summary and no unmet. Nothing in
// the outcome distinguished that from a run that had finished and decided the
// board was done.

// starvedScope seeds a bare board the planner can be pointed at.
func starvedScope(t *testing.T) (*BoardScope, domain.ElementRepository) {
	t.Helper()
	repo := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.Insert(ctx, &domain.Element{
		ID: "b00000000000000000000fed", Type: domain.TypeBoard,
		Content: domain.Content{"title": "Film"}, ACL: &domain.ACL{OwnerID: "alice"},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	scope, err := CompileScope(ctx, repo, TaskSpec{
		Owner: "alice", RootBoardID: "b00000000000000000000fed", Scope: ScopeBoard,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return scope, repo
}

func scriptedTools(calls ...cognition.ScriptedCall) cognition.ScriptedStep {
	return cognition.ScriptedStep{Tools: calls}
}

// A run the step counter stops must say so, in the fields the person and the
// NEXT run actually read: the summary and the unmet list.
func TestStarvation_ACutRunSaysItRanOutAndNamesWhatIsEmpty(t *testing.T) {
	scope, repo := starvedScope(t)
	const runID = "run-starved"
	board := scope.Board.ID
	preProduction := ActionID(runID, 0)

	// Four turns, never a finish: the loop stops counting mid-flow, exactly as
	// the film run did. The last column is created and nothing goes in it.
	provider := cognition.NewScripted(
		scriptedTools(cognition.ScriptedCall{Name: "create_column",
			Input: map[string]any{"parentId": board, "title": "Pre-Production"}}),
		scriptedTools(cognition.ScriptedCall{Name: "create_column",
			Input: map[string]any{"parentId": board, "title": "Production"}}),
		scriptedTools(cognition.ScriptedCall{Name: "create_note",
			Input: map[string]any{"parentId": preProduction, "text": "lock the script"}}),
		scriptedTools(cognition.ScriptedCall{Name: "create_column",
			Input: map[string]any{"parentId": board, "title": "Post-Production"}}),
	)

	task := TaskSpec{
		Intent: "make a film", Owner: "alice", RootBoardID: board, Scope: ScopeBoard,
		Budget: Budget{MaxSteps: 4, MaxActions: 60, MaxTokens: 4000, MaxCostUSD: 1},
	}
	plan, _, err := NewPlanner(provider, repo, nil, nil, nil, nil).
		Run(context.Background(), scope, task, runID, func(EventType, string, map[string]any) {}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(plan.Summary, "ran out") {
		t.Errorf("a truncated run reported no truncation in its summary: %q", plan.Summary)
	}
	if !strings.Contains(plan.Summary, "step 4 of 4") {
		t.Errorf("the summary does not say where the run stopped: %q", plan.Summary)
	}
	if len(plan.Unmet) != 1 {
		t.Fatalf("unmet = %+v, want exactly one entry naming the unfilled containers", plan.Unmet)
	}
	// Both empty columns, and only once: exhaustion and the hollow disclosure
	// name the same containers, and two entries read as two separate failures.
	for _, want := range []string{"Production", "Post-Production"} {
		if !strings.Contains(plan.Unmet[0].Request, want) {
			t.Errorf("unmet does not name %q: %+v", want, plan.Unmet[0])
		}
	}
	if !strings.Contains(plan.Unmet[0].Why, "stopped at step 4") {
		t.Errorf("the reason does not say the run was cut: %+v", plan.Unmet[0])
	}
}

// One empty container is below the hollow floor — deliberately, because "add a
// Done column" is an ordinary request. A run that was CUT is a different fact:
// that container is empty because the run stopped, and it is precisely what the
// next run has to finish.
func TestStarvation_OneUnfilledContainerIsStillReported(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Casting"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "c1", Text: "read-throughs"},
		{Seq: 2, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Locations"},
	}}
	if h := hollowContainers(p); len(h) != 0 {
		t.Fatalf("the fixture is wrong: %v is above the ordinary hollow floor", h)
	}

	discloseExhaustion(p, 9, 24, stopSteps)
	if len(p.Unmet) != 1 || !strings.Contains(p.Unmet[0].Request, "Locations") {
		t.Fatalf("the one container the run never reached is unreported: %+v", p.Unmet)
	}

	// And the ordinary disclosure does not say it a second time.
	discloseHollow(p)
	if len(p.Unmet) != 1 {
		t.Errorf("the same gap was reported twice: %+v", p.Unmet)
	}
}

// Two budgets cut a run and both come out of the same path. Telling somebody
// their run "ran out of room at step 9 of 24" when it had fifteen steps left and
// ran out of MONEY sends them to raise the wrong limit.
func TestStarvation_ACostCappedRunNamesTheRightBudget(t *testing.T) {
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Casting"},
	}}
	discloseExhaustion(p, 9, 24, stopCost)

	if !strings.Contains(p.Summary, "ran out") {
		t.Errorf("no truncation reported at all: %q", p.Summary)
	}
	if strings.Contains(p.Summary, "of 24") {
		t.Errorf("a cost-capped run blames the step budget it never reached: %q", p.Summary)
	}
	if len(p.Unmet) != 1 || !strings.Contains(p.Unmet[0].Why, "cost limit") {
		t.Errorf("the unmet entry blames the wrong budget: %+v", p.Unmet)
	}
}

// A run that finished on its own terms is not told it was cut. The false
// "may be incomplete" warning has been fixed once already, and a summary that
// claims truncation on a complete plan is the same failure in a louder field.
func TestStarvation_AFinishedRunIsNotLabelledTruncated(t *testing.T) {
	scope, repo := starvedScope(t)
	board := scope.Board.ID
	const runID = "run-complete"

	provider := cognition.NewScripted(
		scriptedTools(cognition.ScriptedCall{Name: "create_column",
			Input: map[string]any{"parentId": board, "title": "Casting"}}),
		scriptedTools(cognition.ScriptedCall{Name: "create_note",
			Input: map[string]any{"parentId": ActionID(runID, 0), "text": "read-throughs"}}),
		scriptedTools(cognition.ScriptedCall{Name: "finish",
			Input: map[string]any{"summary": "One column, filled."}}),
		scriptedTools(cognition.ScriptedCall{Name: "finish",
			Input: map[string]any{"summary": "Reviewed; the shape suits the material."}}),
	)
	task := TaskSpec{
		Intent: "add a casting column", Owner: "alice", RootBoardID: board, Scope: ScopeBoard,
		Budget: Budget{MaxSteps: 8, MaxActions: 60, MaxTokens: 4000, MaxCostUSD: 1},
	}
	plan, _, err := NewPlanner(provider, repo, nil, nil, nil, nil).
		Run(context.Background(), scope, task, runID, func(EventType, string, map[string]any) {}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(plan.Summary, "ran out") {
		t.Errorf("a completed run claims it was truncated: %q", plan.Summary)
	}
	for _, n := range plan.Notes {
		if strings.Contains(n, "step limit") {
			t.Errorf("a completed run was flagged as step-limited: %q", n)
		}
	}
}

// The step budget must not be the limit that binds. Sixty changes at the two
// per turn the model was actually doing needed thirty turns; it had fourteen, so
// the cap that governed every authoring run was the one nobody had tuned.
func TestStarvation_StepBudgetLeavesRoomForTheActionBudget(t *testing.T) {
	b := DefaultBudget()
	// Four calls a turn is what the prompt now asks for and well under what a
	// provider allows. If the steps cannot carry the changes at that rate, the
	// action budget is decoration.
	if b.MaxSteps*4 < b.MaxActions {
		t.Errorf("MaxSteps=%d at 4 calls a turn carries %d changes, but the run is allowed %d — "+
			"the step budget is the real ceiling", b.MaxSteps, b.MaxSteps*4, b.MaxActions)
	}
	// A caller may ask for less and never for more; the default is the ceiling.
	asked := Budget{MaxSteps: 999}
	asked.Normalize()
	if asked.MaxSteps != b.MaxSteps {
		t.Errorf("a caller-supplied step budget clamped to %d, want the server envelope %d",
			asked.MaxSteps, b.MaxSteps)
	}
}

// The prompt has to say batching is normal, with a worked example. "Work in as
// few turns as you can" was already there and produced two calls a turn: it
// reads as a request for brevity, not as permission to fire fifteen tools at
// once.
func TestStarvation_PromptTeachesBatchingWithAnExample(t *testing.T) {
	for _, want := range []string{"BATCH YOUR TOOL CALLS", "one turn", "Turn 1", "Turn 3"} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("the batching instruction is missing %q", want)
		}
	}
}
