package agent

import "context"

// Persistence ports. The agent package depends on these interfaces; the mongo
// package implements them and the memory package provides deterministic
// in-process versions for tests — the same ports-and-adapters shape the rest of
// the backend already uses.

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

// EventStore is the append-only run journal.
type EventStore interface {
	// Append assigns the next sequence for the run. The (runId, sequence) pair
	// is unique, which is what makes the journal ordered and gap-free.
	Append(ctx context.Context, ev *Event) error
	// List returns events after a cursor, so a client that reconnects can catch
	// up rather than losing everything it missed.
	List(ctx context.Context, runID string, since int64, limit int) ([]*Event, error)
	DeleteByTenant(ctx context.Context, tenant string) error
}
