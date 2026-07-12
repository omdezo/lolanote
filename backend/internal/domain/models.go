package domain

import "time"

// User mirrors the Keycloak identity plus QomraNote-specific state. Created
// lazily on the first authenticated request; bootstrap also creates the
// private Home board every account is rooted at (§3.1).
type User struct {
	ID          string    `bson:"_id" json:"id"`
	KeycloakSub string    `bson:"keycloakSub" json:"keycloakSub"`
	Email       string    `bson:"email" json:"email"`
	DisplayName string    `bson:"displayName" json:"displayName"`
	AvatarURL   string    `bson:"avatarUrl,omitempty" json:"avatarUrl,omitempty"`
	HomeBoardID string    `bson:"homeBoardId" json:"homeBoardId"`
	Plan        string    `bson:"plan" json:"plan"` // free | pro
	CreatedAt   time.Time `bson:"createdAt" json:"createdAt"`
}

// Comment is one message inside a COMMENT_THREAD element. Authors edit only
// their own; messages cannot be removed from a thread once posted (§4.17).
type Comment struct {
	ID        string              `bson:"_id" json:"id"`
	ThreadID  string              `bson:"threadId" json:"threadId"`
	AuthorID  string              `bson:"authorId" json:"authorId"`
	Body      string              `bson:"body" json:"body"`
	Reactions map[string][]string `bson:"reactions,omitempty" json:"reactions,omitempty"` // emoji → user subs
	CreatedAt time.Time           `bson:"createdAt" json:"createdAt"`
	EditedAt  *time.Time          `bson:"editedAt,omitempty" json:"editedAt,omitempty"`
}

// Label is the tagging layer across items (§4.18). Private to its owner
// until used on a shared board.
type Label struct {
	ID         string    `bson:"_id" json:"id"`
	OwnerID    string    `bson:"ownerId" json:"ownerId"`
	Name       string    `bson:"name" json:"name"`
	Color      string    `bson:"color" json:"color"`
	UsageCount int64     `bson:"usageCount" json:"usageCount"`
	CreatedAt  time.Time `bson:"createdAt" json:"createdAt"`
}

// AttachmentStatus tracks the presigned-upload lifecycle.
type AttachmentStatus string

const (
	AttachmentPresigned AttachmentStatus = "presigned"
	AttachmentUploaded  AttachmentStatus = "uploaded"
)

// Attachment registers a file upload. Bytes go directly to object storage
// via a presigned URL — they never transit the API (§9.10).
type Attachment struct {
	ID          string           `bson:"_id" json:"id"`
	OwnerID     string           `bson:"ownerId" json:"ownerId"`
	Key         string           `bson:"key" json:"key"`
	Filename    string           `bson:"filename" json:"filename"`
	ContentType string           `bson:"contentType" json:"contentType"`
	Size        int64            `bson:"size" json:"size"`
	Status      AttachmentStatus `bson:"status" json:"status"`
	PublicURL   string           `bson:"publicUrl,omitempty" json:"publicUrl,omitempty"`
	CreatedAt   time.Time        `bson:"createdAt" json:"createdAt"`
}

// NotificationKind enumerates the events that notify users (§6.2).
type NotificationKind string

const (
	NotifyMention     NotificationKind = "mention"
	NotifyAssignment  NotificationKind = "assignment"
	NotifyComment     NotificationKind = "comment"
	NotifyBoardChange NotificationKind = "boardChange"
	NotifyShare       NotificationKind = "share"
)

// Notification is one in-app notification row.
type Notification struct {
	ID        string           `bson:"_id" json:"id"`
	UserID    string           `bson:"userId" json:"userId"`
	Kind      NotificationKind `bson:"kind" json:"kind"`
	ActorID   string           `bson:"actorId" json:"actorId"`
	BoardID   string           `bson:"boardId,omitempty" json:"boardId,omitempty"`
	ElementID string           `bson:"elementId,omitempty" json:"elementId,omitempty"`
	Message   string           `bson:"message" json:"message"`
	Read      bool             `bson:"read" json:"read"`
	CreatedAt time.Time        `bson:"createdAt" json:"createdAt"`
}

// Principal is the authenticated caller, extracted from a verified Keycloak
// token. Sub is the stable identity used across ACLs and ownership.
type Principal struct {
	Sub        string
	Email      string
	Name       string
	ShareToken string // optional X-Share-Token accompanying the request
}

// BreadcrumbEntry is one hop in the Home → … → current-board path (§3.2).
type BreadcrumbEntry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
