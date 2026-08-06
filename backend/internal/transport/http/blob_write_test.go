package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"qomranote/backend/internal/auth/authtest"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
	"qomranote/backend/internal/service"
	"qomranote/backend/internal/storage"
)

// SEC3 — the write half of the blob path had no object-level authorization.
//
// Blob READS grew a full permission model: UploadService.Resolve asks who is
// asking, signs per request, and has its own test file arguing every case. The
// PUT that puts the bytes there in the first place checked a bearer token and
// then wrote whatever key the URL named. "Presigned" is a fiction under the
// local driver — the URL is literally /api/v1/blob/<key>, and the key travels
// in every element that shows the file — so any account could PUT over anyone
// else's upload and swap the bytes underneath every card that renders it.
//
// It is not a dev-only concern: STORAGE_DRIVER defaults to "local", and the
// compose file ships that default.
//
// The test drives the real router with the real handler so it proves the guard
// is WIRED, not merely written: a check that exists in UploadService and is
// never called from the route is the failure mode this whole sweep is about.

type blobFixture struct {
	echo *echo.Echo
	idp  *authtest.IDP
	dir  string
}

func newBlobFixture(t *testing.T) *blobFixture {
	t.Helper()
	dir := t.TempDir()
	local, err := storage.NewLocalPresigner(dir, "http://api.test")
	if err != nil {
		t.Fatalf("local presigner: %v", err)
	}
	atts := memory.NewAttachmentRepo()
	ctx := context.Background()
	// Alice presigned one upload. This is the only way a key comes to exist.
	if err := atts.Insert(ctx, &domain.Attachment{
		ID: "att-alice", OwnerID: "alice", Key: "u/alice/att-alice/frame.jpg",
		Filename: "frame.jpg", ContentType: "image/jpeg", Size: 12,
		Status: domain.AttachmentPresigned, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	idp := authtest.NewIDP(t)
	h := &Handlers{
		Verifier: idp.Verifier(t, webClient),
		Local:    local,
		Uploads:  service.NewUploadService(atts, local, func() string { return "att-new" }),
		Log:      zap.NewNop(),
	}
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.HTTPErrorHandler = errorHandler(zap.NewNop())
	registerRoutes(e, h)
	return &blobFixture{echo: e, idp: idp, dir: dir}
}

func (f *blobFixture) put(t *testing.T, sub, key, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/blob/"+key, strings.NewReader(body))
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+f.idp.Sign(t, authtest.Claims{
		Sub: sub, Email: sub + "@example.com", Azp: webClient,
	}))
	rec := httptest.NewRecorder()
	f.echo.ServeHTTP(rec, req)
	return rec.Code
}

func (f *blobFixture) onDisk(t *testing.T, key string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(key)))
	if err != nil {
		return ""
	}
	return string(body)
}

func TestBlobPut_RefusesAKeyThatIsNotTheCallersOwn(t *testing.T) {
	f := newBlobFixture(t)
	const key = "u/alice/att-alice/frame.jpg"

	if code := f.put(t, "alice", key, "the real frame"); code != http.StatusOK {
		t.Fatalf("the owner's own upload was refused: %d", code)
	}
	if got := f.onDisk(t, key); got != "the real frame" {
		t.Fatalf("owner upload did not land: %q", got)
	}

	// Mallory has an account, therefore a valid bearer. She knows the key
	// because it is in the URL of every image on any board she can see.
	if code := f.put(t, "mallory", key, "swapped"); code != http.StatusForbidden {
		t.Fatalf("overwriting another account's upload returned %d, want 403", code)
	}
	if got := f.onDisk(t, key); got != "the real frame" {
		t.Fatalf("the file was replaced anyway: %q — the refusal happened after the write", got)
	}
}

func TestBlobPut_RefusesAKeyNobodyEverPresigned(t *testing.T) {
	f := newBlobFixture(t)
	for _, key := range []string{
		"u/mallory/att-invented/payload.svg", // no such attachment row
		"anything.txt",                       // not the key shape at all
		"u/mallory//payload.svg",             // empty id
	} {
		if code := f.put(t, "mallory", key, "payload"); code != http.StatusForbidden {
			t.Errorf("PUT %q returned %d, want 403 — the store is not a free write surface", key, code)
		}
		if got := f.onDisk(t, key); got != "" {
			t.Errorf("PUT %q wrote %q to disk", key, got)
		}
	}
}

// The key is authorized against the REGISTRY, never by reading the sub out of
// the path. A path that is its own authority is exactly how this was reachable:
// the caller chooses the path.
func TestBlobPut_RefusesAKeyWhoseOwnerSegmentWasForged(t *testing.T) {
	f := newBlobFixture(t)
	// Mallory writes her own sub into the path but names Alice's attachment id.
	const forged = "u/mallory/att-alice/frame.jpg"
	if code := f.put(t, "mallory", forged, "payload"); code != http.StatusForbidden {
		t.Fatalf("a forged owner segment returned %d, want 403", code)
	}
	if got := f.onDisk(t, forged); got != "" {
		t.Fatalf("the forged key was written: %q", got)
	}
}
