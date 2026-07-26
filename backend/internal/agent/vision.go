package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"qomranote/backend/internal/domain"
)

// Vision.
//
// A board of screenshots is a natural way to use QomraNote and, until now, an
// empty room to the agent: IMAGE elements arrived in the digest as a filename
// and nothing else, so "sort these mockups" had nothing to sort on.
//
// Images are attached ON DEMAND, never by default. Forty screenshots would
// exhaust the context and the budget in a single turn, and most runs do not
// need to look at anything. The model asks for a specific element, and only
// then are bytes fetched.

// maxImageBytes bounds one attachment. Providers reject larger payloads anyway,
// and a board's worth of full-resolution photographs is not something to
// discover at the provider's error handler.
const maxImageBytes = 4 << 20 // 4 MiB

// maxImagesPerRun keeps a run's spend predictable. Images are the single most
// expensive thing that can enter a context.
const maxImagesPerRun = 4

// visionTypes is the set providers actually accept. Anything else is described
// by its filename, which is what the digest already does.
var visionTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

// ImageFetcher resolves an attachment into bytes the model can be shown.
//
// It is an interface so the agent depends on "can I see this image" rather than
// on a storage driver: local disk and R2 answer it differently, and a test
// answers it without either.
type ImageFetcher interface {
	Fetch(ctx context.Context, attachmentID string) (data []byte, mediaType string, err error)
}

// HTTPImageFetcher reads an attachment through the URL it is already published
// at, which is the one path that works identically for local disk and R2.
type HTTPImageFetcher struct {
	Attachments domain.AttachmentRepository
	Client      *http.Client
}

// NewHTTPImageFetcher builds a fetcher with a short timeout: a slow blob store
// must not hold a run open.
func NewHTTPImageFetcher(a domain.AttachmentRepository) *HTTPImageFetcher {
	return &HTTPImageFetcher{
		Attachments: a,
		Client:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (f *HTTPImageFetcher) Fetch(ctx context.Context, attachmentID string) ([]byte, string, error) {
	if f == nil || f.Attachments == nil {
		return nil, "", fmt.Errorf("images are not available here")
	}
	att, err := f.Attachments.Get(ctx, attachmentID)
	if err != nil {
		return nil, "", fmt.Errorf("that image could not be found")
	}
	mediaType := strings.ToLower(strings.TrimSpace(att.ContentType))
	if !visionTypes[mediaType] {
		return nil, "", fmt.Errorf("%s is a %s, which cannot be viewed", att.Filename, att.ContentType)
	}
	if att.Size > maxImageBytes {
		return nil, "", fmt.Errorf("%s is too large to look at", att.Filename)
	}
	if att.PublicURL == "" {
		return nil, "", fmt.Errorf("%s has not finished uploading", att.Filename)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, att.PublicURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("that image could not be read")
	}
	res, err := f.Client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("that image could not be read")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 400 {
		return nil, "", fmt.Errorf("that image could not be read")
	}
	// Bounded even though Size was checked: the record and the object can
	// disagree, and the limit that matters is on what enters the context.
	data, err := io.ReadAll(io.LimitReader(res.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("that image could not be read")
	}
	if len(data) > maxImageBytes {
		return nil, "", fmt.Errorf("%s is too large to look at", att.Filename)
	}
	return data, mediaType, nil
}

var _ ImageFetcher = (*HTTPImageFetcher)(nil)
