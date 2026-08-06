package agent

import (
	"context"
	"time"
)

// Persistence ports. The agent package depends on these interfaces; the mongo
// package implements them and the memory package provides deterministic
// in-process versions for tests — the same ports-and-adapters shape the rest of
// the backend already uses.

// HistoryRetention is how long a finished run and its journal survive.
//
// One value, read by the TTL index and by whatever tells the person the number,
// so what is enforced and what is displayed cannot drift. Both collections used
// to be permanent and uncapped: every request typed into the composer, every
// refinement, every mid-run correction and every drafted sentence, retained
// verbatim and indefinitely under the person's tenantSub — including the plans
// they explicitly rejected. Pressing Discard is somebody saying "not this", and
// the system was filing it forever.
//
// 90 days matches TrashRetention and outlives any revert window, so nothing a
// person can still act on is ever swept.
const HistoryRetention = 90 * 24 * time.Hour

// RunStore persists authoritative run state.
type RunStore interface {
	Insert(ctx context.Context, run *Run) error
	Get(ctx context.Context, id string) (*Run, error)
	// Update writes the run only if its stored rev still equals expectedRev,
	// then bumps it. Two workers racing on one run therefore cannot both
	// commit — the loser gets ErrConflict and re-reads.
	Update(ctx context.Context, run *Run, expectedRev int64) error
	// ActiveByBoard returns the board's non-terminal run, if any. This is the
	// single-run-per-board guard (G8); a unique partial index enforces the same
	// rule at the storage layer so a race cannot slip past the read.
	ActiveByBoard(ctx context.Context, boardID string) (*Run, error)
	ListByBoard(ctx context.Context, tenant, boardID string, limit int) ([]*Run, error)
	// Unfinished lists non-terminal runs across all boards. The boot reconciler
	// uses it to resolve runs that a crash left mid-flight.
	Unfinished(ctx context.Context) ([]*Run, error)
	// DeleteByTenant purges a user's runs on account deletion.
	DeleteByTenant(ctx context.Context, tenant string) error
}

// The three interfaces below are OPTIONAL halves of the run store, discovered
// by type assertion at the call site — the same shape TransactionReserver and
// NotificationRetractor already use in the service package.
//
// Widening RunStore itself would break every in-memory double in the test
// suite and the eval harness for capabilities only the production adapter can
// implement (a compound $or, a $group). Each caller below states what it does
// when the assertion fails, and the degraded answer is always the narrower one
// that shipped before — never a wrong one.

// RunOverlapStore answers "is any run live on a board that overlaps mine".
//
// A run's blast radius is its root board's whole SUBTREE, so two runs conflict
// when either root sits inside the other's chain. ActiveByBoard can only ask
// about one exact id, which is why a run rooted at a nested board walked
// straight past the single-run guard.
type RunOverlapStore interface {
	ActiveOverlapping(ctx context.Context, boardID string, ancestorIDs []string) (*Run, error)
}

// RunBoardOwnerStore is the second purge axis and the owner's read path.
//
// Run.Tenant is who STARTED the run; Run.BoardOwnerSub is whose board it read.
// Erasure has to run on both or a collaborator's journal keeps a verbatim copy
// of a deleted account's content.
type RunBoardOwnerStore interface {
	// ListByBoardOwner returns runs on boards this person owns, whoever ran
	// them. The owner's audit view is built from it.
	ListByBoardOwner(ctx context.Context, ownerSub, boardID string, limit int) ([]*Run, error)
	// DeleteByBoardOwner purges runs OTHER people made on this person's boards.
	DeleteByBoardOwner(ctx context.Context, ownerSub string) error
}

// RunAnalyticsStore is the account-wide read path.
//
// ListAgentRuns refused without a boardId and RunStore had no tenant-wide
// list, so "what has the assistant done for me lately" and "what am I
// spending" had no signature to be asked through — while the spend cap worked
// around the gap by calling ListByBoard with an EMPTY board id and summing 500
// unmarshalled documents in Go.
type RunAnalyticsStore interface {
	ListByTenant(ctx context.Context, tenant string, f RunFilter, limit int) ([]*Run, error)
	// AggregateUsage groups cost and counts server-side. The 500-document scan
	// it replaces silently under-reported exactly the heavy days a spend cap
	// exists to catch.
	AggregateUsage(ctx context.Context, tenant string, f RunFilter) (UsageRollup, error)
}

// RunFilter narrows an account-wide run query.
type RunFilter struct {
	Since  time.Time
	States []RunState
	// BoardIDs, when non-empty, restricts to these root boards.
	BoardIDs []string
	// BoardID scopes a spend question to one board, which is what a per-board
	// ceiling needs and what a deployment-wide number could never express.
	BoardID string
}

// UsageRollup is the server-side answer to "how much, over what".
type UsageRollup struct {
	Runs      int64              `json:"runs"`
	CostUSD   float64            `json:"costUsd"`
	InTokens  int64              `json:"inputTokens"`
	OutTokens int64              `json:"outputTokens"`
	ByState   map[RunState]int64 `json:"byState,omitempty"`
}

// EventBoardOwnerPurger is DeleteByTenant's second axis for the journal.
type EventBoardOwnerPurger interface {
	DeleteByBoardOwner(ctx context.Context, ownerSub string) error
}

// EventStore is the append-only run journal.
type EventStore interface {
	// Append assigns the next sequence for the run. The (runId, sequence) pair
	// is unique, which is what makes the journal ordered and gap-free.
	Append(ctx context.Context, ev *Event) error
	// List returns events after a cursor, so a client that reconnects can catch
	// up rather than losing everything it missed.
	List(ctx context.Context, runID string, since int64, limit int) ([]*Event, error)
	// Aggregate groups events by type over a window, optionally narrowed to one
	// tenant and one set of types.
	//
	// The port used to expose only per-run reads, so every aggregate question —
	// how many runs, how many refusals, how many reverts — had no signature it
	// could be asked through, and any answer would have been a full collection
	// scan. One method and two compound indexes is the whole cost.
	Aggregate(ctx context.Context, tenant string, since time.Time, types []EventType) (map[EventType]int64, error)
	DeleteByTenant(ctx context.Context, tenant string) error
}
