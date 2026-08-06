package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// CV6 · CV7 · CG12 · IN17 · DA17 · DA19 · DA26 · DA27 — the breadth probes.

// CV7, and it is the hard clause: a lossy round trip that SILENTLY SUCCEEDS is
// worse than not having the capability at all.
//
// set_note_text replaces the whole body. Handed a note somebody underlined,
// highlighted, coloured or laid out as a table, the translator would write back
// plain paragraphs — destroying work that took real effort, on a surface whose
// review row says only "edit note X".
func TestCV7_RewritingUnexpressibleFormattingIsRefused(t *testing.T) {
	// A document with an underline mark and a highlight — both in the editor's
	// schema, neither in the markdown subset.
	doc := map[string]any{"type": "doc", "content": []any{
		map[string]any{"type": "paragraph", "content": []any{
			map[string]any{"type": "text", "text": "the harbour scene",
				"marks": []any{map[string]any{"type": "underline"}}},
			map[string]any{"type": "text", "text": " runs at dusk",
				"marks": []any{map[string]any{"type": "highlight"}}},
		}},
	}}
	lost := InexpressibleFormatting(doc)
	if !strings.Contains(lost, "underlining") || !strings.Contains(lost, "highlighting") {
		t.Fatalf("the scan named %q; both marks must be named or the refusal cannot "+
			"tell the person what would have been destroyed", lost)
	}

	el := &domain.Element{ID: "d1", Type: domain.TypeDocument,
		Content:  domain.Content{"doc": doc, "textPreview": "the harbour scene runs at dusk"},
		Location: domain.Location{ParentID: "b1"}}
	s := newBreadthStaging(el)
	s.markRead(el.ID, 0, 10_000) // the read gate is a separate rule; satisfy it

	out := s.runSetText(context.Background(), &toolArgs{ElementID: "d1", Text: "a new body"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("a formatted document was rewritten; the formatting is gone and nothing " +
			"in the review list says so")
	}
	if !strings.Contains(out.Content, "underlining") {
		t.Errorf("the refusal does not say what would be lost:\n%s", out.Content)
	}
	if len(s.plan.Actions) != 0 {
		t.Error("the refusal still staged the rewrite")
	}
}

// CV7 — and the plain case must still work, or the refusal is just a wall.
func TestCV7_APlainDocumentIsStillRewritable(t *testing.T) {
	el := &domain.Element{ID: "d1", Type: domain.TypeDocument,
		Content:  domain.Content{"doc": tiptapDoc("one\n\ntwo"), "textPreview": "one\n\ntwo"},
		Location: domain.Location{ParentID: "b1"}}
	s := newBreadthStaging(el)
	s.markRead(el.ID, 0, 10_000)
	out := s.runSetText(context.Background(), &toolArgs{ElementID: "d1", Text: "# Title\n\n- a\n- b"},
		&reply{staging: s})
	if out.IsError {
		t.Fatalf("a plain document was refused: %s", out.Content)
	}
	if len(s.plan.Actions) != 1 {
		t.Fatalf("staged %d actions", len(s.plan.Actions))
	}
}

// CV7 — the compiler must actually WRITE the structure, or the subset is a
// promise in a tool description. The agent could only ever produce paragraphs on
// a product whose editor ships headings, both lists, quotes, code and links.
func TestCV7_TheCompilerWritesRealStructure(t *testing.T) {
	body := "# Treatment\n" +
		"A **harbour** at *dusk*.\n" +
		"- one\n" +
		"- two\n" +
		"1. first\n" +
		"2. second\n" +
		"> somebody said this\n" +
		"See [the brief](https://example.invalid/brief)."
	doc := MarkdownToTiptap(body)

	kinds := map[string]bool{}
	var walk func(any)
	walk = func(n any) {
		switch v := n.(type) {
		case []any:
			for _, c := range v {
				walk(c)
			}
		case map[string]any:
			if k, _ := v["type"].(string); k != "" {
				kinds[k] = true
			}
			if marks, ok := v["marks"].([]any); ok {
				for _, m := range marks {
					if mm, ok := m.(map[string]any); ok {
						if k, _ := mm["type"].(string); k != "" {
							kinds[k] = true
						}
					}
				}
			}
			walk(v["content"])
		}
	}
	walk(doc)
	for _, want := range []string{"heading", "bulletList", "orderedList", "listItem",
		"blockquote", "bold", "italic", "link"} {
		if !kinds[want] {
			t.Errorf("the translator produced no %s node; the format bar offers it and "+
				"the agent still cannot reach it", want)
		}
	}
	// One list node holding many items, not one list per bullet: the second
	// renders correctly and cannot be edited.
	lists := 0
	var count func(any)
	count = func(n any) {
		switch v := n.(type) {
		case []any:
			for _, c := range v {
				count(c)
			}
		case map[string]any:
			if k, _ := v["type"].(string); k == "bulletList" {
				lists++
			}
			count(v["content"])
		}
	}
	count(doc)
	if lists != 1 {
		t.Errorf("two bullets produced %d bulletList nodes; a list per line is a document "+
			"that looks right and cannot be edited", lists)
	}

	// And it round-trips: the digest renders the doc back as the same subset, so
	// the agent can SEE that a document has structure rather than reading a
	// flattened preview and writing back the wall it thought it saw.
	back := TiptapToMarkdown(doc)
	for _, want := range []string{"# Treatment", "- one", "1. first", "> somebody said this",
		"**harbour**", "[the brief](https://example.invalid/brief)"} {
		if !strings.Contains(back, want) {
			t.Errorf("the round trip lost %q:\n%s", want, back)
		}
	}
}

// CV7 — plain text with no markdown must produce exactly the paragraphs it
// always did, or every note already on every board changes shape.
func TestCV7_PlainTextIsUnchanged(t *testing.T) {
	for _, s := range []string{"one line", "two\nlines", "a\n\nb", "نصّ عربي"} {
		if got := domain.PlainTextOf(MarkdownToTiptap(s)); got != s {
			t.Errorf("round trip of %q produced %q", s, got)
		}
	}
}

// CV6 — the digest half. Without the read, a rewrite silently inherits whatever
// auto-detection makes of the new first word, undoing a decision somebody made
// on purpose — and on this product also flipping the card's numerals between ٠١٢٣
// and 0123.
func TestCV6_APinnedDirectionIsVisibleAndSettable(t *testing.T) {
	pinned := &domain.Element{ID: "c1", Type: domain.TypeCard,
		Content: domain.Content{"textPreview": "خطة التصوير", "textDirection": "rtl"}}
	if got := ItemFor(pinned).Direction; got != "rtl" {
		t.Errorf("the digest reads a pinned direction as %q — the agent cannot see that a "+
			"person pinned this, so a rewrite clobbers it", got)
	}
	auto := &domain.Element{ID: "c2", Type: domain.TypeCard,
		Content: domain.Content{"textPreview": "x", "textDirection": "auto"}}
	if got := ItemFor(auto).Direction; got != "" {
		t.Errorf("auto rendered as the pin %q; auto is the default and marking it on every "+
			"line would bury the ones that matter", got)
	}

	// The write compiles to the key the card's own control writes — and auto
	// compiles to nil, exactly as NoteCard does it, rather than to the literal
	// string "auto".
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{"c1": pinned},
	}
	ops, err := CompileOps(&Plan{Actions: []Action{
		{Seq: 0, Kind: ActSetDirection, ElementID: "c1", Text: "auto"},
	}}, scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	content, _ := ops[0].Changes["content"].(map[string]any)
	if v, present := content["textDirection"]; !present || v != nil {
		t.Errorf("auto compiled to %#v; the renderer treats anything not rtl/ltr as auto, so "+
			"a literal \"auto\" works by accident and breaks the moment somebody tightens the read", v)
	}
	undo, _ := ops[0].UndoChanges["content"].(map[string]any)
	if undo["textDirection"] != "rtl" {
		t.Errorf("the inverse restores %#v, not the pin it replaced", undo["textDirection"])
	}
}

// CV6 — a CLONE shares its SOURCE's content, so a direction written onto the
// instance lives on an element nothing renders from. The product's own menu does
// this redirect; doing anything else produces a change that appears to succeed
// and never shows up.
func TestCV6_ACloneRedirectsToItsSource(t *testing.T) {
	source := &domain.Element{ID: "src", Type: domain.TypeCard,
		Content: domain.Content{"textPreview": "خطة"}, Location: domain.Location{ParentID: "b1"}}
	clone := &domain.Element{ID: "cl", Type: domain.TypeClone,
		Content:  domain.Content{"cloneSourceId": "src"},
		Location: domain.Location{ParentID: "b1"}}
	s := newBreadthStaging(source, clone)

	out := s.runSetDirection(context.Background(),
		&toolArgs{ElementID: "cl", Direction: "rtl"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("setting a direction on a synced copy failed: %s", out.Content)
	}
	if len(s.plan.Actions) != 1 || s.plan.Actions[0].ElementID != "src" {
		t.Fatalf("the action targets %v; a clone's content lives at the source, so writing "+
			"to the instance is a change nobody can see", s.plan.Actions)
	}
}

// DA17 — the only place an approved change has an effect the review cannot
// describe. `fanOutCloneUpdates` re-broadcasts the update to every board holding
// an instance, and the edit lands at the SOURCE — so one row saying "edit note X"
// can rewrite that card on boards outside the run's own root.
func TestDA17_EditingASyncedCardStatesItsBlastRadius(t *testing.T) {
	card := &domain.Element{ID: "c1", Type: domain.TypeCard,
		Content:  domain.Content{"textPreview": "the harbour scene"},
		Location: domain.Location{ParentID: "b1"}}
	s := newBreadthStaging(card)
	s.scope.CloneSites = map[string][]string{"c1": {`"Casting"`, `"Ep 2"`}}
	s.markRead("c1", 0, 10_000)

	out := s.runSetText(context.Background(), &toolArgs{ElementID: "c1", Text: "a rewrite"},
		&reply{staging: s})
	if out.IsError {
		t.Fatalf("the edit was refused: %s", out.Content)
	}
	if !strings.Contains(out.Content, "Casting") {
		t.Errorf("the model was not told the edit reaches other boards:\n%s", out.Content)
	}
	joined := strings.Join(s.plan.Notes, "\n")
	if !strings.Contains(joined, "Ep 2") {
		t.Errorf("the plan carries no note about the fan-out, so the outcome card cannot "+
			"surface it:\n%s", joined)
	}
	// And the digest states it on the line where the decision to edit is made.
	s.scope.Items = []Item{{ID: "c1", Type: domain.TypeCard, Text: "the harbour scene", Trust: "user"}}
	if out := s.scope.Render(""); !strings.Contains(out, "🔗 also live on") {
		t.Errorf("the item line does not warn that this card is synced:\n%s", out)
	}
}

// DA19 — the hard clause: the tray's COUNT is stated even when its contents are
// elided. A queue's length is the fact that decides whether to act on it, and it
// was reaching the model as a `[unsorted]` suffix on lines sorted by id.
func TestDA19_TheTrayIsAQueueWithALengthAndAnOrder(t *testing.T) {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard, Content: domain.Content{"title": "Film"}},
		Elements: map[string]*domain.Element{},
		Elided:   map[string]int{},
		ElidedFacts: map[string]*Elision{
			"b1": {Count: 30, Unsorted: 30, Types: map[domain.ElementType]int{domain.TypeCard: 30}},
		},
	}
	// Two visible tray items, deliberately inserted newest-first so the ordering
	// claim is not satisfied by accident.
	for i, id := range []string{"z-newer", "a-older"} {
		idx := float64(2 - i)
		scope.Elements[id] = &domain.Element{ID: id, Type: domain.TypeCard,
			Content:  domain.Content{"textPreview": "captured note " + fmt.Sprint(i)},
			Location: domain.Location{ParentID: "b1", Section: domain.SectionUnsorted, Index: idx}}
		scope.Items = append(scope.Items, itemFor(scope.Elements[id], scope))
	}
	out := scope.Render("")

	if !strings.Contains(out, "UNSORTED — this board's capture tray, 32 item(s)") {
		t.Errorf("the tray's length is not stated (2 shown + 30 elided = 32):\n%s",
			lineContaining(out, "UNSORTED"))
	}
	// Reading order, which is what the tray IS. A queue sorted by id is not a
	// queue, and "file the oldest ten" is inexpressible against one.
	older := strings.Index(out, "a-older")
	newer := strings.Index(out, "z-newer")
	if older < 0 || newer < 0 || older > newer {
		t.Errorf("the tray is not in index order — oldest at %d, newest at %d", older, newer)
	}
	// And once, not twice: a card listed in the queue and again in the items is a
	// card the model may well count twice.
	if strings.Count(out, "a-older") != 1 {
		t.Errorf("a tray item appears %d times:\n%s", strings.Count(out, "a-older"), out)
	}
}

// DA19 — the ambient hint. DetectDrift measured canvas clutter and skipped the
// tray entirely, so the one board state that most obviously wants an agent never
// triggered a suggestion.
func TestDA19_AFillingTrayIsADriftSignal(t *testing.T) {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
	}
	for i := 0; i < driftTrayMin; i++ {
		id := fmt.Sprintf("t%02d", i)
		scope.Elements[id] = &domain.Element{ID: id, Type: domain.TypeCard,
			Location: domain.Location{ParentID: "b1", Section: domain.SectionUnsorted}}
		scope.Items = append(scope.Items, itemFor(scope.Elements[id], scope))
	}
	d := DetectDrift(scope)
	if d == nil || d.Kind != "tray" {
		t.Fatalf("a full capture tray produced %+v; loose cards on a canvas may be a layout "+
			"somebody meant, and a backlogged tray is nobody's intention", d)
	}
}

// DA19 — `arrange` refuses a list, correctly, and had no complement. The column,
// the checklist and the tray are the three containers whose whole meaning is
// their sequence, and they were the three the agent could not sequence.
func TestDA19_ReorderSequencesOneList(t *testing.T) {
	col := &domain.Element{ID: "col", Type: domain.TypeColumn, Location: domain.Location{ParentID: "b1"}}
	a := &domain.Element{ID: "a", Type: domain.TypeCard, Location: domain.Location{ParentID: "col", Index: 1}}
	b := &domain.Element{ID: "b", Type: domain.TypeCard, Location: domain.Location{ParentID: "col", Index: 2}}
	other := &domain.Element{ID: "x", Type: domain.TypeCard, Location: domain.Location{ParentID: "b1"}}
	s := newBreadthStaging(col, a, b, other)

	out := s.runReorder(context.Background(), &toolArgs{ElementIDs: []string{"b", "a"}},
		&reply{staging: s})
	if out.IsError {
		t.Fatalf("reordering a column failed: %s", out.Content)
	}
	if len(s.plan.Actions) != 2 || s.plan.Actions[0].ElementID != "b" {
		t.Fatalf("the moves were not staged in the order given: %+v", s.plan.Actions)
	}

	// Two containers is two decisions wearing one call, and the review row could
	// not describe it honestly.
	s2 := newBreadthStaging(col, a, b, other)
	if out := s2.runReorder(context.Background(), &toolArgs{ElementIDs: []string{"a", "x"}},
		&reply{staging: s2}); !out.IsError {
		t.Error("a reorder spanning two containers was accepted")
	}
	// And the canvas is refused with a pointer at the tool that does work there.
	s3 := newBreadthStaging(col, a, b, other,
		&domain.Element{ID: "y", Type: domain.TypeCard, Location: domain.Location{ParentID: "b1"}})
	out = s3.runReorder(context.Background(), &toolArgs{ElementIDs: []string{"x", "y"}},
		&reply{staging: s3})
	if !out.IsError || !strings.Contains(out.Content, "arrange") {
		t.Errorf("reordering the canvas did not route to arrange: %s", out.Content)
	}
}

// DA19 — the tray orders by index and hangs off the board id, so the parent test
// alone sent every card filed into it in with index 0 and let the database
// decide the queue's order.
func TestDA19_TheTrayGetsRealIndices(t *testing.T) {
	scope := &BoardScope{Board: &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{}}
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActMove, ElementID: "a", ParentID: "b1", Section: string(domain.SectionUnsorted)},
		{Seq: 1, Kind: ActMove, ElementID: "b", ParentID: "b1", Section: string(domain.SectionUnsorted)},
	}}
	OrderPlan(p, scope)
	if p.Actions[0].Index == p.Actions[1].Index {
		t.Fatalf("both tray items landed at index %v — the queue's order is the database's "+
			"to invent", p.Actions[0].Index)
	}
	// The canvas is untouched by the same pass, because a canvas places by
	// coordinate and an index there means nothing.
	canvas := &Plan{Actions: []Action{{Seq: 0, Kind: ActMove, ElementID: "c", ParentID: "b1"}}}
	OrderPlan(canvas, scope)
	if canvas.Actions[0].Index != 0 {
		t.Errorf("a canvas move was given index %v", canvas.Actions[0].Index)
	}
}

// CG12 — the budget edge used to be a hole. The same elements are in memory when
// the decision is taken, so describing them is free and turns "I cannot see" into
// "I know roughly what is there".
func TestCG12_ElidedMaterialIsSummarisedRatherThanCounted(t *testing.T) {
	repo := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	seed := func(el *domain.Element) {
		el.CreatedAt, el.UpdatedAt = now, now
		if err := repo.Insert(ctx, el); err != nil {
			t.Fatal(err)
		}
	}
	seed(&domain.Element{ID: "b1", Type: domain.TypeBoard, Content: domain.Content{"title": "Shoot"}})
	seed(&domain.Element{ID: "list", Type: domain.TypeTaskList,
		Content:  domain.Content{"title": "Call sheet"},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}})
	for i := 0; i < maxTasksShown+12; i++ {
		c := domain.Content{"text": fmt.Sprintf("task %d", i)}
		if i%3 == 0 {
			c["done"] = true
		}
		if i > maxTasksShown {
			c["reminderAt"] = "2026-08-1" + fmt.Sprint(i%10) + "T09:00:00Z"
		}
		seed(&domain.Element{ID: fmt.Sprintf("tk-%03d", i), Type: domain.TypeTask,
			Content: c, Location: domain.Location{ParentID: "list", Index: float64(i)}})
	}

	scope, err := CompileScope(ctx, repo, TaskSpec{Owner: "a", RootBoardID: "b1", Scope: ScopeBoard})
	if err != nil {
		t.Fatal(err)
	}
	roll := scope.ElidedFacts["list"]
	if roll == nil || roll.Count == 0 {
		t.Fatal("nothing was recorded about the elided material")
	}
	summary := roll.Summary()
	if !strings.Contains(summary, "tasks") {
		t.Errorf("the rollup does not say WHAT was cut: %q", summary)
	}
	if !strings.Contains(summary, "unassigned") {
		t.Errorf("the rollup drops ownership, which is half of what \"should I open this\" "+
			"turns on: %q", summary)
	}
	if !strings.Contains(scope.Render(""), summary) {
		t.Error("the rollup was computed and not rendered")
	}
}

// IN17 — the fourth context type: server-computed defects, stated before anybody
// asks. The agent met a board with an empty column and could only discover it by
// inference, so it either missed the defect or "found" one that was not there.
func TestIN17_TheBoardsOwnDefectsAreStated(t *testing.T) {
	scope := &BoardScope{
		Board:       &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements:    map[string]*domain.Element{},
		Elided:      map[string]int{},
		ChildCounts: map[string]map[domain.ElementType]int64{},
		Items: []Item{
			{ID: "c1", Type: domain.TypeColumn, Text: "Ideas"},
			{ID: "c2", Type: domain.TypeColumn, Text: "Done"},
			{ID: "n1", Type: domain.TypeCard, Text: "a card", ParentID: "c2"},
		},
	}
	lints := strings.Join(scope.Lints(), "\n")
	if !strings.Contains(lints, "Ideas") {
		t.Errorf("an empty column was not reported:\n%s", lints)
	}
	if strings.Contains(lints, "Done") {
		t.Errorf("a filled column was reported as empty:\n%s", lints)
	}
	// Observations, never a task list: a run asked to add one card must not go and
	// repair the board because the digest mentioned a defect.
	if block := scope.lintBlock(); !strings.Contains(block, "NOT a task list") {
		t.Errorf("the lint block reads as instructions:\n%s", block)
	}
}

// DA26 — the agent is told to reuse rather than coin and was given nothing to
// choose with, so it coin-flipped between near-synonyms. The number that settles
// it is maintained on every attach and was read by nobody.
func TestDA26_LabelsCarryTheirUsageAndSortByIt(t *testing.T) {
	scope := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Labels: []LabelRef{
			{ID: "l1", Name: "misc", Usage: 1},
			{ID: "l2", Name: "urgent", Usage: 41},
			{ID: "l3", Name: "casting", Usage: 12},
		},
		Elements: map[string]*domain.Element{},
	}
	line := lineContaining(scope.Render(""), "LABELS")
	if !strings.Contains(line, "urgent (41)") {
		t.Errorf("usage counts are not rendered: %s", line)
	}
	if strings.Index(line, "urgent") > strings.Index(line, "misc") {
		t.Errorf("the labels are not ordered by use, so the busiest one is not the first "+
			"one read: %s", line)
	}
}

// DA27 — Comment.Reactions ships with a service method and a REST endpoint, and
// the compiler writes resolved:false on every thread it creates. Nothing read
// either back. A thread carrying six 👍 is the board's own record of consensus.
func TestDA27_AThreadReportsItsTrafficAndWhetherItIsSettled(t *testing.T) {
	stats := ThreadStats{Messages: 3, Reactions: map[string]int{"👍": 6}, Resolved: false}
	got := stats.Render()
	for _, want := range []string{"💬 3", "👍6", "unresolved"} {
		if !strings.Contains(got, want) {
			t.Errorf("the thread line %q is missing %q", got, want)
		}
	}
	if !strings.Contains(ThreadStats{Messages: 1, Resolved: true}.Render(), "resolved") {
		t.Error("a settled thread does not say so — the difference between a live objection " +
			"and a decision already made is the whole question on a shared board")
	}

	// And the verb: a reversible content update, refused when it would change
	// nothing.
	thread := &domain.Element{ID: "t1", Type: domain.TypeCommentThread,
		Content: domain.Content{"resolved": false}, Location: domain.Location{ParentID: "b1"}}
	s := newBreadthStaging(thread)
	if out := s.runResolveThread(context.Background(), &toolArgs{ElementID: "t1"},
		&reply{staging: s}); out.IsError {
		t.Fatalf("resolving an open thread failed: %s", out.Content)
	}
	if len(s.plan.Actions) != 1 || !s.plan.Actions[0].Done {
		t.Fatalf("an omitted flag did not mean resolve: %+v", s.plan.Actions)
	}
	scope := &BoardScope{Board: &domain.Element{ID: "b1"},
		Elements: map[string]*domain.Element{"t1": thread}}
	ops, err := CompileOps(&Plan{Actions: s.plan.Actions}, scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	content, _ := ops[0].Changes["content"].(map[string]any)
	if content["resolved"] != true {
		t.Errorf("resolve compiled to %#v", content)
	}
	undo, _ := ops[0].UndoChanges["content"].(map[string]any)
	if undo["resolved"] != false {
		t.Errorf("the inverse does not reopen it: %#v", undo)
	}
}

// newBreadthStaging wires a staging object over a hand-built scope, which is the
// only way to exercise a handler's refusal branch without driving a whole run.
func newBreadthStaging(els ...*domain.Element) *staging {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard, Content: domain.Content{"title": "Film"}},
		Elements: map[string]*domain.Element{},
		Elided:   map[string]int{},
	}
	for _, el := range els {
		scope.Elements[el.ID] = el
	}
	return &staging{
		runID: "run-x", scope: scope, plan: &Plan{RunID: "run-x"},
		created: map[string]ActionKind{}, readSoFar: map[string]int{},
		failedCalls: map[string]int{}, placedThisRun: map[string]bool{},
		movedThisRun: map[string]bool{},
		task:         TaskSpec{Owner: "alice", RootBoardID: "b1", Budget: DefaultBudget()},
		emit:         func(EventType, string, map[string]any) {},
	}
}

// DA18 — the hard clause: the BATCH-SIZE ANNOTATION is what makes the verb
// honest. `trashBatchId` means a delete removed a container and everything in it
// as one unit, so "restore 1 thing" that returns thirteen is the same
// unreviewable surprise as an edit that silently fans out to four boards.
func TestDA18_RestoreStatesItsBlastRadiusAndRespectsWhoDeletedIt(t *testing.T) {
	repo := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	seed := func(el *domain.Element) {
		el.CreatedAt, el.UpdatedAt = now, now
		if err := repo.Insert(ctx, el); err != nil {
			t.Fatal(err)
		}
	}
	seed(&domain.Element{ID: "b1", Type: domain.TypeBoard,
		Content: domain.Content{"title": "Film"}, ACL: &domain.ACL{OwnerID: "alice"}})
	seed(&domain.Element{ID: "col", Type: domain.TypeColumn,
		Content: domain.Content{"title": "Casting"}, Location: domain.Location{ParentID: "b1"}})
	for i := 0; i < 6; i++ {
		seed(&domain.Element{ID: fmt.Sprintf("card-%d", i), Type: domain.TypeCard,
			Content:  domain.Content{"textPreview": fmt.Sprintf("actor %d", i)},
			Location: domain.Location{ParentID: "col"}})
	}
	// Alice deleted the column and its cards as ONE batch.
	ids := []string{"col", "card-0", "card-1", "card-2", "card-3", "card-4", "card-5"}
	if err := repo.SoftDelete(ctx, ids, "alice", "batch-1", now); err != nil {
		t.Fatal(err)
	}
	// And somebody else tidied one card away separately.
	seed(&domain.Element{ID: "theirs", Type: domain.TypeCard,
		Content:  domain.Content{"textPreview": "a colleague's cleanup"},
		Location: domain.Location{ParentID: "b1"}})
	if err := repo.SoftDelete(ctx, []string{"theirs"}, "bob", "batch-2", now); err != nil {
		t.Fatal(err)
	}

	s := newBreadthStaging()
	s.elements = repo
	s.task.Owner = "alice"
	s.task.Autonomy = AutonomyPreview

	out := s.runListTrash(ctx, &toolArgs{}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("listing the trash failed: %s", out.Content)
	}
	if !strings.Contains(out.Content, "brings back 7 items") {
		t.Errorf("the listing does not state the blast radius, so the model cannot say it "+
			"before proposing:\n%s", out.Content)
	}
	if strings.Contains(out.Content, "colleague") {
		t.Errorf("somebody else's deliberate cleanup was offered for resurrection:\n%s", out.Content)
	}

	if out := s.runRestore(ctx, &toolArgs{ElementID: "col"}, &reply{staging: s}); out.IsError {
		t.Fatalf("restoring failed: %s", out.Content)
	}
	if len(s.plan.Actions) != 1 || s.plan.Actions[0].Kind != ActRestore {
		t.Fatalf("staged %+v", s.plan.Actions)
	}
	if !strings.Contains(s.plan.Actions[0].Summary, "6 more") {
		t.Errorf("the REVIEW ROW does not state the batch count: %q — the model saying it "+
			"in prose is not the same as the row saying it", s.plan.Actions[0].Summary)
	}
	// It compiles to the op the write path already handles, which restores the
	// whole trashBatchId.
	ops, err := CompileOps(&Plan{Actions: s.plan.Actions}, s.scope)
	if err != nil || ops[0].Action != domain.ActionRestore {
		t.Fatalf("restore compiled to %+v (%v)", ops, err)
	}

	// Another person's delete is refused by id as well as hidden from the list —
	// an id the model can see is an id it will try.
	s2 := newBreadthStaging()
	s2.elements, s2.task.Owner, s2.task.Autonomy = repo, "alice", AutonomyPreview
	if out := s2.runRestore(ctx, &toolArgs{ElementID: "theirs"}, &reply{staging: s2}); !out.IsError {
		t.Error("alice restored bob's deletion")
	}

	// And an unattended run cannot reach either verb: a run that quietly
	// un-deletes things is the one shape of this feature nobody asked for.
	s3 := newBreadthStaging()
	s3.elements, s3.task.Owner, s3.task.Autonomy = repo, "alice", AutonomyAuto
	if out := s3.runListTrash(ctx, &toolArgs{}, &reply{staging: s3}); !out.IsError {
		t.Error("an auto-applying run was shown the trash")
	}
}

// CV20 — the hard clause: without reading what siblings ALREADY carry,
// distinctness is a coin flip. And a board tile takes a GRADIENT, so serving the
// note-paper swatches "the way cardSwatches already is" would have produced a
// tile that looks broken beside its siblings.
func TestCV20_BoardTilesAreReadBeforeTheyAreWritten(t *testing.T) {
	scope := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{
			"nb1": {ID: "nb1", Type: domain.TypeBoard,
				Content: domain.Content{"title": "Pre-Production", "color": boardColors["indigo"], "icon": "camera"}},
			"nb2": {ID: "nb2", Type: domain.TypeBoard, Content: domain.Content{"title": "Casting"}},
		},
		Items: []Item{
			{ID: "nb1", Type: domain.TypeBoard, Text: "Pre-Production"},
			{ID: "nb2", Type: domain.TypeBoard, Text: "Casting"},
		},
	}
	block := scope.boardStyleBlock()
	if !strings.Contains(block, "indigo") || !strings.Contains(block, "camera") {
		t.Errorf("a styled sibling's colour and icon are not readable:\n%s", block)
	}
	if !strings.Contains(block, "no colour, no icon") {
		t.Errorf("the unstyled sibling is not identified as the one worth distinguishing:\n%s", block)
	}
	if !strings.Contains(block, "GRADIENT") {
		t.Errorf("the vocabulary does not say a tile is not a card swatch:\n%s", block)
	}

	// A card swatch is refused by name, because a flat pastel where a gradient
	// belongs is exactly the "looks broken beside its siblings" failure.
	s := newBreadthStaging(scope.Elements["nb2"])
	out := s.runStyleBoard(context.Background(),
		&toolArgs{ElementID: "nb2", Color: "amber-paper"}, &reply{staging: s})
	if !out.IsError || !strings.Contains(out.Content, "indigo") {
		t.Errorf("an off-vocabulary colour was not corrected with the real palette: %s", out.Content)
	}

	// The write is exactly what the popover writes, iconUrl nulled: the three
	// icon forms are mutually exclusive, and leaving an uploaded image beside a
	// glyph name means the tile keeps showing the image and the change is invisible.
	s2 := newBreadthStaging(scope.Elements["nb2"])
	if out := s2.runStyleBoard(context.Background(),
		&toolArgs{ElementID: "nb2", Color: "teal", Name: "users"}, &reply{staging: s2}); out.IsError {
		t.Fatalf("a legal style was refused: %s", out.Content)
	}
	ops, err := CompileOps(&Plan{Actions: s2.plan.Actions}, scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	content, _ := ops[0].Changes["content"].(map[string]any)
	if content["color"] != boardColors["teal"] {
		t.Errorf("the tile colour compiled to %#v, not the gradient the tile is drawn with", content["color"])
	}
	if v, present := content["iconUrl"]; !present || v != nil {
		t.Errorf("iconUrl was not cleared (%#v); an uploaded image outranks a glyph, so the "+
			"change would not show", v)
	}
	// A single letter is the product's own second tab, and it is how a board
	// called Q3 gets a tile that says so.
	if !boardIconAllowed("Q") || boardIconAllowed("qq") {
		t.Error("the letter vocabulary is wrong")
	}
}
