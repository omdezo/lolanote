package service

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"qomranote/backend/internal/domain"
)

// UploadService runs the presigned direct-to-storage flow (§9.10):
// presign → browser PUTs bytes straight to storage → complete. The API never
// carries file bytes (except under the local dev driver).
type UploadService struct {
	attachments domain.AttachmentRepository
	presigner   domain.Presigner
	newID       IDGenerator
	// Optional, attached after construction so existing wiring compiles: the
	// authorization half of blob reads, which never had one.
	elements domain.ElementRepository
	access   *AccessResolver
}

// NewUploadService constructs the service.
func NewUploadService(attachments domain.AttachmentRepository, presigner domain.Presigner, newID IDGenerator) *UploadService {
	return &UploadService{attachments: attachments, presigner: presigner, newID: newID}
}

// Plan-style limits (mirrors §4.8's table; generous "pro" defaults here).
const (
	maxImageSize = 100 << 20 // 100 MB
	maxFileSize  = 5 << 30   // 5 GB
)

// PresignResult is what the client needs to perform the upload.
type PresignResult struct {
	AttachmentID string `json:"attachmentId"`
	UploadURL    string `json:"uploadUrl"`
	PublicURL    string `json:"publicUrl"`
}

var unsafeKeyChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Presign validates the request, registers the attachment, and returns the
// upload target.
func (s *UploadService) Presign(ctx context.Context, p *domain.Principal, filename, contentType string, size int64) (*PresignResult, error) {
	if filename == "" || size <= 0 {
		return nil, domain.ErrValidation
	}
	limit := int64(maxFileSize)
	if strings.HasPrefix(contentType, "image/") {
		limit = maxImageSize
	}
	if size > limit {
		return nil, fmt.Errorf("%w: file exceeds the %d byte limit", domain.ErrValidation, limit)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	id := s.newID()
	safe := unsafeKeyChars.ReplaceAllString(path.Base(filename), "_")
	key := fmt.Sprintf("u/%s/%s/%s", p.Sub, id, safe)

	uploadURL, publicURL, err := s.presigner.PresignPut(ctx, key, contentType, size)
	if err != nil {
		return nil, err
	}
	att := &domain.Attachment{
		ID: id, OwnerID: p.Sub, Key: key,
		Filename: filename, ContentType: contentType, Size: size,
		Status: domain.AttachmentPresigned, PublicURL: publicURL,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.attachments.Insert(ctx, att); err != nil {
		return nil, err
	}
	return &PresignResult{AttachmentID: id, UploadURL: uploadURL, PublicURL: publicURL}, nil
}

// AuthorizeUpload answers whether this caller may write bytes at this storage
// key — the write-side twin of Resolve.
//
// The local driver's "presigned" URL is just PUT /api/v1/blob/<key>, and that
// route checked a bearer token and then nothing at all: not who owns the key,
// not whether an attachment with that key exists. So any account could PUT over
// `u/<somebody-else>/<attachmentId>/<file>` and replace another person's
// picture or PDF in place, with every element that shows it silently rendering
// the substitute, and could write unlimited keys nobody ever presigned. The read
// half of this exact path grew a full ACL check (Resolve, and the whole
// attachment_access_test file); the write half kept none, which is the shape
// this sweep exists to find.
//
// A key is authorized by the REGISTRY, not by parsing: the presign step already
// recorded who owns which key, so this reads that row back and compares. Parsing
// the sub out of the path would make the path its own authority, which is how
// the escalation was possible in the first place.
func (s *UploadService) AuthorizeUpload(ctx context.Context, p *domain.Principal, key string) error {
	if p == nil || p.Sub == "" {
		return domain.ErrUnauthorized
	}
	id := attachmentIDFromKey(key)
	if id == "" {
		return domain.ErrForbidden
	}
	att, err := s.attachments.Get(ctx, id)
	if err != nil {
		// Never ErrNotFound: answering "no such key" turns this into an oracle
		// for which attachment ids exist.
		return domain.ErrForbidden
	}
	if att.OwnerID != p.Sub || att.Key != key {
		return domain.ErrForbidden
	}
	return nil
}

// attachmentIDFromKey reads the attachment id out of a storage key.
//
// Presign builds `u/<sub>/<attachmentId>/<safe filename>` and nothing else ever
// mints a key, so the shape is exact rather than heuristic. Anything that does
// not match is not a key this service issued.
func attachmentIDFromKey(key string) string {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "u" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return ""
	}
	return parts[2]
}

// Complete marks the attachment uploaded once the client's PUT succeeded.
func (s *UploadService) Complete(ctx context.Context, p *domain.Principal, id string) (*domain.Attachment, error) {
	att, err := s.attachments.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if att.OwnerID != p.Sub {
		return nil, domain.ErrForbidden
	}
	att.Status = domain.AttachmentUploaded
	if err := s.attachments.Update(ctx, att); err != nil {
		return nil, err
	}
	return att, nil
}

// Get returns an attachment for URL re-resolution.
//
// UNAUTHORIZED by construction and therefore not routed: every caller must go
// through Resolve, which is the one that asks who is asking.
func (s *UploadService) Get(ctx context.Context, id string) (*domain.Attachment, error) {
	return s.attachments.Get(ctx, id)
}

// GetPresigner is the half of a storage driver that can mint a fresh read URL.
// Optional, discovered by assertion, so a driver without one degrades to the
// URL stored at upload time rather than failing.
type GetPresigner interface {
	PresignGet(ctx context.Context, key string) (string, error)
}

// AttachReferrers lets Resolve ask which elements point at an attachment, which
// is how a board viewer is allowed to see a picture they do not own.
type AttachReferrers interface {
	AttachmentReferrers(ctx context.Context, attachmentIDs []string) (map[string]int64, error)
}

// AttachAccess wires the authorization half of blob reads.
func (s *UploadService) AttachAccess(elements domain.ElementRepository, access *AccessResolver) {
	s.elements, s.access = elements, access
}

// Resolve returns a freshly signed read URL for an attachment, after checking
// that this caller may see it.
//
// This is the route the storage driver's own comment promised and nobody built:
// "the client re-resolves stale attachment URLs on 403". Its absence is why
// every image on the production driver went permanently dead after seven days.
//
// It is also where blob reads get an authorization point they have never had —
// GET /api/v1/blob/* takes no credential at all, and the R2 URLs are signed
// bearer credentials that outlive any revocation. The rule: you may read a file
// if you uploaded it, or if some live element that points at it sits on a board
// you can open.
func (s *UploadService) Resolve(ctx context.Context, p *domain.Principal, id string) (string, error) {
	att, err := s.attachments.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if !s.mayRead(ctx, p, att) {
		return "", domain.ErrForbidden
	}
	if signer, ok := s.presigner.(GetPresigner); ok {
		return signer.PresignGet(ctx, att.Key)
	}
	return att.PublicURL, nil
}

// mayRead answers the ACL question for one attachment.
//
// Fails CLOSED: an unreadable element graph denies the read rather than
// allowing it, because the alternative is that a repository blip turns a private
// bucket into a public one.
func (s *UploadService) mayRead(ctx context.Context, p *domain.Principal, att *domain.Attachment) bool {
	if p == nil {
		return false
	}
	if p.Sub != "" && att.OwnerID == p.Sub {
		return true
	}
	if s.elements == nil || s.access == nil {
		return false
	}
	graph, ok := s.elements.(elementsByAttachment)
	if !ok {
		return false
	}
	holders, err := graph.ElementsByAttachment(ctx, att.ID)
	if err != nil {
		return false
	}
	for _, el := range holders {
		if el.IsDeleted() {
			continue
		}
		if role, _, err := s.access.Resolve(ctx, el.ID, p); err == nil && role.CanView() {
			return true
		}
	}
	return false
}

// elementsByAttachment is the reverse index from an uploaded file to the cards
// that show it.
type elementsByAttachment interface {
	ElementsByAttachment(ctx context.Context, attachmentID string) ([]*domain.Element, error)
}
