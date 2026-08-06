package service

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// "Delete forever" hard-deletes on the spot. It never went near the sweeper —
// the purged rows never appear in ExpiringSoon — so the only garbage collector
// in the product never learned they existed. Two things survived every one:
// the uploaded bytes, still fetchable through an unauthenticated blob route with
// the URL sitting in any export or cached board that ever saw it; and the
// journal's verbatim copy of the content, served by GET /boards/:id/transactions
// to any current editor, including one invited after the deletion.
//
// A filmmaker deleting a rejected casting photo had deleted the card, not the
// photograph.

type permanentFixture struct {
	elements *memory.ElementRepo
	atts     *memory.AttachmentRepo
	journal  *memory.TransactionRepo
	blobs    *fakeBlobs
	svc      *ElementService
}

func newPermanentFixture(t *testing.T) *permanentFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	f := &permanentFixture{
		elements: memory.NewElementRepo(),
		atts:     memory.NewAttachmentRepo(),
		journal:  memory.NewTransactionRepo(),
		blobs:    &fakeBlobs{},
	}
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
	mk("dddddddddddddddddddddd01", "BOARD", "", &domain.ACL{OwnerID: "alice", Editors: []string{}},
		domain.Content{"title": "Casting"})
	mk("dddddddddddddddddddddd02", "IMAGE", "dddddddddddddddddddddd01", nil,
		domain.Content{"attachmentId": "att-rejected"})
	// A live card sharing the same upload: a duplicate points at one blob, and
	// collecting on "the element that named it is gone" would take the picture
	// out from under the copy.
	mk("dddddddddddddddddddddd03", "IMAGE", "dddddddddddddddddddddd01", nil,
		domain.Content{"attachmentId": "att-shared"})
	mk("dddddddddddddddddddddd04", "IMAGE", "dddddddddddddddddddddd01", nil,
		domain.Content{"attachmentId": "att-shared"})

	for _, id := range []string{"att-rejected", "att-shared"} {
		if err := f.atts.Insert(ctx, &domain.Attachment{
			ID: id, OwnerID: "alice", Key: "u/alice/" + id + "/f.jpg",
			Status: domain.AttachmentUploaded, CreatedAt: now,
		}); err != nil {
			t.Fatalf("seed attachment: %v", err)
		}
	}
	// The journal holds what the card said, verbatim, in the op's inverse.
	if err := f.journal.Insert(ctx, &domain.Transaction{
		ID: "t-cast", BoardID: "dddddddddddddddddddddd01", UserID: "alice", CreatedAt: now,
		Ops: []domain.Op{
			{ElementID: "dddddddddddddddddddddd02", Action: domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"caption": "not right for the part"}},
				UndoChanges: domain.Content{"content": map[string]any{"caption": ""}}},
			{ElementID: "dddddddddddddddddddddd03", Action: domain.ActionUpdate,
				Changes: domain.Content{"content": map[string]any{"caption": "keep"}}},
		},
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	collector := NewCollector(f.elements, zap.NewNop())
	collector.AttachBlobs(f.atts, f.blobs)
	collector.AttachJournal(f.journal)
	f.svc = NewElementService(f.elements, NewAccessResolver(f.elements),
		IDGenerator(func() string { return "gen" }))
	f.svc.AttachCollector(collector)
	return f
}

func (f *permanentFixture) trash(t *testing.T, id string) {
	t.Helper()
	if err := f.elements.SoftDelete(context.Background(), []string{id}, "alice", id, time.Now().UTC()); err != nil {
		t.Fatalf("trash %s: %v", id, err)
	}
}

func TestDeletePermanently_TakesTheBytesWithTheCard(t *testing.T) {
	f := newPermanentFixture(t)
	ctx := context.Background()
	f.trash(t, "dddddddddddddddddddddd02")

	if err := f.svc.DeletePermanently(ctx, &domain.Principal{Sub: "alice"}, "dddddddddddddddddddddd02"); err != nil {
		t.Fatalf("delete forever: %v", err)
	}
	if len(f.blobs.removed) != 1 || f.blobs.removed[0] != "u/alice/att-rejected/f.jpg" {
		t.Fatalf("blobs removed = %v; the photograph is still in the bucket", f.blobs.removed)
	}
	if _, err := f.atts.Get(ctx, "att-rejected"); err == nil {
		t.Error("the attachment row survived the element that named it")
	}
}

func TestDeletePermanently_KeepsABlobAnotherCardStillShows(t *testing.T) {
	f := newPermanentFixture(t)
	ctx := context.Background()
	f.trash(t, "dddddddddddddddddddddd03")

	if err := f.svc.DeletePermanently(ctx, &domain.Principal{Sub: "alice"}, "dddddddddddddddddddddd03"); err != nil {
		t.Fatalf("delete forever: %v", err)
	}
	if len(f.blobs.removed) != 0 {
		t.Errorf("removed %v out from under a live duplicate", f.blobs.removed)
	}
	if _, err := f.atts.Get(ctx, "att-shared"); err != nil {
		t.Errorf("an attachment a live card still shows was collected: %v", err)
	}
}

// The row stays — "Ali deleted a card" is the audit trail. The content goes.
func TestDeletePermanently_StripsTheContentOutOfBoardHistory(t *testing.T) {
	f := newPermanentFixture(t)
	ctx := context.Background()
	f.trash(t, "dddddddddddddddddddddd02")

	if err := f.svc.DeletePermanently(ctx, &domain.Principal{Sub: "alice"}, "dddddddddddddddddddddd02"); err != nil {
		t.Fatalf("delete forever: %v", err)
	}
	txn, err := f.journal.Get(ctx, "t-cast")
	if err != nil {
		t.Fatalf("the journal row was deleted; the audit trail is the part that must survive: %v", err)
	}
	if len(txn.Ops) != 2 {
		t.Fatalf("ops = %d, want both — redaction removes content, not history", len(txn.Ops))
	}
	purged := txn.Ops[0]
	if len(purged.Changes) != 0 || len(purged.UndoChanges) != 0 {
		t.Errorf("the deleted card's text is still readable through board history: %+v", purged)
	}
	if !purged.Redacted {
		t.Error("nothing marks the op as redacted, so a reader cannot tell it apart from a change with no detail")
	}
	if purged.Action != domain.ActionUpdate || purged.ElementID != "dddddddddddddddddddddd02" {
		t.Errorf("the audit half of the op was lost too: %+v", purged)
	}
	// A drag of forty cards where one was later purged must keep the other
	// thirty-nine's inverses, or undo stops working for changes nobody deleted.
	if kept := txn.Ops[1]; len(kept.Changes) == 0 || kept.Redacted {
		t.Errorf("an op for a still-live element was redacted with it: %+v", kept)
	}
}

func TestEmptyTrash_ReachesTheSameThreeThings(t *testing.T) {
	f := newPermanentFixture(t)
	ctx := context.Background()
	f.trash(t, "dddddddddddddddddddddd02")

	n, err := f.svc.EmptyTrash(ctx, &domain.Principal{Sub: "alice"})
	if err != nil {
		t.Fatalf("empty trash: %v", err)
	}
	if n != 1 {
		t.Fatalf("emptied %d items, want 1", n)
	}
	if len(f.blobs.removed) != 1 {
		t.Errorf("Empty Trash left the bytes behind: %v", f.blobs.removed)
	}
	txn, _ := f.journal.Get(ctx, "t-cast")
	if len(txn.Ops[0].Changes) != 0 {
		t.Error("Empty Trash left the content readable through board history")
	}
}

// Fail closed: an attachment whose referrer query errors is KEPT. Keeping bytes
// nobody wants costs storage; deleting bytes somebody wants costs the thing
// itself.
type blindGraph struct{ *memory.ElementRepo }

func (blindGraph) AttachmentReferrers(context.Context, []string) (map[string]int64, error) {
	return nil, context.DeadlineExceeded
}

func TestDeletePermanently_AFailedReferenceCheckKeepsTheBlob(t *testing.T) {
	f := newPermanentFixture(t)
	ctx := context.Background()
	collector := NewCollector(blindGraph{f.elements}, zap.NewNop())
	collector.AttachBlobs(f.atts, f.blobs)
	f.svc.AttachCollector(collector)
	f.trash(t, "dddddddddddddddddddddd02")

	if err := f.svc.DeletePermanently(ctx, &domain.Principal{Sub: "alice"}, "dddddddddddddddddddddd02"); err != nil {
		t.Fatalf("delete forever: %v", err)
	}
	if len(f.blobs.removed) != 0 {
		t.Errorf("removed %v on an unanswerable reference check", f.blobs.removed)
	}
}
