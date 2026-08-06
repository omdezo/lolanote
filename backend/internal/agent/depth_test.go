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

// nested seeds the shape every post-organizing workspace has and the agent
// could not read: a root board holding nested boards, each holding columns,
// each holding cards. The live runs that produced this work order saw five
// board tiles and reported "5 items in scope" while sixty elements sat inside.
type nestedFixture struct {
	repo  *memory.ElementRepo
	root  string
	sub   string
	col   string
	card  string
	ctx   context.Context
	board *domain.Element
}

func seedNested(t *testing.T) nestedFixture {
	t.Helper()
	repo := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()

	put := func(id string, typ domain.ElementType, parent string, content domain.Content, loc domain.Location) *domain.Element {
		t.Helper()
		loc.ParentID = parent
		el := &domain.Element{ID: id, Type: typ, Content: content, Location: loc, CreatedAt: now, UpdatedAt: now}
		if err := repo.Insert(ctx, el); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		return el
	}

	root := put("root-board", domain.TypeBoard, "", domain.Content{"title": "Film"}, domain.Location{})
	put("sub-board", domain.TypeBoard, "root-board", domain.Content{"title": "Pre-Production"},
		domain.Location{Section: domain.SectionCanvas, Position: domain.Point{X: 0, Y: 0}, Width: 200, Height: 140})
	put("col-casting", domain.TypeColumn, "sub-board", domain.Content{"title": "Casting"},
		domain.Location{Section: domain.SectionCanvas, Position: domain.Point{X: 400, Y: 200}, Width: 320, Height: 500})
	put("card-lead", domain.TypeCard, "col-casting", domain.Content{"textPreview": "audition the lead"},
		domain.Location{})

	return nestedFixture{repo: repo, root: "root-board", sub: "sub-board",
		col: "col-casting", card: "card-lead", ctx: ctx, board: root}
}

func compileNested(t *testing.T, f nestedFixture) *BoardScope {
	t.Helper()
	scope, err := CompileScope(f.ctx, f.repo, TaskSpec{
		Owner: "alice", RootBoardID: f.root, Scope: ScopeBoard,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return scope
}

// The W1 acceptance line, whole: a board containing a board containing a column
// containing a card puts all four in scope, and the digest says what the card
// says. Asserted on the RENDERED digest rather than on the maps, because the
// render loop was itself one level deep — a grandchild could be compiled into
// the scope and then vanish silently on its way to the page.
func TestScope_DescendsIntoNestedBoards(t *testing.T) {
	f := seedNested(t)
	scope := compileNested(t, f)

	for _, id := range []string{f.sub, f.col, f.card} {
		if _, ok := scope.Elements[id]; !ok {
			t.Errorf("%s is not in scope — nothing can be added to it and no "+
				"precondition naming it can pass", id)
		}
	}
	// The fourth level is the root board itself, which lives in scope.Board and
	// deliberately not in Elements: every parent resolution special-cases its id
	// already, and admitting it would make the run's own root a rename and
	// delete target. All four are addressable; three of them are in the map.
	if scope.Board.ID != f.root {
		t.Fatalf("the run's root is %s, not the board it was given", scope.Board.ID)
	}
	// What service.go reports as "read board — N items". It has to be the
	// subtree count, because the two live runs behind this work order both said
	// "5 items in scope" with sixty elements sitting inside those five.
	if len(scope.Items) != 3 {
		t.Errorf("items in scope: %d, want 3 (nested board + column + card)", len(scope.Items))
	}

	out := scope.Render("")
	if !strings.Contains(out, "audition the lead") {
		t.Errorf("the digest never mentions the card three levels down:\n%s", out)
	}
	// The nested board opens a section, so the model reads it as a canvas it can
	// file into rather than as one more tile with a name.
	if !strings.Contains(out, `BOARD sub-board "Pre-Production"`) {
		t.Errorf("the nested board does not render as a section:\n%s", out)
	}
	if !strings.Contains(out, "1 column, 1 card") {
		t.Errorf("the section header does not say what the board holds:\n%s", out)
	}
	// Indentation is the only thing carrying containment in a line-oriented
	// digest, so the card must sit two levels in, not beside its grandparent.
	if !strings.Contains(out, "        card-lead") {
		t.Errorf("the card is not rendered inside its column inside the board:\n%s", out)
	}
}

// cellOf reads absolute pixels. A card inside a nested board sits at that
// board's coordinates, so reporting "B2" would put it in the same named cell as
// something on the root canvas thousands of pixels away — a fact-shaped lie the
// model would then reason about.
func TestScope_NoCanvasCellOffTheRootCanvas(t *testing.T) {
	f := seedNested(t)
	scope := compileNested(t, f)

	for _, it := range scope.Items {
		if it.ParentID != "" && it.Cell != "" {
			t.Errorf("%s is inside %s and still claims cell %s", it.ID, it.ParentID, it.Cell)
		}
	}
}

// Geometry needs to know what a sub-canvas already holds. The committed op for
// one column on a nested board carried no position and no width at all, because
// the single root Occupied box was the only occupancy anything computed.
func TestScope_OccupancyIsPerCanvas(t *testing.T) {
	f := seedNested(t)
	scope := compileNested(t, f)

	sub := scope.CanvasOccupancy(f.sub)
	if sub.Empty {
		t.Fatalf("the nested board's canvas reads as empty; it holds a column")
	}
	if sub.MinX != 400 || sub.MinY != 200 || sub.MaxX != 720 || sub.MaxY != 700 {
		t.Errorf("nested canvas box = %+v, want the column's own box", sub)
	}
	// The root box still describes the root board, and the two are not the same
	// region: packing a column onto the sub-board against the root's box would
	// drop it on top of what is there.
	root := scope.CanvasOccupancy(f.root)
	if root.Empty || root.MaxY != 140 {
		t.Errorf("root canvas box = %+v, want the nested board's tile", root)
	}
	// A canvas nobody walked must read as EMPTY rather than as a zero-sized box
	// at the origin, which is what the bare map would say.
	if got := scope.CanvasOccupancy("never-walked"); !got.Empty {
		t.Errorf("an unwalked canvas reads as %+v, not as empty", got)
	}
}

// The escape hatch has to be nameable. read_board widens the scope, so an
// elided element is still reachable — but only if the digest says which board
// to read and the model is told that reading it is the move.
func TestScope_ElisionPointsAtReadBoard(t *testing.T) {
	repo := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.Insert(ctx, &domain.Element{ID: "root-board", Type: domain.TypeBoard,
		Content: domain.Content{"title": "Wall"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, &domain.Element{ID: "col-wall", Type: domain.TypeColumn,
		Content:   domain.Content{"title": "Everything"},
		Location:  domain.Location{ParentID: "root-board", Section: domain.SectionCanvas},
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxPerContainer+7; i++ {
		if err := repo.Insert(ctx, &domain.Element{
			ID: fmt.Sprintf("card-%03d", i), Type: domain.TypeCard,
			Content:   domain.Content{"textPreview": fmt.Sprintf("beat %d", i)},
			Location:  domain.Location{ParentID: "col-wall", Index: float64(i)},
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	scope, err := CompileScope(ctx, repo, TaskSpec{Owner: "alice", RootBoardID: "root-board", Scope: ScopeBoard})
	if err != nil {
		t.Fatal(err)
	}
	if scope.Elided["col-wall"] != 7 {
		t.Fatalf("elided count = %d, want 7", scope.Elided["col-wall"])
	}
	out := scope.Render("")
	// The breakdown rides with the count now: "12 more" is a reason to ignore a
	// container and "12 more (9 cards, 3 boards)" is a reason to open one, and
	// the aggregation behind it is the one the human's board tiles already use.
	if !strings.Contains(out, "… and 7 more inside (7 cards) (read_board col-wall for the rest)") {
		t.Errorf("the elision note does not say how to go and get the rest:\n%s", out)
	}

	// Elided is a TOKEN budget, not a membership one. The card whose text did
	// not fit is still an id the run may name — Preconditions rejects anything
	// outside Elements, and the duplicate guard scans the same map for siblings,
	// so collapsing the two caps made "too long to print" mean "does not exist".
	if _, ok := scope.Elements["card-031"]; !ok {
		t.Error("an elided card is not addressable; its id would be rejected as out of scope")
	}
	if strings.Contains(out, "card-031") {
		t.Errorf("an elided card was printed anyway — the budget bought nothing:\n%s", out)
	}
	if len(scope.Items) != 26 {
		t.Errorf("printed items = %d, want 26 (the column plus %d cards)", len(scope.Items), maxPerContainer)
	}
}

// Past the depth cap the walk stops admitting and keeps counting. A board that
// renders as holding nothing while it holds thirty cards is the same lie the
// depth-blind scope told, one level further down.
func TestScope_PastTheDepthCapCountsRatherThanLies(t *testing.T) {
	repo := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	parent := ""
	// root → b1 → b2 → b3 → b4 → b5, one board per level.
	for i := 0; i <= 5; i++ {
		id := fmt.Sprintf("board-%d", i)
		loc := domain.Location{ParentID: parent, Section: domain.SectionCanvas}
		if err := repo.Insert(ctx, &domain.Element{ID: id, Type: domain.TypeBoard,
			Content:  domain.Content{"title": fmt.Sprintf("Level %d", i)},
			Location: loc, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		parent = id
	}

	scope, err := CompileScope(ctx, repo, TaskSpec{Owner: "alice", RootBoardID: "board-0", Scope: ScopeBoard})
	if err != nil {
		t.Fatal(err)
	}
	// Four levels below the root are read; the fifth is counted, not read.
	for _, id := range []string{"board-1", "board-2", "board-3", "board-4"} {
		if _, ok := scope.Elements[id]; !ok {
			t.Errorf("%s is within the depth cap and is not in scope", id)
		}
	}
	if _, ok := scope.Elements["board-5"]; ok {
		t.Error("board-5 is past the depth cap and was admitted anyway")
	}
	if scope.Elided["board-4"] != 1 {
		t.Errorf("board-4's unread child is not counted: %d", scope.Elided["board-4"])
	}
	out := scope.Render("")
	if !strings.Contains(out, "read_board board-4 for the rest") {
		t.Errorf("the deepest board read does not offer a way in:\n%s", out)
	}
}

// A card three levels down has to be a legal destination, not merely a legible
// one: Preconditions rejects any action naming an element outside the scope, so
// depth blindness made "add a note to the casting column" impossible rather
// than unreliable.
func TestScope_DeepContainerIsAValidParent(t *testing.T) {
	f := seedNested(t)
	scope := compileNested(t, f)

	s := &staging{
		runID: "run-depth", scope: scope, elements: f.repo,
		task:        TaskSpec{Budget: Budget{MaxActions: 60}},
		plan:        &Plan{},
		created:     map[string]ActionKind{},
		failedCalls: map[string]int{},
		quotas:      newQuotas(),
		emit:        func(EventType, string, map[string]any) {},
	}
	out := s.runCreateBoard(f.ctx, &toolArgs{ParentID: f.col, Text: "call the agent"}, &reply{
		staging: s, call: scriptedCall(toolCreateNote)})
	if out.IsError {
		t.Fatalf("creating a note inside a column two levels down: %s", out.Content)
	}
	ops, err := CompileOps(s.plan, scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("ops = %d, want 1", len(ops))
	}
	loc, _ := ops[0].Changes["location"].(map[string]any)
	if loc == nil || loc["parentId"] != f.col {
		t.Errorf("the note does not land in the deep column: %+v", ops[0].Changes)
	}
}
