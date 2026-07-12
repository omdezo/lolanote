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
}

// NewUploadService constructs the service.
func NewUploadService(attachments domain.AttachmentRepository, presigner domain.Presigner, newID IDGenerator) *UploadService {
	return &UploadService{attachments: attachments, presigner: presigner, newID: newID}
}

// Plan-style limits (mirrors §4.8's table; generous "pro" defaults here).
const (
	maxImageSize = 100 << 20      // 100 MB
	maxFileSize  = 5 << 30        // 5 GB
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
func (s *UploadService) Get(ctx context.Context, id string) (*domain.Attachment, error) {
	return s.attachments.Get(ctx, id)
}
