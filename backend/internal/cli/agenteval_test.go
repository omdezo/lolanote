package cli

import (
	"context"
	"strings"
	"testing"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The corpus itself is only ever exercised against the live model, which costs
// money and cannot run in CI — so every harness change it depends on has landed
// untested, and a probe that silently graded nothing would have looked exactly
// like a probe that passed.
//
// These drive the real runProbe with a scripted provider: the seed, the digest,
// the budget override, the seeded history and the apply step all run for real,
// and each G grader is shown both an answer it must accept and the wrong answer
// it exists to catch.

func probeByID(t *testing.T, id string) probe {
	t.Helper()
	for _, p := range corpus() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no probe %s in the corpus", id)
	return probe{}
}

func call(name string, input map[string]any) cognition.ScriptedCall {
	return cognition.ScriptedCall{Name: name, Input: input}
}

func turn(calls ...cognition.ScriptedCall) cognition.ScriptedStep {
	return cognition.ScriptedStep{Tools: calls}
}

func finish(summary string) cognition.ScriptedStep {
	return turn(call("finish", map[string]any{"summary": summary}))
}

// runScripted plays a canned model against a probe and returns what the grader
// will be handed.
//
// One extra turn is always scripted at the end: a plan that calls finish gets a
// review turn before the run is allowed to end, and a fixture that stops at its
// own finish fails with "no step N" rather than with anything about the probe.
// Runs that never finish — the exhaustion probe — never reach it.
func runScripted(t *testing.T, p probe, steps ...cognition.ScriptedStep) (evalResult, *cognition.Scripted) {
	t.Helper()
	provider := cognition.NewScripted(append(steps, finish("Reviewed; it suits the material."))...)
	res := runProbe(context.Background(), provider, p)
	if res.Err != nil {
		t.Fatalf("%s: the run itself failed: %v", p.ID, res.Err)
	}
	return res, provider
}

// The fixture's whole reason to exist: a card two containers below the root is
// in scope and in the printed digest. Every other G probe assumes it.
func TestG5_TheDigestReachesACardThreeLevelsDown(t *testing.T) {
	p := probeByID(t, "G5")
	res, _ := runScripted(t, p, finish("Three boards, five columns, nine cards."))

	if _, ok := res.Scope.Elements[colCasting]; !ok {
		t.Error("the Casting column inside Pre-Production is not in scope — nothing can be filed into it")
	}
	if _, ok := res.Scope.Elements[colEditing]; !ok {
		t.Error("the empty Editing column is not in scope")
	}
	if v := gradeOf(p, res); v != "" {
		t.Errorf("G5 rejected a run that changed nothing on a board it could see: %s", v)
	}
}

// G1 grades WHERE the cards land, which is the only part of "add three casting
// cards" a machine can check — and the part every depth-blind run got wrong by
// building a new column at the top instead.
func TestG1_AcceptsCardsFiledIntoTheNestedColumnAndRejectsTheRest(t *testing.T) {
	p := probeByID(t, "G1")

	inside := []cognition.ScriptedCall{}
	for _, text := range []string{"Read with the lead", "Chemistry test", "Callback: the brother"} {
		inside = append(inside, call("create_note", map[string]any{
			"parentId": colCasting, "text": text,
		}))
	}
	res, _ := runScripted(t, p, turn(inside...), finish("Three more names to see."))
	if v := gradeOf(p, res); v != "" {
		t.Errorf("G1 rejected three cards filed into the Casting column: %s", v)
	}

	// The observed failure: a new top-level column for cards that had a column
	// waiting for them.
	res, _ = runScripted(t, p,
		turn(call("create_column", map[string]any{"parentId": evalBoardID, "title": "Casting"})),
		finish("Made somewhere to put them."))
	if gradeOf(p, res) == "" {
		t.Error("G1 accepted a run that built a new column on the root instead of using the " +
			"Casting column two levels down — the probe grades nothing")
	}
}

// G2 is the "complete" follow-up: the previous run's unmet list names the empty
// container, so finishing it where it stands is the answer and a second copy of
// it beside the first is the observed failure.
func TestG2_SeededHistoryReachesTheModelAndFillingBeatsRebuilding(t *testing.T) {
	p := probeByID(t, "G2")

	res, provider := runScripted(t, p,
		turn(call("move_element", map[string]any{
			"elementId": "da71000000000000000000a7", "parentId": colEditing,
		})),
		finish("Moved the cut into the Editing column the last run left empty."))

	// The history is the whole point of the probe: if it never reaches the
	// context, the model is being asked to resolve "complete" from nothing and
	// the probe is measuring a different question.
	if len(provider.Calls) == 0 {
		t.Fatal("the provider was never called")
	}
	var sent strings.Builder
	for _, m := range provider.Calls[0].Messages {
		sent.WriteString(m.Text)
	}
	if !strings.Contains(sent.String(), "LEFT UNDONE") {
		t.Error("the seeded previous run never reached the model's context — probe.History is not wired")
	}

	if v := gradeOf(p, res); v != "" {
		t.Errorf("G2 rejected a run that filled the column the previous run named: %s", v)
	}

	res, _ = runScripted(t, p,
		turn(call("create_column", map[string]any{"parentId": nestedPost, "title": "Editing"})),
		finish("Set up Editing."))
	if gradeOf(p, res) == "" {
		t.Error("G2 accepted a run that answered 'complete' by building structure again")
	}
}

// G3 forces the step budget down. Without the override there is no way to reach
// the truncated state except by paying for a full-length run, which is why the
// honest-exhaustion path shipped with no eval over it at all.
func TestG3_ForcedLowStepsProducesAnHonestlyTruncatedPlan(t *testing.T) {
	p := probeByID(t, "G3")
	if p.Budget == nil || p.Budget.MaxSteps != 4 {
		t.Fatalf("G3 is meant to force the step budget down; it carries %+v", p.Budget)
	}

	// Four turns that each stage a shelf and never call finish — the shape of a
	// run cut mid-flow, with its last containers created and left empty.
	var steps []cognition.ScriptedStep
	for _, title := range []string{"Development", "Financing", "Delivery", "Archive"} {
		steps = append(steps, turn(call("create_column", map[string]any{
			"parentId": evalBoardID, "title": title,
		})))
	}
	res, provider := runScripted(t, p, steps...)

	// Count LOOP turns, not raw provider calls. A forced one-shot — the
	// outline sketch, the judge, the reflection after a rejection — carries
	// ForceTool and is deliberately not a step: it is spent outside the loop
	// and never decrements MaxSteps. This assertion read len(Calls) while it
	// meant "steps", so the day the outline pre-phase landed it reported 5 on a
	// 4-step budget and looked like the override had broken, when the loop had
	// in fact run exactly four times.
	var loopTurns int
	for _, c := range provider.Calls {
		if c.ForceTool == "" {
			loopTurns++
		}
	}
	if loopTurns != 4 {
		t.Errorf("the run took %d model turns on a 4-step budget — the override is not reaching "+
			"the planner", loopTurns)
	}
	if v := gradeOf(p, res); v != "" {
		t.Errorf("G3 rejected a run the step budget cut short: %s\nsummary: %q\nunmet: %+v",
			v, res.Plan.Summary, res.Plan.Unmet)
	}
}

// G4 is the only probe that commits, because the property is one only the write
// path can have. This proves the apply step actually runs — a harness that
// quietly skipped it would make the probe pass on every plan ever proposed.
func TestG4_TheApplyStepRunsAndTheStrandedConnectorIsGone(t *testing.T) {
	p := probeByID(t, "G4")

	res, _ := runScripted(t, p,
		turn(call("move_element", map[string]any{
			"elementId": "10a5000000000000000000a1", "parentId": nestedPre,
		})),
		finish("Filed the script card into Pre-Production."))

	if !res.Verdict.Passed {
		t.Fatalf("the scripted move failed preconditions, so nothing was applied: %+v", res.Verdict.Criteria)
	}
	if !res.Applied {
		t.Fatalf("the plan was never applied (%v) — G4 would grade a board nothing had happened to", res.ApplyErr)
	}

	// The move separated the two endpoints, so the arrow between them can no
	// longer be drawn anywhere and must not still be sitting on the root canvas.
	arrow, err := res.Board.Get(context.Background(), "11e0000000000000000000a1")
	if err != nil {
		t.Fatalf("reading the connector back: %v", err)
	}
	if !arrow.IsDeleted() {
		t.Errorf("the connector still lives on %s with its endpoints on two different canvases",
			arrow.Location.ParentID)
	}
	if stranded := strandedLines(res); len(stranded) > 0 {
		t.Errorf("stranded connectors survived the commit: %v", stranded)
	}
	if v := gradeOf(p, res); v != "" {
		t.Errorf("G4 rejected a grouping run that left no stranded connector: %s", v)
	}
}

// And the other half of G4: a line whose endpoints end up apart is what the
// grader has to catch. Left live by hand, it must fail the probe.
func TestG4_GraderCatchesAConnectorLeftPointingAcrossTwoCanvases(t *testing.T) {
	ctx := context.Background()
	repo := newProbeRepo(ctx)
	// Move one endpoint into a nested board WITHOUT going through the write
	// path, which is exactly the state the sweep exists to prevent.
	if _, err := repo.MergePatch(ctx, "10a5000000000000000000a1", domain.Content{
		"location": map[string]any{"parentId": nestedPre, "section": "CANVAS"},
	}); err != nil {
		t.Fatalf("reparent: %v", err)
	}
	if got := strandedLines(evalResult{Board: repo}); len(got) != 1 {
		t.Errorf("the grader found %d stranded connector(s); the board has exactly one", len(got))
	}
}

// G6 is the idempotent redirect: asking for a column that already exists and is
// empty must not produce a second one.
func TestG6_RejectsASecondColumnBesideTheEmptyOne(t *testing.T) {
	p := probeByID(t, "G6")

	res, _ := runScripted(t, p,
		turn(call("create_note", map[string]any{
			"parentId": colEditing, "text": "Assembly cut by the 12th",
		})),
		finish("Put the cutting stages into the Editing column that was already there."))
	if v := gradeOf(p, res); v != "" {
		t.Errorf("G6 rejected a run that filled the existing Editing column: %s", v)
	}

	// The live failure, staged onto the plan by hand: the redirect not firing and
	// a duplicate shelf appearing beside the empty one. Spelled differently on
	// purpose — the fold is what has to catch it, not string equality.
	dup := res
	dup.Plan = &agent.Plan{
		Summary: res.Plan.Summary,
		Actions: append(append([]agent.Action{}, res.Plan.Actions...), agent.Action{
			Seq: len(res.Plan.Actions), Kind: agent.ActCreateColumn,
			ElementID: "aaaa000000000000000000a1", ParentID: nestedPost, Title: "editing:",
		}),
	}
	if gradeOf(p, dup) == "" {
		t.Error("G6 accepted a second 'Editing' beside the one already in Post-Production — " +
			"the grader's name fold is not catching it")
	}
}

// newProbeRepo seeds the grouping fixture the way runProbe does, for graders
// that need a board without a model run behind it.
func newProbeRepo(ctx context.Context) *memory.ElementRepo {
	repo := memory.NewElementRepo()
	seedNestedGrouping(ctx, repo, evalBoardID)
	return repo
}
