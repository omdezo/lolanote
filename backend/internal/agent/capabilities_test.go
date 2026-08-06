package agent

import (
	"context"
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// capStaging builds a board with one of everything the new tools revise, so a
// test can assert against real elements rather than a fixture shaped to pass.
func capStaging() *staging {
	scope := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{
			"col-1": {ID: "col-1", Type: domain.TypeColumn,
				Content:  domain.Content{"title": "Pre-Production", "collapsed": false},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
			"card-1": {ID: "card-1", Type: domain.TypeCard,
				Content:  domain.Content{"textPreview": "Lock the script\nCast the leads\nScout locations"},
				Location: domain.Location{ParentID: "col-1", Index: 1}},
			"card-2": {ID: "card-2", Type: domain.TypeCard,
				Content:  domain.Content{"textPreview": "Permits"},
				Location: domain.Location{ParentID: "col-1", Index: 2}},
			"doc-1": {ID: "doc-1", Type: domain.TypeDocument,
				Content: domain.Content{"title": "Treatment",
					"textPreview": "A portrait of a potter in Nizwa.",
					"doc":         map[string]any{"type": "doc"}},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
			"table-1": {ID: "table-1", Type: domain.TypeTable,
				Content: domain.Content{"cells": []any{
					[]any{"Item", "Cost"},
					[]any{"Camera", "4000"},
				}},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
			"link-1": {ID: "link-1", Type: domain.TypeLink,
				Content:  domain.Content{"url": "https://old.example", "title": "Reference"},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
			"img-1": {ID: "img-1", Type: domain.TypeImage,
				Content:  domain.Content{"url": "https://x/y.png"},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
			"other-board": {ID: "other-board", Type: domain.TypeBoard,
				Content:  domain.Content{"title": "Budget"},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
		},
	}
	return &staging{
		runID: "run-cap", scope: scope,
		task:        TaskSpec{Budget: Budget{MaxActions: 60}},
		plan:        &Plan{},
		created:     map[string]ActionKind{},
		failedCalls: map[string]int{},
		quotas:      newQuotas(),
		emit:        func(EventType, string, map[string]any) {},
	}
}

func lastAction(t *testing.T, s *staging) Action {
	t.Helper()
	if len(s.plan.Actions) == 0 {
		t.Fatal("nothing was staged")
	}
	return s.plan.Actions[len(s.plan.Actions)-1]
}

// contentOf compiles one action and returns the content map the renderer will
// read. Asserting on the ACTION alone has let four separate capabilities ship
// writing a key nothing renders: rows vs cells, filename vs caption, label vs
// title, text vs textPreview.
func contentOf(t *testing.T, s *staging, a Action) map[string]any {
	t.Helper()
	ops, err := CompileOps(&Plan{Actions: []Action{a}}, s.scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(ops) == 0 {
		t.Fatal("the action compiled to no ops — it would do nothing on the board")
	}
	content, _ := ops[0].Changes["content"].(map[string]any)
	if content == nil {
		t.Fatalf("compiled op carries no content: %+v", ops[0].Changes)
	}
	return content
}

// Asked to "write the treatment", the agent produced a note. A note is a
// sticky, and three paragraphs on a sticky is how a board becomes unreadable.
// The DOCUMENT type has had a toolbar button the whole time.
func TestCapability_WritesADocument(t *testing.T) {
	s := capStaging()
	body := "A portrait of a potter in Nizwa.\n\nShot over four days in June."

	out := s.runWriteDocument(context.Background(),
		&toolArgs{ParentID: "b1", Title: "Treatment", Body: body}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("writing a document failed: %s", out.Content)
	}

	a := lastAction(t, s)
	if a.Kind.ElementType() != domain.TypeDocument {
		t.Errorf("produced a %s, want a DOCUMENT", a.Kind.ElementType())
	}
	c := contentOf(t, s, a)
	if c["title"] != "Treatment" {
		t.Errorf("title = %v", c["title"])
	}
	// DocumentCard reads textPreview for the summary and doc for the body.
	if !strings.Contains(c["textPreview"].(string), "Nizwa") {
		t.Errorf("textPreview does not carry the body: %v", c["textPreview"])
	}
	if c["doc"] == nil {
		t.Error("no rich-text doc — the page opens blank")
	}
}

// An empty page is worse than a note: it looks like work and holds nothing.
func TestCapability_DocumentNeedsSubstance(t *testing.T) {
	s := capStaging()
	out := s.runWriteDocument(context.Background(),
		&toolArgs{ParentID: "b1", Title: "Treatment", Body: "   "}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("staged a document with no body")
	}
}

// The swatch card slices fixed offsets out of the hex string, so a short or
// bare value renders as NaN rather than failing loudly.
func TestCapability_ColorIsCoercedToWhatTheCardCanRead(t *testing.T) {
	for _, given := range []string{"#1B2A4A", "1b2a4a", "#abc"} {
		s := capStaging()
		out := s.runAddColor(context.Background(),
			&toolArgs{ParentID: "b1", Color: given, Title: "Night exteriors"}, &reply{staging: s})
		if out.IsError {
			t.Fatalf("%q was refused: %s", given, out.Content)
		}
		c := contentOf(t, s, lastAction(t, s))
		hex, _ := c["hex"].(string)
		if len(hex) != 7 || hex[0] != '#' {
			t.Errorf("%q produced hex %q, which the swatch card cannot parse", given, hex)
		}
		if c["displayFormat"] != "HEX" {
			t.Errorf("%q: displayFormat = %v", given, c["displayFormat"])
		}
	}
}

func TestCapability_ColorRefusesWhatIsNotAColor(t *testing.T) {
	s := capStaging()
	out := s.runAddColor(context.Background(),
		&toolArgs{ParentID: "b1", Color: "warm terracotta"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("staged a swatch from prose")
	}
	if !strings.Contains(out.Content, "#") {
		t.Errorf("the refusal does not show the shape that would work: %s", out.Content)
	}
}

// A shortcut points at the real board rather than copying it.
func TestCapability_LinksToAnExistingBoard(t *testing.T) {
	s := capStaging()
	out := s.runLinkBoard(context.Background(),
		&toolArgs{ParentID: "b1", BoardID: "other-board"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("linking failed: %s", out.Content)
	}
	c := contentOf(t, s, lastAction(t, s))
	if c["targetBoardId"] != "other-board" {
		t.Errorf("targetBoardId = %v — the shortcut points nowhere", c["targetBoardId"])
	}
	// Falls back to the target's own name, so an unnamed shortcut still reads.
	if c["title"] != "Budget" {
		t.Errorf("title = %v, want the board's own name", c["title"])
	}
}

func TestCapability_ShortcutOnlyPointsAtBoards(t *testing.T) {
	s := capStaging()
	out := s.runLinkBoard(context.Background(),
		&toolArgs{ParentID: "b1", BoardID: "card-1", Title: "x"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("made a shortcut to a card")
	}
}

// Asked to add a line to a budget table, the agent's only route was a SECOND
// table beside the first — the same failure as the to-do list before add_tasks.
func TestCapability_EditsAnExistingTable(t *testing.T) {
	s := capStaging()
	out := s.runEditTable(context.Background(), &toolArgs{
		ElementID: "table-1",
		Rows:      [][]string{{"Item", "Cost"}, {"Camera", "4000"}, {"Lighting", "1200"}},
	}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("editing the table failed: %s", out.Content)
	}

	ops, err := CompileOps(&Plan{Actions: []Action{lastAction(t, s)}}, s.scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if ops[0].Action != domain.ActionUpdate {
		t.Errorf("action = %s; editing must update the table, not create one", ops[0].Action)
	}
	if ops[0].ElementID != "table-1" {
		t.Errorf("wrote to %s instead of the table", ops[0].ElementID)
	}
	// TableCard reads content.cells. Writing anything else produces a table
	// that exists and shows nothing.
	c, _ := ops[0].Changes["content"].(map[string]any)
	rows, ok := c["cells"].([][]string)
	if !ok || len(rows) != 3 {
		t.Fatalf("cells = %#v", c["cells"])
	}
	// The inverse carries the WHOLE prior grid: cells is replaced wholesale, so
	// a delta would restore the table to a state it was never in.
	undo, _ := ops[0].UndoChanges["content"].(map[string]any)
	if undo["cells"] == nil {
		t.Error("no inverse — an undo would leave the new grid in place")
	}
}

// A ragged grid renders with cells missing. Padding costs nothing; refusing
// costs a turn over something trivially fixable.
func TestCapability_TableRowsAreSquaredOff(t *testing.T) {
	s := capStaging()
	out := s.runEditTable(context.Background(), &toolArgs{
		ElementID: "table-1",
		Rows:      [][]string{{"Item", "Cost", "Day"}, {"Camera"}},
	}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("a ragged grid was refused instead of padded: %s", out.Content)
	}
	for i, row := range lastAction(t, s).Rows {
		if len(row) != 3 {
			t.Errorf("row %d has %d cells, want 3", i, len(row))
		}
	}
}

func TestCapability_EditTableRefusesWhatIsNotATable(t *testing.T) {
	s := capStaging()
	out := s.runEditTable(context.Background(),
		&toolArgs{ElementID: "card-1", Rows: [][]string{{"a"}}}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("edited a card as a table")
	}
}

func TestCapability_RepointsALink(t *testing.T) {
	s := capStaging()
	out := s.runSetURL(context.Background(),
		&toolArgs{ElementID: "link-1", URL: "https://new.example/page"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("repointing failed: %s", out.Content)
	}
	c := contentOf(t, s, lastAction(t, s))
	if c["url"] != "https://new.example/page" {
		t.Errorf("url = %v", c["url"])
	}
}

func TestCapability_LinkNeedsAFullAddress(t *testing.T) {
	s := capStaging()
	out := s.runSetURL(context.Background(),
		&toolArgs{ElementID: "link-1", URL: "new.example"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("staged a link with no scheme")
	}
}

// A new label rides the existing rename path as its own action, so the review
// list shows both changes rather than one that silently does two things.
func TestCapability_LinkTitleIsItsOwnChange(t *testing.T) {
	s := capStaging()
	s.runSetURL(context.Background(), &toolArgs{
		ElementID: "link-1", URL: "https://new.example", Title: "Location permits",
	}, &reply{staging: s})

	if len(s.plan.Actions) != 2 {
		t.Fatalf("staged %d actions, want the url and the rename separately", len(s.plan.Actions))
	}
	if s.plan.Actions[1].Kind != ActRename {
		t.Errorf("second action = %s", s.plan.Actions[1].Kind)
	}
}

func TestCapability_CaptionsAPicture(t *testing.T) {
	s := capStaging()
	out := s.runSetCaption(context.Background(),
		&toolArgs{ElementID: "img-1", Title: "Hands working clay"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("captioning failed: %s", out.Content)
	}
	c := contentOf(t, s, lastAction(t, s))
	if c["caption"] != "Hands working clay" {
		t.Errorf("caption = %v", c["caption"])
	}
}

// Captions belong on pictures. Pointing the model at rename is more useful
// than refusing, because it names the tool that does work.
func TestCapability_CaptionRefusalNamesTheRightTool(t *testing.T) {
	s := capStaging()
	out := s.runSetCaption(context.Background(),
		&toolArgs{ElementID: "card-1", Title: "x"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("captioned a card")
	}
	if !strings.Contains(out.Content, "rename") {
		t.Errorf("the refusal does not say what to use instead: %s", out.Content)
	}
}

func TestCapability_FoldsAColumn(t *testing.T) {
	s := capStaging()
	yes := true
	out := s.runCollapse(context.Background(),
		&toolArgs{ElementID: "col-1", Collapsed: &yes}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("collapsing failed: %s", out.Content)
	}
	c := contentOf(t, s, lastAction(t, s))
	if c["collapsed"] != true {
		t.Errorf("collapsed = %v", c["collapsed"])
	}
}

// Already how it wants it: not an error worth retrying, and a no-op in the
// review list is noise the person has to read past.
func TestCapability_CollapseIsANoOpWhenAlreadyThere(t *testing.T) {
	s := capStaging()
	no := false
	out := s.runCollapse(context.Background(),
		&toolArgs{ElementID: "col-1", Collapsed: &no}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("a no-op was reported as an error: %s", out.Content)
	}
	if len(s.plan.Actions) != 0 {
		t.Error("staged a change that changes nothing")
	}
}

// "Episode two starts from episode one." A synced clone is the wrong tool for
// that and there was no right one.
func TestCapability_DuplicatesAWholeSubtree(t *testing.T) {
	s := capStaging()
	out := s.runDuplicate(context.Background(),
		&toolArgs{ElementID: "col-1", Title: "Episode 2"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("duplicating failed: %s", out.Content)
	}

	a := lastAction(t, s)
	// The column plus its two cards. Resolved at STAGING time so the review
	// list can say three, not one.
	if len(a.Copies) != 3 {
		t.Fatalf("resolved %d copies, want the column and its two cards", len(a.Copies))
	}
	if !strings.Contains(out.Content, "3") {
		t.Errorf("the person is not told how many elements this creates: %s", out.Content)
	}

	ops, err := CompileOps(&Plan{Actions: []Action{a}}, s.scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("compiled to %d ops, want 3", len(ops))
	}
	// Every id is new: a duplicate that reused ids would overwrite the original.
	for _, op := range ops {
		if _, collides := s.scope.Elements[op.ElementID]; collides {
			t.Errorf("copy reuses the existing id %s", op.ElementID)
		}
		if op.Action != domain.ActionCreate {
			t.Errorf("op action = %s", op.Action)
		}
	}
	// Parents precede children, which is what the write path's scope check
	// needs when a child parents to something created in the same transaction.
	root := ops[0].ElementID
	for _, op := range ops[1:] {
		loc, _ := op.Changes["location"].(map[string]any)
		if loc["parentId"] != root {
			t.Errorf("child %s parents to %v, not to the copied column", op.ElementID, loc["parentId"])
		}
	}
	// The copy takes the new name; the original is untouched.
	rootContent, _ := ops[0].Changes["content"].(map[string]any)
	if rootContent["title"] != "Episode 2" {
		t.Errorf("copy title = %v", rootContent["title"])
	}
	if s.scope.Elements["col-1"].Content["title"] != "Pre-Production" {
		t.Error("duplicating renamed the ORIGINAL")
	}
}

// Deterministic ids: a retried apply must write the same ops rather than a
// second copy of everything.
func TestCapability_DuplicateIDsAreStable(t *testing.T) {
	first := capStaging()
	first.runDuplicate(context.Background(), &toolArgs{ElementID: "col-1"}, &reply{staging: first})
	second := capStaging()
	second.runDuplicate(context.Background(), &toolArgs{ElementID: "col-1"}, &reply{staging: second})

	a, b := lastAction(t, first), lastAction(t, second)
	for i := range a.Copies {
		if a.Copies[i].NewID != b.Copies[i].NewID {
			t.Fatalf("copy %d got different ids across runs: %s vs %s",
				i, a.Copies[i].NewID, b.Copies[i].NewID)
		}
	}
}

// The copy would be its own descendant, and the subtree walk would never reach
// the bottom.
func TestCapability_CannotDuplicateSomethingIntoItself(t *testing.T) {
	s := capStaging()
	out := s.runDuplicate(context.Background(),
		&toolArgs{ElementID: "col-1", ParentID: "col-1"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("copied a column into itself")
	}
}

// A note that grew into a page. The element keeps its id, so arrows drawn to
// it and comments on it survive — deleting and recreating would cut both.
func TestCapability_ConvertsANoteIntoADocument(t *testing.T) {
	s := capStaging()
	out := s.runConvert(context.Background(),
		&toolArgs{ElementID: "card-1", Becomes: "DOCUMENT"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("converting failed: %s", out.Content)
	}

	ops, err := CompileOps(&Plan{Actions: []Action{lastAction(t, s)}}, s.scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if ops[0].ElementID != "card-1" {
		t.Errorf("wrote to %s — the id must survive a conversion", ops[0].ElementID)
	}
	if ops[0].Changes["type"] != string(domain.TypeDocument) {
		t.Errorf("type = %v", ops[0].Changes["type"])
	}
	c, _ := ops[0].Changes["content"].(map[string]any)
	if c["title"] != "Lock the script" {
		t.Errorf("title = %v, want the note's first line", c["title"])
	}
	// The inverse restores the type AND the content keys the conversion
	// overwrites — type alone would leave a CARD holding a document's title.
	undo := ops[0].UndoChanges
	if undo["type"] != string(domain.TypeCard) || undo["content"] == nil {
		t.Errorf("inverse is incomplete: %#v", undo)
	}
}

// A checklist's ITEMS are separate elements, which the conversion op cannot
// create. Without the paired add_tasks the person gets an empty list where
// their note was: technically a conversion, and a total loss of the content.
func TestCapability_ConvertingToAChecklistCarriesTheItems(t *testing.T) {
	s := capStaging()
	out := s.runConvert(context.Background(),
		&toolArgs{ElementID: "card-1", Becomes: "TASK_LIST"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("converting failed: %s", out.Content)
	}
	if len(s.plan.Actions) != 2 {
		t.Fatalf("staged %d actions; the items are missing", len(s.plan.Actions))
	}
	items := s.plan.Actions[1]
	if items.Kind != ActAddTasks || len(items.Tasks) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if items.Tasks[0] != "Lock the script" {
		t.Errorf("first item = %q", items.Tasks[0])
	}
}

// "- " and "1." are how people write lists. Carrying the bullet across would
// double it up against the checkbox the list already draws.
func TestCapability_ChecklistLinesLoseTheirBullets(t *testing.T) {
	got := checklistLines("- Lock the script\n2) Cast the leads\n* Scout locations\n\n")
	want := []string{"Lock the script", "Cast the leads", "Scout locations"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCapability_ConvertRefusesTypesThatWouldLoseWhatTheyAre(t *testing.T) {
	s := capStaging()
	out := s.runConvert(context.Background(),
		&toolArgs{ElementID: "table-1", Becomes: "CARD"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("converted a table into a note, discarding its grid")
	}
}

// Every new kind must compile. A kind that stages and does not compile is the
// exact failure mode create_table, connect and clone_here all shipped with.
func TestCapability_EveryNewKindCompiles(t *testing.T) {
	yes := true
	s := capStaging()
	for _, a := range []Action{
		{Kind: ActWriteDocument, ElementID: "n1", ParentID: "b1", Title: "T", Text: "body"},
		{Kind: ActAddColor, ElementID: "n2", ParentID: "b1", Color: "#1b2a4a"},
		{Kind: ActLinkBoard, ElementID: "n3", ParentID: "b1", FromID: "other-board", Title: "Budget"},
		{Kind: ActEditTable, ElementID: "table-1", Rows: [][]string{{"a"}}},
		{Kind: ActSetURL, ElementID: "link-1", URL: "https://x"},
		{Kind: ActSetCaption, ElementID: "img-1", Title: "c"},
		{Kind: ActCollapse, ElementID: "col-1", Collapsed: &yes},
		{Kind: ActConvert, ElementID: "card-1", Becomes: "DOCUMENT", Text: "x"},
		{Kind: ActDuplicate, ElementID: "card-2", ParentID: "b1",
			Copies: []CopySpec{{NewID: "cp1", SourceID: "card-2", ParentID: "b1"}}},
	} {
		ops, err := CompileOps(&Plan{Actions: []Action{a}}, s.scope)
		if err != nil {
			t.Errorf("%s does not compile: %v", a.Kind, err)
			continue
		}
		if len(ops) == 0 {
			t.Errorf("%s compiles to no ops — it would do nothing on the board", a.Kind)
		}
		if _, known := SpecFor(a.Kind); !known {
			t.Errorf("%s has no spec", a.Kind)
		}
	}
}

// Scope is a snapshot: after the first conversion is STAGED the element is
// still a CARD as far as the type check can see, so a second call sailed
// through. A live run did exactly that — two converts and two add_tasks on one
// note, which would have put every item on the list twice.
func TestCapability_ConvertsAtMostOnce(t *testing.T) {
	s := capStaging()
	args := &toolArgs{ElementID: "card-1", Becomes: "TASK_LIST"}

	if out := s.runConvert(context.Background(), args, &reply{staging: s}); out.IsError {
		t.Fatalf("first convert failed: %s", out.Content)
	}
	staged := len(s.plan.Actions)

	out := s.runConvert(context.Background(), args, &reply{staging: s})
	if out.IsError {
		t.Errorf("the second call should be a no-op, not an error: %s", out.Content)
	}
	if len(s.plan.Actions) != staged {
		t.Errorf("converted twice: %d actions, was %d — every item lands on the list twice",
			len(s.plan.Actions), staged)
	}
}

// The heading is the LIST's name, not its first item. convertOps takes the
// first line as the title, and feeding the same text to checklistLines put it
// on the list as well: "Delivery checklist" became a list called "Delivery
// checklist" whose first thing to tick off was "Delivery checklist".
func TestCapability_ChecklistHeadingIsNotAlsoAnItem(t *testing.T) {
	s := capStaging()
	s.scope.Elements["note-h"] = &domain.Element{
		ID: "note-h", Type: domain.TypeCard,
		Content: domain.Content{"textPreview": "Delivery checklist\n" +
			"- Picture lock\n- Sound mix\n- Colour grade"},
		Location: domain.Location{ParentID: "b1"},
	}

	out := s.runConvert(context.Background(),
		&toolArgs{ElementID: "note-h", Becomes: "TASK_LIST"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("convert failed: %s", out.Content)
	}
	items := s.plan.Actions[1].Tasks
	if len(items) != 3 {
		t.Fatalf("got %d items: %v", len(items), items)
	}
	for _, item := range items {
		if item == "Delivery checklist" {
			t.Error("the list's own name is on the list as something to tick off")
		}
	}
}

// A heading is only a heading when the lines under it are bulleted. "Buy milk /
// Buy bread" is two items, and guessing wrong there silently drops a task.
func TestCapability_UnbulletedListKeepsEveryLine(t *testing.T) {
	s := capStaging()
	// card-1 is three plain lines with no bullets anywhere.
	out := s.runConvert(context.Background(),
		&toolArgs{ElementID: "card-1", Becomes: "TASK_LIST"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("convert failed: %s", out.Content)
	}
	if items := s.plan.Actions[1].Tasks; len(items) != 3 {
		t.Errorf("got %d items, want all three: %v", len(items), items)
	}
}

// The digest is the fourth thing a capability needs: an edit tool over a value
// the model cannot see is a tool it will only ever use to overwrite.
//
// Two of these were reading a key the element does not carry — the same class
// of bug as rows/cells, label/title and text/textPreview, each of which shipped
// green and did nothing on the board.
func TestDigest_ShowsWhatTheNewToolsEdit(t *testing.T) {
	for _, tc := range []struct {
		name string
		el   *domain.Element
		want string
	}{{
		name: "a swatch shows its colour",
		el: &domain.Element{Type: domain.TypeColorSwatch,
			Content: domain.Content{"hex": "#1b2a4a", "title": "Night exteriors"}},
		want: "#1b2a4a",
	}, {
		name: "a folded column says so",
		el: &domain.Element{Type: domain.TypeColumn,
			Content: domain.Content{"title": "Post", "collapsed": true}},
		want: "folded",
	}, {
		name: "a shortcut says where it goes",
		el: &domain.Element{Type: domain.TypeAlias,
			Content: domain.Content{"title": "Budget", "targetBoardId": "brd-9"}},
		want: "brd-9",
	}, {
		name: "a link shows its address",
		el: &domain.Element{Type: domain.TypeLink,
			Content: domain.Content{"url": "https://example.test/x"}},
		want: "example.test",
	}, {
		name: "an image shows its caption",
		el: &domain.Element{Type: domain.TypeImage,
			Content: domain.Content{"caption": "Hands working clay"}},
		want: "Hands working clay",
	}, {
		name: "a table shows its cells",
		el: &domain.Element{Type: domain.TypeTable,
			Content: domain.Content{"title": "Budget", "cells": []any{
				[]any{"Item", "Cost"}, []any{"Camera", "4000"}}}},
		want: "Camera",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := textFor(tc.el, nil)
			if !strings.Contains(got, tc.want) {
				t.Errorf("digest reads %q, which does not carry %q — "+
					"the model cannot revise what it cannot see", got, tc.want)
			}
		})
	}
}

// A duplicate's ElementID names the SOURCE it copies, not anything it creates.
// Everywhere that reached for ElementID on a create was wrong about it, and the
// worst of them silently discarded unrelated work: dropping the duplicate from
// the review list marked the ORIGINAL as a dead parent, so every action aimed
// at the original was cascade-dropped with it.
func TestCapability_DroppingACopyDoesNotDropTheOriginalsWork(t *testing.T) {
	s := capStaging()
	// Something else in the same plan adds a card to the ORIGINAL column.
	if _, err := s.add(Action{
		Kind: ActCreateNote, ParentID: "col-1", Text: "Insurance certificate",
	}); err != nil {
		t.Fatal(err)
	}
	if out := s.runDuplicate(context.Background(),
		&toolArgs{ElementID: "col-1", Title: "Episode 2"}, &reply{staging: s}); out.IsError {
		t.Fatalf("duplicate failed: %s", out.Content)
	}

	// The person drops the copy from the review list.
	kept := ApplyAdjustments(s.plan, []Adjustment{{Seq: 1, Kind: AdjustDrop}}, s.scope)

	var survived bool
	for _, a := range kept.Actions {
		if a.Kind == ActCreateNote && a.ParentID == "col-1" {
			survived = true
		}
		if a.Kind == ActDuplicate {
			t.Error("the dropped duplicate is still in the plan")
		}
	}
	if !survived {
		t.Error("dropping the COPY also dropped a card being added to the ORIGINAL")
	}
}

// Documents were write-once: write_document could create one and nothing could
// revise it — the same create-only failure the tables had. Asked to tighten a
// treatment, the only route was a SECOND treatment beside the first.
//
// The content map is what this turns on. A document renders its summary from
// textPreview and its page from doc, so an edit that carries one and not the
// other leaves the card and the page saying different things.
func TestCapability_EditsADocumentInPlace(t *testing.T) {
	s := capStaging()
	revised := "A portrait of a potter in Nizwa.\n\nShot over four days in June, in the falaj gardens."

	out := s.runSetText(context.Background(),
		&toolArgs{ElementID: "doc-1", Text: revised}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("editing a document was refused: %s", out.Content)
	}

	c := contentOf(t, s, lastAction(t, s))
	if !strings.Contains(c["textPreview"].(string), "falaj") {
		t.Errorf("textPreview does not carry the revision: %v", c["textPreview"])
	}
	if c["doc"] == nil {
		t.Error("no rich-text doc — the card would update and the page open unchanged")
	}
	// The title is not in the patch at all. MergePatch merges the content map
	// key by key, so an edit that named title would be the only thing that could
	// erase the document's name.
	if _, present := c["title"]; present {
		t.Errorf("the edit rewrites title as well: %v", c["title"])
	}
}

// And the inverse restores both halves. Undoing a document edit that only put
// back textPreview would leave the page holding the new text under the old
// summary.
func TestCapability_UndoingADocumentEditRestoresThePage(t *testing.T) {
	s := capStaging()
	if out := s.runSetText(context.Background(),
		&toolArgs{ElementID: "doc-1", Text: "Something else entirely"}, &reply{staging: s}); out.IsError {
		t.Fatalf("edit refused: %s", out.Content)
	}
	ops, err := CompileOps(s.plan, s.scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	undo, _ := ops[0].UndoChanges["content"].(map[string]any)
	if undo == nil {
		t.Fatal("no inverse at all")
	}
	if undo["textPreview"] != "A portrait of a potter in Nizwa." {
		t.Errorf("inverse textPreview = %v", undo["textPreview"])
	}
	if undo["doc"] == nil {
		t.Error("the inverse restores the summary and leaves the page rewritten")
	}
}

// "What can I parent to?" must answer with the copy, never the source.
func TestCapability_CreatedIDsNameTheCopiesNotTheSource(t *testing.T) {
	s := capStaging()
	s.runDuplicate(context.Background(), &toolArgs{ElementID: "col-1"}, &reply{staging: s})

	a := lastAction(t, s)
	made := a.CreatedIDs()
	if len(made) != 3 {
		t.Fatalf("CreatedIDs = %v, want one per copied element", made)
	}
	for _, id := range made {
		if id == "col-1" || id == "card-1" || id == "card-2" {
			t.Errorf("CreatedIDs names %s, which already exists — a later action "+
				"aimed at the copy would land in the thing being copied", id)
		}
	}
}
