package service

import (
	"context"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// CommitBus delivers a committed transaction to everything that reacts to one.
//
// Committing is publishing. Before this, every reaction to a write was another
// hand-rolled call at the bottom of ApplyWithMeta — broadcast to the room, fan
// out to the boards holding a clone, ring the assignment bell — and each new one
// ("when something lands in Unsorted, file it", "tell me when the budget column
// changes", refresh the search index) would have been the next line in the same
// method. Four bespoke side effects is a pattern; seven is a god object.
//
// Delivery is SYNCHRONOUS and recovered per subscriber. Synchronous because the
// callers it replaced were, and because an asynchronous hand-off with no durable
// queue silently drops work on restart — a worse failure than a slow drag.
// Recovered because a rule must never be able to fail a human's drag: a
// subscriber that panics loses its own reaction and nothing else. The seam is
// what matters; a bounded worker pool or a real broker slots in behind it
// without any publisher changing.
type CommitBus struct {
	subs []busEntry
	log  *zap.Logger
}

type busEntry struct {
	name string
	sub  domain.TransactionSubscriber
}

// NewCommitBus constructs an empty bus.
func NewCommitBus(log *zap.Logger) *CommitBus {
	return &CommitBus{log: log.Named("commitbus")}
}

// Subscribe registers a reaction. The name appears in the log when it fails,
// because "a subscriber panicked" with no name is unactionable.
func (b *CommitBus) Subscribe(name string, sub domain.TransactionSubscriber) {
	if sub == nil {
		return
	}
	b.subs = append(b.subs, busEntry{name: name, sub: sub})
}

// Publish delivers to every subscriber, isolating each one's failure.
func (b *CommitBus) Publish(ctx context.Context, txn *domain.Transaction) {
	for _, entry := range b.subs {
		b.deliver(ctx, entry, txn)
	}
}

func (b *CommitBus) deliver(ctx context.Context, entry busEntry, txn *domain.Transaction) {
	defer func() {
		if r := recover(); r != nil {
			// The write already happened and is already journalled. Reporting a
			// committed transaction as a failure because something downstream
			// fell over would be the larger lie.
			b.log.Error("a commit subscriber panicked; its reaction did not happen",
				zap.String("subscriber", entry.name),
				zap.String("txn", txn.ID),
				zap.Any("panic", r))
		}
	}()
	entry.sub.OnCommitted(ctx, txn)
}

// subscriberFunc adapts a plain function, so a service can register one of its
// own methods without growing a type per reaction.
type subscriberFunc func(ctx context.Context, txn *domain.Transaction)

func (f subscriberFunc) OnCommitted(ctx context.Context, txn *domain.Transaction) { f(ctx, txn) }
