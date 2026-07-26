package agent_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// A screenplay board — eight scene columns of eight cards each — is what
// exposed the old fixed 260px row height: the second row of columns landed on
// top of the first, because a column holding eight cards is nearly 900px tall.
func TestLayout_WrappedRowsDoNotOverlap(t *testing.T) {
	const board = "b0000000000000000000000001"
	const line = "INT. SECRET BUNKER - NIGHT. Agent E2E, codenamed 'Phantom', receives her mission."

	plan := &agent.Plan{}
	seq := 0
	add := func(a agent.Action) {
		a.Seq = seq
		seq++
		plan.Actions = append(plan.Actions, a)
	}

	cols := make([]string, 0, 8)
	for c := 0; c < 8; c++ {
		id := fmt.Sprintf("column%02d", c)
		cols = append(cols, id)
		add(agent.Action{
			Kind: agent.ActCreateColumn, ElementID: id, ParentID: board,
			Title: fmt.Sprintf("Scene %d", c+1),
		})
	}
	for _, id := range cols {
		for k := 0; k < 8; k++ {
			add(agent.Action{
				Kind: agent.ActCreateNote, ElementID: fmt.Sprintf("%s-n%d", id, k),
				ParentID: id, Text: line,
			})
		}
	}

	scope := &agent.BoardScope{
		Board:    &domain.Element{ID: board, Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Occupied: agent.Rect{Empty: true},
	}
	agent.LayoutPlan(plan, scope)

	// Every column must have a box, and no two boxes may intersect.
	type box struct {
		title                  string
		x1, y1, x2, y2, height float64
	}
	var boxes []box
	for _, a := range plan.Actions {
		if a.Kind != agent.ActCreateColumn {
			continue
		}
		if a.Position == nil {
			t.Fatalf("%s got no position", a.Title)
		}
		// Eight three-line cards plus column chrome — the height the renderer
		// will actually produce, computed independently of the layout code.
		h := 104.0 + 8*(27.0+3*21.0) + 7*8.0
		boxes = append(boxes, box{
			title: a.Title,
			x1:    a.Position.X, y1: a.Position.Y,
			x2: a.Position.X + a.Position.Width, y2: a.Position.Y + h,
			height: h,
		})
	}
	if len(boxes) != 8 {
		t.Fatalf("want 8 columns, got %d", len(boxes))
	}

	for i := 0; i < len(boxes); i++ {
		for j := i + 1; j < len(boxes); j++ {
			a, b := boxes[i], boxes[j]
			if a.x1 < b.x2 && b.x1 < a.x2 && a.y1 < b.y2 && b.y1 < a.y2 {
				t.Errorf("%s (%.0f,%.0f)-(%.0f,%.0f) overlaps %s (%.0f,%.0f)-(%.0f,%.0f)",
					a.title, a.x1, a.y1, a.x2, a.y2, b.title, b.x1, b.y1, b.x2, b.y2)
			}
		}
	}

	// The plan must actually have wrapped, or the test proves nothing.
	rows := map[float64]int{}
	for _, b := range boxes {
		rows[b.y1]++
	}
	if len(rows) < 2 {
		t.Fatalf("expected the row to wrap, got %d row(s)", len(rows))
	}
}

// An empty column is short; the row below it should not be pushed down as if it
// were full. Guards against over-correcting the fix into a giant gap.
func TestLayout_EmptyColumnsPackTightly(t *testing.T) {
	const board = "b0000000000000000000000002"
	plan := &agent.Plan{}
	for c := 0; c < 6; c++ {
		plan.Actions = append(plan.Actions, agent.Action{
			Seq: c, Kind: agent.ActCreateColumn,
			ElementID: fmt.Sprintf("column%02d", c), ParentID: board,
			Title: fmt.Sprintf("C%d", c),
		})
	}
	scope := &agent.BoardScope{
		Board:    &domain.Element{ID: board, Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Occupied: agent.Rect{Empty: true},
	}
	agent.LayoutPlan(plan, scope)

	var secondRowY float64
	for _, a := range plan.Actions {
		if a.Position != nil && a.Position.Y > 0 {
			secondRowY = a.Position.Y
			break
		}
	}
	if secondRowY == 0 {
		t.Fatal("expected six columns to wrap into a second row")
	}
	if secondRowY > 220 {
		t.Errorf("empty columns pushed the next row to y=%.0f; expected a tight pack", secondRowY)
	}
}

// A title the header would clip is rejected at the tool boundary, so the model
// gets a reason and can coin a shorter one inside the same run. Silently
// truncating would ship "SCENE 3: THE DATA CHI" to the user and call it done.
func TestLabelBudget_RejectsClippingTitleAndRecovers(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{
				"parentId": boardID, "title": "Scene 3: The Data Chip",
			}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Data Chip"}),
		}},
		finish("Made one column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Group the scenes", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.awaitState(t, run.ID, agent.StateProposed)

	if got := len(proposed.Plan.Actions); got != 1 {
		t.Fatalf("want 1 staged column (the over-long one rejected), got %d", got)
	}
	if got := proposed.Plan.Actions[0].Title; got != "Data Chip" {
		t.Fatalf("want the recovered short title, got %q", got)
	}
}

// The self-view must state facts the model can act on. These are the exact
// observations the screenplay board should have produced and did not.
func TestSelfView_ReportsTheWallAndTheLopsidedColumn(t *testing.T) {
	const board = "b0000000000000000000000003"
	plan := &agent.Plan{}
	seq := 0
	add := func(a agent.Action) { a.Seq = seq; seq++; plan.Actions = append(plan.Actions, a) }

	for c := 0; c < 8; c++ {
		add(agent.Action{
			Kind: agent.ActCreateColumn, ElementID: fmt.Sprintf("column%02d", c),
			ParentID: board, Title: fmt.Sprintf("Scene %d", c+1),
		})
	}
	// Seven columns of six, one of one: lopsided, and one left empty.
	for c := 0; c < 7; c++ {
		n := 6
		if c == 6 {
			n = 1
		}
		for k := 0; k < n; k++ {
			add(agent.Action{
				Kind: agent.ActCreateNote, ElementID: fmt.Sprintf("c%02dn%d", c, k),
				ParentID: fmt.Sprintf("column%02d", c), Text: "a line of scene description",
			})
		}
	}

	scope := &agent.BoardScope{
		Board:    &domain.Element{ID: board, Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Occupied: agent.Rect{Empty: true},
	}
	view := agent.RenderSelfView(plan, scope)

	for _, want := range []string{"ARRANGEMENT", "row 1", "Scene 1(6)", "wall"} {
		if !strings.Contains(view, want) {
			t.Errorf("self-view missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "empty") {
		t.Errorf("self-view did not flag the empty column:\n%s", view)
	}
}

// The loop must GRANT the revision turn, not just render it: a model that
// notices a problem has to be able to fix it before the plan reaches a person.
func TestReviewTurn_LetsTheModelReviseAfterFinishing(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Branding"}),
		}},
		finish("Two columns."),
		// The turn the loop forces. Here the model decides the split was too
		// coarse and adds a third column rather than confirming.
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Launch"}),
			{Name: "finish", Input: map[string]any{"summary": "Split the third theme out."}},
		}},
	)
	h.seedBoard(t, boardID, "a note", "another note")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Group these", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.awaitState(t, run.ID, agent.StateProposed)

	if got := len(proposed.Plan.Actions); got != 3 {
		t.Fatalf("want 3 columns after the revision turn, got %d", got)
	}
	// And the run must not be labelled incomplete: it finished, reviewed, and
	// finished again — that is a complete run, not a truncated one.
	for _, n := range proposed.Plan.Notes {
		if strings.Contains(n, "may be incomplete") {
			t.Errorf("a reviewed run was reported as incomplete: %q", n)
		}
	}
}

// Labels the agent coins live in the PLAN until apply. A preview the user
// discards must leave their taxonomy exactly as it found it — otherwise
// "nothing is written until you accept" is false for one element type.
func TestLabels_CoinedOnlyOnApply(t *testing.T) {
	runIDGuess := fmt.Sprintf("%024x", 0xa9e07001)
	newLabel := agent.ActionID(runIDGuess+":label", 0)

	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_label", map[string]any{"name": "Blocked"}),
		}},
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("apply_label", map[string]any{
				"elementId": cardID(boardID, 0), "labelId": newLabel,
			}),
		}},
		finish("Tagged the blocked item."),
		confirm(),
	)
	h.seedBoard(t, boardID, "waiting on legal", "ready to go")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Flag what is blocked", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.awaitState(t, run.ID, agent.StateProposed)

	// Planned, not persisted.
	if n := h.labels.Count(owner); n != 0 {
		t.Fatalf("preview created %d label(s); a preview must write nothing", n)
	}
	if len(proposed.Plan.NewLabels) != 1 {
		t.Fatalf("want 1 label carried on the plan, got %d", len(proposed.Plan.NewLabels))
	}

	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := h.labels.Count(owner); n != 1 {
		t.Fatalf("apply created %d label(s), want 1", n)
	}
	el, err := h.elements.Get(ctx, cardID(boardID, 0))
	if err != nil {
		t.Fatalf("get card: %v", err)
	}
	if len(el.LabelIDs) != 1 || el.LabelIDs[0] != newLabel {
		t.Fatalf("card carries %v, want [%s]", el.LabelIDs, newLabel)
	}
}

// A label id the model was never shown can only have come from board content —
// the same signal as an out-of-scope element id, and refused the same way.
func TestLabels_RefusesAnIdItWasNeverShown(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("apply_label", map[string]any{
				"elementId": cardID(boardID, 0), "labelId": "ffffffffffffffffffffffff",
			}),
		}},
		finish("done"),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Tag it", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Nothing staged means the run has nothing to propose, which is PARTIAL.
	final := h.awaitState(t, run.ID, agent.StatePartial, agent.StateProposed)
	if final.Plan != nil && len(final.Plan.Actions) > 0 {
		t.Fatalf("a foreign label id was staged: %+v", final.Plan.Actions)
	}
}

// Refinement must keep the run's identity and accumulate cost against it, and
// the second pass must see what the first proposed. Discard-and-retype was the
// old workaround; it lost both properties.
func TestRefine_ReplansWithTheSteerAndKeepsTheRun(t *testing.T) {
	h := newHarness(t,
		// First pass: two columns.
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Branding"}),
		}},
		finish("Two columns."),
		confirm(),
		// Second pass, after the steer: three.
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Branding"}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Launch"}),
		}},
		finish("Three columns."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note", "another note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Group these", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first := h.awaitState(t, run.ID, agent.StateProposed)
	if len(first.Plan.Actions) != 2 {
		t.Fatalf("first pass staged %d, want 2", len(first.Plan.Actions))
	}

	refined, err := h.svc.Refine(ctx, h.principal, run.ID, "Split the launch work out too")
	if err != nil {
		t.Fatalf("refine: %v", err)
	}
	if refined.ID != run.ID {
		t.Fatalf("refine minted a new run %s; it must keep %s", refined.ID, run.ID)
	}
	// The run is ALREADY proposed, so waiting on the state alone would return
	// the first pass's plan. Wait for the plan itself to change.
	second := h.awaitPlan(t, run.ID, 3)
	if len(second.Task.Refinements) != 1 {
		t.Fatalf("the steer was not recorded on the run: %+v", second.Task.Refinements)
	}
	// Still one run, so still one board slot and one cost meter.
	if !second.Active {
		t.Fatal("a refined run should still hold its board slot")
	}
}

// Refining anything other than a live proposal is a mistake, not a no-op: it
// would silently replan work the user already applied.
func TestRefine_RejectsWhatIsNotProposed(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
		}},
		finish("One column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	run, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Group these", Autonomy: agent.AutonomyPreview,
	})
	h.awaitState(t, run.ID, agent.StateProposed)
	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := h.svc.Refine(ctx, h.principal, run.ID, "actually make it two"); err == nil {
		t.Fatal("refining an applied run must fail")
	}
	// And an empty steer is not a steer.
	run2, _ := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID + "x", Intent: "x", Autonomy: agent.AutonomyPreview,
	})
	if run2 != nil {
		if _, err := h.svc.Refine(ctx, h.principal, run2.ID, "   "); err == nil {
			t.Fatal("an empty steer must be rejected")
		}
	}
}

// S2: per-element timestamps catch edits but not insertions. A card added to
// the board while the plan sat unapplied touches nothing the plan names, so
// every timestamp still matches — and the plan would commit against a board it
// never saw, silently orphaning the new card.
func TestMembership_InsertionInvalidatesAPendingPlan(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
			call("move_element", map[string]any{
				"elementId": cardID(boardID, 0), "parentId": agent.ActionID(fmt.Sprintf("%024x", 0xa9e07001), 0),
			}),
		}},
		finish("One column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note", "another note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Group these", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.awaitState(t, run.ID, agent.StateProposed)

	// A colleague adds a card. Nothing the plan targets is touched.
	if err := h.elements.Insert(ctx, &domain.Element{
		ID: "eeeeeeeeeeeeeeeeeeeeeee1", Type: domain.TypeCard,
		Location:  domain.Location{ParentID: boardID, Section: domain.SectionUnsorted},
		Content:   domain.Content{"textPreview": "added while you were reading"},
		CreatedBy: owner, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil); err == nil {
		t.Fatal("apply succeeded against a board that gained an item; it must report the plan stale")
	}
	// And the run returns to the human rather than wedging.
	after, err := h.svc.Get(ctx, h.principal, run.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.State != agent.StateProposed {
		t.Fatalf("run left in %s; a rejected apply must return it to PROPOSED", after.State)
	}
}

// S3: containment already drops foreign ids. The remaining risk is that the
// REST of a run steered by board content applies without anyone looking.
func TestInjection_RepeatedAttemptsQuarantineThePlan(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("move_element", map[string]any{"elementId": "dddddddddddddddddddddd01", "parentId": boardID}),
			call("move_element", map[string]any{"elementId": "dddddddddddddddddddddd02", "parentId": boardID}),
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
		}},
		finish("Made a column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	// AUTO mode: without quarantine this would apply with nobody looking.
	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Organize", Autonomy: agent.AutonomyAuto,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	final := h.awaitState(t, run.ID, agent.StateProposed, agent.StateCompleted, agent.StatePartial)
	if final.State == agent.StateCompleted {
		t.Fatal("a run that board content repeatedly steered was auto-applied")
	}
	if final.Plan == nil || !final.Plan.Quarantined {
		t.Fatalf("plan was not quarantined after repeated out-of-scope ids: %+v", final.Plan)
	}
}

// I2: the agent could guess or refuse, with nothing in between. One question,
// asked before anything is staged, beats a confidently wrong plan.
func TestAsk_PausesForAnAnswerAndTheAnswerIsARefinement(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			{Name: "ask", Input: map[string]any{
				"question": "Group by theme or by date?",
				"options":  []string{"By theme", "By date"},
			}},
		}},
		// After the answer arrives as a refinement, the run plans for real.
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
		}},
		finish("Grouped by theme."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note", "another note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Organize this", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	asked := h.awaitState(t, run.ID, agent.StateProposed)
	if asked.Plan == nil || asked.Plan.Question == nil {
		t.Fatalf("run did not surface a question: %+v", asked.Plan)
	}
	if len(asked.Plan.Actions) != 0 {
		t.Fatal("a question must not stage changes")
	}
	if len(asked.Plan.Question.Options) != 2 {
		t.Fatalf("want 2 options, got %v", asked.Plan.Question.Options)
	}

	// Answering reuses the refinement path — no second mechanism needed.
	if _, err := h.svc.Refine(ctx, h.principal, run.ID, "By theme"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	answered := h.awaitPlan(t, run.ID, 1)
	if answered.Plan.Question != nil {
		t.Fatal("the question should be gone once answered")
	}
}

// One question per run, and never after staging has begun.
func TestAsk_OnlyOnceAndOnlyBeforeStaging(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
			{Name: "ask", Input: map[string]any{"question": "Actually, by date?"}},
		}},
		finish("One column."),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Organize", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	final := h.awaitState(t, run.ID, agent.StateProposed)
	if final.Plan.Question != nil {
		t.Fatal("asking after staging must be refused, not honoured")
	}
	if len(final.Plan.Actions) != 1 {
		t.Fatalf("the staged work should survive the refused question, got %d", len(final.Plan.Actions))
	}
}
