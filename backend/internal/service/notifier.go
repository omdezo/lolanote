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
	events        domain.EventBroadcaster // optional: live badge pushes
}

// NewNotifier constructs the gate.
func NewNotifier(notifications domain.NotificationRepository, users domain.UserRepository) *Notifier {
	return &Notifier{notifications: notifications, users: users}
}

// AttachEvents enables realtime "notification.new" pushes to the recipient's
// open connections (the bell updates without waiting for the poll).
func (n *Notifier) AttachEvents(events domain.EventBroadcaster) { n.events = events }

// wants reports whether the recipient accepts notifications of this kind.
// Unknown kinds default to allowed so new event types are never lost.
func (n *Notifier) wants(ctx context.Context, sub string, kind domain.NotificationKind) bool {
	// A deployment can wire notifications without a local user mirror, and the
	// agent's outcome producer now calls this from paths that predate one. A nil
	// store means "no preference on file", which is delivery — the same answer
	// the error branch below already gives.
	if n.users == nil {
		return true
	}
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
	case domain.NotifyAgentRun:
		return s.WantsAgentRuns()
	default:
		return true
	}
}

// NotificationRetractor is the half of the notification store that can take a
// bell back. Optional and discovered by assertion, like the other capabilities
// the storage layer grew after its port was written.
type NotificationRetractor interface {
	DeleteByIDs(ctx context.Context, ids []string) error
}

// Retract withdraws notifications a reverted change had rung.
//
// The inbox was outside the invertible set: undoing a run took its tasks back
// and left the colleague holding an item for work that no longer existed, while
// the outcome card said the board was back as it was. Deleting rather than
// superseding, because the honest end state is that the assignment never
// happened — there is nothing left to link to.
func (n *Notifier) Retract(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}
	retractor, ok := n.notifications.(NotificationRetractor)
	if !ok {
		return
	}
	if err := retractor.DeleteByIDs(ctx, ids); err != nil {
		return
	}
	// No per-user push: the recipient's bell recounts on its next read, and
	// pushing "one fewer" would need the recipient list this call does not have.
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
	if n.notifications == nil {
		return
	}
	if err := n.notifications.Insert(ctx, note); err != nil {
		return
	}
	if n.events != nil {
		n.events.NotifyUser(note.UserID, "notification.new", note)
	}
}
