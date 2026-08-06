package service

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// Nothing consumed the transaction stream server-side. The only implementation
// of the broadcaster port was the WebSocket hub — in process, fire-and-forget —
// and every reaction to a write was another hand-rolled call at the bottom of
// the one method that applies ops. "When something lands in Unsorted, file it",
// "tell me when the budget column changes" and the search index refresh each
// needed a place to hang a consumer, and there was none.

type recordingSubscriber struct{ seen []string }

func (r *recordingSubscriber) OnCommitted(_ context.Context, txn *domain.Transaction) {
	r.seen = append(r.seen, txn.ID)
}

type panickingSubscriber struct{}

func (panickingSubscriber) OnCommitted(context.Context, *domain.Transaction) {
	panic("a rule blew up")
}

func TestCommitBus_ACommitReachesEveryConsumer(t *testing.T) {
	elements := memory.NewElementRepo()
	seedOwnedBoard(t, elements, "cd00000000000000000ba001")
	svc, _ := partialWriteFixture(t, elements)

	watcher := &recordingSubscriber{}
	svc.Subscribe("watcher", watcher)

	txn, err := svc.Apply(context.Background(), &domain.Principal{Sub: "alice"},
		"cd00000000000000000ba001", "", []domain.Op{createOp(hexID(20), "cd00000000000000000ba001")})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(watcher.seen) != 1 || watcher.seen[0] != txn.ID {
		t.Fatalf("subscriber saw %v, want the committed transaction %s", watcher.seen, txn.ID)
	}
}

// A rule must never be able to fail a human's drag. The write has already
// happened and is already journalled by the time a subscriber runs; reporting
// the commit as a failure because something downstream fell over would be the
// larger lie.
func TestCommitBus_AFailingConsumerDoesNotFailTheWrite(t *testing.T) {
	elements := memory.NewElementRepo()
	seedOwnedBoard(t, elements, "cd00000000000000000ba001")
	svc, journal := partialWriteFixture(t, elements)

	svc.Subscribe("exploding-rule", panickingSubscriber{})
	after := &recordingSubscriber{}
	svc.Subscribe("still-runs", after)

	if _, err := svc.Apply(context.Background(), &domain.Principal{Sub: "alice"},
		"cd00000000000000000ba001", "", []domain.Op{createOp(hexID(21), "cd00000000000000000ba001")}); err != nil {
		t.Fatalf("a subscriber panic failed the drag: %v", err)
	}
	if journal.Count() != 1 {
		t.Errorf("journal rows = %d, want the commit recorded", journal.Count())
	}
	if len(after.seen) != 1 {
		t.Error("one subscriber's panic swallowed the next subscriber's turn")
	}
}

func TestCommitBus_ANilSubscriberIsIgnoredRatherThanPanicking(t *testing.T) {
	bus := NewCommitBus(zap.NewNop())
	bus.Subscribe("nothing", nil)
	bus.Publish(context.Background(), &domain.Transaction{ID: "t"})
}
