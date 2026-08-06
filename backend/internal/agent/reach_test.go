package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// What the agent's capabilities actually DO, at the tool boundary — where a
// refusal still reaches the model in time for it to choose something else.

// fakeComments is the smallest thing that can hold a conversation.
type fakeComments struct {
	byThread map[string][]*domain.Comment
}

func (f *fakeComments) Insert(_ context.Context, c *domain.Comment) error {
	f.byThread[c.ThreadID] = append(f.byThread[c.ThreadID], c)
	return nil
}
func (f *fakeComments) Get(_ context.Context, id string) (*domain.Comment, error) {
	for _, msgs := range f.byThread {
		for _, m := range msgs {
			if m.ID == id {
				return m, nil
			}
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeComments) ListByThread(_ context.Context, threadID string) ([]*domain.Comment, error) {
	return f.byThread[threadID], nil
}
func (f *fakeComments) Update(_ context.Context, _ *domain.Comment) error { return nil }

func reachStaging() *staging {
	s := reviseStaging()
	s.scope.Elements = map[string]*domain.Element{
		"task-1": {ID: "task-1", Type: domain.TypeTask,
			Content:  domain.Content{"text": "Lock the cut"},
			Location: domain.Location{ParentID: "list-1"}},
		"card-1": {ID: "card-1", Type: domain.TypeCard,
			Content:  domain.Content{"textPreview": "Harbour interview"},
			Location: domain.Location{ParentID: "col-1", Section: domain.SectionCanvas}},
		"col-1": {ID: "col-1", Type: domain.TypeColumn,
			Content:  domain.Content{"title": "Casting"},
			Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
		"head-1": {ID: "head-1", Type: domain.TypeCard,
			Content:  domain.Content{"textPreview": "PRE-PRODUCTION", "variant": headingVariant},
			Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
		"thread-1": {ID: "thread-1", Type: domain.TypeCommentThread,
			Content:  domain.Content{"resolved": false},
			Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
		"table-1": {ID: "table-1", Type: domain.TypeTable,
			Content:  domain.Content{"cells": longShotList()},
			Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
	}
	return s
}

func longShotList() [][]string {
	rows := [][]string{{"Shot", "Scene", "Lens"}}
	for i := 1; i <= 40; i++ {
		rows = append(rows, []string{fmt.Sprintf("%d", i), "12", ""})
	}
	return rows
}

func call(s *staging, name string) *reply {
	return &reply{staging: s, call: cognition.ToolCall{ID: "c1", Name: name}}
}

// DA9 — "remind me about this note next Tuesday" was accepted, shown in the
// review list as a real change, committed, and never fired: the sweeper queries
// type:TASK and this handler had no type check at all. A confident yes and
// nothing on Tuesday.
func TestReach_ARemindersRefusalRoutesRatherThanStops(t *testing.T) {
	s := reachStaging()
	out := s.runRemind(context.Background(),
		&toolArgs{ElementID: "card-1", When: "2026-09-01T09:00"}, call(s, toolRemind))
	if !out.IsError {
		t.Fatalf("a reminder was accepted on a CARD, which the sweep will never see: %s", out.Content)
	}
	if !strings.Contains(out.Content, "to-do list") {
		t.Errorf("the refusal does not say where reminders DO live, so the model has nowhere "+
			"to go with it: %s", out.Content)
	}
	if len(s.plan.Actions) != 0 {
		t.Error("the refused reminder was staged anyway")
	}
}

// DF25 — the schema handed the model a UTC instant and this user's every call
// time is Asia/Muscat +04, so a 05:30 crew call was written as 09:30 local. The
// conversion happens server-side, once.
func TestReach_ALocalCallTimeIsConvertedServerSide(t *testing.T) {
	s := reachStaging()
	s.scope.Timezone = "Asia/Muscat"
	out := s.runRemind(context.Background(),
		&toolArgs{ElementID: "task-1", When: "2026-09-01T05:30"}, call(s, toolRemind))
	if out.IsError {
		t.Fatalf("a local wall-clock time was refused: %s", out.Content)
	}
	a := lastAction(t, s)
	if a.RemindAt != "2026-09-01T01:30:00Z" {
		t.Errorf("05:30 in Muscat was stored as %q, want 2026-09-01T01:30:00Z — a constant "+
			"four hours is worse than a variable one, because it looks deliberate", a.RemindAt)
	}
	// And the review row is echoed in the person's own clock, so a wrong reading
	// is caught before the morning it matters.
	if !strings.Contains(a.Summary, "05:30") {
		t.Errorf("the review row says %q rather than the local time the person typed", a.Summary)
	}
}

// An explicit zone still wins: a model that wrote an offset meant it.
func TestReach_AnExplicitOffsetIsHonoured(t *testing.T) {
	s := reachStaging()
	s.scope.Timezone = "Asia/Muscat"
	out := s.runRemind(context.Background(),
		&toolArgs{ElementID: "task-1", When: "2026-09-01T09:00:00Z"}, call(s, toolRemind))
	if out.IsError {
		t.Fatalf("an RFC3339 instant was refused: %s", out.Content)
	}
	if got := lastAction(t, s).RemindAt; got != "2026-09-01T09:00:00Z" {
		t.Errorf("an explicit UTC instant was shifted to %q", got)
	}
}

// DA6 — the conversation was unreadable, so "what did the team decide?" was
// unattemptable and the run summarised a shared board's artifacts while missing
// its deliberation.
func TestReach_TheConversationCanBeRead(t *testing.T) {
	s := reachStaging()
	s.comments = &fakeComments{byThread: map[string][]*domain.Comment{
		"thread-1": {
			{ID: "m1", ThreadID: "thread-1", AuthorID: "omar", Body: "The harbour is booked for the 3rd."},
			{ID: "m2", ThreadID: "thread-1", AuthorID: "sara", Body: "Then the interview moves to the 4th."},
		},
	}}
	s.scope.People = []PersonRef{{ID: "omar", Name: "Omar"}, {ID: "sara", Name: "Sara"}}

	out := s.runReadComments(context.Background(),
		&toolArgs{ElementID: "thread-1"}, call(s, toolReadComments))
	if out.IsError {
		t.Fatalf("the conversation could not be read: %s", out.Content)
	}
	if !strings.Contains(out.Content, "moves to the 4th") {
		t.Errorf("the decision is not in the output:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "Sara") {
		t.Errorf("nothing says who said it, which is most of what a discussion is:\n%s", out.Content)
	}
	// Somebody's words are DATA, labelled like every other segment.
	if !strings.Contains(out.Content, "⟨user⟩") {
		t.Errorf("comment bodies enter the context unlabelled:\n%s", out.Content)
	}
}

// And the agent's own note lands beside the thing it is about, rather than at
// the root where nobody connects it to anything.
func TestReach_ACommentAnchorsToWhatItIsAbout(t *testing.T) {
	s := reachStaging()
	out := s.runComment(context.Background(),
		&toolArgs{ElementID: "card-1", Text: "Kept this out of Casting — it is a location note."},
		call(s, toolComment))
	if out.IsError {
		t.Fatalf("an anchored comment was refused: %s", out.Content)
	}
	if got := lastAction(t, s).ParentID; got != "col-1" {
		t.Errorf("the note landed on %q rather than beside card-1's own container", got)
	}
}

// DF14 — a 40-row shot list must be pageable, or the run answers "which shots
// have no lens noted?" from the six rows in the listing.
func TestReach_ALongTableCanBePaged(t *testing.T) {
	s := reachStaging()
	out := s.runReadTable(context.Background(),
		&toolArgs{ElementID: "table-1", FromRow: 20, Count: 5}, call(s, toolReadTable))
	if out.IsError {
		t.Fatalf("the table could not be paged: %s", out.Content)
	}
	if !strings.Contains(out.Content, "40 rows") && !strings.Contains(out.Content, "41 rows") {
		t.Errorf("the page does not state the table's true size:\n%s", out.Content)
	}
	// The header rides on every page, or a page of a shot list is a grid of
	// strings whose columns the model has to remember from an earlier turn.
	if !strings.Contains(out.Content, "Lens") {
		t.Errorf("the page arrived without its column names:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "fromRow=25") {
		t.Errorf("nothing says how to get the next page:\n%s", out.Content)
	}
}

// CV18 — a heading names a REGION. Filed into a column it stops being a
// landmark and the region loses its name.
func TestReach_AHeadingCannotBeFiledIntoAColumn(t *testing.T) {
	s := reachStaging()
	out := s.runMove(context.Background(),
		&toolArgs{ElementID: "head-1", ParentID: "col-1"}, call(s, toolMove))
	if !out.IsError {
		t.Fatalf("a section title was filed into one of the sections it titles: %s", out.Content)
	}
	if !strings.Contains(out.Content, "heading") {
		t.Errorf("the refusal does not explain: %s", out.Content)
	}
}

// A conversation stays where the discussion happened.
func TestReach_AConversationCannotBeMoved(t *testing.T) {
	s := reachStaging()
	out := s.runMove(context.Background(),
		&toolArgs{ElementID: "thread-1", ParentID: "col-1"}, call(s, toolMove))
	if !out.IsError {
		t.Fatalf("a comment thread was detached from its subject: %s", out.Content)
	}
}

// IL4 — refine deleted the person's pending row edits and never told the model
// about them, so the second plan re-proposed exactly what had just been removed.
func TestReach_RefineReplaysTheTypedEditsAsFacts(t *testing.T) {
	prior := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", ParentID: "b1", Title: "Ideas"},
		{Seq: 1, Kind: ActCreateNote, ElementID: "n1", ParentID: "c1", Text: "a stray thought"},
		{Seq: 2, Kind: ActCreateColumn, ElementID: "c2", ParentID: "b1", Title: "Casting"},
	}}
	replay := describeAdjustments(prior, []Adjustment{
		{Kind: AdjustDrop, Seq: 0},
		{Kind: AdjustRetitle, Seq: 2, Value: "Cast"},
	})
	if replay == "" {
		t.Fatal("the person's typed edits never reached the model — the only non-lossy " +
			"correction channel there is, deleted on the way")
	}
	if !strings.Contains(replay, "REMOVED") || !strings.Contains(replay, "Ideas") {
		t.Errorf("the drop is not stated, so the redo will propose it again:\n%s", replay)
	}
	if !strings.Contains(replay, "do not propose it again") {
		t.Errorf("the drop reads as background rather than as an instruction:\n%s", replay)
	}
	if !strings.Contains(replay, "Cast") {
		t.Errorf("the rename is not carried, so the model reverts to its own wording:\n%s", replay)
	}
}

// CV21 — search reaches the whole account and the plan reaches one subtree, and
// the two kinds of line looked identical.
func TestReach_SearchHitsSayWhetherTheyAreWritable(t *testing.T) {
	s := reachStaging()
	s.elements = &searchRepo{hits: []*domain.Element{
		{ID: "card-1", Type: domain.TypeCard, Content: domain.Content{"textPreview": "Harbour interview"}},
		{ID: "far-away", Type: domain.TypeCard, Content: domain.Content{"textPreview": "Harbour permit"}},
	}}
	out := s.runSearch(context.Background(), &toolArgs{Query: "harbour"}, call(s, toolSearch))
	if out.IsError {
		t.Fatalf("search failed: %s", out.Content)
	}
	if !strings.Contains(lineContaining(out.Content, "card-1"), "[on this board]") {
		t.Errorf("a writable hit is not marked as one:\n%s", out.Content)
	}
	if !strings.Contains(lineContaining(out.Content, "far-away"), "not writable") {
		t.Errorf("a hit on another board looks exactly like one the run may move, which is "+
			"how the agent contradicts itself thirty seconds after finding something:\n%s",
			out.Content)
	}
}

// searchRepo answers Search and nothing else; every other method is unreachable
// from the one handler under test.
type searchRepo struct {
	domain.ElementRepository
	hits []*domain.Element
}

func (r *searchRepo) Search(_ context.Context, _, _ string, _ int) ([]*domain.Element, error) {
	return r.hits, nil
}

func TestReach_ParseWorkspaceTimeRejectsNonsense(t *testing.T) {
	if _, err := parseWorkspaceTime("next tuesday", "Asia/Muscat"); err == nil {
		t.Error("prose was accepted as a timestamp, which is how a reminder lands in 0001")
	}
	got, err := parseWorkspaceTime("2026-09-01", "Asia/Muscat")
	if err != nil {
		t.Fatalf("a bare date was refused: %v", err)
	}
	if want := time.Date(2026, 8, 31, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("midnight in Muscat is %s, want %s", got, want)
	}
}

// MP14 — the PEOPLE block published every collaborator's raw subject id, and
// the model could echo one into a card, a summary or a document it wrote.
//
// Both halves are asserted here because either alone is worse than neither:
// aliasing the digest without teaching the handler to map back would stage the
// literal string "person1" into content.assigneeId, and teaching the handler
// without aliasing the digest would leave the ids in context.
func TestMP14_PeopleAreAliasedInContextAndResolvedServerSide(t *testing.T) {
	s := reachStaging()
	s.scope.People = []PersonRef{
		{ID: "sub-omar-4f2a", Name: "Omar", Alias: "person1"},
		{ID: "sub-sara-91bd", Name: "Sara", Alias: "person2"},
	}
	s.scope.Elements["task-1"].Content["assigneeId"] = "sub-sara-91bd"
	// Render prints Items, so the row has to be projected through the same
	// itemFor the digest walk uses — that projection is where the handle is
	// substituted.
	s.scope.Items = append(s.scope.Items, itemFor(s.scope.Elements["task-1"], s.scope))

	out := s.scope.Render("")
	for _, sub := range []string{"sub-omar-4f2a", "sub-sara-91bd"} {
		if strings.Contains(out, sub) {
			t.Errorf("subject id %s reached the model's context; it can be echoed into "+
				"card text or a written document from there:\n%s", sub, out)
		}
	}
	if !strings.Contains(out, "person1=Omar") || !strings.Contains(out, "person2=Sara") {
		t.Errorf("the roster is unusable — set_assignee has no handle to name:\n%s", out)
	}
	// The quiet second leak: an assigned checklist row published a sub of its own.
	if !strings.Contains(out, "@person2") {
		t.Errorf("an assigned task does not name its owner by handle:\n%s", out)
	}

	// And the handler maps the handle back, so what is staged is the real sub.
	res := s.runAssign(context.Background(),
		&toolArgs{ElementID: "task-1", UserID: "person2"}, call(s, toolAssign))
	if res.IsError {
		t.Fatalf("the alias the digest published was refused: %s", res.Content)
	}
	if got := lastAction(t, s).AssigneeID; got != "sub-sara-91bd" {
		t.Errorf("staged assigneeId is %q — the alias was written onto the card instead "+
			"of being resolved, so the assignment points at nobody", got)
	}
}

// MP9 — "flag this to Sara" produced a comment Sara was never told about: the
// tool took the argument, the announcer took the slice, and the run between
// them passed nil.
func TestMP9_CommentMentionsResolveToRealSubs(t *testing.T) {
	s := reachStaging()
	s.scope.People = []PersonRef{{ID: "sub-sara-91bd", Name: "Sara", Alias: "person2"}}

	out := s.runComment(context.Background(), &toolArgs{
		ElementID: "card-1", Text: "Harbour permit is still open.",
		Mentions: []string{"person2", "person9"},
	}, call(s, toolComment))
	if out.IsError {
		t.Fatalf("a comment with mentions was refused: %s", out.Content)
	}
	written := s.plan.NewComments
	if len(written) != 1 {
		t.Fatalf("staged %d comments, want 1", len(written))
	}
	// The real sub, because that is what the notifier rings.
	if got := written[0].Mentions; len(got) != 1 || got[0] != "sub-sara-91bd" {
		t.Errorf("mentions staged as %v, want [sub-sara-91bd] — an unresolved handle "+
			"notifies nobody, and a literal alias notifies nobody twice", got)
	}
}
