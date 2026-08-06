package domain

import "time"

// User mirrors the Keycloak identity plus QomraNote-specific state. Created
// lazily on the first authenticated request; bootstrap also creates the
// private Home board every account is rooted at (§3.1).
type User struct {
	ID          string        `bson:"_id" json:"id"`
	KeycloakSub string        `bson:"keycloakSub" json:"keycloakSub"`
	Email       string        `bson:"email" json:"email"`
	DisplayName string        `bson:"displayName" json:"displayName"`
	AvatarURL   string        `bson:"avatarUrl,omitempty" json:"avatarUrl,omitempty"`
	HomeBoardID string        `bson:"homeBoardId" json:"homeBoardId"`
	Plan        string        `bson:"plan" json:"plan"` // free | pro
	Settings    *UserSettings `bson:"settings,omitempty" json:"settings,omitempty"`
	CreatedAt   time.Time     `bson:"createdAt" json:"createdAt"`
}

// EffectiveSettings returns the user's settings with defaults filled in for
// accounts created before the settings system existed.
func (u *User) EffectiveSettings() UserSettings {
	if u.Settings == nil {
		return DefaultSettings()
	}
	s := *u.Settings
	s.Normalize()
	return s
}

// Comment is one message inside a COMMENT_THREAD element. Authors edit only
// their own; messages cannot be removed from a thread once posted (§4.17).
type Comment struct {
	ID        string              `bson:"_id" json:"id"`
	ThreadID  string              `bson:"threadId" json:"threadId"`
	AuthorID  string              `bson:"authorId" json:"authorId"`
	Body      string              `bson:"body" json:"body"`
	Reactions map[string][]string `bson:"reactions,omitempty" json:"reactions,omitempty"` // emoji → user subs
	// Origin distinguishes a person's message from the assistant's.
	//
	// The agent's one channel for talking ON the board wrote a row claiming the
	// human had typed it. AuthorID has to stay the human — it is the ACL key
	// and the edit key — so the honesty lives here, and the bubble renders with
	// the assistant's face and a link to the run that wrote it.
	Origin     string `bson:"origin,omitempty" json:"origin,omitempty"`
	AgentRunID string `bson:"agentRunId,omitempty" json:"agentRunId,omitempty"`
	// Mentions are the subs this comment flags, resolved when it was written.
	//
	// Carried on the row rather than re-parsed out of Body: the agent's comments
	// name people by an alias that only existed for the life of one run, so
	// there is nothing in the text left to parse afterwards. This is what lets a
	// comment written by a run ring the same @mention notification a human's
	// does — without it the tool accepted the argument and told nobody.
	Mentions  []string   `bson:"mentions,omitempty" json:"mentions,omitempty"`
	CreatedAt time.Time  `bson:"createdAt" json:"createdAt"`
	EditedAt  *time.Time `bson:"editedAt,omitempty" json:"editedAt,omitempty"`
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
	NotifyReminder    NotificationKind = "reminder"
	// NotifyTrashExpiring warns that something deleted is about to stop being
	// recoverable — JN21. The product made four recovery promises with four
	// lifetimes and never showed one of them; the trash window in particular was
	// enforced silently by a sweep that logged a count and told nobody. The
	// person who deleted a sequence in March had no way to learn, in June, that
	// June was the last week to change their mind.
	NotifyTrashExpiring NotificationKind = "trashExpiring"
	// NotifyAgentRun is the outcome of an assistant run.
	//
	// The event most worth notifying about had no producer at all. A run that
	// auto-applied, a plan quarantined and waiting for a decision, or a run
	// finishing after the tab closed all landed silently: the realtime event
	// reaches only open sockets on that board, and the board-open recovery only
	// finds a run on the board you happen to return to — so a run on board A
	// finishing while you are on board B left no trace anywhere a person looks.
	// On a shared board an assistant run is the largest single change anyone
	// makes all week and it produced one signal, visible only to whoever was
	// already watching.
	NotifyAgentRun NotificationKind = "agentRun"
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
	// Origin is OriginHuman or OriginAgent — who really did the thing this bell
	// is about.
	//
	// The write path knew the truth two frames up the stack and threw it away
	// before composing the message, so "Omar assigned you a task" was rung for
	// an assignment Omar never saw. That is the most corrosive lie an agent can
	// tell in a team: it converts a model's guess into a social commitment from
	// a colleague.
	Origin string `bson:"origin,omitempty" json:"origin,omitempty"`
	// AgentRunID deep-links the bell to the run journal, which is the answer to
	// "why me?" — and the only place that answer exists.
	AgentRunID string    `bson:"agentRunId,omitempty" json:"agentRunId,omitempty"`
	CreatedAt  time.Time `bson:"createdAt" json:"createdAt"`
}

// Principal is the authenticated caller, extracted from a verified Keycloak
// token. Sub is the stable identity used across ACLs and ownership.
type Principal struct {
	Sub        string
	Email      string
	Name       string
	ShareToken string // optional X-Share-Token accompanying the request
	// SharePassword is the password the caller supplied for a password-protected
	// view link, verbatim as it arrived.
	//
	// It rides the principal because the password is a property of the CREDENTIAL,
	// not of one route. It used to be an argument to exactly one handler —
	// GET /shared/:token — so a link the owner had put a password on was gated
	// only on the courtesy call the browser makes first, and the token alone
	// opened /boards/:id, /children and /export?format=json. The client has been
	// sending X-Share-Password on every request since password links shipped;
	// nothing but that handler ever read it.
	SharePassword string
	// ExpiresAt is the verified token's expiry. Long-lived sessions built on
	// one verification (WebSockets) must not outlive the credential.
	ExpiresAt time.Time
	// RequestID and Source are stamped at the auth boundary and carried to
	// every write the request causes.
	//
	// The HTTP layer has minted a request id since the first commit and thrown
	// it away one middleware later, so provenance was a manual join across three
	// stores. Riding on the Principal rather than through fifteen signatures
	// means a call site cannot forget to pass it — which is the property that
	// makes "every write records its source" an invariant rather than a habit.
	RequestID string
	Source    string
	// IP and UserAgent are stamped at the same boundary as RequestID and travel
	// the same way, because §9 requires an audit row to say who did what, when,
	// and FROM WHERE — and the transport is the only layer that can know the
	// last one. A row written from a service that never saw an HTTP request
	// still locates the caller, for the same reason RequestID does.
	//
	// UserAgent is attacker-controlled and lands in a database row: it arrives
	// here already bounded, and nothing downstream may treat it as trustworthy
	// input.
	IP        string
	UserAgent string
	// PlatformRoles are the realm roles the verified token carried; empty for
	// everyone but platform staff. Always a copy of the claims slice, never an
	// alias: the principal is snapshotted on paths that outlive the request
	// (the WebSocket ticket), and a shared backing array would let one of those
	// copies change under the other.
	PlatformRoles []string
	// Delegation is non-nil only when the AI agent is acting on this
	// principal's behalf. It ATTENUATES: ACL checks still key on Sub, and the
	// write path additionally requires every op to satisfy the grant. Human
	// requests carry nil here and behave exactly as before.
	Delegation *Delegation
}

// IsAgent reports whether this principal is the AI harness acting under a grant
// rather than a human at a keyboard.
func (p *Principal) IsAgent() bool { return p != nil && p.Delegation != nil }

// HasPlatformRole reports whether the caller holds the named realm role. Nil
// principal and empty role both answer false, so a missing caller or an unset
// role constant cannot read as a grant.
func (p *Principal) HasPlatformRole(r string) bool {
	if p == nil || r == "" {
		return false
	}
	for _, have := range p.PlatformRoles {
		if have == r {
			return true
		}
	}
	return false
}

// BreadcrumbEntry is one hop in the Home → … → current-board path (§3.2).
type BreadcrumbEntry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
