package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The instrument, tested. The corpus itself only ever runs against the live
// model, so every harness change it depends on has historically landed
// untested — and a tier that silently fell back to the old path would look
// exactly like a tier that worked.

// IL17(a): the Service-backed tier really is the whole product. A probe that
// says Service must produce a durable run, per-state timestamps and journal
// rows — none of which exist on the plan-only path, because none of the layers
// that write them are instantiated there.
func TestServiceTier_ProducesADurableRunAndAJournal(t *testing.T) {
	p := probe{
		ID: "T-service", Prompt: "one column", Seed: seedSharedWorkspace,
		Service: true, Apply: true,
	}
	provider := cognition.NewScripted(
		turn(call("create_column", map[string]any{"parentId": evalBoardID, "title": "Sorted"})),
		finish("One column."),
		finish("Reviewed."),
	)
	res := runProbe(context.Background(), provider, p)
	if res.Err != nil {
		t.Fatalf("service-tier probe failed: %v", res.Err)
	}
	if res.Run == nil {
		t.Fatal("no durable run — the probe fell through to the plan-only path")
	}
	if _, ok := res.Run.StateAt[agent.StateProposed]; !ok {
		t.Error("no PROPOSED stamp on the run; the state machine never ran")
	}
	if len(res.Journal) == 0 {
		t.Fatal("the journal is empty — the EventStore is not wired to the probe")
	}
	if !res.Applied {
		t.Fatalf("the plan never committed (%v)", res.ApplyErr)
	}
	if len(res.Run.TransactionIDs) == 0 {
		t.Error("the run records no transaction, so nothing about it is revertible or auditable")
	}
}

// IL17(a) continued: an adjustment is the defining artefact of the learning
// loop — "the person dropped these rows and kept the rest" — and it was
// inexpressible as a probe while the harness drove the planner directly.
func TestServiceTier_AnAdjustmentReachesTheAppliedPlan(t *testing.T) {
	p := probe{
		ID: "T-adjust", Prompt: "two columns", Seed: seedSharedWorkspace,
		Service: true, Apply: true,
		Adjustments: []agent.Adjustment{{Kind: agent.AdjustDrop, Seq: 0}},
	}
	provider := cognition.NewScripted(
		turn(
			call("create_column", map[string]any{"parentId": evalBoardID, "title": "Dropped"}),
			call("create_column", map[string]any{"parentId": evalBoardID, "title": "Kept"}),
		),
		finish("Two columns."),
		finish("Reviewed."),
	)
	res := runProbe(context.Background(), provider, p)
	if res.Err != nil || res.Run == nil {
		t.Fatalf("service-tier probe failed: %v", res.Err)
	}
	if !res.Applied {
		t.Fatalf("the adjusted plan never committed (%v)", res.ApplyErr)
	}
	if len(res.Plan.Actions) != 1 {
		t.Fatalf("the applied plan carries %d action(s); the dropped row survived the adjustment",
			len(res.Plan.Actions))
	}
	if res.Plan.Actions[0].Title != "Kept" {
		t.Errorf("the wrong row was dropped: %q survived", res.Plan.Actions[0].Title)
	}
}

// IL17(b): the write path now sees a delegation. Without one,
// TransactionService skips its entire delegation block — expiry, containment,
// MaxOps, per-op capability, the content denylist — in a single nil check, so
// the corpus was exercising model-built ops against no envelope at all.
func TestApplyPlan_CommitsUnderTheSameGrantTheProductMints(t *testing.T) {
	grant := evalDelegation(probeRunID("T-grant"), agent.DefaultBudget())
	if grant.RootBoardID != evalBoardID {
		t.Fatalf("the grant is rooted at %q, not the probe's board", grant.RootBoardID)
	}
	if !grant.Allows(domain.CapElementCreate) {
		t.Error("the grant cannot create, so no probe could ever apply anything")
	}
	if grant.MaxOps == 0 {
		t.Error("the grant has no op ceiling, which is one of the checks being skipped")
	}

	// And the envelope actually bites: an expired grant must refuse the commit
	// rather than sail through. This is FR12's acceptance line, and it was
	// invisible to the corpus because expiry lives inside the skipped block.
	ctx := context.Background()
	repo := newProbeRepo(ctx)
	scope, err := agent.CompileScope(ctx, repo, agent.TaskSpec{
		Intent: "x", Owner: evalOwner, RootBoardID: evalBoardID, Scope: agent.ScopeBoard,
	})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	plan := &agent.Plan{Actions: []agent.Action{{
		Seq: 0, Kind: agent.ActCreateColumn,
		ElementID: agent.ActionID(probeRunID("T-grant"), 0),
		ParentID:  evalBoardID, Section: "CANVAS", Title: "Late",
	}}}
	if err := applyPlan(ctx, repo, plan, scope, grant); err != nil {
		t.Fatalf("a live grant was refused: %v", err)
	}
	expired := evalDelegation(probeRunID("T-grant"), agent.DefaultBudget())
	expired.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	plan.Actions[0].ElementID = agent.ActionID(probeRunID("T-grant"), 1)
	if err := applyPlan(ctx, repo, plan, scope, expired); err == nil {
		t.Error("an EXPIRED grant committed — the harness is still bypassing the envelope")
	}
}

// IL17(c): distinct run ids. Every probe reused one literal, which is harmless
// while nothing persists and a collision the moment the Service tier writes a
// run keyed by it.
func TestProbeRunIDs_AreDistinctAndWellFormed(t *testing.T) {
	seen := map[string]string{}
	for _, p := range corpus() {
		id := probeRunID(p.ID)
		if len(id) != 24 {
			t.Errorf("%s: run id %q is %d characters, not a 24-hex id", p.ID, id, len(id))
		}
		if other, dup := seen[id]; dup {
			t.Errorf("%s and %s share the run id %s", p.ID, other, id)
		}
		seen[id] = p.ID
	}
}

// MP11: the fixture that makes the multiplayer corner measurable at all.
func TestSharedWorkspace_CarriesASecondPrincipal(t *testing.T) {
	ctx := context.Background()
	repo := newSharedRepo(ctx)
	board, err := repo.Get(ctx, evalBoardID)
	if err != nil {
		t.Fatalf("read the board: %v", err)
	}
	if board.ACL == nil || len(board.ACL.Editors) == 0 {
		t.Fatal("the shared fixture has no Editors — it is the same single-owner board as every other seed")
	}
	if board.ACL.Editors[0] != evalCollaborator {
		t.Errorf("editor is %q, want the second principal", board.ACL.Editors[0])
	}
	if board.ACL.PublicEditLink == "" {
		t.Error("no public edit link — the link-holder principal has no fixture")
	}

	all, err := repo.Descendants(ctx, evalBoardID, false)
	if err != nil {
		t.Fatalf("descendants: %v", err)
	}
	authors := map[string]bool{}
	for _, el := range all {
		authors[el.CreatedBy] = true
	}
	if !authors[evalOwner] || !authors[evalCollaborator] {
		t.Errorf("createdBy values are %v — with one author nothing can tell "+
			"'somebody else wrote this' from 'you wrote this'", authors)
	}
}

// And the second principal is real all the way down: an editor may start a run,
// and the run is filed under THEM rather than under the owner.
func TestServiceTier_ACollaboratorCanRunOnABoardTheyDoNotOwn(t *testing.T) {
	p := probe{
		ID: "T-collab", Prompt: "one column", Seed: seedSharedWorkspace,
		Service: true, As: evalCollaborator,
	}
	provider := cognition.NewScripted(
		turn(call("create_column", map[string]any{"parentId": evalBoardID, "title": "Theirs"})),
		finish("One column."),
		finish("Reviewed."),
	)
	res := runProbe(context.Background(), provider, p)
	if res.Err != nil {
		t.Fatalf("an editor could not start a run on a board shared with them: %v", res.Err)
	}
	if res.Run.Tenant != evalCollaborator {
		t.Errorf("the run is filed under %q, not the editor who asked for it", res.Run.Tenant)
	}
}

// MP7, measured rather than asserted: run history is fetched per tenant, so the
// owner's memory does not reach the collaborator. The probe exists to make that
// a number instead of an assumption.
func TestServiceTier_TheOwnersHistoryDoesNotReachTheCollaborator(t *testing.T) {
	provider := cognition.NewScripted(finish("Nothing to do."), finish("Reviewed."))
	p := probe{
		ID: "T-history", Prompt: "complete", Seed: seedSharedWorkspace,
		Service: true, As: evalCollaborator,
		History: ownerHistory(), HistoryBy: evalOwner,
	}
	if res := runProbe(context.Background(), provider, p); res.Err != nil {
		t.Fatalf("probe failed: %v", res.Err)
	}
	var sent strings.Builder
	for _, m := range provider.Calls[0].Messages {
		sent.WriteString(m.Text)
	}
	if strings.Contains(sent.String(), "LEFT UNDONE") {
		t.Skip("the owner's unmet list now reaches a collaborator — MP7 has been addressed elsewhere")
	}
	// The same seed, run as the OWNER, must see it — otherwise this test is
	// passing because the history seeding is broken rather than because tenancy
	// is doing its job.
	own := cognition.NewScripted(finish("Nothing to do."), finish("Reviewed."))
	p.As = evalOwner
	p.ID = "T-history-owner"
	if res := runProbe(context.Background(), own, p); res.Err != nil {
		t.Fatalf("owner probe failed: %v", res.Err)
	}
	var mine strings.Builder
	for _, m := range own.Calls[0].Messages {
		mine.WriteString(m.Text)
	}
	if !strings.Contains(mine.String(), "LEFT UNDONE") {
		t.Fatal("the seeded prior run does not reach its OWN author either — " +
			"probe.History is not wired on the Service tier")
	}
}

// CG4: only flaky probes repeat, and the floor is what decides the sweep rather
// than any single miss.
func TestSweep_RepeatsOnlyFlakyProbesAndGradesTheRate(t *testing.T) {
	flaky := probesByID()["G2"]
	if !flaky.Flaky || flaky.Floor <= 0 {
		t.Fatal("G2 was characterised as 5-in-6 in prose; it must carry that as data")
	}
	steady := probesByID()["G5"]
	if steady.Flaky {
		t.Fatal("a deterministic probe is marked flaky, which would multiply the sweep's cost for nothing")
	}
	if got := floorFor(steady); got != 1 {
		t.Errorf("a non-flaky probe's floor is %v, want 1 — anything less makes a real failure optional", got)
	}

	// Five of six passes holds a 5/6 floor; four does not.
	o := probeOutcome{id: "G2", runs: 6, passes: 5}
	if !o.held(flaky) {
		t.Errorf("5/6 was read as below a 5/6 floor (rate %.4f, floor %.4f)", o.rate(), floorFor(flaky))
	}
	if (probeOutcome{id: "G2", runs: 6, passes: 4}).held(flaky) {
		t.Error("4/6 held a 5/6 floor — the floor is not binding")
	}
	// And one miss on a deterministic probe still fails the sweep.
	if (probeOutcome{id: "G5", runs: 1, passes: 0}).held(steady) {
		t.Error("a failed deterministic probe was reported as holding")
	}
}

// DF36: the domain grade actually reads the plan's words, and the fourteen
// film-shaped probes are no longer graded on shape alone.
func TestDomainGrade_FailsFabricatedProcedureAndAcceptsTheRealThing(t *testing.T) {
	p := probesByID()["D1"]
	if p.Domain == nil {
		t.Fatal("D1 asks for shooting paperwork in Muscat and grades nothing about it")
	}

	generic := evalResult{Plan: &agent.Plan{
		Summary: "Set up the permits board.",
		Actions: []agent.Action{
			{Kind: agent.ActCreateColumn, Title: "Paperwork"},
			{Kind: agent.ActCreateNote, Text: "Apply for the permit"},
			{Kind: agent.ActCreateNote, Text: "Wait for approval"},
		},
	}}
	if domainGrade(p, generic) == "" {
		t.Error("a plan naming no authority, no lead time and no artefact passed the domain grade")
	}

	real := evalResult{Plan: &agent.Plan{
		Summary: "Permits for a public-space shoot in Muscat.",
		Actions: []agent.Action{
			{Kind: agent.ActCreateNote, Text: "Filming permit application to the Ministry of Information"},
			{Kind: agent.ActCreateNote, Text: "Royal Oman Police notification for the harbour road"},
			{Kind: agent.ActCreateNote, Text: "CAA drone permit — allow three weeks before the shoot"},
			{Kind: agent.ActCreateNote, Text: "Public liability insurance certificate"},
		},
	}}
	if v := domainGrade(p, real); v != "" {
		t.Errorf("a plan naming the actual authorities was failed: %s", v)
	}

	// And a placeholder left in the answer is caught, because structure over a
	// TBD is the exact shape of a run that produced no substance.
	tbd := evalResult{Plan: &agent.Plan{Summary: real.Plan.Summary,
		Actions: append(append([]agent.Action{}, real.Plan.Actions...),
			agent.Action{Kind: agent.ActCreateNote, Text: "Permit fee: TBD"})}}
	if !strings.Contains(domainGrade(p, tbd), "placeholder") {
		t.Error("a TBD in the answer was not caught")
	}
}

// The domain grade must not be able to pass a probe its structural grade fails,
// or adding it would have loosened the corpus rather than tightened it.
func TestGradeOf_KeepsBothJudgementsSeparate(t *testing.T) {
	p := probe{
		ID:     "T-both",
		Domain: &rubric{Requires: [][]string{{"muscat"}}},
		Grade:  func(evalResult) string { return "structural fault" },
	}
	res := evalResult{Plan: &agent.Plan{Summary: "Muscat"}}
	if v := gradeOf(p, res); !strings.Contains(v, "structural fault") {
		t.Errorf("a satisfied rubric masked a structural failure: %q", v)
	}
	p.Grade = func(evalResult) string { return "" }
	res.Plan.Summary = "somewhere"
	if gradeOf(p, res) == "" {
		t.Error("a clean structural grade masked an unmet rubric")
	}
}

func newSharedRepo(ctx context.Context) *memory.ElementRepo {
	repo := memory.NewElementRepo()
	seedSharedWorkspace(ctx, repo, evalBoardID)
	return repo
}
