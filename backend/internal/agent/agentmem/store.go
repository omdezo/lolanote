package agentmem

import (
	"context"
	"sort"
	"sync"
	"time"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/domain"
)

// Package agentmem provides in-memory agent stores for tests.
//
// It lives beside the agent package rather than in repository/memory because
// the agent imports the service layer, and repository/memory is imported by
// that layer's own tests — putting these adapters there would close an import
// cycle. Same role as repository/memory, one package over.

// In-memory agent stores. These exist so the whole harness — admission,
// delegation, the state machine, op compilation, verification, and revert — can
// be exercised by `go test` with no database and no API key. The model is the
// only thing a test replaces; everything else runs for real.

// RunRepo is an in-memory agent.RunStore.
type RunRepo struct {
	mu    sync.RWMutex
	items map[string]*agent.Run
}

// NewRunRepo constructs an empty store.
func NewRunRepo() *RunRepo { return &RunRepo{items: map[string]*agent.Run{}} }

var _ agent.RunStore = (*RunRepo)(nil)

func cloneRun(r *agent.Run) *agent.Run {
	cp := *r
	if r.Plan != nil {
		pl := *r.Plan
		cp.Plan = &pl
	}
	if r.Verdict != nil {
		v := *r.Verdict
		cp.Verdict = &v
	}
	cp.TransactionIDs = append([]string(nil), r.TransactionIDs...)
	cp.RevertedElementIDs = append([]string(nil), r.RevertedElementIDs...)
	// The timing maps are copied, not aliased. A store that hands back a map the
	// caller can still write through is not a store — a test asserting "the
	// stamp was persisted" would pass on a mutation that never reached it.
	if r.StateAt != nil {
		cp.StateAt = make(map[agent.RunState]time.Time, len(r.StateAt))
		for k, v := range r.StateAt {
			cp.StateAt[k] = v
		}
	}
	if r.RevertedAt != nil {
		cp.RevertedAt = make(map[string]time.Time, len(r.RevertedAt))
		for k, v := range r.RevertedAt {
			cp.RevertedAt[k] = v
		}
	}
	return &cp
}

// Insert stores a run, rejecting a second active run on the same board — the
// same guard the Mongo adapter enforces with a unique partial index (G8).
func (r *RunRepo) Insert(_ context.Context, run *agent.Run) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[run.ID]; exists {
		return domain.ErrConflict
	}
	for _, existing := range r.items {
		if existing.Task.RootBoardID == run.Task.RootBoardID && existing.State.Active() {
			return domain.ErrConflict
		}
	}
	run.Rev = 1
	r.items[run.ID] = cloneRun(run)
	return nil
}

func (r *RunRepo) Get(_ context.Context, id string) (*agent.Run, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if run, ok := r.items[id]; ok {
		return cloneRun(run), nil
	}
	return nil, domain.ErrNotFound
}

// Update is a compare-and-swap on rev: a stale writer loses rather than
// silently clobbering a newer state.
func (r *RunRepo) Update(_ context.Context, run *agent.Run, expectedRev int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.items[run.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.Rev != expectedRev {
		return domain.ErrConflict
	}
	run.Rev = stored.Rev + 1
	r.items[run.ID] = cloneRun(run)
	return nil
}

func (r *RunRepo) ActiveByBoard(_ context.Context, boardID string) (*agent.Run, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, run := range r.items {
		if run.Task.RootBoardID == boardID && run.State.Active() {
			return cloneRun(run), nil
		}
	}
	return nil, domain.ErrNotFound
}

// ListByBoard filters by tenant, and by board when boardID is non-empty (the
// empty case is the tenant-wide read the daily cost cap uses).
func (r *RunRepo) ListByBoard(_ context.Context, tenant, boardID string, limit int) ([]*agent.Run, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*agent.Run
	for _, run := range r.items {
		if run.Tenant != tenant {
			continue
		}
		if boardID != "" && run.Task.RootBoardID != boardID {
			continue
		}
		out = append(out, cloneRun(run))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *RunRepo) Unfinished(_ context.Context) ([]*agent.Run, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*agent.Run
	for _, run := range r.items {
		if run.State.Active() {
			out = append(out, cloneRun(run))
		}
	}
	return out, nil
}

func (r *RunRepo) DeleteByTenant(_ context.Context, tenant string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, run := range r.items {
		if run.Tenant == tenant {
			delete(r.items, id)
		}
	}
	return nil
}

// EventRepo is an in-memory agent.EventStore.
type EventRepo struct {
	mu     sync.RWMutex
	items  []*agent.Event
	nextBy map[string]int64
}

// NewEventRepo constructs an empty journal.
func NewEventRepo() *EventRepo {
	return &EventRepo{nextBy: map[string]int64{}}
}

var _ agent.EventStore = (*EventRepo)(nil)

// Append assigns the run's next sequence number, keeping the journal ordered
// and gap-free so a client cursor is meaningful.
func (r *EventRepo) Append(_ context.Context, ev *agent.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextBy[ev.RunID]++
	ev.Sequence = r.nextBy[ev.RunID]
	cp := *ev
	r.items = append(r.items, &cp)
	return nil
}

func (r *EventRepo) List(_ context.Context, runID string, since int64, limit int) ([]*agent.Event, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*agent.Event
	for _, ev := range r.items {
		if ev.RunID == runID && ev.Sequence > since {
			cp := *ev
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Aggregate groups events by type over a window, mirroring the Mongo `$group`
// so a test can assert on the same numbers a dashboard would read.
func (r *EventRepo) Aggregate(_ context.Context, tenant string, since time.Time, types []agent.EventType) (map[agent.EventType]int64, error) {
	want := map[agent.EventType]bool{}
	for _, t := range types {
		want[t] = true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[agent.EventType]int64{}
	for _, ev := range r.items {
		if ev.At.Before(since) {
			continue
		}
		if tenant != "" && ev.Tenant != tenant {
			continue
		}
		if len(want) > 0 && !want[ev.Type] {
			continue
		}
		out[ev.Type]++
	}
	return out, nil
}

// DeleteByTenant drops one tenant's rows. It used to be a no-op on the grounds
// that the in-memory journal carried no tenant; it carries one now, so the
// adapter can honour the same contract the Mongo one does.
func (r *EventRepo) DeleteByTenant(_ context.Context, tenant string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.items[:0]
	for _, ev := range r.items {
		if ev.Tenant != tenant {
			kept = append(kept, ev)
		}
	}
	r.items = kept
	return nil
}

// All returns every recorded event, for assertions.
func (r *EventRepo) All() []*agent.Event {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*agent.Event, 0, len(r.items))
	for _, ev := range r.items {
		cp := *ev
		out = append(out, &cp)
	}
	return out
}
