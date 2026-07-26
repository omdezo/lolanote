package agentmem

import (
	"context"
	"sort"
	"sync"

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

// DeleteByTenant is a no-op here: the in-memory journal is per-test and does
// not carry a tenant index. The Mongo adapter implements the real purge.
func (r *EventRepo) DeleteByTenant(_ context.Context, _ string) error { return nil }

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
