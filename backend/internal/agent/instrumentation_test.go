package agent_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/agentmem"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
	"qomranote/backend/internal/service"
)

// The measurement layer, tested where it is read rather than where it is
// written. Nothing here is product behaviour — it is the instrument every
// later question about the agent has to be asked through, and each of these
// tests pins a way the instrument was previously lying.

// Every terminal state wrote the same CompletedAt, and COMPLETED→REVERTED is a
// legal edge — so a reverted run's apply time was overwritten by its revert
// time. Both ends of the interval "how long did this change stand before the
// person took it back" lived in one field, and the second write erased the
// first.
func TestInstrumentation_EachStateKeepsItsOwnTimestamp(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Sorted"}),
		}},
		finish("One column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "One column", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.awaitState(t, run.ID, agent.StateProposed)

	applied, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.State != agent.StateCompleted {
		t.Fatalf("state = %s (%s)", applied.State, applied.Reason)
	}
	appliedAt, ok := applied.StateAt[agent.StateCompleted]
	if !ok {
		t.Fatal("no COMPLETED stamp — the run cannot say when it landed")
	}

	// A gap the revert has to be measurable across. Without it the two stamps
	// can be equal for an honest reason and the assertion proves nothing.
	time.Sleep(3 * time.Millisecond)

	reverted, err := h.svc.Revert(ctx, h.principal, run.ID)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if reverted.State != agent.StateReverted {
		t.Fatalf("state = %s, want REVERTED", reverted.State)
	}

	revertedAt, ok := reverted.StateAt[agent.StateReverted]
	if !ok {
		t.Fatal("no REVERTED stamp")
	}
	if !revertedAt.After(appliedAt) {
		t.Errorf("revert stamp %v is not after the apply stamp %v — the interval regret is measured over is zero",
			revertedAt, appliedAt)
	}
	if got := reverted.StateAt[agent.StateCompleted]; !got.Equal(appliedAt) {
		t.Errorf("the COMPLETED stamp moved when the run was reverted (%v → %v)", appliedAt, got)
	}
	if reverted.CompletedAt == nil || !reverted.CompletedAt.Equal(appliedAt) {
		t.Errorf("completedAt = %v, want the apply time %v — the revert overwrote it again",
			reverted.CompletedAt, appliedAt)
	}
	// And the three timings LP3's approval-fatigue argument needs are now
	// subtraction rather than guesswork.
	for _, st := range []agent.RunState{agent.StatePlanning, agent.StateRunning, agent.StateProposed, agent.StateApplying} {
		if _, ok := reverted.StateAt[st]; !ok {
			t.Errorf("no %s stamp — time-to-decide and model latency are still uncomputable", st)
		}
	}
}

// A partial revert stamped only UpdatedAt, so N per-action undos spread over an
// hour collapsed into one timestamp and N−1 of the intervals were simply gone.
func TestInstrumentation_PartialRevertStampsEachElement(t *testing.T) {
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	first := agent.ActionID(runIDGuess, 0)
	second := agent.ActionID(runIDGuess, 1)

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Keep"}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Drop"}),
		}},
		finish("Two columns."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Two columns", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.awaitState(t, run.ID, agent.StateProposed)
	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if _, err := h.svc.RevertOne(ctx, h.principal, run.ID, []string{first}); err != nil {
		t.Fatalf("first revert: %v", err)
	}
	time.Sleep(3 * time.Millisecond)
	after, err := h.svc.RevertOne(ctx, h.principal, run.ID, []string{second})
	if err != nil {
		t.Fatalf("second revert: %v", err)
	}

	a, okA := after.RevertedAt[first]
	b, okB := after.RevertedAt[second]
	if !okA || !okB {
		t.Fatalf("revertedAt = %v, want a stamp for each undone element", after.RevertedAt)
	}
	if !b.After(a) {
		t.Errorf("both undos carry the same time (%v, %v) — they were minutes apart and the record cannot say so", a, b)
	}
}

// Refine and Steer both emitted run.created with an untranslated English
// Message as the only discriminator, so count(type = run.created) was runs plus
// refinements plus steers and all three rates were wrong from the first query.
func TestJournal_RefineAndSteerAreNotAdmissions(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "First pass"}),
		}},
		finish("One column."),
		confirm(),
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Second pass"}),
		}},
		finish("Revised."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "One column", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.awaitPlan(t, run.ID, 1)
	if _, err := h.svc.Refine(ctx, h.principal, run.ID, "make it two", nil); err != nil {
		t.Fatalf("refine: %v", err)
	}
	h.awaitState(t, run.ID, agent.StateProposed)

	byType := map[agent.EventType]int{}
	for _, ev := range h.events.All() {
		byType[ev.Type]++
	}
	if byType[agent.EvRunCreated] != 1 {
		t.Errorf("run.created × %d — one run was admitted, so a run count built on this event type is wrong by %d",
			byType[agent.EvRunCreated], byType[agent.EvRunCreated]-1)
	}
	if byType[agent.EvRefined] != 1 {
		t.Errorf("run.refined × %d, want 1 — the refine rate has no event to count", byType[agent.EvRefined])
	}

	// The same journal, read the way a dashboard would read it: grouped, over a
	// window, without touching a single run document.
	counts, err := h.events.Aggregate(ctx, owner, time.Now().Add(-time.Hour), nil)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if counts[agent.EvRunCreated] != 1 || counts[agent.EvRefined] != 1 {
		t.Errorf("aggregate = %v, want one admission and one refinement", counts)
	}
	if counts[agent.EvRunState] == 0 {
		t.Error("the aggregate sees no state transitions — the events carry no tenant, so it is filtering everything out")
	}
}

// No run recorded the code that produced it, so an apply rate that moved after
// a deploy could not be attributed to anything and no metric survived a
// release.
func TestInstrumentation_RunCarriesTheBuildThatProducedIt(t *testing.T) {
	h := newHarness(t, finish("Nothing to do."), confirm())
	h.seedBoard(t, boardID, "a note")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "have a look", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if run.Build.PromptHash == "" {
		t.Error("no prompt hash — a prompt change cannot be told apart from a model change")
	}
	if run.Build.CatalogueHash == "" {
		t.Error("no catalogue hash — adding a tool leaves no trace on the runs that had it")
	}
	if run.Build.BudgetsHash == "" {
		t.Error("no budgets hash — a step-ceiling change is invisible to every aggregate")
	}
	if agent.CurrentBuild() != run.Build {
		t.Error("the stamp is not stable within one process, so it cannot be a grouping key")
	}
}

// An event the journal could not record is a hole in the only record of what
// the agent did on someone's board. It used to be a Warn, which made those
// holes indistinguishable from a quiet run — and they appear under load,
// exactly when someone is watching.
func TestJournal_AFailedAppendEscalates(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	elements := memory.NewElementRepo()
	txnRepo := memory.NewTransactionRepo()
	access := service.NewAccessResolver(elements)
	n := 0
	newID := func() string { n++; return fmt.Sprintf("%024x", 0xbad00000+n) }

	svc := agent.NewService(agent.Config{
		Elements: elements,
		Txns:     service.NewTransactionService(elements, txnRepo, access, nil, service.IDGenerator(newID), zap.NewNop()),
		TxnRepo:  txnRepo, Access: access,
		Runs:     agentmem.NewRunRepo(),
		Events:   deafJournal{},
		Provider: cognition.NewScripted(finish("Nothing to do."), confirm()),
		NewID:    newID, Log: zap.New(core),
	})

	ctx := context.Background()
	now := time.Now().UTC()
	if err := elements.Insert(ctx, &domain.Element{
		ID: boardID, Type: domain.TypeBoard,
		Content:   domain.Content{"title": "Q3 Launch"},
		ACL:       &domain.ACL{OwnerID: owner, Editors: []string{}},
		CreatedBy: owner, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	if _, err := svc.Create(ctx, &domain.Principal{Sub: owner}, agent.CreateRequest{
		BoardID: boardID, Intent: "have a look", Autonomy: agent.AutonomyPreview,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if logs.FilterLevelExact(zapcore.ErrorLevel).Len() == 0 {
		t.Fatalf("a dropped journal entry was logged at %v, not as an error — an audit hole is not a warning",
			levelsOf(logs))
	}
}

func levelsOf(logs *observer.ObservedLogs) []zapcore.Level {
	var out []zapcore.Level
	for _, e := range logs.All() {
		out = append(out, e.Level)
	}
	return out
}

// deafJournal accepts nothing, which is what a Mongo primary stepping down
// looks like from here.
type deafJournal struct{}

func (deafJournal) Append(context.Context, *agent.Event) error { return domain.ErrConflict }
func (deafJournal) List(context.Context, string, int64, int) ([]*agent.Event, error) {
	return nil, nil
}
func (deafJournal) Aggregate(context.Context, string, time.Time, []agent.EventType) (map[agent.EventType]int64, error) {
	return nil, nil
}
func (deafJournal) DeleteByTenant(context.Context, string) error { return nil }
