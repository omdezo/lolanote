package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// BlobRemover deletes stored bytes for a key (the local driver implements
// it; nil when the storage backend manages lifecycle itself, e.g. an R2
// bucket lifecycle rule).
type BlobRemover interface {
	Remove(key string) error
}

// MaintenanceService is the scheduled housekeeping previously only reachable
// through the manual CLI (§3.4, GAPS 1.5): expired-trash purge (90-day
// retention) and garbage collection of abandoned presigned uploads.
type MaintenanceService struct {
	elements    domain.ElementRepository
	attachments domain.AttachmentRepository
	blobs       BlobRemover // optional
	log         *zap.Logger
}

// NewMaintenanceService constructs the sweeper.
func NewMaintenanceService(elements domain.ElementRepository, attachments domain.AttachmentRepository, blobs BlobRemover, log *zap.Logger) *MaintenanceService {
	return &MaintenanceService{elements: elements, attachments: attachments, blobs: blobs, log: log.Named("maintenance")}
}

// SweepOnce purges expired trash and collects stale presigned attachments.
func (s *MaintenanceService) SweepOnce(ctx context.Context) {
	if purged, err := s.elements.PurgeExpired(ctx, time.Now().Add(-domain.TrashRetention)); err != nil {
		s.log.Warn("trash purge failed", zap.Error(err))
	} else if purged > 0 {
		s.log.Info("expired trash purged", zap.Int64("count", purged))
	}

	// Presigned-but-never-completed uploads older than a day are abandoned.
	stale, err := s.attachments.StalePresigned(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.log.Warn("stale attachment scan failed", zap.Error(err))
		return
	}
	removed := 0
	for _, a := range stale {
		if s.blobs != nil {
			if err := s.blobs.Remove(a.Key); err != nil {
				s.log.Warn("blob removal failed", zap.String("key", a.Key), zap.Error(err))
				continue
			}
		}
		if err := s.attachments.Delete(ctx, a.ID); err != nil {
			s.log.Warn("attachment row removal failed", zap.String("id", a.ID), zap.Error(err))
			continue
		}
		removed++
	}
	if removed > 0 {
		s.log.Info("abandoned uploads collected", zap.Int("count", removed))
	}
}

// Run sweeps immediately, then on the interval until the context ends.
func (s *MaintenanceService) Run(ctx context.Context, every time.Duration) {
	s.SweepOnce(ctx)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SweepOnce(ctx)
		}
	}
}
