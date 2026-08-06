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

// What the agent can SEE, on a board shaped like the ones this product's users
// actually build: a checklist, a diagram, a conversation, a long table, and a
// board somebody stopped touching six weeks ago. Every one of those was
// invisible, and each was invisible for its own reason.

type sightFixture struct {
	repo *memory.ElementRepo
	ctx  context.Context
}

func (f sightFixture) put(t *testing.T, id string, typ domain.ElementType, parent string, content domain.Content) *domain.Element {
	t.Helper()
	return f.putAt(t, id, typ, parent, content, time.Now().UTC())
}

func (f sightFixture) putAt(t *testing.T, id string, typ domain.ElementType, parent string, content domain.Content, when time.Time) *domain.Element {
	t.Helper()
	el := &domain.Element{
		ID: id, Type: typ, Content: content,
		Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
		CreatedAt: when, UpdatedAt: when,
	}
	if err := f.repo.Insert(f.ctx, el); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	return el
}

func (f sightFixture) compile(t *testing.T, root string) *BoardScope {
	t.Helper()
	scope, err := CompileScope(f.ctx, f.repo, TaskSpec{
		Owner: "alice", RootBoardID: root, Scope: ScopeBoard,
	})
	if err != nil {
		t.Fatalf("compile scope: %v", err)
	}
	return scope
}

func newSight(t *testing.T) sightFixture {
	t.Helper()
	f := sightFixture{repo: memory.NewElementRepo(), ctx: context.Background()}
	f.put(t, "b1", domain.TypeBoard, "", domain.Content{"title": "Film"})
	return f
}

// DA4 — TASK elements never entered scope, so set_task_done, set_assignee and
// set_reminder could never resolve a checklist item that already existed. "Tick
// off what we finished" failed as an invented id, which reads as the model
// hallucinating rather than as the server being blind.
func TestSight_ChecklistItemsAreVisibleAndAddressable(t *testing.T) {
	f := newSight(t)
	f.put(t, "list-1", domain.TypeTaskList, "b1", domain.Content{"title": "Delivery"})
	f.put(t, "task-1", domain.TypeTask, "list-1", domain.Content{
		"text": "Lock the cut", "done": true})
	f.put(t, "task-2", domain.TypeTask, "list-1", domain.Content{
		"text": "Deliver the DCP", "done": false,
		"dueDate": "2020-01-01", "assigneeId": "omar", "reminderAt": "2020-01-01T09:00:00Z"})

	scope := f.compile(t, "b1")
	if !scope.Has("task-1") || !scope.Has("task-2") {
		t.Fatalf("checklist items never entered the scope, so no tool can name one: %v", scope.Elements)
	}
	out := scope.Render("")
	if !strings.Contains(out, "Lock the cut") || !strings.Contains(out, "Deliver the DCP") {
		t.Fatalf("the checklist rendered as a title with no rows:\n%s", out)
	}
	if !strings.Contains(out, "[x] Lock the cut") {
		t.Errorf("a finished item is indistinguishable from an open one:\n%s", out)
	}
	if !strings.Contains(out, "[ ] Deliver the DCP") {
		t.Errorf("an open item is indistinguishable from a finished one:\n%s", out)
	}
	if !strings.Contains(out, "@omar") {
		t.Errorf("nothing says who owns the task, so \"who owns this?\" is unanswerable:\n%s", out)
	}
	if !strings.Contains(out, "OVERDUE") {
		t.Errorf("a task due in 2020 is not flagged overdue, so \"what is late?\" is unanswerable:\n%s", out)
	}
	if !strings.Contains(out, "⏰") {
		t.Errorf("a reminder that is already set is invisible, so the agent will set a second:\n%s", out)
	}
}

// And a TASK still cannot be moved anywhere but a checklist. Admitting them so
// the agent can READ one must not make "move this task onto the canvas" legal —
// a task on a board canvas is drawn by nothing.
func TestSight_AChecklistItemStillCannotLeaveItsList(t *testing.T) {
	for _, parent := range []domain.ElementType{
		domain.TypeBoard, domain.TypeColumn,
	} {
		if CanHold(parent, domain.TypeTask) {
			t.Errorf("a %s may hold a TASK — the row would exist and render nowhere", parent)
		}
	}
	if !CanHold(domain.TypeTaskList, domain.TypeTask) {
		t.Error("a to-do list may not hold a task, which is the only thing it is for")
	}
}

// DA5 — LINE never entered scope, so edgesAmong iterated a set that could never
// contain one. arrange(ids,"flow") and arrange(ids,"tree") silently fell back to
// an edgeless layout on exactly the boards that had a diagram on them.
func TestSight_ConnectorsAreReadableAsAGraph(t *testing.T) {
	f := newSight(t)
	f.put(t, "card-a", domain.TypeCard, "b1", domain.Content{"textPreview": "Script"})
	f.put(t, "card-b", domain.TypeCard, "b1", domain.Content{"textPreview": "Storyboard"})
	f.put(t, "line-1", domain.TypeLine, "b1", domain.Content{
		"fromId": "card-a", "toId": "card-b", "label": "leads to", "relation": "leads_to"})
	// A connector with one end off the board describes a shape the run cannot
	// see, and citing it would name an id that is not in the listing.
	f.put(t, "line-2", domain.TypeLine, "b1", domain.Content{
		"fromId": "card-a", "toId": "somewhere-else"})

	scope := f.compile(t, "b1")
	if len(scope.Edges) != 1 {
		t.Fatalf("scope carries %d edges, want 1 (the resolvable one): %+v", len(scope.Edges), scope.Edges)
	}
	if got := edgesAmong([]string{"card-a", "card-b"}, scope); len(got) != 1 {
		t.Errorf("edgesAmong found %d edges between two connected cards — arrange falls back "+
			"to an edgeless layout on any board that already has a diagram", len(got))
	}
	out := scope.Render("")
	if !strings.Contains(out, "card-a → card-b") {
		t.Errorf("the connector never reaches the page, so the agent will draw a second one "+
			"between the same pair:\n%s", out)
	}
	if !strings.Contains(out, "leads to") {
		t.Errorf("the connector's label is dropped, so \"what depends on what\" is unanswerable:\n%s", out)
	}
	// A LINE is a relationship, not an element to be moved.
	if scope.Has("line-1") {
		t.Error("a connector entered the movable set — occupancy and filing must keep ignoring it")
	}
}

// DA6 — the collaboration layer was one-way. COMMENT_THREAD was excluded from
// the scope on an argument about MOVING a conversation, applied to READING one.
func TestSight_ConversationsAreVisibleAndUnmovable(t *testing.T) {
	f := newSight(t)
	f.put(t, "thread-1", domain.TypeCommentThread, "b1", domain.Content{"resolved": false})

	scope := f.compile(t, "b1")
	if !scope.Has("thread-1") {
		t.Fatal("the conversation is not addressable, so read_comments cannot name it")
	}
	out := scope.Render("")
	if !strings.Contains(out, "unresolved") {
		t.Errorf("an open argument reads the same as a settled one:\n%s", out)
	}
	if !strings.Contains(out, "read_comments") {
		t.Errorf("nothing tells the model the conversation can be opened:\n%s", out)
	}
	if organizable[domain.TypeCommentThread] {
		t.Error("a conversation entered the movable set — moving it detaches it from its subject")
	}
}

// JN10 — not one element the agent saw carried a date, on a product whose users
// open a board a month after wrap. Relative to the board's own tempo, so an
// actively worked board prints nothing.
func TestSight_StaleItemsAreFlaggedAndFreshOnesAreNot(t *testing.T) {
	f := newSight(t)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		f.putAt(t, fmt.Sprintf("fresh-%d", i), domain.TypeCard, "b1",
			domain.Content{"textPreview": fmt.Sprintf("today %d", i)}, now.Add(-time.Hour))
	}
	f.putAt(t, "ancient", domain.TypeCard, "b1",
		domain.Content{"textPreview": "the old location"}, now.AddDate(0, -3, 0))

	out := f.compile(t, "b1").Render("")
	if !strings.Contains(out, "THIS BOARD: last touched") {
		t.Errorf("the board has no tempo at all — \"what has gone stale here\" is unanswerable:\n%s", out)
	}
	line := lineContaining(out, "the old location")
	if !strings.Contains(line, "⏳") {
		t.Errorf("a card untouched for three months on a board worked on today is unmarked: %q", line)
	}
	if strings.Contains(lineContaining(out, "today 0"), "⏳") {
		t.Errorf("a card written an hour ago is marked stale, which is how a time axis becomes noise:\n%s", out)
	}
}

func TestSight_AFreshBoardPrintsNoAges(t *testing.T) {
	f := newSight(t)
	now := time.Now().UTC()
	for i := 0; i < 6; i++ {
		f.putAt(t, fmt.Sprintf("c-%d", i), domain.TypeCard, "b1",
			domain.Content{"textPreview": fmt.Sprintf("beat %d", i)}, now.Add(-time.Duration(i)*time.Hour))
	}
	if out := f.compile(t, "b1").Render(""); strings.Contains(out, "⏳") {
		t.Errorf("every line on an actively worked board carries an age, which is what makes "+
			"an absolute threshold expensive:\n%s", out)
	}
}

// SC20 — the fair-share divisor was the frontier's width, computed once with no
// redistribution, so a container holding two cards spent 2 of its share of 13
// and the other 11 evaporated. The addressable set must fill.
func TestSight_TheBudgetIsActuallySpent(t *testing.T) {
	f := newSight(t)
	// One wide level: many columns, most of them nearly empty, one enormous.
	// This is the exact shape that stranded budget — the divisor shrank
	// everybody's allowance to serve a container that did not want it.
	total := 0
	for c := 0; c < 20; c++ {
		col := fmt.Sprintf("col-%02d", c)
		f.put(t, col, domain.TypeColumn, "b1", domain.Content{"title": col})
		total++
		cards := 2
		if c == 0 {
			cards = 500
		}
		for i := 0; i < cards; i++ {
			f.put(t, fmt.Sprintf("%s-card-%03d", col, i), domain.TypeCard, col,
				domain.Content{"textPreview": fmt.Sprintf("%s beat %d", col, i)})
			total++
		}
	}
	scope := f.compile(t, "b1")
	want := MaxScopeElements()
	if total < want {
		want = total
	}
	if len(scope.Elements) != want {
		t.Errorf("%d elements addressable on a %d-element workspace, want %d — one wide level "+
			"shrank every container's allowance while the global budget still had room, so "+
			"the leftover slots evaporated rather than being redistributed",
			len(scope.Elements), total, want)
	}
	// And nobody renders as a bare label while there is budget left.
	for _, col := range []string{"col-05", "col-19"} {
		found := false
		for _, it := range scope.Items {
			if it.ParentID == col {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s rendered zero children while budget remained — the difference "+
				"between \"a column about casting\" and \"a column\"", col)
		}
	}
}

// SC3 + DA16 — the header printed the count of what got IN, in the slot a
// reader parses as the count of what is THERE, and the elision note said how
// many without saying what.
func TestSight_TheHeaderStatesBothNumbersAndTheElisionSaysWhat(t *testing.T) {
	f := newSight(t)
	f.put(t, "col-1", domain.TypeColumn, "b1", domain.Content{"title": "Everything"})
	for i := 0; i < maxPerContainer+9; i++ {
		f.put(t, fmt.Sprintf("card-%03d", i), domain.TypeCard, "col-1",
			domain.Content{"textPreview": fmt.Sprintf("beat %d", i)})
	}
	for i := 0; i < 4; i++ {
		f.put(t, fmt.Sprintf("sub-%d", i), domain.TypeBoard, "col-1", domain.Content{"title": "sub"})
	}

	scope := f.compile(t, "b1")
	out := scope.Render("")
	header := strings.SplitN(out, "\n", 2)[0]
	if !strings.Contains(header, "of at least") {
		t.Errorf("the header reports the scope's size as the board's size:\n%s", header)
	}
	elision := lineContaining(out, "more inside")
	if !strings.Contains(elision, "cards") && !strings.Contains(elision, "boards") {
		t.Errorf("the elision note says how many and not what, which is a reason to ignore a "+
			"container rather than to open one: %q", elision)
	}
}

// DA24 — the agent could commit to a board without knowing it was published.
func TestSight_SharingIsReadableAndInheritedDownward(t *testing.T) {
	f := newSight(t)
	root := f.repo
	_ = root
	// A private sub-board inside a publicly linked parent is still public.
	parent := &domain.Element{ID: "pub", Type: domain.TypeBoard,
		Content:  domain.Content{"title": "Client"},
		ACL:      &domain.ACL{OwnerID: "alice", ViewLink: &domain.ViewLink{Token: "t0ken"}},
		Location: domain.Location{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.repo.Insert(f.ctx, parent); err != nil {
		t.Fatal(err)
	}
	child := &domain.Element{ID: "inner", Type: domain.TypeBoard,
		Content:   domain.Content{"title": "Drafts"},
		ACL:       &domain.ACL{OwnerID: "alice"},
		Location:  domain.Location{ParentID: "pub", Section: domain.SectionCanvas},
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.repo.Insert(f.ctx, child); err != nil {
		t.Fatal(err)
	}

	scope := f.compile(t, "inner")
	if scope.Sharing != ExposurePublic {
		t.Fatalf("a sub-board of a publicly linked board reads as %v — the agent would draft "+
			"a private note onto a world-readable canvas", scope.Sharing)
	}
	if out := scope.Render(""); !strings.Contains(out, "world-readable") {
		t.Errorf("nothing warns the run that what it writes is public:\n%s", out)
	}
}

func TestSight_APrivateBoardSaysSo(t *testing.T) {
	f := newSight(t)
	if out := f.compile(t, "b1").Render(""); !strings.Contains(out, "SHARING: private") {
		t.Errorf("the digest does not state exposure at all, so \"who can see this?\" is "+
			"unanswerable in either direction:\n%s", out)
	}
}

// CV18 — the agent made landmarks and then could not see them.
func TestSight_HeadingsReadAsLandmarks(t *testing.T) {
	f := newSight(t)
	f.put(t, "head-1", domain.TypeCard, "b1", domain.Content{
		"textPreview": "PRE-PRODUCTION", "variant": headingVariant})
	out := f.compile(t, "b1").Render("")
	if !strings.Contains(out, "HEADING head-1") {
		t.Errorf("a heading renders as an ordinary card, so a second run will file its own "+
			"section title into the section it titles:\n%s", out)
	}
}

// CV19 — a write with no matching read. The bucket must come from the same
// table the handler writes from, or the round trip is two constants agreeing by
// coincidence.
func TestSight_SizesRoundTripThroughOneTable(t *testing.T) {
	for name, width := range sizeWidths {
		if got := SizeBucket(width); got != name {
			t.Errorf("resize writes %q as %.0fpx and the digest reads it back as %q",
				name, width, got)
		}
	}
	if got := SizeBucket(defaultCardWidth); got != "" {
		t.Errorf("a card at the default width reports size %q — every line on a uniform "+
			"board would carry a flag nobody needs", got)
	}
	if got := SizeBucket(0); got != "" {
		t.Errorf("an element with no width reports size %q", got)
	}
	if got := SizeBucket(1200); got != "" {
		t.Errorf("a hand-dragged 1200px card reports %q, which is a lie the agent would act on", got)
	}
}

// CV22 — the label filter is a view the person deliberately constructed, and it
// was invisible to the run.
func TestSight_TheLabelFilterReachesTheDigest(t *testing.T) {
	f := newSight(t)
	blocked := &domain.Element{ID: "card-x", Type: domain.TypeCard,
		Content:   domain.Content{"textPreview": "waiting on the permit"},
		LabelIDs:  []string{"lab-blocked"},
		Location:  domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := f.repo.Insert(f.ctx, blocked); err != nil {
		t.Fatal(err)
	}
	f.put(t, "card-y", domain.TypeCard, "b1", domain.Content{"textPreview": "not blocked"})

	scope, err := CompileScope(f.ctx, f.repo, TaskSpec{
		Owner: "alice", RootBoardID: "b1", Scope: ScopeBoard,
		ActiveLabelIDs: []string{"lab-blocked"},
	})
	if err != nil {
		t.Fatal(err)
	}
	scope.Labels = []LabelRef{{ID: "lab-blocked", Name: "Blocked"}}
	out := scope.Render("")
	if !strings.Contains(out, "filtering this board to: Blocked") {
		t.Errorf("nothing says what the person is looking at:\n%s", out)
	}
	if !strings.Contains(lineContaining(out, "waiting on the permit"), "⭐") {
		t.Errorf("the item on their screen is not marked:\n%s", out)
	}
	if strings.Contains(lineContaining(out, "not blocked"), "⭐") {
		t.Errorf("a dimmed item is marked as visible:\n%s", out)
	}
}

// DF14 — the table digest truncated at 6 rows × 5 columns, so every canonical
// film document was invisible on re-read.
func TestSight_ALongTableStatesItsTrueShape(t *testing.T) {
	rows := make([][]string, 0, 41)
	rows = append(rows, []string{"Shot", "Scene", "Size", "Lens", "Movement", "Cast", "Notes", "Day", "Unit"})
	for i := 1; i <= 40; i++ {
		rows = append(rows, []string{
			fmt.Sprintf("%d", i), "12", "MS", "", "static", "Layla", "", "3", "main"})
	}
	out := tableDigest("Shot list", rows)

	if !strings.Contains(out, "Lens") {
		t.Errorf("the header row is clipped, so the model cannot know the table HAS a Lens "+
			"column — the columns are the schema:\n%s", out)
	}
	if !strings.Contains(out, "40 rows × 9 columns") {
		t.Errorf("the elision does not state the true dimensions, so the model cannot tell "+
			"how much it is missing:\n%s", out)
	}
	if !strings.Contains(out, "read_table") {
		t.Errorf("nothing points at the paging read, so the run answers from the sample:\n%s", out)
	}
}

// A short, narrow table now arrives whole. The 6-row cap made a decision grid
// and a stripboard equally invisible.
func TestSight_AShortTableArrivesWhole(t *testing.T) {
	rows := [][]string{{"Option", "Cost"}}
	for i := 1; i <= 14; i++ {
		rows = append(rows, []string{fmt.Sprintf("option %d", i), "1000"})
	}
	out := tableDigest("Comparison", rows)
	if strings.Contains(out, "more of") {
		t.Errorf("a 15×2 table is still elided; its whole area is 30 cells:\n%s", out)
	}
	if !strings.Contains(out, "option 14") {
		t.Errorf("the last row of a short table never reaches the model:\n%s", out)
	}
}

// lineContaining returns the rendered line holding needle, for assertions that
// are about one item's flags rather than about the page.
func lineContaining(out, needle string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
