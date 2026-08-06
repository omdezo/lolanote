package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The write path and the board read, at the ceilings they are actually
// configured for.
//
// Nothing in this repository had a benchmark, and no test anywhere came within
// an order of magnitude of the numbers the system declares about itself: a
// delegated grant allows 240 ops, a board open renders whatever a person has
// built. A twelve-element fixture cannot observe a per-op query, and a per-op
// query is exactly what turns one approved plan into two hundred round trips.
//
// Asserted on QUERY COUNT rather than wall time. A millisecond threshold on
// shared CI hardware fails for reasons unrelated to the code; round trips are
// the thing that actually degrades, and they are stable.

const (
	scaleBoard = "5ca1e00000000000000000c1"
	scaleOwner = "alice"
)

// countingElements counts reads and writes through the repository, so a test
// can say "this many round trips for this many ops" instead of "this felt slow".
type countingElements struct {
	domain.ElementRepository
	gets     atomic.Int64
	getMany  atomic.Int64
	children atomic.Int64
	desc     atomic.Int64
	patches  atomic.Int64
}

func (c *countingElements) Get(ctx context.Context, id string) (*domain.Element, error) {
	c.gets.Add(1)
	return c.ElementRepository.Get(ctx, id)
}

// One batch is one round trip, and it is counted as one. Leaving it out would
// make a change that replaces N walks with one $in per depth look free, which is
// the same kind of lie as not measuring at all.
func (c *countingElements) GetMany(ctx context.Context, ids []string) ([]*domain.Element, error) {
	c.getMany.Add(1)
	return c.ElementRepository.GetMany(ctx, ids)
}

func (c *countingElements) Children(ctx context.Context, f domain.ElementFilter) ([]*domain.Element, error) {
	c.children.Add(1)
	return c.ElementRepository.Children(ctx, f)
}

func (c *countingElements) Descendants(ctx context.Context, root string, incl bool) ([]*domain.Element, error) {
	c.desc.Add(1)
	return c.ElementRepository.Descendants(ctx, root, incl)
}

func (c *countingElements) MergePatch(ctx context.Context, id string, patch domain.Content) (*domain.Element, error) {
	c.patches.Add(1)
	return c.ElementRepository.MergePatch(ctx, id, patch)
}

func (c *countingElements) reads() int64 {
	return c.gets.Load() + c.getMany.Load() + c.children.Load() + c.desc.Load()
}

// seedScaleBoard puts one board with n columns under it, each holding cards, and
// returns the column ids.
func seedScaleBoard(ctx context.Context, repo *memory.ElementRepo, cols, cards int) []string {
	now := time.Now().UTC()
	put := func(el *domain.Element) {
		el.CreatedBy = scaleOwner
		el.CreatedAt, el.UpdatedAt = now, now
		_ = repo.Insert(ctx, el)
	}
	put(&domain.Element{ID: scaleBoard, Type: domain.TypeBoard,
		Content: domain.Content{"title": "Big"}, ACL: &domain.ACL{OwnerID: scaleOwner}})
	ids := make([]string, 0, cols)
	for c := 0; c < cols; c++ {
		colID := fmt.Sprintf("5ca1c0000000000000%06x", c)
		put(&domain.Element{ID: colID, Type: domain.TypeColumn,
			Content:  domain.Content{"title": fmt.Sprintf("Stage %d", c)},
			Location: domain.Location{ParentID: scaleBoard, Section: domain.SectionCanvas, Position: domain.Point{X: float64(c) * 344}, Width: 320, Height: 400}})
		ids = append(ids, colID)
		for k := 0; k < cards; k++ {
			put(&domain.Element{ID: fmt.Sprintf("5ca1d000000000%04x%06x", c, k), Type: domain.TypeCard,
				Content:  domain.Content{"textPreview": fmt.Sprintf("stage %d item %d", c, k)},
				Location: domain.Location{ParentID: colID, Section: domain.SectionCanvas, Index: float64(k)}})
		}
	}
	return ids
}

// createOps builds n creates on one board — the shape of a large approved plan.
func createOps(parent string, n int) []domain.Op {
	ops := make([]domain.Op, 0, n)
	for i := 0; i < n; i++ {
		ops = append(ops, domain.Op{
			ElementID: fmt.Sprintf("5ca1e100000000000000%04x", i),
			Action:    domain.ActionCreate,
			Changes: map[string]any{
				"type":     string(domain.TypeCard),
				"content":  map[string]any{"textPreview": fmt.Sprintf("staged %d", i)},
				"location": map[string]any{"parentId": parent, "section": "CANVAS"},
			},
		})
	}
	return ops
}

// A delegated grant allows 240 ops and no test anywhere applied more than a
// handful. What this pins is the SHAPE of the cost: pre-validation plus the
// write, bounded per op, with no walk of the board per op hiding inside it.
func TestScale_ApplyOf240OpsStaysLinear(t *testing.T) {
	ctx := context.Background()
	base := memory.NewElementRepo()
	seedScaleBoard(ctx, base, 8, 20)
	elements := &countingElements{ElementRepository: base}
	access := NewAccessResolver(elements)
	svc := NewTransactionService(elements, memory.NewTransactionRepo(), access, nil,
		IDGenerator(func() string { return "5ca1e00000000000000000ff" }), zap.NewNop())

	const n = 240
	ops := createOps(scaleBoard, n)
	before := elements.reads()
	if _, err := svc.Apply(ctx, &domain.Principal{Sub: scaleOwner}, scaleBoard, "bench", ops); err != nil {
		t.Fatalf("apply %d ops: %v", n, err)
	}
	perOp := float64(elements.reads()-before) / float64(n)
	// Six is generous and still catches the failure that matters: a walk of the
	// board's tree per op reads in the hundreds, not the units.
	if perOp > 6 {
		t.Errorf("%.1f reads per op over a %d-op transaction — the cost is superlinear in the plan size", perOp, n)
	}
}

// The canonical agent plan is a FILING run, where every action is a move — and
// a move was the expensive op, not the cheap one. Four independent walks asked
// the same containment question over the same elements in the same request:
// verifyOpScope, verifyDelegation, assertNoCycle, and captureMoves with its own
// un-memoised Get plus an ancestor walk per op, standing ten lines below a cache
// built for exactly that. A 240-op plan over a four-deep workspace issued on the
// order of a thousand serial round trips before a byte was written, which made
// the real ceiling on plan size latency rather than the budget number.
func TestScale_AFilingRunDoesNotWalkTheTreePerMove(t *testing.T) {
	ctx := context.Background()
	base := memory.NewElementRepo()
	cols := seedScaleBoard(ctx, base, 8, 30)
	elements := &countingElements{ElementRepository: base}
	access := NewAccessResolver(elements)
	svc := NewTransactionService(elements, memory.NewTransactionRepo(), access, nil,
		IDGenerator(func() string { return "5ca1e00000000000000000fe" }), zap.NewNop())

	// Every card in the first four columns files into the fifth: the shape of
	// "put everything that is done into Done".
	var ops []domain.Op
	for c := 0; c < 4; c++ {
		for k := 0; k < 30; k++ {
			ops = append(ops, domain.Op{
				ElementID: fmt.Sprintf("5ca1d000000000%04x%06x", c, k),
				Action:    domain.ActionMove,
				Changes: domain.Content{"location": map[string]any{
					"parentId": cols[4], "section": "CANVAS", "index": float64(k),
				}},
			})
		}
	}
	before := elements.reads()
	if _, err := svc.Apply(ctx, &domain.Principal{Sub: scaleOwner}, scaleBoard, "bench", ops); err != nil {
		t.Fatalf("apply %d moves: %v", len(ops), err)
	}
	perOp := float64(elements.reads()-before) / float64(len(ops))
	// One batch per level of depth plus the connector tidy-up's one read per
	// canvas — a small constant, and nowhere near the per-op walk it replaced.
	if perOp > 1 {
		t.Errorf("%.1f reads per move over a %d-op filing run — the containment walk is still per op",
			perOp, len(ops))
	}
}

// Opening a board is the single hottest read in the product, and it was never
// measured against a board anyone would call big.
func TestScale_BoardChildrenReadsPerContainerNotPerCard(t *testing.T) {
	ctx := context.Background()
	readsFor := func(cards int) int64 {
		base := memory.NewElementRepo()
		seedScaleBoard(ctx, base, 10, cards)
		elements := &countingElements{ElementRepository: base}
		boards := NewBoardService(elements, nil, NewAccessResolver(elements))
		if _, err := boards.Children(ctx, &domain.Principal{Sub: scaleOwner}, scaleBoard); err != nil {
			t.Fatalf("children: %v", err)
		}
		return elements.reads()
	}
	small, large := readsFor(10), readsFor(100)
	if small != large {
		t.Errorf("ten times the cards moved the read count from %d to %d — "+
			"a board open is issuing a query per card", small, large)
	}
}

// ---- benchmarks -------------------------------------------------------------

func BenchmarkApplyWithMeta240Ops(b *testing.B) {
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		elements := memory.NewElementRepo()
		seedScaleBoard(ctx, elements, 8, 20)
		access := NewAccessResolver(elements)
		svc := NewTransactionService(elements, memory.NewTransactionRepo(), access, nil,
			IDGenerator(func() string { return "5ca1e00000000000000000ff" }), zap.NewNop())
		ops := createOps(scaleBoard, 240)
		b.StartTimer()
		if _, err := svc.Apply(ctx, &domain.Principal{Sub: scaleOwner}, scaleBoard, "bench", ops); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBoardChildren1000Elements(b *testing.B) {
	ctx := context.Background()
	elements := memory.NewElementRepo()
	seedScaleBoard(ctx, elements, 20, 49) // 1 board + 20 columns + 980 cards
	boards := NewBoardService(elements, nil, NewAccessResolver(elements))
	p := &domain.Principal{Sub: scaleOwner}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := boards.Children(ctx, p, scaleBoard); err != nil {
			b.Fatal(err)
		}
	}
}
