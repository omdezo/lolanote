package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// Blob reads had no authorization point of any kind, and the URL stored in
// element content was a signed bearer credential for direct bucket access that
// travelled into every board payload, every export and every offline mirror —
// so "I revoked their access" was untrue for as long as the signature lasted,
// and the signature lasted a week. The same design also meant every image went
// permanently dead on day seven, because the re-resolution route the storage
// driver's own comment promised was never built.

// stubPresigner is a driver that can mint a fresh read URL, which is what makes
// the indirection possible at all.
type stubPresigner struct{ minted int }

func (p *stubPresigner) PresignPut(context.Context, string, string, int64) (string, string, error) {
	return "https://bucket/put", "https://bucket/get?X-Amz-Signature=stale", nil
}

func (p *stubPresigner) PresignGet(_ context.Context, key string) (string, error) {
	p.minted++
	return "https://bucket/" + key + "?X-Amz-Signature=fresh", nil
}

func attachmentFixture(t *testing.T) (*UploadService, *memory.ElementRepo, *memory.AttachmentRepo, *stubPresigner) {
	t.Helper()
	elements := memory.NewElementRepo()
	atts := memory.NewAttachmentRepo()
	presigner := &stubPresigner{}
	uploads := NewUploadService(atts, presigner, func() string { return "6666666666666666666ab0aa" })
	uploads.AttachAccess(elements, NewAccessResolver(elements))

	ctx := context.Background()
	now := time.Now().UTC()
	if err := elements.Insert(ctx, &domain.Element{
		ID: "6666666666666666666ab001", Type: domain.TypeBoard,
		Content: domain.Content{"title": "Stills"},
		ACL: &domain.ACL{
			OwnerID: "alice", Editors: []string{"bob"},
			ViewLink: &domain.ViewLink{Token: "client-view-link"},
		},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	if err := elements.Insert(ctx, &domain.Element{
		ID: "6666666666666666666ab002", Type: domain.TypeImage,
		Location:  domain.Location{ParentID: "6666666666666666666ab001"},
		Content:   domain.Content{"attachmentId": "att-still"},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	if err := atts.Insert(ctx, &domain.Attachment{
		ID: "att-still", OwnerID: "alice", Key: "u/alice/att-still/frame.jpg",
		Status: domain.AttachmentUploaded, CreatedAt: now,
		PublicURL: "https://bucket/get?X-Amz-Signature=stale",
	}); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	// A file this person uploaded that no element points at — a request
	// attachment. Only its owner may read it.
	if err := atts.Insert(ctx, &domain.Attachment{
		ID: "att-brief", OwnerID: "alice", Key: "u/alice/att-brief/brief.pdf",
		Status: domain.AttachmentUploaded, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed brief: %v", err)
	}
	return uploads, elements, atts, presigner
}

func TestAttachmentBlob_SignsFreshPerRequestAndOnlyForPeopleWhoMayLookAtIt(t *testing.T) {
	uploads, _, _, presigner := attachmentFixture(t)
	ctx := context.Background()

	t.Run("the owner", func(t *testing.T) {
		url, err := uploads.Resolve(ctx, &domain.Principal{Sub: "alice"}, "att-still")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !strings.Contains(url, "fresh") {
			t.Errorf("url = %q; a stored signature is what dies after seven days", url)
		}
	})

	t.Run("an editor on the board the picture is on", func(t *testing.T) {
		if _, err := uploads.Resolve(ctx, &domain.Principal{Sub: "bob"}, "att-still"); err != nil {
			t.Fatalf("a collaborator could not see a picture on their own board: %v", err)
		}
	})

	t.Run("a read-only link holder", func(t *testing.T) {
		if _, err := uploads.Resolve(ctx, &domain.Principal{ShareToken: "client-view-link"}, "att-still"); err != nil {
			t.Fatalf("a view link that shows the board could not show its images: %v", err)
		}
	})

	t.Run("a stranger", func(t *testing.T) {
		_, err := uploads.Resolve(ctx, &domain.Principal{Sub: "mallory"}, "att-still")
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("a stranger was handed a bucket URL (err = %v)", err)
		}
	})

	t.Run("nobody at all", func(t *testing.T) {
		_, err := uploads.Resolve(ctx, &domain.Principal{}, "att-still")
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("an anonymous caller with no token was handed a bucket URL (err = %v)", err)
		}
	})

	t.Run("a file no element points at is the uploader's alone", func(t *testing.T) {
		if _, err := uploads.Resolve(ctx, &domain.Principal{Sub: "alice"}, "att-brief"); err != nil {
			t.Fatalf("the uploader could not read their own file: %v", err)
		}
		if _, err := uploads.Resolve(ctx, &domain.Principal{Sub: "bob"}, "att-brief"); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("a request attachment leaked to a board collaborator (err = %v)", err)
		}
	})

	if presigner.minted == 0 {
		t.Error("no URL was ever signed; the route is handing back the stored credential")
	}
}

// Revocation has to be immediate BY CONSTRUCTION, which is the whole argument
// for signing per request: the moment the ACL changes, the next read is refused.
func TestAttachmentBlob_RevocationTakesEffectOnTheNextRead(t *testing.T) {
	uploads, elements, _, _ := attachmentFixture(t)
	ctx := context.Background()
	bob := &domain.Principal{Sub: "bob"}

	if _, err := uploads.Resolve(ctx, bob, "att-still"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := elements.SetACL(ctx, "6666666666666666666ab001",
		&domain.ACL{OwnerID: "alice", Editors: []string{}}); err != nil {
		t.Fatalf("remove editor: %v", err)
	}
	if _, err := uploads.Resolve(ctx, bob, "att-still"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a removed collaborator can still fetch the board's files (err = %v)", err)
	}
}

// Fails closed. A blip in the element graph must not turn a private bucket into
// a public one.
func TestAttachmentBlob_AnUnreadableGraphRefusesRatherThanAllows(t *testing.T) {
	atts := memory.NewAttachmentRepo()
	if err := atts.Insert(context.Background(), &domain.Attachment{
		ID: "att-x", OwnerID: "alice", Key: "u/alice/att-x/f.jpg",
		Status: domain.AttachmentUploaded, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// No element repository attached at all: the service cannot answer "who may
	// see this", so it must not answer "everyone".
	uploads := NewUploadService(atts, &stubPresigner{}, func() string { return "x" })
	if _, err := uploads.Resolve(context.Background(),
		&domain.Principal{Sub: "mallory"}, "att-x"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an unanswerable permission question was answered yes (err = %v)", err)
	}
}
