package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// ReminderService turns due task reminders into notifications (§4.11). It
// runs as a background sweep inside `serve`: every tick it collects TASK
// elements whose reminderAt has passed, notifies the assignee (falling back
// to the task creator), and stamps reminderSent so each reminder fires once.
// Email/push delivery rides the same notification records once SMTP lands.
type ReminderService struct {
	elements domain.ElementRepository
	access   *AccessResolver
	notifier *Notifier
	newID    IDGenerator
	log      *zap.Logger
}

// NewReminderService constructs the sweeper.
func NewReminderService(elements domain.ElementRepository, access *AccessResolver, notifier *Notifier, newID IDGenerator, log *zap.Logger) *ReminderService {
	return &ReminderService{elements: elements, access: access, notifier: notifier, newID: newID, log: log.Named("reminders")}
}

// SweepOnce processes everything currently due and returns how many
// reminders fired.
func (s *ReminderService) SweepOnce(ctx context.Context) (int, error) {
	due, err := s.elements.DueTaskReminders(ctx, time.Now().UTC(), 200)
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, task := range due {
		// Resolve the owning board for the deep link; a broken chain still
		// marks the reminder handled so it cannot wedge the sweep forever.
		var boardID, boardTitle string
		if _, board, err := s.access.Resolve(ctx, task.ID, &domain.Principal{Sub: task.CreatedBy}); err == nil && board != nil {
			boardID, boardTitle = board.ID, board.Title()
		}

		recipient, _ := task.Content["assigneeId"].(string)
		if recipient == "" {
			recipient = task.CreatedBy
		}
		text, _ := task.Content["text"].(string)
		message := "Reminder: \"" + text + "\""
		if boardTitle != "" {
			message += " on \"" + boardTitle + "\""
		}
		s.notifier.Notify(ctx, &domain.Notification{
			ID: s.newID(), UserID: recipient, Kind: domain.NotifyReminder,
			ActorID: task.CreatedBy, BoardID: boardID, ElementID: task.ID,
			Message: message, CreatedAt: time.Now().UTC(),
		})
		if _, err := s.elements.MergePatch(ctx, task.ID, domain.Content{
			"content": map[string]any{"reminderSent": true},
		}); err != nil {
			s.log.Warn("mark reminder sent", zap.String("task", task.ID), zap.Error(err))
			continue
		}
		fired++
	}
	return fired, nil
}

// Run sweeps on the interval until the context ends.
func (s *ReminderService) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := s.SweepOnce(ctx); err != nil {
				s.log.Warn("reminder sweep failed", zap.Error(err))
			} else if n > 0 {
				s.log.Info("reminders fired", zap.Int("count", n))
			}
		}
	}
}
