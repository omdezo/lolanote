package agent_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/agentmem"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
	"qomranote/backend/internal/service"
)

// These tests run the ENTIRE harness — admission, delegation, scope
// compilation, the tool loop, plan compilation, the real transaction write
// path, verification, and revert — with only the model replaced by a fixture.
// Everything that can actually corrupt a user's board is exercised for real.

const (
	boardID = "b0a1d0000000000000000001"
	otherID = "c0ffee0000000000000000002"
	owner   = "alice"
)

type harness struct {
	svc       *agent.Service
	elements  *memory.ElementRepo
	runs      *agentmem.RunRepo
	events    *agentmem.EventRepo
	txns      *memory.TransactionRepo
	labels    *memory.LabelRepo
	provider  *cognition.Scripted
	principal *domain.Principal
}

func newHarness(t *testing.T, steps ...cognition.ScriptedStep) *harness {
	t.Helper()
	elements := memory.NewElementRepo()
	txnRepo := memory.NewTransactionRepo()
	labels := memory.NewLabelRepo()
	access := service.NewAccessResolver(elements)

	n := 0
	newID := func() string { n++; return fmt.Sprintf("%024x", 0xa9e07000+n) }
	txnSvc := service.NewTransactionService(elements, txnRepo, access, nil, service.IDGenerator(newID), zap.NewNop())
	provider := cognition.NewScripted(steps...)
	runs := agentmem.NewRunRepo()
	events := agentmem.NewEventRepo()

	svc := agent.NewService(agent.Config{
		Elements: elements, Txns: txnSvc, TxnRepo: txnRepo, Access: access, Labels: labels,
		Runs: runs, Events: events, Provider: provider,
		NewID: newID, Log: zap.NewNop(),
	})
	return &harness{
		svc: svc, elements: elements, runs: runs, events: events,
		txns: txnRepo, provider: provider, labels: labels,
		principal: &domain.Principal{Sub: owner, Name: "Alice"},
	}
}

func cardID(board string, i int) string { return fmt.Sprintf("%sc%03x", board[:20], i+1) }

func cardIDs(board string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, cardID(board, i))
	}
	return out
}

// seedBoard creates a board plus n unsorted cards.
func (h *harness) seedBoard(t *testing.T, board string, texts ...string) []string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := h.elements.Insert(ctx, &domain.Element{
		ID: board, Type: domain.TypeBoard,
		Content:   domain.Content{"title": "Q3 Launch"},
		ACL:       &domain.ACL{OwnerID: owner, Editors: []string{}},
		CreatedBy: owner, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	ids := make([]string, 0, len(texts))
	for i, text := range texts {
		id := cardID(board, i)
		if err := h.elements.Insert(ctx, &domain.Element{
			ID: id, Type: domain.TypeCard,
			Location:  domain.Location{ParentID: board, Section: domain.SectionUnsorted, Index: float64(i)},
			Content:   domain.Content{"textPreview": text},
			CreatedBy: owner, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed card: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

// seedColumn puts a column on a board that already exists. The board a person
// has already organized is the common case, and a fixture that only ever seeds
// loose cards cannot reach it.
func (h *harness) seedColumn(t *testing.T, board, id, title string) string {
	t.Helper()
	now := time.Now().UTC()
	if err := h.elements.Insert(context.Background(), &domain.Element{
		ID: id, Type: domain.TypeColumn,
		Location: domain.Location{
			ParentID: board, Section: domain.SectionCanvas,
			Position: domain.Point{X: 0, Y: 0}, Width: 280, Height: 400,
		},
		Content:   domain.Content{"title": title},
		CreatedBy: owner, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed column: %v", err)
	}
	return id
}

// seedScattered builds a board with loose, overlapping canvas cards — the shape
// a composition request acts on. Without geometry the arrange tools refuse
// everything and a tool-choice eval cannot distinguish right from wrong.
func (h *harness) seedScattered(t *testing.T, board string) {
	t.Helper()
	h.seedBoard(t, board, "a note", "another note")
	now := time.Now().UTC()
	for i, p := range [][2]float64{{10, 10}, {24, 22}, {320, 15}} {
		if err := h.elements.Insert(context.Background(), &domain.Element{
			ID: fmt.Sprintf("%sx%03d", board[:20], i), Type: domain.TypeCard,
			Location: domain.Location{
				ParentID: board, Section: domain.SectionCanvas,
				Position: domain.Point{X: p[0], Y: p[1]}, Width: 280, Height: 120,
			},
			Content:   domain.Content{"textPreview": fmt.Sprintf("scattered %d", i)},
			CreatedBy: owner, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed scattered: %v", err)
		}
	}
}

// awaitPlan polls until the run's plan holds exactly n actions. Refinement
// re-enters PROPOSED from PROPOSED, so a state-only wait observes the previous
// pass and passes for the wrong reason.
func (h *harness) awaitPlan(t *testing.T, runID string, n int) *agent.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last *agent.Run
	for time.Now().Before(deadline) {
		run, err := h.svc.Get(context.Background(), h.principal, runID)
		if err == nil {
			last = run
			if run.State == agent.StateProposed && run.Plan != nil && len(run.Plan.Actions) == n {
				return run
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := -1
	if last != nil && last.Plan != nil {
		got = len(last.Plan.Actions)
	}
	t.Fatalf("plan never reached %d actions (last: %d)", n, got)
	return nil
}

// awaitState polls until the run reaches one of the wanted states. Runs execute
// in a goroutine, so the test observes the state machine rather than the call.
func (h *harness) awaitState(t *testing.T, runID string, want ...agent.RunState) *agent.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := h.svc.Get(context.Background(), h.principal, runID)
		if err == nil {
			for _, w := range want {
				if run.State == w {
					return run
				}
			}
			if run.State.Terminal() {
				t.Fatalf("run reached terminal state %s (%s), wanted %v", run.State, run.Reason, want)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach %v in time", runID, want)
	return nil
}

// finish is the tail of every scripted plan.
func finish(summary string) cognition.ScriptedStep {
	return cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
		{Name: "finish", Input: map[string]any{"summary": summary}},
	}}
}

// confirm answers the review turn the loop forces after finish: the model is
// shown its own arrangement and, finding nothing worth changing, finishes again.
// Every fixture carries one, because every real run takes this turn.
func confirm() cognition.ScriptedStep {
	return cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
		{Name: "finish", Input: map[string]any{"summary": "Reviewed; the shape suits the material."}},
	}}
}

func call(name string, input map[string]any) cognition.ScriptedCall {
	return cognition.ScriptedCall{Name: name, Input: input}
}

// ---------------------------------------------------------------------------

// TestAgent_BuildsNestedStructure is the capability this whole rewrite exists
// for: the agent creates a board, then puts a column inside THAT board, then a
// note inside the column — three levels the earlier design could not express.
func TestAgent_BuildsNestedStructure(t *testing.T) {
	// Ids are deterministic from the run, so the fixture can reference what
	// earlier steps create — exactly as a real model would from tool output.
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	newBoard := agent.ActionID(runIDGuess, 0)
	newColumn := agent.ActionID(runIDGuess, 1)

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_board", map[string]any{"parentId": boardID, "title": "Launch plan"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": newBoard, "title": "Research"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_note", map[string]any{"parentId": newColumn, "text": "Check competitor pricing"}),
			call("create_todo", map[string]any{
				"parentId": newColumn, "title": "Before launch",
				"tasks": []string{"Book venue", "Email finance"},
			}),
		}},
		finish("Made a launch board with a research column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note", "another note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Set up a launch plan", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if run.ID != runIDGuess {
		t.Fatalf("run id assumption broken: got %s", run.ID)
	}

	proposed := h.awaitState(t, run.ID, agent.StateProposed)
	if got := len(proposed.Plan.Actions); got != 4 {
		t.Fatalf("want 4 actions, got %d", got)
	}
	if n := h.txns.Count(); n != 0 {
		t.Fatalf("preview wrote %d transactions; it must write none", n)
	}

	applied, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.State != agent.StateCompleted {
		t.Fatalf("want COMPLETED, got %s (%s)", applied.State, applied.Reason)
	}
	if n := h.txns.Count(); n != 1 {
		t.Fatalf("want exactly 1 transaction, got %d", n)
	}

	// The structure must actually exist, nested as planned.
	board, err := h.elements.Get(ctx, newBoard)
	if err != nil || board.Type != domain.TypeBoard || board.Location.ParentID != boardID {
		t.Fatalf("nested board not created correctly: %+v (%v)", board, err)
	}
	col, err := h.elements.Get(ctx, newColumn)
	if err != nil || col.Type != domain.TypeColumn || col.Location.ParentID != newBoard {
		t.Fatalf("column not inside the new board: %+v (%v)", col, err)
	}
	kids, _ := h.elements.Children(ctx, domain.ElementFilter{ParentID: newColumn})
	if len(kids) != 2 {
		t.Fatalf("want a note and a to-do inside the column, got %d", len(kids))
	}
	// A to-do list's items are real elements too.
	for _, k := range kids {
		if k.Type == domain.TypeTaskList {
			tasks, _ := h.elements.Children(ctx, domain.ElementFilter{ParentID: k.ID})
			if len(tasks) != 2 {
				t.Fatalf("want 2 tasks under the to-do list, got %d", len(tasks))
			}
		}
	}

	// And the whole thing reverses.
	if _, err := h.svc.Revert(ctx, h.principal, run.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if b, err := h.elements.Get(ctx, newBoard); err == nil && !b.IsDeleted() {
		t.Fatal("revert left the created board on the canvas")
	}
}

// TestAgent_RejectsOutOfScopeIDs is the injection detector. The fixture stands
// in for a model talked into naming an element it was never shown.
func TestAgent_RejectsOutOfScopeIDs(t *testing.T) {
	victim := "deadbeefdeadbeefdead0001"
	ids := cardIDs(boardID, 3)

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("rename", map[string]any{"elementId": victim, "title": "pwned"}),
			call("create_note", map[string]any{"parentId": boardID, "text": "a legitimate note"}),
		}},
		finish("done"),
		confirm(),
	)
	h.seedBoard(t, boardID, "one", "two", "three")
	_ = ids
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{BoardID: boardID, Intent: "tidy up"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.awaitState(t, run.ID, agent.StateProposed)

	for _, a := range proposed.Plan.Actions {
		if a.ElementID == victim {
			t.Fatal("an out-of-scope element id survived into the plan")
		}
	}
	if len(proposed.Plan.Actions) != 1 {
		t.Fatalf("want only the legitimate action, got %d", len(proposed.Plan.Actions))
	}

	var flagged bool
	for _, ev := range h.events.All() {
		if ev.Type == agent.EvSecIDOutOfScope {
			flagged = true
		}
	}
	if !flagged {
		t.Fatal("rejecting an out-of-scope id must emit a security event, not fail silently")
	}
}

// TestAgent_UnattendedRunCannotDelete asserts the capability is withheld rather
// than merely discouraged: an auto run is never even offered the tool.
func TestAgent_UnattendedRunCannotDelete(t *testing.T) {
	ids := cardIDs(boardID, 3)
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("delete_element", map[string]any{"elementId": ids[0]}),
		}},
		finish("done"),
		confirm(),
	)
	h.seedBoard(t, boardID, "one", "two", "three")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "clean up", Autonomy: agent.AutonomyAuto,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := h.svc.Get(ctx, h.principal, run.ID)
		if got.State.Terminal() {
			// The tool is absent from an unattended catalogue, so the call is
			// refused and the run has nothing to do.
			if got.Plan != nil {
				for _, a := range got.Plan.Actions {
					if a.Kind == agent.ActDelete {
						t.Fatal("an unattended run staged a deletion")
					}
				}
			}
			el, err := h.elements.Get(ctx, ids[0])
			if err != nil || el.IsDeleted() {
				t.Fatal("an unattended run deleted an element")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("run never terminated")
}

// TestAgent_StalePlanRejected covers exact-action binding: a collaborator moved
// a targeted card while the human was reviewing, so the approval no longer
// describes reality and must not commit.
func TestAgent_StalePlanRejected(t *testing.T) {
	ids := cardIDs(boardID, 3)
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("rename", map[string]any{"elementId": ids[0], "title": "Renamed"}),
		}},
		finish("done"),
		confirm(),
	)
	h.seedBoard(t, boardID, "one", "two", "three")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{BoardID: boardID, Intent: "rename the first card"})
	h.awaitState(t, run.ID, agent.StateProposed)

	if _, err := h.elements.MergePatch(ctx, ids[0], domain.Content{
		"content": map[string]any{"textPreview": "edited by a peer"},
	}); err != nil {
		t.Fatalf("peer edit: %v", err)
	}

	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil); err == nil {
		t.Fatal("apply must refuse a plan whose targeted elements have changed")
	}
	if n := h.txns.Count(); n != 0 {
		t.Fatalf("a stale apply wrote %d transactions; it must write none", n)
	}
	after, _ := h.svc.Get(ctx, h.principal, run.ID)
	if after.State != agent.StateProposed {
		t.Fatalf("want the run to remain PROPOSED, got %s", after.State)
	}
}

// TestAgent_DropCascades proves the review affordance is coherent: dropping a
// container also drops what the plan put inside it, because those children
// would otherwise have no parent.
func TestAgent_DropCascades(t *testing.T) {
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	newColumn := agent.ActionID(runIDGuess, 0)

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Ideas"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_note", map[string]any{"parentId": newColumn, "text": "first idea"}),
			call("create_note", map[string]any{"parentId": boardID, "text": "unrelated note"}),
		}},
		finish("done"),
		confirm(),
	)
	h.seedBoard(t, boardID, "one", "two")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{BoardID: boardID, Intent: "add some ideas"})
	proposed := h.awaitState(t, run.ID, agent.StateProposed)
	if len(proposed.Plan.Actions) != 3 {
		t.Fatalf("want 3 actions, got %d", len(proposed.Plan.Actions))
	}

	// Drop the column; the note inside it must go with it, the unrelated note
	// must survive.
	applied, err := h.svc.Apply(ctx, h.principal, run.ID, []agent.Adjustment{{Kind: agent.AdjustDrop, Seq: 0}}, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied.Plan.Actions) != 1 {
		t.Fatalf("want 1 surviving action, got %d", len(applied.Plan.Actions))
	}
	if applied.Plan.Actions[0].Text != "unrelated note" {
		t.Fatalf("the wrong action survived: %+v", applied.Plan.Actions[0])
	}
	if _, err := h.elements.Get(ctx, newColumn); err == nil {
		t.Fatal("a dropped action was still committed")
	}
}

// TestAgent_SecondRunOnSameBoardRejected covers the single-run-per-board guard:
// two concurrent runs would interleave writes on one canvas.
func TestAgent_SecondRunOnSameBoardRejected(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_note", map[string]any{"parentId": boardID, "text": "hello"}),
		}},
		finish("done"),
		confirm(),
	)
	h.seedBoard(t, boardID, "one", "two")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{BoardID: boardID, Intent: "add a note"})
	h.awaitState(t, run.ID, agent.StateProposed)

	if _, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{BoardID: boardID, Intent: "again"}); err == nil {
		t.Fatal("a second run on the same board must be rejected")
	}
	if _, err := h.svc.Discard(ctx, h.principal, run.ID, nil); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := h.runs.ActiveByBoard(ctx, boardID); err == nil {
		t.Fatal("a discarded run must release the board's run slot")
	}
}

// TestAgent_RequiresIntent asserts the harness refuses to guess.
func TestAgent_RequiresIntent(t *testing.T) {
	h := newHarness(t)
	h.seedBoard(t, boardID, "one", "two")
	if _, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{BoardID: boardID}); err == nil {
		t.Fatal("a run with no instruction must be rejected")
	}
}

// TestDelegation_CannotEscapeBoard asserts the containment boundary directly at
// the write path: a grant scoped to one board cannot touch another, even though
// its human principal owns both.
func TestDelegation_CannotEscapeBoard(t *testing.T) {
	h := newHarness(t)
	ids := h.seedBoard(t, boardID, "a", "b", "c")
	otherIDs := h.seedBoard(t, otherID, "x", "y", "z")
	ctx := context.Background()

	access := service.NewAccessResolver(h.elements)
	txnSvc := service.NewTransactionService(h.elements, h.txns, access, nil,
		service.IDGenerator(func() string { return "ffffffffffffffffffffffff" }), zap.NewNop())

	agentPrincipal := &domain.Principal{
		Sub: owner, Name: "Alice",
		Delegation: &domain.Delegation{
			RunID: "r1", OnBehalfOf: owner, RootBoardID: boardID,
			Capabilities: []domain.Capability{domain.CapElementMove},
			Consequence:  domain.ConsequenceReversibleWrite,
			MaxOps:       10,
			ExpiresAt:    time.Now().Add(time.Minute),
		},
	}

	move := func(id string) []domain.Op {
		return []domain.Op{{
			ElementID: id, Action: domain.ActionMove,
			Changes:     domain.Content{"location": map[string]any{"index": 5.0}},
			UndoChanges: domain.Content{"location": map[string]any{"index": 0.0}},
		}}
	}

	if _, err := txnSvc.Apply(ctx, agentPrincipal, boardID, "", move(ids[0])); err != nil {
		t.Fatalf("in-scope move rejected: %v", err)
	}
	if _, err := txnSvc.Apply(ctx, agentPrincipal, otherID, "", move(otherIDs[0])); err == nil {
		t.Fatal("a delegation scoped to one board must not write to another")
	}
	if _, err := txnSvc.Apply(ctx, agentPrincipal, boardID, "", []domain.Op{{
		ElementID: ids[1], Action: domain.ActionDelete,
	}}); err == nil {
		t.Fatal("a REVERSIBLE_WRITE grant must not be able to delete")
	}

	expired := *agentPrincipal
	d := *agentPrincipal.Delegation
	d.ExpiresAt = time.Now().Add(-time.Second)
	expired.Delegation = &d
	if _, err := txnSvc.Apply(ctx, &expired, boardID, "", move(ids[0])); err == nil {
		t.Fatal("an expired delegation must not be able to write")
	}
}

// TestActionID_Deterministic underpins idempotency: a retried apply must
// produce the same ids, so a replay collides instead of duplicating elements.
func TestActionID_Deterministic(t *testing.T) {
	a := agent.ActionID("run-1", 0)
	if a != agent.ActionID("run-1", 0) {
		t.Fatal("action ids must be stable for a given run and position")
	}
	if a == agent.ActionID("run-1", 1) || a == agent.ActionID("run-2", 0) {
		t.Fatal("action ids must differ across positions and runs")
	}
	if !domain.ObjectIDPattern.MatchString(a) {
		t.Fatalf("action id %q is not a valid element id", a)
	}
}

// TestStateMachine_RejectsIllegalEdges keeps the machine honest.
func TestStateMachine_RejectsIllegalEdges(t *testing.T) {
	if agent.CanTransition(agent.StateCancelled, agent.StateRunning) {
		t.Fatal("a cancelled run must not resume")
	}
	if agent.CanTransition(agent.StateRunning, agent.StateCompleted) {
		t.Fatal("completion must go through verification")
	}
	if !agent.CanTransition(agent.StateVerifying, agent.StateCompleted) {
		t.Fatal("verification must be able to complete a run")
	}
	if !agent.CanTransition(agent.StateCompleted, agent.StateReverted) {
		t.Fatal("a completed run must be revertible")
	}
}
