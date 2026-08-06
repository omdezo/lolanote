package agent_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The corpus never leaves the small.
//
// `grep -rn "func Benchmark" backend/` returned nothing across the whole
// repository, and the largest fixture anywhere was twelve elements — against a
// scope budget of 400, an action ceiling of 60 and a delegated op ceiling of
// 240. Every failure that lives in this corner (the scope collapsing to
// container names, a header reporting a fraction of the board as the whole of
// it, a container rendering zero children while budget remained) is invisible
// at twelve elements and would have been caught by one fixture at the ceiling.
//
// So: a parametric workspace, three sizes spanning the ceiling, and the first
// benchmarks in the tree. The benchmarks are gated on QUERY COUNT rather than
// wall time — a wall-clock threshold on shared CI hardware fails for reasons
// that have nothing to do with the code, and the thing actually worth bounding
// is round trips.

// buildWorkspace fills a repo with boards × columns × cards under one root and
// returns the total element count, the root included.
func buildWorkspace(ctx context.Context, repo *memory.ElementRepo, root string, boards, cols, cards int) int {
	now := time.Now().UTC()
	put := func(el *domain.Element) {
		el.CreatedBy = owner
		el.CreatedAt = now
		el.UpdatedAt = now
		_ = repo.Insert(ctx, el)
	}
	put(&domain.Element{ID: root, Type: domain.TypeBoard,
		Content: domain.Content{"title": "Scale"}, ACL: &domain.ACL{OwnerID: owner}})
	n := 1
	for b := 0; b < boards; b++ {
		boardID := fmt.Sprintf("%s-b%03d", root, b)
		put(&domain.Element{ID: boardID, Type: domain.TypeBoard,
			Content:  domain.Content{"title": fmt.Sprintf("Unit %d", b)},
			Location: domain.Location{ParentID: root, Section: domain.SectionCanvas, Position: domain.Point{X: float64(b) * 340, Y: 0}, Width: 260, Height: 180}})
		n++
		for c := 0; c < cols; c++ {
			colID := fmt.Sprintf("%s-c%03d", boardID, c)
			put(&domain.Element{ID: colID, Type: domain.TypeColumn,
				Content:  domain.Content{"title": fmt.Sprintf("Stage %d", c)},
				Location: domain.Location{ParentID: boardID, Section: domain.SectionCanvas, Position: domain.Point{X: float64(c) * 344}, Width: 320, Height: 400}})
			n++
			for k := 0; k < cards; k++ {
				put(&domain.Element{ID: fmt.Sprintf("%s-k%03d", colID, k), Type: domain.TypeCard,
					Content:  domain.Content{"textPreview": fmt.Sprintf("Unit %d stage %d item %d", b, c, k)},
					Location: domain.Location{ParentID: colID, Section: domain.SectionCanvas, Index: float64(k)}})
				n++
			}
		}
	}
	return n
}

// countingRepo counts the READS a call makes. Fifteen lines, and the whole
// reason DA23's "the probe is a query-count benchmark, which the suite does not
// currently know how to assert" was true.
type countingRepo struct {
	domain.ElementRepository
	children atomic.Int64
	gets     atomic.Int64
}

func (r *countingRepo) Children(ctx context.Context, f domain.ElementFilter) ([]*domain.Element, error) {
	r.children.Add(1)
	return r.ElementRepository.Children(ctx, f)
}

func (r *countingRepo) Get(ctx context.Context, id string) (*domain.Element, error) {
	r.gets.Add(1)
	return r.ElementRepository.Get(ctx, id)
}

func (r *countingRepo) reads() int64 { return r.children.Load() + r.gets.Load() }

const scaleRoot = "5ca1e00000000000000000b1"

// The three sizes. Roughly 400 (the scope budget itself), 2,000 and 9,000 — the
// last being the board this corner was written from.
var scaleSizes = []struct {
	name                string
	boards, cols, cards int
}{
	{"about 400", 6, 6, 10},
	{"about 2000", 12, 8, 20},
	{"about 9000", 24, 12, 32},
}

func TestScale_ScopeStaysHonestAsTheBoardGrows(t *testing.T) {
	for _, size := range scaleSizes {
		t.Run(size.name, func(t *testing.T) {
			ctx := context.Background()
			base := memory.NewElementRepo()
			total := buildWorkspace(ctx, base, scaleRoot, size.boards, size.cols, size.cards)
			repo := &countingRepo{ElementRepository: base}

			scope, err := agent.CompileScope(ctx, repo, agent.TaskSpec{
				Intent: "have a look", Owner: owner, RootBoardID: scaleRoot, Scope: agent.ScopeBoard,
			})
			if err != nil {
				t.Fatalf("compile scope over %d elements: %v", total, err)
			}

			// The addressable set must stop at the budget and not one element
			// past it: Preconditions rejects anything outside this map, so a
			// scope that overruns is a plan that cannot be applied.
			if len(scope.Elements) > agent.MaxScopeElements() {
				t.Errorf("%d elements addressable against a budget of %d",
					len(scope.Elements), agent.MaxScopeElements())
			}

			// The header states a number. On a board this size that number is
			// necessarily a fraction of the whole, and it must be the fraction
			// the run can actually act on — a header claiming the board is 230
			// items when it holds 9,331 is the digest lying about its own reach.
			header := strings.SplitN(scope.Render(""), "\n", 2)[0]
			want := fmt.Sprintf("— %d items visible here", len(scope.Items))
			if !strings.Contains(header, want) {
				t.Errorf("header %q does not state the %d items it actually carries", header, len(scope.Items))
			}
			// And BOTH numbers, or the fraction is indistinguishable from the
			// whole. "230 items" in the slot a reader parses as "how much is
			// here" is how a run answers "how many cards do I have?" with 2% of
			// the board and no hedge at all.
			if total > len(scope.Items) && !strings.Contains(header, "of at least") {
				t.Errorf("header %q shows %d of %d elements and never says so",
					header, len(scope.Items), total)
			}
			if total > agent.MaxScopeElements() && len(scope.Elided) == 0 {
				t.Errorf("%d elements were compiled down to %d and the digest elides nothing — "+
					"the run cannot tell the edge of the budget from the edge of the board",
					total, len(scope.Items))
			}

			// No container may render as empty while budget remained. That is
			// the exact shape of the lie the depth-blind scope told: a column
			// with thirty cards in it described as holding nothing.
			rendered := scope.Render("")
			for id, el := range scope.Elements {
				if el.Type != domain.TypeColumn {
					continue
				}
				printed := false
				for _, it := range scope.Items {
					if it.ParentID == id {
						printed = true
						break
					}
				}
				if !printed && scope.Elided[id] == 0 {
					t.Errorf("column %s renders zero children and elides nothing, but it holds %d cards",
						id, size.cards)
					break
				}
			}
			if !strings.Contains(rendered, "BOARD "+scaleRoot) {
				t.Error("the digest does not name the root board")
			}

			// Round trips, not milliseconds. The walk is breadth-first over
			// containers, so the count scales with CONTAINERS visited and must
			// never scale with cards.
			if reads := repo.reads(); reads > int64(size.boards*(size.cols+1)+8) {
				t.Errorf("CompileScope issued %d reads over %d elements — the walk is reading leaves",
					reads, total)
			}
		})
	}
}

// The read cost must not move with the number of CARDS, only with the number of
// containers. Two workspaces with the same shape and wildly different card
// counts are the cleanest way to state that.
func TestScale_ScopeReadCostTracksContainersNotCards(t *testing.T) {
	ctx := context.Background()
	readsFor := func(cards int) int64 {
		base := memory.NewElementRepo()
		buildWorkspace(ctx, base, scaleRoot, 8, 6, cards)
		repo := &countingRepo{ElementRepository: base}
		if _, err := agent.CompileScope(ctx, repo, agent.TaskSpec{
			Intent: "look", Owner: owner, RootBoardID: scaleRoot, Scope: agent.ScopeBoard,
		}); err != nil {
			t.Fatalf("compile: %v", err)
		}
		return repo.reads()
	}
	small, large := readsFor(4), readsFor(40)
	if small != large {
		t.Errorf("ten times the cards changed the read count from %d to %d — "+
			"the scope walk is issuing a query per leaf", small, large)
	}
}

// ---- benchmarks -------------------------------------------------------------
//
// The first in the repository. Run with `go test -bench Scale ./internal/agent`;
// what CI asserts is the query count above, not these numbers.

func BenchmarkCompileScope(b *testing.B) {
	for _, size := range scaleSizes {
		b.Run(size.name, func(b *testing.B) {
			ctx := context.Background()
			repo := memory.NewElementRepo()
			buildWorkspace(ctx, repo, scaleRoot, size.boards, size.cols, size.cards)
			task := agent.TaskSpec{
				Intent: "look", Owner: owner, RootBoardID: scaleRoot, Scope: agent.ScopeBoard,
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := agent.CompileScope(ctx, repo, task); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkScopeRender(b *testing.B) {
	ctx := context.Background()
	repo := memory.NewElementRepo()
	buildWorkspace(ctx, repo, scaleRoot, 24, 12, 32)
	scope, err := agent.CompileScope(ctx, repo, agent.TaskSpec{
		Intent: "look", Owner: owner, RootBoardID: scaleRoot, Scope: agent.ScopeBoard,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = scope.Render("")
	}
}
