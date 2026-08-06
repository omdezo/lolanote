package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// What "Delete forever" actually reaches.
//
// The enumerate-by-hand pattern in DeleteAccount is what lost transactions,
// comments and blobs — three collections, three separate omissions, each of
// them invisible because the method returns nil either way. Only a test that
// walks the collections can stop it losing the fourth.

// fakeBlobs records what was removed, and can be told to fail.
type fakeBlobs struct {
	removed []string
	fail    bool
}

func (f *fakeBlobs) Remove(key string) error {
	if f.fail {
		return errors.New("bucket unreachable")
	}
	f.removed = append(f.removed, key)
	return nil
}

type deletionFixture struct {
	elements *memory.ElementRepo
	labels   *memory.LabelRepo
	atts     *memory.AttachmentRepo
	comments *memory.CommentRepo
	journal  *memory.TransactionRepo
	blobs    *fakeBlobs
	audit    *memory.AuditRepo
	svc      *AccountService
}

func newDeletionFixture(t *testing.T) *deletionFixture {
	t.Helper()
	f := &deletionFixture{
		elements: memory.NewElementRepo(),
		labels:   memory.NewLabelRepo(),
		atts:     memory.NewAttachmentRepo(),
		comments: memory.NewCommentRepo(),
		journal:  memory.NewTransactionRepo(),
		blobs:    &fakeBlobs{},
	}
	audit, auditRepo := testAudit()
	f.audit = auditRepo
	f.svc = NewAccountService(newStubUsers(), f.elements, f.labels, f.atts,
		stubNotifications{}, nil, audit, zap.NewNop())
	f.svc.AttachBlobs(f.blobs)
	f.svc.AttachComments(f.comments)
	f.svc.AttachJournal(f.journal)
	return f
}

// seedSolo builds a board owned by alice with nobody else on it: one image
// card, one comment thread with a message, one journal row.
func (f *deletionFixture) seedSolo(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(id, typ, parent string, acl *domain.ACL, content domain.Content) {
		if err := f.elements.Insert(ctx, &domain.Element{
			ID: id, Type: domain.ElementType(typ),
			Location: domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:  content, ACL: acl,
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	mk("ffffffffffffffffffffff01", "BOARD", "", &domain.ACL{OwnerID: "alice", Editors: []string{}},
		domain.Content{"title": "Private"})
	mk("ffffffffffffffffffffff02", "IMAGE", "ffffffffffffffffffffff01", nil,
		domain.Content{"attachmentId": "att-1", "url": "https://bucket/u/alice/att-1/still.jpg"})
	mk("ffffffffffffffffffffff03", "COMMENT_THREAD", "ffffffffffffffffffffff01", nil, domain.Content{})

	if err := f.atts.Insert(ctx, &domain.Attachment{
		ID: "att-1", OwnerID: "alice", Key: "u/alice/att-1/still.jpg",
		Filename: "still.jpg", Status: domain.AttachmentUploaded, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	if err := f.comments.Insert(ctx, &domain.Comment{
		ID: "c-1", ThreadID: "ffffffffffffffffffffff03", AuthorID: "alice",
		Body: "the client hated this take", CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	// A message alice left on somebody ELSE's board: still hers, still has to go.
	if err := f.comments.Insert(ctx, &domain.Comment{
		ID: "c-2", ThreadID: "somebody-elses-thread", AuthorID: "alice",
		Body: "the budget will not survive this", CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed foreign comment: %v", err)
	}
	if err := f.journal.Insert(ctx, &domain.Transaction{
		ID: "t-1", BoardID: "ffffffffffffffffffffff01", UserID: "alice", CreatedAt: now,
		Ops: []domain.Op{{
			ElementID: "ffffffffffffffffffffff02", Action: domain.ActionUpdate,
			Changes: domain.Content{"content": map[string]any{"textPreview": "the whole of what alice wrote"}},
		}},
	}); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}
}

func TestDeleteAccount_ReachesTheBlobsTheCommentsAndTheJournal(t *testing.T) {
	f := newDeletionFixture(t)
	f.seedSolo(t)
	ctx := context.Background()

	if err := f.svc.DeleteAccount(ctx, &domain.Principal{Sub: "alice"}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if len(f.blobs.removed) != 1 || f.blobs.removed[0] != "u/alice/att-1/still.jpg" {
		t.Errorf("blobs removed = %v; the confirmation dialog promises every uploaded file", f.blobs.removed)
	}
	if got := f.comments.Count(); got != 0 {
		t.Errorf("%d comment(s) survived — non-portable AND non-erasable", got)
	}
	if got := f.journal.Count(); got != 0 {
		t.Errorf("%d journal row(s) survived; each one is a full content snapshot keyed by the deleted user", got)
	}
	if _, err := f.atts.Get(ctx, "att-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("the attachment row survived: %v", err)
	}
	if _, err := f.elements.Get(ctx, "ffffffffffffffffffffff01"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("the board survived: %v", err)
	}
}

// A partial purge that reports success is the bug. If the bucket refuses, the
// rows must still go — but the failure has to be visible, not returned as nil
// with the files quietly left behind and no longer nameable.
func TestDeleteAccount_ABucketFailureDoesNotStopTheRestOfThePurge(t *testing.T) {
	f := newDeletionFixture(t)
	f.blobs.fail = true
	f.seedSolo(t)
	ctx := context.Background()

	if err := f.svc.DeleteAccount(ctx, &domain.Principal{Sub: "alice"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := f.comments.Count(); got != 0 {
		t.Errorf("a blob failure blocked the comment purge (%d left)", got)
	}
}

// Deleting your account destroyed your collaborators' work, silently.
//
// OwnedBoards is decided by the board's ACL; CreatedBy is per element. So "every
// board you own" quietly meant "everything four people made inside them" —
// hard-deleted, not into the 90-day trash, not exportable afterwards, with no
// warning before and no notification after.
func TestDeleteAccount_RefusesWhenItWouldDestroySomebodyElsesWork(t *testing.T) {
	f := newDeletionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(id, title string, editors []string) {
		if err := f.elements.Insert(ctx, &domain.Element{
			ID: id, Type: domain.TypeBoard,
			Content:   domain.Content{"title": title},
			ACL:       &domain.ACL{OwnerID: "alice", Editors: editors},
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	mk("aaaaaaaaaaaaaaaaaaaaab01", "Ep 1 Production", []string{"director", "producer"})
	mk("aaaaaaaaaaaaaaaaaaaaab02", "Lookbook", []string{"dop"})
	mk("aaaaaaaaaaaaaaaaaaaaab03", "Scratch", nil)
	// The producer's budget table, made by them, living on alice's board.
	if err := f.elements.Insert(ctx, &domain.Element{
		ID: "aaaaaaaaaaaaaaaaaaaaab04", Type: domain.TypeTable,
		Location:  domain.Location{ParentID: "aaaaaaaaaaaaaaaaaaaaab01"},
		Content:   domain.Content{"cells": []any{}},
		CreatedBy: "producer", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed table: %v", err)
	}

	err := f.svc.DeleteAccount(ctx, &domain.Principal{Sub: "alice"})
	if err == nil {
		t.Fatal("account deletion destroyed two collaborators' boards without asking")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("refused with %v; it should read as a conflict the person can resolve", err)
	}
	for _, want := range []string{"Ep 1 Production", "Lookbook"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q — %q", want, err.Error())
		}
	}
	if strings.Contains(err.Error(), "Scratch") {
		t.Errorf("the refusal names an unshared board: %q", err.Error())
	}

	// And the work is still there. This is the assertion the item asks for.
	for _, id := range []string{"aaaaaaaaaaaaaaaaaaaaab01", "aaaaaaaaaaaaaaaaaaaaab04"} {
		if _, gerr := f.elements.Get(ctx, id); gerr != nil {
			t.Errorf("%s was destroyed by a refused deletion: %v", id, gerr)
		}
	}
}

// The other direction: once the boards are unshared, the delete goes through.
// A refusal that cannot be resolved is a trap, not a guard.
func TestDeleteAccount_SucceedsOnceTheSharedBoardsAreUnshared(t *testing.T) {
	f := newDeletionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := f.elements.Insert(ctx, &domain.Element{
		ID: "aaaaaaaaaaaaaaaaaaaaac01", Type: domain.TypeBoard,
		Content:   domain.Content{"title": "Ep 1 Production"},
		ACL:       &domain.ACL{OwnerID: "alice", Editors: []string{"director"}},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := f.svc.DeleteAccount(ctx, &domain.Principal{Sub: "alice"}); err == nil {
		t.Fatal("expected the shared board to block deletion")
	}

	if err := f.elements.SetACL(ctx, "aaaaaaaaaaaaaaaaaaaaac01",
		&domain.ACL{OwnerID: "alice", Editors: []string{}}); err != nil {
		t.Fatalf("unshare: %v", err)
	}
	if err := f.svc.DeleteAccount(ctx, &domain.Principal{Sub: "alice"}); err != nil {
		t.Fatalf("delete after unsharing: %v", err)
	}
	if _, err := f.elements.Get(ctx, "aaaaaaaaaaaaaaaaaaaaac01"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("the board survived a deletion that should have gone through: %v", err)
	}
}

// The trash purge removed the card and left the photograph — still in the
// bucket, still fetchable, and with the row that named it gone there was
// nothing left able to enumerate it.
func TestSweep_CollectsTheBytesBehindPurgedElements(t *testing.T) {
	elements := memory.NewElementRepo()
	atts := memory.NewAttachmentRepo()
	comments := memory.NewCommentRepo()
	blobs := &fakeBlobs{}
	ctx := context.Background()
	long := time.Now().UTC().Add(-2 * domain.TrashRetention)

	mkTrashed := func(id, typ string, content domain.Content) {
		el := &domain.Element{
			ID: id, Type: domain.ElementType(typ), Content: content,
			CreatedBy: "alice", CreatedAt: long, UpdatedAt: long,
		}
		if err := elements.Insert(ctx, el); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		if err := elements.SoftDelete(ctx, []string{id}, "alice", id, long); err != nil {
			t.Fatalf("trash %s: %v", id, err)
		}
	}
	mkTrashed("bbbbbbbbbbbbbbbbbbbbbc01", "IMAGE", domain.Content{"attachmentId": "att-gone"})
	mkTrashed("bbbbbbbbbbbbbbbbbbbbbc02", "COMMENT_THREAD", domain.Content{})
	// A live card sharing an attachment with a trashed duplicate: the picture
	// must NOT be collected out from under it.
	mkTrashed("bbbbbbbbbbbbbbbbbbbbbc03", "IMAGE", domain.Content{"attachmentId": "att-shared"})
	if err := elements.Insert(ctx, &domain.Element{
		ID: "bbbbbbbbbbbbbbbbbbbbbc04", Type: domain.TypeImage,
		Content:   domain.Content{"attachmentId": "att-shared"},
		CreatedBy: "alice", CreatedAt: long, UpdatedAt: long,
	}); err != nil {
		t.Fatalf("seed live twin: %v", err)
	}

	for _, id := range []string{"att-gone", "att-shared"} {
		if err := atts.Insert(ctx, &domain.Attachment{
			ID: id, OwnerID: "alice", Key: "u/alice/" + id + "/f.jpg",
			Status: domain.AttachmentUploaded, CreatedAt: long,
		}); err != nil {
			t.Fatalf("seed attachment: %v", err)
		}
	}
	if err := comments.Insert(ctx, &domain.Comment{
		ID: "c-9", ThreadID: "bbbbbbbbbbbbbbbbbbbbbc02", AuthorID: "alice", CreatedAt: long,
	}); err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	sweeper := NewMaintenanceService(elements, atts, blobs, zap.NewNop())
	sweeper.AttachComments(comments)
	sweeper.SweepOnce(ctx)

	if len(blobs.removed) != 1 || blobs.removed[0] != "u/alice/att-gone/f.jpg" {
		t.Errorf("blobs removed = %v; want exactly the orphan", blobs.removed)
	}
	if _, err := atts.Get(ctx, "att-shared"); err != nil {
		t.Errorf("an attachment a LIVE card still shows was collected: %v", err)
	}
	if got := comments.Count(); got != 0 {
		t.Errorf("%d message(s) outlived the thread they were posted on", got)
	}
}

// ---- stubs ------------------------------------------------------------------

type stubNotifications struct{}

func (stubNotifications) Insert(context.Context, *domain.Notification) error { return nil }
func (stubNotifications) ListByUser(context.Context, string, bool, int) ([]*domain.Notification, error) {
	return nil, nil
}
func (stubNotifications) MarkRead(context.Context, string, []string) error { return nil }
func (stubNotifications) DeleteByUser(context.Context, string) error       { return nil }

type stubUsers struct{ rows map[string]*domain.User }

func newStubUsers() *stubUsers {
	return &stubUsers{rows: map[string]*domain.User{
		"alice": {ID: "u1", KeycloakSub: "alice", Email: "alice@example.com"},
	}}
}

func (s *stubUsers) GetBySub(_ context.Context, sub string) (*domain.User, error) {
	if u, ok := s.rows[sub]; ok {
		return u, nil
	}
	return nil, domain.ErrNotFound
}
func (s *stubUsers) GetByEmail(context.Context, string) (*domain.User, error) {
	return nil, domain.ErrNotFound
}
func (s *stubUsers) Insert(context.Context, *domain.User) error { return nil }
func (s *stubUsers) Update(context.Context, *domain.User) error { return nil }
func (s *stubUsers) UpdateSettings(context.Context, string, *domain.UserSettings) error {
	return nil
}
func (s *stubUsers) Delete(_ context.Context, sub string) error {
	delete(s.rows, sub)
	return nil
}
