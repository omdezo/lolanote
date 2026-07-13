package domain

import (
	"context"
	"time"
)

// Repository interfaces are the ports of the architecture: services depend on
// these, the mongo package implements them. Nothing above this layer knows
// which database backs the store.

// ElementFilter narrows element queries.
type ElementFilter struct {
	ParentID       string
	Section        Section // optional
	Types          []ElementType
	IncludeDeleted bool
}

// ElementRepository persists the element graph.
type ElementRepository interface {
	Insert(ctx context.Context, el *Element) error
	Get(ctx context.Context, id string) (*Element, error)
	GetMany(ctx context.Context, ids []string) ([]*Element, error)
	// Children answers the core query: all live elements owned by a parent.
	Children(ctx context.Context, f ElementFilter) ([]*Element, error)
	// Descendants walks the containment tree breadth-first from root.
	Descendants(ctx context.Context, rootID string, includeDeleted bool) ([]*Element, error)
	// MergePatch deep-merges a JSON-merge-patch into the document
	// (content/location/labelIds…), bumps updatedAt, returns the new doc.
	MergePatch(ctx context.Context, id string, patch Content) (*Element, error)
	SetACL(ctx context.Context, id string, acl *ACL) error
	// SoftDelete stamps a shared trashBatchId across every id so the delete
	// can be restored as one unit (§3.4 cascade semantics).
	SoftDelete(ctx context.Context, ids []string, by, batchID string, at time.Time) error
	Restore(ctx context.Context, ids []string) error
	// RestoreBatch un-trashes every element sharing a trashBatchId.
	RestoreBatch(ctx context.Context, batchID string) error
	HardDelete(ctx context.Context, ids []string) error
	// Trashed lists a user's trash: everything they deleted plus deletions
	// inside boards they own.
	Trashed(ctx context.Context, ownerSub string) ([]*Element, error)
	// Search does a text lookup across the caller's reachable elements.
	Search(ctx context.Context, ownerSub, query string, limit int) ([]*Element, error)
	// CloneInstances lists CLONE elements pointing at a source card.
	CloneInstances(ctx context.Context, sourceID string) ([]*Element, error)
	// BoardsOwnedBy lists boards owned/edited by a user (template picker, search scope).
	BoardsOwnedBy(ctx context.Context, sub string, templatesOnly bool) ([]*Element, error)
	// BoardsByShareToken finds boards whose edit or view link carries the token.
	BoardsByShareToken(ctx context.Context, token string) ([]*Element, error)
	// DueTaskReminders lists live TASK elements whose content.reminderAt
	// (RFC3339 UTC string) has passed and that have not been notified yet.
	DueTaskReminders(ctx context.Context, now time.Time, limit int) ([]*Element, error)
	// OwnedBoards lists every BOARD whose ACL owner is sub, optionally
	// including trashed ones — account deletion purges these trees.
	OwnedBoards(ctx context.Context, sub string, includeDeleted bool) ([]*Element, error)
	// RemoveEditorEverywhere strips a departing user from every board ACL.
	RemoveEditorEverywhere(ctx context.Context, sub string) error
	// CountsByParent aggregates live child counts per parent, per type —
	// feeds the "N boards, N cards, N files" board-tile subtitles.
	CountsByParent(ctx context.Context, parentIDs []string) (map[string]map[ElementType]int64, error)
	PurgeExpired(ctx context.Context, olderThan time.Time) (int64, error)
}

// TransactionRepository is the append-only mutation history.
type TransactionRepository interface {
	Insert(ctx context.Context, t *Transaction) error
	ListByBoard(ctx context.Context, boardID string, limit int) ([]*Transaction, error)
}

// UserRepository persists account rows.
type UserRepository interface {
	GetBySub(ctx context.Context, sub string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	Insert(ctx context.Context, u *User) error
	Update(ctx context.Context, u *User) error
	// UpdateSettings persists the (already normalized) settings document.
	UpdateSettings(ctx context.Context, sub string, s *UserSettings) error
	// Delete removes the account row (account deletion — the caller purges
	// the user's content first).
	Delete(ctx context.Context, sub string) error
}

// CommentRepository persists thread messages.
type CommentRepository interface {
	Insert(ctx context.Context, c *Comment) error
	Get(ctx context.Context, id string) (*Comment, error)
	ListByThread(ctx context.Context, threadID string) ([]*Comment, error)
	Update(ctx context.Context, c *Comment) error
}

// LabelRepository persists labels.
type LabelRepository interface {
	Insert(ctx context.Context, l *Label) error
	Get(ctx context.Context, id string) (*Label, error)
	ListByOwner(ctx context.Context, ownerSub string) ([]*Label, error)
	Update(ctx context.Context, l *Label) error
	Delete(ctx context.Context, id string) error
	DeleteByOwner(ctx context.Context, ownerSub string) error
	IncrementUsage(ctx context.Context, id string, delta int64) error
}

// AttachmentRepository persists the upload registry.
type AttachmentRepository interface {
	Insert(ctx context.Context, a *Attachment) error
	Get(ctx context.Context, id string) (*Attachment, error)
	Update(ctx context.Context, a *Attachment) error
	// StalePresigned lists attachments that were presigned but never completed
	// before the cutoff — abandoned uploads to garbage-collect.
	StalePresigned(ctx context.Context, olderThan time.Time) ([]*Attachment, error)
	Delete(ctx context.Context, id string) error
	DeleteByOwner(ctx context.Context, ownerSub string) error
}

// NotificationRepository persists in-app notifications.
type NotificationRepository interface {
	Insert(ctx context.Context, n *Notification) error
	ListByUser(ctx context.Context, sub string, unreadOnly bool, limit int) ([]*Notification, error)
	MarkRead(ctx context.Context, sub string, ids []string) error
	DeleteByUser(ctx context.Context, sub string) error
}

// IdentityProvider looks identities up in the auth server (Keycloak).
type IdentityProvider interface {
	FindUserByEmail(ctx context.Context, email string) (sub, displayName string, err error)
}

// AccountManager mutates identities in the auth server (Keycloak Admin API).
// Optional: nil when the admin client lacks credentials — profile changes then
// apply to the local mirror only and password changes are unavailable.
type AccountManager interface {
	// UpdateProfile changes the stored name/email. Empty fields are left as-is.
	UpdateProfile(ctx context.Context, sub, firstName, lastName, email string) error
	// VerifyPassword checks the user's current password (via a direct grant).
	VerifyPassword(ctx context.Context, usernameOrEmail, password string) error
	// SetPassword replaces the user's password (non-temporary).
	SetPassword(ctx context.Context, sub, newPassword string) error
	// DeleteUser removes the identity permanently.
	DeleteUser(ctx context.Context, sub string) error
}

// Presigner abstracts object storage (Cloudflare R2 or the local dev driver).
type Presigner interface {
	// PresignPut returns where the client should PUT the bytes and the URL
	// the stored object will be readable from afterwards.
	PresignPut(ctx context.Context, key, contentType string, size int64) (uploadURL, publicURL string, err error)
}

// TransactionBroadcaster pushes committed transactions to every live client
// on a board except the originator (the realtime hub implements this).
type TransactionBroadcaster interface {
	BroadcastTransaction(boardID string, t *Transaction)
}

// EventBroadcaster pushes ad-hoc realtime events: to everyone on a board
// (new comments) or to every connection of one user across boards (new
// notifications). The realtime hub implements this.
type EventBroadcaster interface {
	BroadcastEvent(boardID, event string, data any)
	NotifyUser(sub, event string, data any)
}
