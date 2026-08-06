package service

import (
	"context"

	"qomranote/backend/internal/domain"
)

// ExportedAttachment names a file an export refers to, without carrying a way
// into the bucket.
//
// An export is an archive, not a set of keys. The presigned URL that used to sit
// in content.url is an AWS SigV4 credential for direct bucket access that
// bypasses the application entirely: it travelled into every `format=json`
// export and every "download my data" bundle, and stayed valid for seven days
// after somebody's access was revoked. For a production board holding unreleased
// stills, "I revoked their access" was simply not true for anyone holding a file
// they had already downloaded.
type ExportedAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

// stripCredentialURLs removes the signed URL from every element that names an
// attachment, leaving the attachmentId as the reference.
//
// The id is enough: the blob route resolves it behind an ACL check, so an
// importer or a person reading the archive can still find the file through the
// application — which is the only place the permission to see it is known.
func stripCredentialURLs(els []*domain.Element) {
	for _, el := range els {
		if el == nil || el.Content == nil {
			continue
		}
		if att, _ := el.Content["attachmentId"].(string); att == "" {
			continue
		}
		delete(el.Content, "url")
	}
}

// attachmentManifest lists the files an export refers to.
//
// Best effort: a row that cannot be read is omitted rather than failing the
// export, because an archive missing one filename is worth more than no archive.
func attachmentManifest(ctx context.Context, atts domain.AttachmentRepository, els []*domain.Element) []ExportedAttachment {
	if atts == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []ExportedAttachment
	for _, el := range els {
		if el == nil || el.Content == nil {
			continue
		}
		id, _ := el.Content["attachmentId"].(string)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		att, err := atts.Get(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, ExportedAttachment{
			ID: att.ID, Filename: att.Filename,
			ContentType: att.ContentType, Size: att.Size,
		})
	}
	return out
}
