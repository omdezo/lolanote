package memory

import (
	"context"
	"sync"

	"qomranote/backend/internal/domain"
)

// LabelRepo is the in-memory LabelRepository used by tests.
//
// It exists because the agent can now coin labels, and the property that
// matters — a discarded preview leaves the user's taxonomy untouched — cannot
// be asserted without a repository to observe.
type LabelRepo struct {
	mu   sync.RWMutex
	rows map[string]*domain.Label
}

func NewLabelRepo() *LabelRepo {
	return &LabelRepo{rows: map[string]*domain.Label{}}
}

func (r *LabelRepo) Insert(_ context.Context, l *domain.Label) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rows[l.ID]; exists {
		return domain.ErrConflict
	}
	cp := *l
	r.rows[l.ID] = &cp
	return nil
}

func (r *LabelRepo) Get(_ context.Context, id string) (*domain.Label, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l, ok := r.rows[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *l
	return &cp, nil
}

func (r *LabelRepo) ListByOwner(_ context.Context, ownerSub string) ([]*domain.Label, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*domain.Label
	for _, l := range r.rows {
		if l.OwnerID == ownerSub {
			cp := *l
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *LabelRepo) Update(_ context.Context, l *domain.Label) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[l.ID]; !ok {
		return domain.ErrNotFound
	}
	cp := *l
	r.rows[l.ID] = &cp
	return nil
}

func (r *LabelRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rows, id)
	return nil
}

func (r *LabelRepo) DeleteByOwner(_ context.Context, ownerSub string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, l := range r.rows {
		if l.OwnerID == ownerSub {
			delete(r.rows, id)
		}
	}
	return nil
}

func (r *LabelRepo) IncrementUsage(_ context.Context, id string, delta int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.rows[id]
	if !ok {
		return domain.ErrNotFound
	}
	l.UsageCount += delta
	return nil
}

// Count is a test affordance: how many labels exist for an owner.
func (r *LabelRepo) Count(ownerSub string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, l := range r.rows {
		if l.OwnerID == ownerSub {
			n++
		}
	}
	return n
}

var _ domain.LabelRepository = (*LabelRepo)(nil)
