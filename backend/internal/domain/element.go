// Package domain is the pure model layer: no HTTP, no Mongo, no framework
// imports. Everything on a QomraNote board is a typed Element positioned by a
// location.parentId hierarchy — the same closed-set element model Milanote
// ships (19 types, Research Report §9.4 / Appendix A).
package domain

import (
	"regexp"
	"time"
)

// ElementType is the closed set of element kinds.
type ElementType string

const (
	TypeBoard         ElementType = "BOARD"
	TypeAlias         ElementType = "ALIAS" // shortcut to a board elsewhere
	TypeColumn        ElementType = "COLUMN"
	TypeCard          ElementType = "CARD" // rich-text note
	TypeLink          ElementType = "LINK"
	TypeLine          ElementType = "LINE"
	TypeImage         ElementType = "IMAGE"
	TypeFile          ElementType = "FILE"
	TypeCommentThread ElementType = "COMMENT_THREAD"
	TypeTaskList      ElementType = "TASK_LIST"
	TypeTask          ElementType = "TASK"
	TypeClone         ElementType = "CLONE" // synced-note instance
	TypeSketch        ElementType = "SKETCH"
	TypeAnnotation    ElementType = "ANNOTATION" // drawing on top of an image
	TypeColorSwatch   ElementType = "COLOR_SWATCH"
	TypeDocument      ElementType = "DOCUMENT"
	TypeTable         ElementType = "TABLE"
	TypeSkeleton      ElementType = "SKELETON" // client-side loading placeholder
	TypeUnknown       ElementType = "UNKNOWN"  // forward-compatibility fallback
)

// AllElementTypes enumerates every valid type (SKELETON/UNKNOWN included so
// old clients round-trip content they do not understand).
var AllElementTypes = map[ElementType]bool{
	TypeBoard: true, TypeAlias: true, TypeColumn: true, TypeCard: true,
	TypeLink: true, TypeLine: true, TypeImage: true, TypeFile: true,
	TypeCommentThread: true, TypeTaskList: true, TypeTask: true, TypeClone: true,
	TypeSketch: true, TypeAnnotation: true, TypeColorSwatch: true,
	TypeDocument: true, TypeTable: true, TypeSkeleton: true, TypeUnknown: true,
}

// Valid reports whether t is a recognized element type.
func (t ElementType) Valid() bool { return AllElementTypes[t] }

// IsContainer reports whether the type owns children via location.parentId.
func (t ElementType) IsContainer() bool {
	return t == TypeBoard || t == TypeColumn || t == TypeTaskList
}

// Section places an element within its parent board's spatial model:
// the freeform canvas, the slide-out Unsorted capture tray, or nowhere
// special (children of columns/lists use index ordering instead).
type Section string

const (
	SectionCanvas   Section = "CANVAS"
	SectionUnsorted Section = "UNSORTED"
)

// ObjectIDPattern matches Mongo ObjectIds; Milanote's client enforces the
// exact same shape on every element id (§9.4).
var ObjectIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{24}$`)

// Point is a canvas coordinate.
type Point struct {
	X float64 `bson:"x" json:"x"`
	Y float64 `bson:"y" json:"y"`
}

// Location describes containment plus placement: which parent owns the
// element, where it sits on the canvas, and how it is ordered inside
// ordered containers (columns, task lists, the Unsorted tray).
type Location struct {
	ParentID string  `bson:"parentId" json:"parentId"`
	Section  Section `bson:"section" json:"section"`
	Position Point   `bson:"position" json:"position"`
	Index    float64 `bson:"index" json:"index"` // fractional indexing keeps reorders single-writes
	Width    float64 `bson:"width" json:"width"`
	Height   float64 `bson:"height" json:"height"`
}

// ACL is meaningful on BOARD elements and cascades to all nested content
// (sharing a board shares its whole subtree, §6.1).
//
// The two link tokens are bearer credentials for the whole subtree, and they
// are `json:"-"` so no serialization boundary can emit one by accident. A view
// link holder used to be able to read the board's PUBLIC EDIT token straight
// out of an ordinary board GET, which turns read-only into read-write with no
// step in between. service.ShareState is the one door they leave through.
type ACL struct {
	OwnerID        string    `bson:"ownerId" json:"ownerId"`
	Editors        []string  `bson:"editors" json:"editors"`
	PublicEditLink string    `bson:"publicEditLink,omitempty" json:"-"`
	ViewLink       *ViewLink `bson:"viewLink,omitempty" json:"viewLink,omitempty"`
	// AgentPolicy is the owner's answer to "who may run the assistant here, and
	// how far may it go without me". Nil means the defaults below.
	AgentPolicy *AgentPolicy `bson:"agentPolicy,omitempty" json:"agentPolicy,omitempty"`
}

// AgentAllow is the audience a board-level agent permission is open to.
type AgentAllow string

const (
	// AgentAllowUnset means "use the default", which differs per field.
	AgentAllowUnset AgentAllow = ""
	// AgentAllowNone: nobody, including the owner.
	AgentAllowNone AgentAllow = "none"
	// AgentAllowOwner: only the person who owns the board.
	AgentAllowOwner AgentAllow = "owner"
	// AgentAllowEditors: anyone the ACL names as an editor.
	AgentAllowEditors AgentAllow = "editors"
)

// AgentPolicy is a property of the BOARD, not of the request.
//
// Delegation was correctly attenuated per principal and completely unaware that
// a board has more than one stakeholder. The only consent gate on a run was
// "can the caller edit here", so a collaborator could set autonomy=auto and
// have thirty model-chosen actions land on somebody else's board with no
// preview by anyone but themselves — while the owner had no way to say "no AI
// on this board", "preview only", or "at most $2/day of assistant here", which
// are ordinary requirements for a client-facing or contractual board.
//
// Owner-writable only, through the same requireOwner gate sharing goes through:
// the place an owner already decides who may edit is where they decide who may
// automate.
type AgentPolicy struct {
	// Allow is who may start a run at all. Default: editors.
	Allow AgentAllow `bson:"allow,omitempty" json:"allow,omitempty"`
	// AutoApply is who may have a plan written without a human preview.
	// Default: owner — which is the whole point of the item. A non-owner asking
	// for auto is DOWNGRADED to preview with a disclosed reason, never refused:
	// a hard refusal makes the assistant look broken on every shared board.
	AutoApply AgentAllow `bson:"autoApply,omitempty" json:"autoApply,omitempty"`
	// DailyCapUSD bounds model spend attributable to this board per UTC day.
	// Zero means "only the deployment-wide cap applies".
	DailyCapUSD float64 `bson:"dailyCapUsd,omitempty" json:"dailyCapUsd,omitempty"`
	// ChargedTo says whose ledger a run here draws from: "runner" (default) or
	// "owner". An owner who hosts a contractor's work can choose to carry it.
	ChargedTo string `bson:"capChargedTo,omitempty" json:"capChargedTo,omitempty"`
}

// ChargedToOwner reports whether runs on this board draw on the owner's budget.
func (a *AgentPolicy) ChargedToOwner() bool { return a != nil && a.ChargedTo == "owner" }

// MayRun reports whether a principal holding this role may start a run here.
// Nil policy — every board that predates the field — means editors, which is
// exactly what shipped before.
func (a *AgentPolicy) MayRun(isOwner bool) bool {
	allow := AgentAllowEditors
	if a != nil && a.Allow != AgentAllowUnset {
		allow = a.Allow
	}
	switch allow {
	case AgentAllowNone:
		return false
	case AgentAllowOwner:
		return isOwner
	default:
		return true
	}
}

// MayAutoApply reports whether this principal may skip the preview.
// The default is owner-only, deliberately narrower than what shipped.
func (a *AgentPolicy) MayAutoApply(isOwner bool) bool {
	allow := AgentAllowOwner
	if a != nil && a.AutoApply != AgentAllowUnset {
		allow = a.AutoApply
	}
	switch allow {
	case AgentAllowNone:
		return false
	case AgentAllowEditors:
		return true
	default:
		return isOwner
	}
}

// ViewLink is a read-only or presentation share link with optional feedback
// rights, password, and welcome message.
type ViewLink struct {
	Token          string `bson:"token" json:"-"`
	AllowFeedback  bool   `bson:"allowFeedback" json:"allowFeedback"`
	RequireAccount bool   `bson:"requireAccount" json:"requireAccount"`
	PasswordHash   string `bson:"passwordHash,omitempty" json:"-"`
	WelcomeMessage string `bson:"welcomeMessage,omitempty" json:"welcomeMessage,omitempty"`
}

// Content is the per-type payload. It stays schemaless at this layer —
// handlers/services validate what matters per type; unknown keys survive
// round-trips so newer clients never lose data on older servers.
type Content map[string]any

// Element is the single core abstraction: every card, board, line, comment
// thread, swatch — everything — is one of these.
type Element struct {
	ID        string      `bson:"_id" json:"id"`
	Type      ElementType `bson:"type" json:"type"`
	Location  Location    `bson:"location" json:"location"`
	Content   Content     `bson:"content" json:"content"`
	ACL       *ACL        `bson:"acl,omitempty" json:"acl,omitempty"`
	LabelIDs  []string    `bson:"labelIds,omitempty" json:"labelIds,omitempty"`
	CreatedBy string      `bson:"createdBy" json:"createdBy"`
	CreatedAt time.Time   `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time   `bson:"updatedAt" json:"updatedAt"`
	DeletedAt *time.Time  `bson:"deletedAt,omitempty" json:"deletedAt,omitempty"`
	DeletedBy string      `bson:"deletedBy,omitempty" json:"deletedBy,omitempty"`
	// TrashBatchID groups elements trashed in one delete so that restoring a
	// container brings back exactly what that delete removed — and nothing
	// that was already trashed separately beforehand (§3.4).
	TrashBatchID string `bson:"trashBatchId,omitempty" json:"trashBatchId,omitempty"`
}

// IsDeleted reports whether the element currently lives in the Trash.
func (e *Element) IsDeleted() bool { return e.DeletedAt != nil }

// Title extracts the human title for boards/columns/documents, falling back
// to the text preview notes carry for search.
func (e *Element) Title() string {
	for _, key := range []string{"title", "textPreview", "filename", "url"} {
		if v, ok := e.Content[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// TrashRetention is how long deleted items survive before permanent
// deletion (Milanote retains for 3 months, §3.4).
const TrashRetention = 90 * 24 * time.Hour
