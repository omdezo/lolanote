package memory

import (
	"context"
	"sync"
	"time"

	"qomranote/backend/internal/domain"
)

// AttachmentRepo is the in-memory AttachmentRepository used by tests. It exists
// because the agent can now look at images, and the guards that matter — size,
// type, upload state — cannot be asserted without a repository to seed.
type AttachmentRepo struct {
	mu   sync.RWMutex
	rows map[string]*domain.Attachment
}

func NewAttachmentRepo() *AttachmentRepo {
	return &AttachmentRepo{rows: map[string]*domain.Attachment{}}
}

func (r *AttachmentRepo) Insert(_ context.Context, a *domain.Attachment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *a
	r.rows[a.ID] = &cp
	return nil
}

func (r *AttachmentRepo) Get(_ context.Context, id string) (*domain.Attachment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.rows[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (r *AttachmentRepo) Update(_ context.Context, a *domain.Attachment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[a.ID]; !ok {
		return domain.ErrNotFound
	}
	cp := *a
	r.rows[a.ID] = &cp
	return nil
}

func (r *AttachmentRepo) StalePresigned(_ context.Context, olderThan time.Time) ([]*domain.Attachment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*domain.Attachment
	for _, a := range r.rows {
		if a.Status == domain.AttachmentPresigned && a.CreatedAt.Before(olderThan) {
			cp := *a
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *AttachmentRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rows, id)
	return nil
}

func (r *AttachmentRepo) DeleteByOwner(_ context.Context, ownerSub string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, a := range r.rows {
		if a.OwnerID == ownerSub {
			delete(r.rows, id)
		}
	}
	return nil
}

var _ domain.AttachmentRepository = (*AttachmentRepo)(nil)
