package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// JN21 — nothing ever told anyone that a recoverable thing was about to stop
// being recoverable.
//
// `PurgeExpired` returned a count to a log line. The 90-day window was stated to
// the user in exactly one place — the trash panel's EMPTY state — so the
// sentence rendered only when there was nothing left to lose, and every actual
// row showed the date it was deleted, which is the one date nobody needs.
//
// Two properties are worth pinning, and the second is the one that decides
// whether the feature is usable at all: a warning that arrives 400 times for one
// decision is an inbox nobody opens, and JN18 says ending a production is
// exactly that shape.

type warnFixture struct {
	elements *memory.ElementRepo
	notes    *fakeNotifications
	svc      *MaintenanceService
}

func newWarnFixture(t *testing.T) *warnFixture {
	t.Helper()
	f := &warnFixture{
		elements: memory.NewElementRepo(),
		notes:    newFakeNotifications(),
	}
	log := zap.NewNop()
	f.svc = NewMaintenanceService(f.elements, memory.NewAttachmentRepo(), nil, log)
	n := 0
	f.svc.AttachNotifier(NewNotifier(f.notes, nil), func() string {
		n++
		return fmt.Sprintf("note-%d", n)
	})
	return f
}

// trash seeds one deleted element whose ninety days run out in `until`.
func (f *warnFixture) trash(t *testing.T, id, batch string, until time.Duration) {
	t.Helper()
	deletedAt := time.Now().UTC().Add(-domain.TrashRetention).Add(until)
	if err := f.elements.Insert(context.Background(), &domain.Element{
		ID: id, Type: domain.TypeCard, CreatedBy: "alice", DeletedBy: "alice",
		DeletedAt: &deletedAt, TrashBatchID: batch,
		Content: domain.Content{"title": "the March cut"},
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func (f *warnFixture) bells(t *testing.T) []*domain.Notification {
	t.Helper()
	got, err := f.notes.ListByUser(context.Background(), "alice", false, 100)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSweep_WarnsAWeekBeforeSomethingStopsBeingRecoverable(t *testing.T) {
	f := newWarnFixture(t)
	// Three days of life left: inside the week's notice.
	f.trash(t, "eeeeeeeeeeeeeeeeeeeeee01", "", 3*24*time.Hour)
	// Thirty days left: nothing to say yet. Warning early is how a deadline
	// becomes furniture people stop reading.
	f.trash(t, "eeeeeeeeeeeeeeeeeeeeee02", "", 30*24*time.Hour)

	f.svc.SweepOnce(context.Background())

	got := f.bells(t)
	if len(got) != 1 {
		t.Fatalf("expected exactly one warning, got %d", len(got))
	}
	if got[0].Kind != domain.NotifyTrashExpiring {
		t.Fatalf("wrong kind: %q", got[0].Kind)
	}
	// The message has to name the thing and the window. "Something expires
	// soon" is the shape of notification people learn to ignore.
	if !strings.Contains(got[0].Message, "the March cut") || !strings.Contains(got[0].Message, "7 days") {
		t.Fatalf("message says too little: %q", got[0].Message)
	}
}

func TestSweep_AWholeProductionIsOneWarningNotFourHundred(t *testing.T) {
	f := newWarnFixture(t)
	// The JN18 shape: deleting a wrapped production cascades the entire subtree
	// under one trashBatchId. One decision, made in March, must not become four
	// hundred bells in June.
	for i := 0; i < 400; i++ {
		f.trash(t, primitiveID(i), "batch-ep1", 2*24*time.Hour)
	}

	f.svc.SweepOnce(context.Background())

	if got := f.bells(t); len(got) != 1 {
		t.Fatalf("a 400-element deletion produced %d warnings; the batch is the unit", len(got))
	}
}

func TestSweep_DoesNotRingTheSameBellEverySixHours(t *testing.T) {
	f := newWarnFixture(t)
	f.trash(t, "eeeeeeeeeeeeeeeeeeeeee01", "", 3*24*time.Hour)

	// The sweep runs every six hours, so a warning with no memory would be 28
	// identical notifications before the thing was even deleted.
	f.svc.SweepOnce(context.Background())
	f.svc.SweepOnce(context.Background())
	f.svc.SweepOnce(context.Background())

	if got := f.bells(t); len(got) != 1 {
		t.Fatalf("three sweeps rang %d bells for one deletion", len(got))
	}
}

func TestSweep_WithNoNotifierStillPurges(t *testing.T) {
	// The notifier is attached separately, and a deployment that has not wired
	// it must still sweep — silently, which is the behaviour that existed
	// before this and is exactly what the warning exists to end, but not
	// broken.
	elements := memory.NewElementRepo()
	svc := NewMaintenanceService(elements, memory.NewAttachmentRepo(), nil, zap.NewNop())
	long := time.Now().UTC().Add(-2 * domain.TrashRetention)
	if err := elements.Insert(context.Background(), &domain.Element{
		ID: "eeeeeeeeeeeeeeeeeeeeee09", Type: domain.TypeCard, DeletedBy: "alice",
		DeletedAt: &long, Content: domain.Content{"title": "long gone"},
	}); err != nil {
		t.Fatal(err)
	}
	svc.SweepOnce(context.Background())
	if _, err := elements.Get(context.Background(), "eeeeeeeeeeeeeeeeeeeeee09"); err == nil {
		t.Fatal("an expired element survived a sweep with no notifier attached")
	}
}

// primitiveID builds a distinct valid 24-hex id from a counter.
func primitiveID(i int) string {
	const digits = "0123456789abcdef"
	out := []byte("000000000000000000000000")
	for pos := len(out) - 1; pos >= 0 && i > 0; pos-- {
		out[pos] = digits[i%16]
		i /= 16
	}
	return string(out)
}
