package service

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// Revert was not the inverse of commit. The write path's post-commit side
// effects were outside the invertible set while the copy promised the board was
// back as it was: undo a run that assigned four tasks and four colleagues keep an
// inbox item for work that no longer exists. InvertOps cannot reach a bell —
// it is not an op — so the transaction has to carry the ids.

type fakeNotifications struct {
	rows map[string]*domain.Notification
}

func newFakeNotifications() *fakeNotifications {
	return &fakeNotifications{rows: map[string]*domain.Notification{}}
}

func (f *fakeNotifications) Insert(_ context.Context, n *domain.Notification) error {
	f.rows[n.ID] = n
	return nil
}
func (f *fakeNotifications) ListByUser(_ context.Context, sub string, _ bool, _ int) ([]*domain.Notification, error) {
	var out []*domain.Notification
	for _, n := range f.rows {
		if n.UserID == sub {
			out = append(out, n)
		}
	}
	return out, nil
}
func (f *fakeNotifications) MarkRead(context.Context, string, []string) error { return nil }
func (f *fakeNotifications) DeleteByUser(context.Context, string) error       { return nil }
func (f *fakeNotifications) DeleteByIDs(_ context.Context, ids []string) error {
	for _, id := range ids {
		delete(f.rows, id)
	}
	return nil
}

// absentUsers is the "no local mirror yet" case, which Notifier treats as
// deliver-rather-than-drop — the behaviour that matters for this test.
type absentUsers struct{}

func (absentUsers) GetBySub(context.Context, string) (*domain.User, error) {
	return nil, domain.ErrNotFound
}
func (absentUsers) GetByEmail(context.Context, string) (*domain.User, error) {
	return nil, domain.ErrNotFound
}
func (absentUsers) Insert(context.Context, *domain.User) error { return nil }
func (absentUsers) Update(context.Context, *domain.User) error { return nil }
func (absentUsers) Delete(context.Context, string) error       { return nil }
func (absentUsers) UpdateSettings(context.Context, string, *domain.UserSettings) error {
	return nil
}

func assignmentFixture(t *testing.T) (*TransactionService, *fakeNotifications, string, string) {
	t.Helper()
	ctx := context.Background()
	elements := memory.NewElementRepo()
	board := "cd00000000000000000ba001"
	task := "cd00000000000000000bt001"
	now := time.Now().UTC()
	if err := elements.Insert(ctx, &domain.Element{
		ID: board, Type: domain.TypeBoard,
		Content:   domain.Content{"title": "Shoot"},
		ACL:       &domain.ACL{OwnerID: "alice", Editors: []string{"bob"}},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	if err := elements.Insert(ctx, &domain.Element{
		ID: task, Type: domain.TypeTask,
		Location:  domain.Location{ParentID: board, Section: domain.SectionCanvas},
		Content:   domain.Content{"text": "Lock the cut"},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	notes := newFakeNotifications()
	svc, _ := partialWriteFixture(t, elements)
	svc.AttachNotifier(NewNotifier(notes, absentUsers{}))
	return svc, notes, board, task
}

func TestAssignment_TheTransactionRecordsTheBellItRang(t *testing.T) {
	svc, notes, board, task := assignmentFixture(t)
	txn, err := svc.Apply(context.Background(), &domain.Principal{Sub: "alice", Name: "Alice"},
		board, "", []domain.Op{{
			ElementID: task, Action: domain.ActionUpdate,
			Changes: domain.Content{"content": map[string]any{"assigneeId": "bob"}},
		}})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if len(notes.rows) != 1 {
		t.Fatalf("%d notifications sent, want 1", len(notes.rows))
	}
	if len(txn.NotificationIDs) != 1 {
		t.Fatalf("the transaction records %v; a bell no transaction names is a bell undo cannot see",
			txn.NotificationIDs)
	}
	if _, ok := notes.rows[txn.NotificationIDs[0]]; !ok {
		t.Errorf("the recorded id %q names no notification", txn.NotificationIDs[0])
	}
}

func TestAssignment_RevertingTheChangeTakesTheBellBack(t *testing.T) {
	svc, notes, board, task := assignmentFixture(t)
	txn, err := svc.Apply(context.Background(), &domain.Principal{Sub: "alice", Name: "Alice"},
		board, "", []domain.Op{{
			ElementID: task, Action: domain.ActionUpdate,
			Changes: domain.Content{"content": map[string]any{"assigneeId": "bob"}},
		}})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	svc.RetractNotifications(context.Background(), txn)

	if len(notes.rows) != 0 {
		t.Errorf("%d notifications survived the revert — bob still has an inbox item for work that was undone",
			len(notes.rows))
	}
}

// A store with no retract half must not panic or lie; it simply keeps the row,
// which is the same posture every other optional capability here takes.
func TestAssignment_ARetractWithoutStorageSupportIsSilent(t *testing.T) {
	notes := newFakeNotifications()
	svc := &TransactionService{log: zap.NewNop()}
	svc.AttachNotifier(NewNotifier(struct {
		domain.NotificationRepository
	}{notes}, absentUsers{}))
	svc.RetractNotifications(context.Background(), &domain.Transaction{NotificationIDs: []string{"n1"}})
}
