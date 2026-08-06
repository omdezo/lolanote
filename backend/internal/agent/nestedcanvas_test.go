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

// The W3 fixture: a root board holding a nested board whose canvas ALREADY
// carries a row of two columns at real coordinates.
//
// Every earlier layout fixture seeded its existing columns on the ROOT canvas,
// which is precisely why nothing caught the shipped op — the only canvas under
// test was the one that already worked. The op the live run committed for the
// column `Concept` was {parentId, section, index:0}: no position, no width, so
// the board rendered width-0 columns scattered over the real ones.
func nestedCanvasStaging(t *testing.T, canvas ...*domain.Element) *staging {
	t.Helper()
	repo := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	put := func(el *domain.Element) {
		t.Helper()
		el.CreatedAt, el.UpdatedAt = now, now
		if err := repo.Insert(ctx, el); err != nil {
			t.Fatalf("seed %s: %v", el.ID, err)
		}
	}
	put(&domain.Element{ID: "root-board", Type: domain.TypeBoard,
		Content: domain.Content{"title": "Film"}})
	put(&domain.Element{ID: "sub-board", Type: domain.TypeBoard,
		Content: domain.Content{"title": "Pre-Production"},
		Location: domain.Location{ParentID: "root-board", Section: domain.SectionCanvas,
			Position: domain.Point{X: 0, Y: 0}, Width: 200, Height: 140}})
	for _, el := range canvas {
		put(el)
	}

	scope, err := CompileScope(ctx, repo, TaskSpec{
		Owner: "alice", RootBoardID: "root-board", Scope: ScopeBoard,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return &staging{
		runID: "run-geo", scope: scope, elements: repo,
		task:        TaskSpec{Budget: Budget{MaxActions: 60}},
		plan:        &Plan{},
		created:     map[string]ActionKind{},
		failedCalls: map[string]int{},
		quotas:      newQuotas(),
		emit:        func(EventType, string, map[string]any) {},
	}
}

// onSubBoard is one column already sitting on the nested board's canvas.
func onSubBoard(id, title string, x, y float64) *domain.Element {
	return &domain.Element{ID: id, Type: domain.TypeColumn,
		Content: domain.Content{"title": title},
		Location: domain.Location{ParentID: "sub-board", Section: domain.SectionCanvas,
			Position: domain.Point{X: x, Y: y}, Width: ColumnWidth, Height: 500}}
}

// compiledLocation is the location map the write path will actually commit.
//
// Asserted through CompileOps rather than off the Action, because the Action is
// not what renders: the whole bug was a plan whose action looked fine and whose
// op carried no geometry at all.
func compiledLocation(t *testing.T, s *staging) map[string]any {
	t.Helper()
	LayoutPlan(s.plan, s.scope)
	ops, err := CompileOps(s.plan, s.scope)
	if err != nil {
		t.Fatalf("compile ops: %v", err)
	}
	if len(ops) == 0 {
		t.Fatal("the plan compiled to no ops — it would do nothing on the board")
	}
	loc, _ := ops[0].Changes["location"].(map[string]any)
	if loc == nil {
		t.Fatalf("compiled op carries no location: %+v", ops[0].Changes)
	}
	return loc
}

// opBox is the rectangle a compiled create will occupy. height is the caller's,
// because the server does not commit one — it is the renderer's answer.
func opBox(t *testing.T, loc map[string]any, height float64) box {
	t.Helper()
	pos, ok := loc["position"].(map[string]any)
	if !ok {
		t.Fatalf("the committed location has NO POSITION: %+v — this is the op that "+
			"shipped, and a column without a coordinate renders on top of the ones that have one", loc)
	}
	width, _ := loc["width"].(float64)
	if width <= 0 {
		t.Fatalf("the committed location has width %v: %+v — a width-0 column is invisible", loc["width"], loc)
	}
	x, _ := pos["x"].(float64)
	y, _ := pos["y"].(float64)
	return box{left: x, top: y, right: x + width, bottom: y + height}
}

func overlaps(a, b box) bool {
	return a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom
}

// The W3 acceptance line: a plan filing a column into an EXISTING nested board
// compiles an op whose location carries a position and a width, and that
// position does not intersect the boxes of that board's existing columns.
//
// The existing row starts at the sub-canvas ORIGIN on purpose: packing a
// sub-board from (0,0) — what every non-root canvas used to do, because an
// empty canvas is where the packer thought it always was — then lands the new
// column exactly on top of the first existing one.
func TestLayout_ColumnFiledIntoAnExistingNestedBoardIsPlaced(t *testing.T) {
	concept := onSubBoard("col-concept", "Concept", 0, 0)
	casting := onSubBoard("col-casting", "Casting", 344, 0)
	s := nestedCanvasStaging(t, concept, casting)

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Title: "Locations"},
		&reply{staging: s, call: scriptedCall(toolCreateColumn)})
	if out.IsError {
		t.Fatalf("staging a column into the nested board failed: %s", out.Content)
	}

	got := opBox(t, compiledLocation(t, s), 500)
	for _, el := range []*domain.Element{concept, casting} {
		if overlaps(got, elementBox(el)) {
			t.Errorf("the new column at %+v lands on %s at %+v", got, el.ID, elementBox(el))
		}
	}
}

// And it joins the row it lands next to. A nested board is a kanban board too:
// a column belongs at the END of the row that is already there, not in a band
// beneath it and not at the sub-canvas origin.
func TestLayout_NewColumnJoinsTheNestedBoardsOwnRow(t *testing.T) {
	s := nestedCanvasStaging(t,
		onSubBoard("col-concept", "Concept", 400, 200),
		onSubBoard("col-casting", "Casting", 744, 200))

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Title: "Locations"},
		&reply{staging: s, call: scriptedCall(toolCreateColumn)})
	if out.IsError {
		t.Fatalf("staging failed: %s", out.Content)
	}

	loc := compiledLocation(t, s)
	pos, _ := loc["position"].(map[string]any)
	if pos == nil {
		t.Fatalf("no position: %+v", loc)
	}
	if y, _ := pos["y"].(float64); y != 200 {
		t.Errorf("the new column is at y=%v; the row it joins is at y=200", y)
	}
	if x, _ := pos["x"].(float64); x <= 1064 {
		t.Errorf("the new column is at x=%v; the existing row ends at x=1064", x)
	}
}

// A sub-canvas with no row of columns — loose content, a stray card — packs
// BELOW what is on that canvas, and against that canvas's own bounds.
//
// The seeded card sits at the sub-board's own origin while the root canvas is
// occupied somewhere else entirely; reading the root's box here is what would
// drop the note thousands of pixels from anything.
func TestLayout_NestedCanvasPacksBelowItsOwnContent(t *testing.T) {
	loose := &domain.Element{ID: "card-loose", Type: domain.TypeCard,
		Content: domain.Content{"textPreview": "a stray thought"},
		Location: domain.Location{ParentID: "sub-board", Section: domain.SectionCanvas,
			Position: domain.Point{X: 0, Y: 0}, Width: CardWidth, Height: 400}}
	s := nestedCanvasStaging(t, loose)

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Text: "scout the wadi"},
		&reply{staging: s, call: scriptedCall(toolCreateNote)})
	if out.IsError {
		t.Fatalf("staging a note into the nested board failed: %s", out.Content)
	}

	got := opBox(t, compiledLocation(t, s), 48)
	if overlaps(got, elementBox(loose)) {
		t.Errorf("the note at %+v lands on the card already at %+v", got, elementBox(loose))
	}
}

// The viewport is a rectangle in the ROOT board's coordinates. Anchoring a
// sub-canvas to it would place that board's new content at numbers read off a
// different canvas — a coordinate that means nothing where it lands.
func TestLayout_ViewportAnchorsTheRootCanvasOnly(t *testing.T) {
	s := nestedCanvasStaging(t, onSubBoard("col-concept", "Concept", 400, 200))
	s.scope.Viewport = &Viewport{X: 6000, Y: 3000, Width: 1400, Height: 900}

	if out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "sub-board", Text: "inside"},
		&reply{staging: s, call: scriptedCall(toolCreateNote)}); out.IsError {
		t.Fatalf("staging into the nested board failed: %s", out.Content)
	}
	if out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "root-board", Text: "outside"},
		&reply{staging: s, call: scriptedCall(toolCreateNote)}); out.IsError {
		t.Fatalf("staging onto the root failed: %s", out.Content)
	}

	LayoutPlan(s.plan, s.scope)
	nested, root := s.plan.Actions[0], s.plan.Actions[1]
	if nested.Position == nil || root.Position == nil {
		t.Fatalf("one of the two notes got no position: %+v %+v", nested.Position, root.Position)
	}
	if nested.Position.X >= 6000 || nested.Position.Y >= 3000 {
		t.Errorf("the note inside the nested board was placed at (%v,%v) — the root "+
			"board's viewport, which describes a different canvas",
			nested.Position.X, nested.Position.Y)
	}
	if root.Position.X < 6000 || root.Position.Y < 3000 {
		t.Errorf("the note on the root board was placed at (%v,%v), away from the "+
			"region the person is looking at", root.Position.X, root.Position.Y)
	}
}

// read_board is the escape hatch out of the budget: it widens the ids a run may
// name. It has to widen the GEOMETRY too, now that those ids can be filed into.
//
// Otherwise the model reads a board precisely because the digest elided it,
// files something into it, and the packer — holding no occupancy for a canvas
// nobody walked — starts at the origin, on top of the content that was just
// read out to it.
func TestLayout_ReadingABoardMeasuresItsCanvas(t *testing.T) {
	// A board past its parent's print allowance: addressable, never walked. The
	// twenty-five cards ahead of it are what spend the allowance.
	seed := make([]*domain.Element, 0, 28)
	for i := 0; i < maxPerContainer; i++ {
		seed = append(seed, &domain.Element{
			ID: fmt.Sprintf("card-%02d", i), Type: domain.TypeCard,
			Content: domain.Content{"textPreview": fmt.Sprintf("beat %d", i)},
			Location: domain.Location{ParentID: "sub-board", Section: domain.SectionCanvas,
				Index: float64(i), Position: domain.Point{X: float64(i) * 300}, Width: CardWidth, Height: 120}})
	}
	seed = append(seed,
		&domain.Element{ID: "deep-board", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Locations"},
			Location: domain.Location{ParentID: "sub-board", Section: domain.SectionCanvas,
				Index: 99, Position: domain.Point{X: 9000, Y: 0}, Width: 200, Height: 140}},
		&domain.Element{ID: "card-deep", Type: domain.TypeCard,
			Content: domain.Content{"textPreview": "the wadi"},
			Location: domain.Location{ParentID: "deep-board", Section: domain.SectionCanvas,
				Position: domain.Point{X: 0, Y: 0}, Width: CardWidth, Height: 400}})
	s := nestedCanvasStaging(t, seed...)

	if _, ok := s.scope.Elements["deep-board"]; !ok {
		t.Fatal("the fixture is wrong: the deep board was meant to be addressable")
	}
	if !s.scope.CanvasOccupancy("deep-board").Empty {
		t.Fatal("the fixture is wrong: the deep board's canvas was meant to be unwalked")
	}

	if out := s.runReadBoard(context.Background(), &toolArgs{BoardID: "deep-board"},
		&reply{staging: s, call: scriptedCall(toolReadBoard)}); out.IsError {
		t.Fatalf("reading the board failed: %s", out.Content)
	}
	if out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "deep-board", Text: "scout at first light"},
		&reply{staging: s, call: scriptedCall(toolCreateNote)}); out.IsError {
		t.Fatalf("staging into the board just read failed: %s", out.Content)
	}

	got := opBox(t, compiledLocation(t, s), 48)
	existing := box{left: 0, top: 0, right: CardWidth, bottom: 400}
	if overlaps(got, existing) {
		t.Errorf("the note at %+v lands on the card at %+v that read_board had just "+
			"listed — reading widened the ids and not the geometry", got, existing)
	}
}

// Now that a plan can place things on more than one canvas, the self-view has
// to describe them separately. Measuring across canvases subtracts two numbers
// from different coordinate systems: the model would be told the board is twice
// as wide as either canvas actually is, and shown a "row" pairing a root column
// with one inside a sub-board because both happen to sit at the same y.
func TestSelfView_DescribesEachCanvasSeparately(t *testing.T) {
	s := nestedCanvasStaging(t, onSubBoard("col-concept", "Concept", 0, 0))

	for _, in := range []*toolArgs{
		{ParentID: "root-board", Title: "Budget"},
		{ParentID: "sub-board", Title: "Locations"},
	} {
		if out := s.runCreateBoard(context.Background(), in,
			&reply{staging: s, call: scriptedCall(toolCreateColumn)}); out.IsError {
			t.Fatalf("staging into %s failed: %s", in.ParentID, out.Content)
		}
	}

	view := RenderSelfView(s.plan, s.scope)
	for _, want := range []string{"ARRANGEMENT on this board", "ARRANGEMENT on Pre-Production"} {
		if !strings.Contains(view, want) {
			t.Errorf("the self-view does not name each canvas (%q missing):\n%s", want, view)
		}
	}
	// 320 is one column; the span from the root column's left edge to the
	// sub-board column's right edge is 660, and it measures nothing real.
	if strings.Contains(view, "660 px wide") {
		t.Errorf("the self-view measured across two canvases:\n%s", view)
	}

	// A plan on ONE canvas says nothing about which — naming the only board
	// there is would be noise on the common case.
	single := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "n1", ParentID: "root-board", Title: "Only"},
	}}
	if got := RenderSelfView(single, s.scope); !strings.Contains(got, "ARRANGEMENT —") {
		t.Errorf("a single-canvas plan named its canvas anyway:\n%s", got)
	}
}

// The distinction the whole function turns on: a COLUMN's children are ordered
// by index, not placed. Widening "which parents are canvases" must not hand a
// coordinate to something a container will lay out itself — two answers to
// "where does this go", one of which the renderer ignores.
func TestLayout_ColumnChildrenStillGetNoCoordinate(t *testing.T) {
	s := nestedCanvasStaging(t, onSubBoard("col-concept", "Concept", 400, 200))

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "col-concept", Text: "log the premise"},
		&reply{staging: s, call: scriptedCall(toolCreateNote)})
	if out.IsError {
		t.Fatalf("staging into the column failed: %s", out.Content)
	}

	loc := compiledLocation(t, s)
	if _, ok := loc["position"]; ok {
		t.Errorf("a card inside a column was given a canvas coordinate: %+v", loc)
	}
	if _, ok := loc["width"]; ok {
		t.Errorf("a card inside a column was given a canvas width: %+v", loc)
	}
}
