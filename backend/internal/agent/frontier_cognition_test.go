package agent

import (
	"context"
	"strings"
	"testing"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// CG8 · CG9 · CG10 · CG11 · CG13 · CG14 · LP13 — the cognition wave's probes.

// tiers records which policy each turn was routed to, so a probe can assert on
// the routing rather than on a comment claiming it happens.
type tierRecorder struct {
	inner cognition.Provider
	seen  []cognition.Request
}

func (t *tierRecorder) Name() string  { return t.inner.Name() }
func (t *tierRecorder) Model() string { return t.inner.Model() }
func (t *tierRecorder) Complete(ctx context.Context, req cognition.Request) (*cognition.Response, error) {
	t.seen = append(t.seen, req)
	return t.inner.Complete(ctx, req)
}

func (t *tierRecorder) tierOf(label string) cognition.Tier {
	for _, r := range t.seen {
		if strings.HasPrefix(r.Label, label) {
			return r.Tier
		}
	}
	return ""
}

// CG8 — the probe the hard clause names: the review turn must reach the strong
// policy, and the turns before anything is staged must not.
//
// The tier follows the WORK rather than the step number. Everything up to the
// first staged action is reading — the board listing, a read_board, deciding
// which register the request is in — and a cheap model transcribes that
// identically. From the first staged change the run is authoring structure,
// which is the judgement a stronger model measurably does better and the reason
// both remaining flaky probes are judgement probes.
func TestCG8_TheReviewTurnRunsOnTheStrongPolicy(t *testing.T) {
	scope, repo := starvedScope(t)
	board := scope.Board.ID

	rec := &tierRecorder{inner: cognition.NewScripted(
		// Turn 0: a pure read. Nothing staged, so this is transcription.
		scriptedTools(cognition.ScriptedCall{Name: "read_board",
			Input: map[string]any{"boardId": board}}),
		// Turn 1: the first write. From here the run is authoring.
		scriptedTools(cognition.ScriptedCall{Name: "create_column",
			Input: map[string]any{"parentId": board, "title": "Casting"}}),
		scriptedTools(cognition.ScriptedCall{Name: "finish",
			Input: map[string]any{"summary": "one column", "confidence": "sure"}}),
		// The review turn the loop forces after finish.
		scriptedTools(cognition.ScriptedCall{Name: "finish",
			Input: map[string]any{"summary": "one column", "confidence": "sure"}}),
	)}

	task := TaskSpec{
		Intent: "set up casting", Owner: "alice", RootBoardID: board, Scope: ScopeBoard,
		SkipOutline: true,
		Budget:      Budget{MaxSteps: 6, MaxActions: 60, MaxTokens: 4000, MaxCostUSD: 1},
	}
	if _, _, err := NewPlanner(rec, repo, nil, nil, nil, nil).
		Run(context.Background(), scope, task, "run-tier", func(EventType, string, map[string]any) {}, nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := rec.tierOf("plan.step.0"); got != cognition.TierFast {
		t.Errorf("the opening read ran on the %q policy; a turn that stages nothing is "+
			"transcription and was paying authoring rates", got)
	}
	// Step 3 is the review turn: the loop appends the review as a user message and
	// takes another turn, by which point actions are staged.
	if got := rec.tierOf("plan.step.3"); got != cognition.TierStrong {
		t.Errorf("the review turn ran on the %q policy — judgement is its entire point, "+
			"and both flaky probes are judgement probes", got)
	}
}

// CG9 — CONTEXT ISOLATION IS THE ENTIRE MECHANISM.
//
// The review turn appends to the SAME message list, so the model judging the
// plan has its own authoring reasoning in context and is being asked to disagree
// with itself. Leaking that transcript into the judge's request makes the second
// opinion worthless: it would simply be the author with a different label.
func TestCG9_TheJudgeNeverSeesTheAuthorsReasoning(t *testing.T) {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard, Content: domain.Content{"title": "Film"}},
		Elements: map[string]*domain.Element{},
	}
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Casting"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Locations"},
	}}
	provider := cognition.NewScripted().OnAside(cognition.ScriptedStep{
		Tools: []cognition.ScriptedCall{{Name: "judge", Input: map[string]any{
			"verdict": "weak", "weakest": "both columns are empty", "missing": "the actual work",
		}}},
	})

	text, _, err := SecondOpinion(context.Background(), provider, plan, scope,
		TaskSpec{Intent: "set up the film", Budget: DefaultBudget()})
	if err != nil {
		t.Fatalf("second opinion: %v", err)
	}
	req, _ := provider.LastCall()

	if len(req.Messages) != 1 {
		t.Fatalf("the judge received %d messages; a fresh context is one turn, or it is "+
			"the authoring conversation wearing a hat", len(req.Messages))
	}
	if len(req.Tools) != 1 || req.ForceTool != "judge" {
		t.Errorf("the judge was offered %d tool(s) with ForceTool=%q — it must have NO staging "+
			"authority whatsoever, which means the capability is absent rather than refused",
			len(req.Tools), req.ForceTool)
	}
	if req.Tier != cognition.TierStrong {
		t.Errorf("the judge ran on the %q policy; judging is the one thing the strong "+
			"policy exists for", req.Tier)
	}
	if strings.Contains(req.System, "You act by calling tools") {
		t.Error("the judge was handed the author's rulebook, so it will re-derive the " +
			"author's reasoning — which is the failure this exists to avoid")
	}
	// And the answer must arrive as a quoted third party. A harness assertion gets
	// argued with; quoted words are the one register observed to change what this
	// model does.
	if !strings.Contains(text, "⟨reviewer⟩") {
		t.Errorf("the verdict is not attributed to anybody:\n%s", text)
	}
	if !strings.Contains(text, "both columns are empty") {
		t.Errorf("the verdict did not carry the finding:\n%s", text)
	}
}

// CG9 — and it is not paid for on every run. The gate is the checks the harness
// already computes and already treats as evidence of a suspect plan.
func TestCG9_ASoundSmallPlanBuysNoSecondOpinion(t *testing.T) {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
	}
	// One document is a complete answer to "write the treatment" — WholeInOne, no
	// critique, no mismatch, well under the size floor.
	small := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActWriteDocument, ElementID: "d1", ParentID: "b1",
			Title: "Treatment", Text: strings.Repeat("real prose. ", 40)},
	}}
	if wantsSecondOpinion(small, scope, TaskSpec{Intent: "write the treatment", Budget: DefaultBudget()}) {
		t.Error("a sound one-action plan bought a judge; the cost has to scale with " +
			"suspicion, not with the run rate")
	}
}

// CG10 — the hard clause: an Arabic numbered board must produce Arabic numbered
// columns, OR the critique must name the violation.
//
// The prompt half of house style has failed three times in this codebase's
// history. This is the half with teeth: arithmetic over what was actually
// staged, appended to the MEASURED block the review turn already makes the model
// answer.
func TestCG10_AnArabicNumberedBoardNamesTheViolation(t *testing.T) {
	scope := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Items: []Item{
			{ID: "c1", Type: domain.TypeColumn, Text: "١ ـ التحضير"},
			{ID: "c2", Type: domain.TypeColumn, Text: "٢ ـ التصوير"},
			{ID: "c3", Type: domain.TypeColumn, Text: "٣ ـ المونتاج"},
			{ID: "n1", Type: domain.TypeCard, Text: "اجتماع مع فريق الإنتاج لمناقشة الميزانية والجدول الزمني"},
			{ID: "n2", Type: domain.TypeCard, Text: "حجز مواقع التصوير في مسقط وصلالة قبل نهاية الشهر"},
		},
		Elements: map[string]*domain.Element{},
	}

	c := MeasureConventions(scope)
	if !c.Numbered {
		t.Error("three numbered columns did not read as a numbering convention")
	}
	if c.Script != "arabic" {
		t.Fatalf("the dominant script measured as %q — the bilingual signal is the one "+
			"axis with the largest visible failure and the cheapest computation", c.Script)
	}
	if !strings.Contains(c.Render(), "ARABIC") {
		t.Errorf("the HOUSE STYLE block does not state the language:\n%s", c.Render())
	}

	// An English-phrased request produces English unnumbered columns. The plan is
	// wrong twice over and both must be named.
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "n1", ParentID: "b1", Title: "Distribution"},
		{Seq: 1, Kind: ActCreateColumn, ElementID: "n2", ParentID: "b1", Title: "Marketing"},
	}}
	found := strings.Join(ConformanceCritique(plan, scope), "\n")
	if !strings.Contains(found, "leading number") {
		t.Errorf("the numbering violation was not named:\n%s", found)
	}
	if !strings.Contains(found, "arabic") {
		t.Errorf("the language violation was not named — the request's language is not "+
			"the board's language, and the board wins:\n%s", found)
	}

	// A conforming plan must be silent, or the critique is noise people learn to
	// skip past.
	good := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "n1", ParentID: "b1", Title: "٤ ـ التوزيع"},
	}}
	if lines := ConformanceCritique(good, scope); len(lines) != 0 {
		t.Errorf("a conforming plan was criticised: %v", lines)
	}
}

// CG10 — a board with no habits states none. A HOUSE STYLE block that says
// nothing costs tokens on every turn and teaches the model to skip the heading.
func TestCG10_AYoungBoardAssertsNoConvention(t *testing.T) {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Items:    []Item{{ID: "c1", Type: domain.TypeColumn, Text: "Ideas"}},
		Elements: map[string]*domain.Element{},
	}
	if got := MeasureConventions(scope).Render(); got != "" {
		t.Errorf("a one-column board asserted a house style:\n%s", got)
	}
}

// CG11 — THE REJECTION IS THE FEATURE.
//
// The field exists to make the interpretation QUOTABLE. A plan that says "I took
// a reading" and cannot say which reading has moved the ambiguity out of the
// summary and into a chip, which is worse than leaving it where it was.
func TestCG11_AnUnquotedReadingIsRefused(t *testing.T) {
	if _, _, err := resolveConfidence("reading", ""); err == nil {
		t.Fatal("confidence=reading with no reading was accepted; the whole point is that " +
			"an unquoted interpretation is worth nothing")
	} else if !strings.Contains(err.Error(), "reading") {
		t.Errorf("the refusal does not name the field to fill: %v", err)
	}
	if _, _, err := resolveConfidence("guess", "   "); err == nil {
		t.Error("whitespace passed as a declared reading")
	}
	conf, reading, err := resolveConfidence("reading",
		"taking this as: finish filling the columns the last run left empty")
	if err != nil || conf != ConfidenceReading || !strings.HasPrefix(reading, "taking this as") {
		t.Errorf("a quoted reading was not accepted: %q %q %v", conf, reading, err)
	}
	// Silence is not a retraction. The loop forces a SECOND finish after the
	// review turn, and that turn is asked to look at the arrangement — not to
	// restate its certainty. Overwriting on silence is the bug that blanked
	// `remember` and `unmet` on every run that took the review turn.
	if conf, _, err := resolveConfidence("", ""); err != nil || conf != "" {
		t.Errorf("an omitted confidence was treated as an answer: %q %v", conf, err)
	}
	if _, _, err := resolveConfidence("fairly-sure", "x"); err == nil {
		t.Error("an invented confidence level was accepted")
	}
}

// CG13 — the hard clause: pre-staging must reproduce the prior plan's ids
// EXACTLY, or per-action revert and the fingerprint both break.
//
// Per-action revert indexes into the proposed plan by element id and the
// fingerprint is taken over the same ids, so a refinement that re-derived them
// would break both — silently, because the plan would still look right.
func TestCG13_ARefinementCarriesTheExactIdsForward(t *testing.T) {
	prior := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "col-aaa", ParentID: "b1", Title: "Casting"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "note-bbb", ParentID: "col-aaa", Text: "Book the reader"},
		{Seq: 2, Kind: ActMove, ElementID: "existing-1", ParentID: "col-aaa"},
	}}
	s := &staging{
		runID: "run-1", plan: &Plan{RunID: "run-1"}, created: map[string]ActionKind{},
		task:  TaskSpec{Budget: DefaultBudget()},
		scope: &BoardScope{Board: &domain.Element{ID: "b1"}, Elements: map[string]*domain.Element{}},
	}
	if n := s.preStage(prior); n != 3 {
		t.Fatalf("pre-staged %d of 3 actions", n)
	}
	for i, want := range []string{"col-aaa", "note-bbb", "existing-1"} {
		if got := s.plan.Actions[i].ElementID; got != want {
			t.Errorf("action %d carried forward as %q, want %q — a re-minted id breaks "+
				"per-action revert and the fingerprint together", i, got, want)
		}
	}
	// And the created set has to know about them, or undo_staged cannot withdraw
	// what the person is refining and a later action cannot parent to it.
	if s.created["col-aaa"] != ActCreateColumn {
		t.Error("the carried-forward column is not withdrawable; undo_staged would refuse " +
			"the very rows the refinement exists to remove")
	}
	if _, isCreate := s.created["existing-1"]; isCreate {
		t.Error("a MOVE was registered as this run's own creation, so undo_staged would " +
			"offer to withdraw an element that already exists on the board")
	}
	// The rendering the model reads must carry the ids, or it cannot address the
	// plan and falls back to re-authoring it.
	if !strings.Contains(describeStaged(s.plan), "col-aaa") {
		t.Errorf("the staged list is unaddressable:\n%s", describeStaged(s.plan))
	}
}

// CG14 — two complete action sets with independent layout and one shared
// fingerprint.
func TestCG14_VariantsAreSealedMeasuredAndFingerprintedTogether(t *testing.T) {
	p := &Plan{RunID: "r1"}
	shapeA := make([]Action, 0, variantFloor)
	for i := 0; i < variantFloor; i++ {
		shapeA = append(shapeA, Action{Seq: i, Kind: ActMove, ElementID: "card-" + string(rune('a'+i)), ParentID: "col-1"})
	}
	p.Variants = []Variant{{Label: "one column per scene", Actions: shapeA}}
	p.Actions = []Action{
		{Seq: 0, Kind: ActMove, ElementID: "card-a", ParentID: "col-x"},
		{Seq: 1, Kind: ActMove, ElementID: "card-z", ParentID: "col-x"},
	}
	SealVariants(p)

	if len(p.Variants) != 2 {
		t.Fatalf("sealing produced %d variants, want 2", len(p.Variants))
	}
	if len(p.Actions) != len(shapeA) {
		t.Errorf("the plan proper is %d actions; it must be Variants[0], so every consumer "+
			"downstream keeps reading Plan.Actions and none of them learns about the choice",
			len(p.Actions))
	}
	// The fingerprint unions across shapes, because the person may apply either
	// one — a fingerprint over the first would let the second commit against an
	// element that had moved under it.
	targets := strings.Join(p.TargetIDs(), ",")
	if !strings.Contains(targets, "card-z") {
		t.Errorf("TargetIDs = %s; the alternative's targets are unpinned", targets)
	}
	dests := strings.Join(p.DestinationParentIDs("b1"), ",")
	if !strings.Contains(dests, "col-1") || !strings.Contains(dests, "col-x") {
		t.Errorf("DestinationParentIDs = %s; both shapes' destinations must be pinned", dests)
	}
}

// CG14 — one shape is not a choice, and a picker over one option is a control
// that does nothing.
func TestCG14_ASingleShapeOffersNoPicker(t *testing.T) {
	p := &Plan{Variants: []Variant{{Label: "only", Actions: []Action{{Seq: 0, Kind: ActCreateColumn, Title: "A"}}}}}
	SealVariants(p)
	if p.Variants != nil {
		t.Error("a lone shape was offered as an alternative to nothing")
	}
	if len(p.Actions) != 1 {
		t.Error("the lone shape's actions were lost")
	}
}

// CG14 — the picker has to reach the write path.
//
// The server built K shapes, laid every one out and measured them, and apply
// committed Variants[0] whichever one the person chose. That is the failure mode
// that hides: never wrong, only always less than what was offered, so the
// segmented control looked live and the choice went nowhere.
func TestCG14_ApplyCommitsTheShapeThePersonPicked(t *testing.T) {
	shape := func(parent string) []Action {
		return []Action{{Seq: 0, Kind: ActMove, ElementID: "card-a", ParentID: parent}}
	}
	newPlan := func() *Plan {
		return &Plan{RunID: "r1", Actions: shape("col-first"), Variants: []Variant{
			{Label: "per scene", Actions: shape("col-first")},
			{Label: "per act", Actions: shape("col-second")},
		}}
	}

	// No index: an unchanged client keeps the shape the run led with.
	run := &Run{Plan: newPlan()}
	if err := chooseVariant(run, nil); err != nil {
		t.Fatalf("nil index: %v", err)
	}
	if run.Plan.Actions[0].ParentID != "col-first" {
		t.Errorf("a client that sent no choice got %q, not the shape the plan led with",
			run.Plan.Actions[0].ParentID)
	}

	// The second shape, chosen.
	run = &Run{Plan: newPlan()}
	pick := 1
	if err := chooseVariant(run, &pick); err != nil {
		t.Fatalf("index 1: %v", err)
	}
	if got := run.Plan.Actions[0].ParentID; got != "col-second" {
		t.Errorf("applied destination %q, want col-second — the picked shape never "+
			"reached Plan.Actions, so the choice was decorative", got)
	}
	if run.Plan.ChosenVariant != 1 {
		t.Errorf("ChosenVariant = %d; the correction record does not say what was taken",
			run.Plan.ChosenVariant)
	}
	// The shapes turned down survive: the fingerprint was taken over the union,
	// and a shape somebody looked at and rejected is the clearest preference
	// signal the product collects.
	if len(run.Plan.Variants) != 2 {
		t.Errorf("applying dropped the alternatives; %d left", len(run.Plan.Variants))
	}

	// An index this plan never offered is refused rather than clamped — a
	// silently clamped choice commits a shape nobody picked.
	run = &Run{Plan: newPlan()}
	for _, bad := range []int{-1, 2} {
		if err := chooseVariant(run, &bad); err == nil {
			t.Errorf("variant index %d was accepted", bad)
		}
	}
}

// LP13 — the disagreement IS the question, computed server-side from compiled
// artifacts rather than by asking the model how unsure it feels.
//
// Agreement is silent. Word count is not ambiguity; two shapes the same run
// built, compared field by field, are a real uncertainty signal and they are
// free because the shapes already exist.
func TestLP13_VariantDisagreementDecidesWhetherToAsk(t *testing.T) {
	coarse := Variant{Label: "three acts", Actions: []Action{
		{Kind: ActCreateColumn, ParentID: "b1", Title: "Act I"},
		{Kind: ActCreateColumn, ParentID: "b1", Title: "Act II"},
	}}
	fine := Variant{Label: "one per scene", Actions: []Action{
		{Kind: ActCreateColumn, ParentID: "b1", Title: "Scene 1"},
		{Kind: ActCreateColumn, ParentID: "b1", Title: "Scene 2"},
		{Kind: ActCreateColumn, ParentID: "b1", Title: "Scene 3"},
	}}

	d := CompareVariants(coarse, fine)
	if !d.Concentrated() {
		t.Fatalf("two shapes differing only in how coarsely they group did not read as one "+
			"decision: %+v", d)
	}
	q := AskWhichShape(&Plan{Variants: []Variant{coarse, fine}})
	if q == nil {
		t.Fatal("a concentrated disagreement asked nothing")
	}
	if !strings.Contains(q.Text, "coarsely") {
		t.Errorf("the question does not name the decision: %q", q.Text)
	}
	if len(q.Options) != 2 || !strings.Contains(q.Options[0], "three acts") {
		t.Errorf("the options are not the shapes themselves: %v — the person must be picking "+
			"a structure rather than answering an abstraction", q.Options)
	}

	// Two phrasings of one answer is not a decision, and asking about it is the
	// noise that teaches people to dismiss the card.
	same := Variant{Label: "b", Actions: coarse.Actions}
	if AskWhichShape(&Plan{Variants: []Variant{coarse, same}}) != nil {
		t.Error("identical shapes produced a question")
	}
}

// CG14 — alternatives are for STRUCTURAL decisions. A second shape offered over
// three cards teaches people to click through the picker without looking, which
// destroys the feature on the plans where it matters.
func TestCG14_SmallPlansCannotOfferAlternatives(t *testing.T) {
	s := &staging{
		runID: "r1", plan: &Plan{Actions: []Action{{Seq: 0, Kind: ActCreateColumn, Title: "A"}}},
		created: map[string]ActionKind{}, task: TaskSpec{Budget: DefaultBudget()},
	}
	if _, err := s.SnapshotVariant("tiny", "why"); err == nil {
		t.Fatal("a one-action plan was offered as one of two shapes")
	}
}
