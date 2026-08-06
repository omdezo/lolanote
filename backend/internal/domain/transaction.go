package domain

import (
	"context"
	"time"
)

// Action is what an op does to its element.
type Action string

const (
	ActionCreate  Action = "create"
	ActionUpdate  Action = "update"
	ActionMove    Action = "move"
	ActionDelete  Action = "delete" // soft delete → Trash
	ActionRestore Action = "restore"
)

// Op is one element mutation. Changes is a JSON-merge-patch-style forward
// patch; UndoChanges is its precomputed inverse, replayed by clients for
// undo/redo. The same payload that mutates locally is what gets broadcast —
// one code path for local and remote mutations (§9.5, §9.9).
type Op struct {
	ElementID   string  `bson:"elementId" json:"elementId"`
	Action      Action  `bson:"action" json:"action"`
	Changes     Content `bson:"changes,omitempty" json:"changes,omitempty"`
	UndoChanges Content `bson:"undoChanges,omitempty" json:"undoChanges,omitempty"`

	// Redacted marks an op whose element has since been permanently deleted.
	//
	// A delete op carries the deleted element's prior content verbatim in
	// UndoChanges, and board history serves the journal to any current editor —
	// so an element that was trashed and then Emptied stayed readable, by anyone
	// invited afterwards, through an endpoint no UI has ever shown. Stripping the
	// content is what makes "Delete forever" true; keeping the op is what keeps
	// "Ali deleted a card" auditable. The flag exists so a reader can say the
	// difference out loud instead of rendering a change with no detail as a bug.
	Redacted bool `bson:"redacted,omitempty" json:"redacted,omitempty"`

	// Kind names the agent ActionKind this op was compiled from, and is empty
	// for every human edit.
	//
	// It exists so the delegation guard can ask "which content keys does this
	// action kind declare it writes" instead of maintaining a denylist of keys
	// it must not. The denylist was already wrong — it named isHome and
	// isTemplate and missed `locked`, `cloneSourceId` and `agentInstructions`,
	// the standing rules that constrain the agent — and a list every feature
	// author has to remember to extend will be wrong again.
	Kind string `bson:"kind,omitempty" json:"kind,omitempty"`
}

// Transaction is the unit of mutation, undo, and broadcast. A multi-select
// drag of N cards is ONE transaction with N ops, not N transactions.
type Transaction struct {
	ID        string    `bson:"_id" json:"id"`
	BoardID   string    `bson:"boardId" json:"boardId"`
	UserID    string    `bson:"userId" json:"userId"`
	ClientID  string    `bson:"clientId" json:"clientId"` // originating socket; excluded from echo
	Ops       []Op      `bson:"ops" json:"ops"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`

	// Origin distinguishes a human's edit from one the AI agent made on their
	// behalf. Empty means human (every transaction written before the agent
	// existed). Clients use it to label incoming broadcasts; the audit view
	// uses it to answer "what did the AI change?".
	Origin string `bson:"origin,omitempty" json:"origin,omitempty"`
	// AgentRunID links every transaction a run committed back to that run, which
	// is what makes a whole run revertible as one unit.
	AgentRunID string `bson:"agentRunId,omitempty" json:"agentRunId,omitempty"`

	// RequestID is the id the HTTP layer already mints per request, carried all
	// the way to the write.
	//
	// echo's RequestID middleware has been registered since the first commit and
	// its value went nowhere: the request log was Debug-only, the agent's events
	// and the board's transactions shared nothing but a runId, and "why did my
	// board change last Tuesday" meant joining three stores by hand. With an
	// external caller it gets strictly worse — a change can originate from a
	// source the board's own history cannot name.
	RequestID string `bson:"requestId,omitempty" json:"requestId,omitempty"`

	// Source is WHERE the write came in: web, api, mcp, email, schedule, rule.
	//
	// Distinct from Origin, which says human-or-agent. Both are needed to
	// answer the real question: "changed by Claude via MCP on behalf of you" is
	// two facts, and the audit view could previously state neither of them
	// beyond "the agent". Invariant: every write records its source.
	Source string `bson:"source,omitempty" json:"source,omitempty"`

	// State separates a claimed id from a recorded commit.
	//
	// The journal row used to be written LAST, after every op had landed, so a
	// retried commit re-ran the whole batch and only then hit the duplicate id.
	// A caller that pins its id (the agent derives one from its run precisely so
	// a retry is recognisable) now claims the row first and settles it after, and
	// the state is what tells the two apart. Empty means committed — every row
	// written before this existed, and every human write, whose generated id
	// cannot collide and so needs no claim.
	State string `bson:"state,omitempty" json:"state,omitempty"`

	// NotificationIDs are the bells this commit rang.
	//
	// Recorded because a revert has to be able to take them back: undoing a run
	// that assigned four tasks left four colleagues holding a notification for
	// work that no longer exists, under a sentence promising the board was back
	// as it was. A side effect nothing records is a side effect undo cannot see.
	NotificationIDs []string `bson:"notificationIds,omitempty" json:"notificationIds,omitempty"`
}

// TransactionSubscriber is notified once, after a transaction is journalled.
//
// COMMITTING IS PUBLISHING. Everything that wanted to react to a write —
// broadcasting to a room, updating synced notes, ringing an assignment bell —
// was hand-rolled as another call at the bottom of the one method that applies
// ops, and every future one ("when something lands in Unsorted, file it", "tell
// me when the budget column changes", refresh the search index) would have
// arrived as the fourth, fifth and sixth. That is how that method becomes the
// god object the architecture wave just finished splitting.
//
// The interface is also the seam a real broker slots into: the in-process
// implementation is the honest fit for one replica, and two replicas mean two
// buses that cannot see each other.
type TransactionSubscriber interface {
	OnCommitted(ctx context.Context, txn *Transaction)
}

// Transaction origins.
const (
	OriginHuman = "human"
	OriginAgent = "agent"
)

// Transaction sources — WHERE the write entered the system.
//
// Orthogonal to Origin: a run started from an MCP host on somebody's behalf is
// (agent, mcp), a person dragging a card is (human, web). Every write records
// its source; SourceWeb is the default so nothing that predates this field
// reads as "unknown".
const (
	SourceWeb      = "web"
	SourceAPI      = "api"
	SourceMCP      = "mcp"
	SourceEmail    = "email"
	SourceSchedule = "schedule"
	SourceRule     = "rule"
)

// Transaction states. Committed is the empty string so no migration is needed:
// every row that predates the claim is exactly what it always was.
const (
	TxnCommitted = ""
	TxnApplying  = "applying"
)
