package agentmem

import (
	"context"
	"sync"
	"time"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/domain"
)

// MemoryRepo is an in-memory agent.MemoryStore.
//
// It exists so the whole standing-rules loop — save, rank, render, cite, decay —
// can be exercised by `go test` with no database, the same way RunRepo and
// EventRepo let the harness run without one. The suspension arithmetic lives
// here rather than in the service for a reason worth stating: "overridden twice
// is suspended" is a property of the store, and a rule that decayed differently
// depending on which adapter was wired would be a rule nobody could reason about.
type MemoryRepo struct {
	mu    sync.RWMutex
	items map[string]*agent.Memory
}

// NewMemoryRepo constructs an empty store.
func NewMemoryRepo() *MemoryRepo { return &MemoryRepo{items: map[string]*agent.Memory{}} }

var _ agent.MemoryStore = (*MemoryRepo)(nil)

func cloneMemory(m *agent.Memory) *agent.Memory {
	cp := *m
	if m.Rule != nil {
		r := *m.Rule
		r.RunIDs = append([]string(nil), m.Rule.RunIDs...)
		cp.Rule = &r
	}
	cp.EvidenceRunIDs = append([]string(nil), m.EvidenceRunIDs...)
	return &cp
}

// List returns this board's rules plus the tenant's account-wide ones. Both, in
// one read, because the digest has to state a precedence between them and
// cannot do that having been handed only one.
func (r *MemoryRepo) List(_ context.Context, tenant, boardID string) ([]*agent.Memory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*agent.Memory
	for _, m := range r.items {
		if m.Tenant != tenant {
			continue
		}
		if m.BoardID != "" && m.BoardID != boardID {
			continue
		}
		out = append(out, cloneMemory(m))
	}
	return out, nil
}

// Upsert writes a rule, preserving the usage counters an existing row carries.
//
// Preserving them is the point: a rule re-saved from the suggestion card is the
// SAME rule, and resetting its history on every save would make decay
// unreachable for exactly the rules people confirm most often.
func (r *MemoryRepo) Upsert(_ context.Context, m *agent.Memory) error {
	if m == nil || m.ID == "" {
		return domain.ErrValidation
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prior, ok := r.items[m.ID]; ok {
		m.Hits, m.Misses = prior.Hits, prior.Misses
		m.CreatedAt = prior.CreatedAt
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.Status == "" {
		m.Status = agent.MemoryActive
	}
	r.items[m.ID] = cloneMemory(m)
	return nil
}

func (r *MemoryRepo) Delete(_ context.Context, tenant, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.items[id]; ok && m.Tenant == tenant {
		delete(r.items, id)
		return nil
	}
	return domain.ErrNotFound
}

// overrideLimit is how many overrides suspend a rule.
//
// Two, and never one: a person departing from their own convention once is a
// person making an exception, and auto-suspending on it would make the memory
// forget the moment it was inconvenient. Suspended rather than deleted, so the
// list can still say what it used to believe.
const overrideLimit = 2

// Record folds one run's usage back in.
func (r *MemoryRepo) Record(_ context.Context, tenant string, applied, overridden []string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range applied {
		m, ok := r.items[id]
		if !ok || m.Tenant != tenant {
			continue // an id nothing knows must never create a row
		}
		m.Hits++
		m.LastConfirmedAt = at
	}
	for _, id := range overridden {
		m, ok := r.items[id]
		if !ok || m.Tenant != tenant {
			continue
		}
		m.Misses++
		if m.Misses >= overrideLimit {
			m.Status = agent.MemorySuspended
		}
	}
	return nil
}

func (r *MemoryRepo) DeleteByTenant(_ context.Context, tenant string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, m := range r.items {
		if m.Tenant == tenant {
			delete(r.items, id)
		}
	}
	return nil
}
