package service

import (
	"context"

	"qomranote/backend/internal/domain"
)

// Notifier is the single gate every in-app notification passes through. It
// enforces the recipient's per-kind notification preferences (settings →
// Emails & notifications) so muted event kinds are dropped at the source
// rather than filtered in every producer. Email delivery honors the same
// switchboard once SMTP is wired.
type Notifier struct {
	notifications domain.NotificationRepository
	users         domain.UserRepository
}

// NewNotifier constructs the gate.
func NewNotifier(notifications domain.NotificationRepository, users domain.UserRepository) *Notifier {
	return &Notifier{notifications: notifications, users: users}
}

// wants reports whether the recipient accepts notifications of this kind.
// Unknown kinds default to allowed so new event types are never lost.
func (n *Notifier) wants(ctx context.Context, sub string, kind domain.NotificationKind) bool {
	u, err := n.users.GetBySub(ctx, sub)
	if err != nil {
		return true // no local mirror yet — deliver rather than drop
	}
	s := u.EffectiveSettings().Notifications
	switch kind {
	case domain.NotifyMention:
		return s.Mentions
	case domain.NotifyComment:
		return s.Comments
	case domain.NotifyShare:
		return s.Shares
	case domain.NotifyAssignment:
		return s.Assignments
	case domain.NotifyBoardChange:
		return s.BoardChanges
	case domain.NotifyReminder:
		return s.Reminders
	default:
		return true
	}
}

// Notify inserts the notification unless the recipient muted its kind.
// Best-effort by design: notification failures never fail the action that
// produced them, so errors are swallowed (matching the previous call sites).
func (n *Notifier) Notify(ctx context.Context, note *domain.Notification) {
	if note == nil || note.UserID == "" {
		return
	}
	if !n.wants(ctx, note.UserID, note.Kind) {
		return
	}
	_ = n.notifications.Insert(ctx, note)
}
