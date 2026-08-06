package domain

import (
	"context"
	"time"
)

// AuditOutcome is what happened, not what was attempted. A refusal is a row of
// its own: the attempts that were DENIED are the first thing anyone reads in a
// security review, and a log that only records successes cannot show them.
type AuditOutcome string

const (
	AuditOK     AuditOutcome = "ok"
	AuditDenied AuditOutcome = "denied"
	AuditError  AuditOutcome = "error"
)

// AuditActor is who acted, in full.
//
// Sub is always the identity the request authenticated as. The other three are
// the ways that identity can be reached by someone or something else, and each
// one is a question an auditor asks in its own right: ImpersonatedBy names the
// staff member acting as this user (§11.4), AgentRunID names the assistant run
// holding the delegation grant, and PlatformRole records the staff privilege
// that admitted the call. Collapsing any of them into Sub would make a support
// session indistinguishable from the customer's own work.
type AuditActor struct {
	Sub            string `bson:"sub" json:"sub"`
	ImpersonatedBy string `bson:"impersonatedBy,omitempty" json:"impersonatedBy,omitempty"`
	AgentRunID     string `bson:"agentRunId,omitempty" json:"agentRunId,omitempty"`
	PlatformRole   string `bson:"platformRole,omitempty" json:"platformRole,omitempty"`
}

// AuditTarget is what was acted on.
//
// Label is the human-readable name AS IT WAS when the event happened, copied
// rather than joined: the row has to stay readable after the board is renamed
// and after it is deleted, and a viewer that resolves ids live can show neither.
type AuditTarget struct {
	Type  string `bson:"type" json:"type"`
	ID    string `bson:"id" json:"id"`
	Label string `bson:"label,omitempty" json:"label,omitempty"`
}

// AuditEvent is one row of the append-only audit log (§9).
//
// Action is the dotted taxonomy — `org.member_role_changed`, `share.external`,
// `admin.impersonation_started`, `agent.run_applied` — kept a plain string so a
// new verb is a new call site rather than a migration, and queried by prefix so
// the segments stay meaningful.
type AuditEvent struct {
	ID string `bson:"_id" json:"id"`
	// OrgID is absent for personal-account events. Every org-scoped read filters
	// on it, so an empty value is a row no org viewer can ever return.
	OrgID   string       `bson:"orgId,omitempty" json:"orgId,omitempty"`
	Actor   AuditActor   `bson:"actor" json:"actor"`
	Action  string       `bson:"action" json:"action"`
	Target  AuditTarget  `bson:"target" json:"target"`
	Outcome AuditOutcome `bson:"outcome" json:"outcome"`
	IP      string       `bson:"ip,omitempty" json:"ip,omitempty"`
	UA      string       `bson:"ua,omitempty" json:"ua,omitempty"`
	// RequestID joins the row to the zap logs and to the transaction journal —
	// Principal.RequestID already reaches every write the request causes, so the
	// join is free and the alternative is a manual correlation across three
	// stores.
	RequestID string `bson:"requestId,omitempty" json:"requestId,omitempty"`
	// Meta carries the structured before/after of the change. Structured keys,
	// never a rendered sentence: the viewer's filters and the CSV export both
	// read this, and a prose summary is queryable by nothing.
	Meta map[string]any `bson:"meta,omitempty" json:"meta,omitempty"`
	At   time.Time      `bson:"at" json:"at"`
	// Chain is reserved for Enterprise tamper evidence —
	// sha256(prev.chain + canonical(this)), §12.5. Nothing writes it yet; it is
	// declared here so the stored shape does not change when hash-chain mode
	// arrives, because a chain that starts mid-collection can only attest to the
	// rows written after it.
	Chain string `bson:"chain,omitempty" json:"chain,omitempty"`
}

// AuditFilter narrows an audit read.
type AuditFilter struct {
	// OrgID scopes the read to one tenant. Empty means unscoped, which only the
	// platform layer may ask for.
	OrgID string
	// ActionPrefix matches the taxonomy by segment: "org." selects every
	// membership and settings event, "org.member_" narrows to the member ones.
	ActionPrefix string
	Since        time.Time
	Until        time.Time
	// Cursor is the id of the last row of the previous page. Ids are
	// time-ordered, so paging on the id rather than on At is stable across rows
	// written in the same instant.
	Cursor string
	Limit  int
}

// AuditRepository is the port for the audit log, and append-only is enforced by
// the port having no other verbs: there is no Update and no Delete on this
// interface, so no code above this layer can be written that rewrites or erases
// a row — not a service, not a handler, not a future one added by someone who
// has not read §9. Retention is a TTL boundary inside the database rather than
// a method here, because a delete the application can call is a delete anyone
// who reaches the application can call.
type AuditRepository interface {
	Insert(ctx context.Context, ev *AuditEvent) error
	// List returns newest first, with the cursor for the next page — empty when
	// this page is the last one.
	List(ctx context.Context, f AuditFilter) ([]*AuditEvent, string, error)
}
