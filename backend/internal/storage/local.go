package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"qomranote/backend/internal/domain"
)

// LocalPresigner is the development fallback used until Cloudflare R2
// credentials are configured (STORAGE_DRIVER=local). It keeps the exact same
// presign contract; "presigned" URLs simply point back at the API, which
// accepts the PUT and writes to disk.
type LocalPresigner struct {
	dir     string
	apiBase string
}

var _ domain.Presigner = (*LocalPresigner)(nil)

// NewLocalPresigner ensures the upload directory exists.
func NewLocalPresigner(dir, apiBase string) (*LocalPresigner, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("local storage dir: %w", err)
	}
	return &LocalPresigner{dir: dir, apiBase: strings.TrimSuffix(apiBase, "/")}, nil
}

func (p *LocalPresigner) PresignPut(_ context.Context, key, _ string, _ int64) (string, string, error) {
	upload := p.apiBase + "/api/v1/blob/" + key
	return upload, upload, nil
}

// Path maps a storage key to its on-disk location, refusing traversal.
func (p *LocalPresigner) Path(key string) (string, error) {
	clean := filepath.Clean(key)
	if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
		return "", domain.ErrValidation
	}
	return filepath.Join(p.dir, clean), nil
}

// Remove deletes a stored blob (attachment garbage collection).
func (p *LocalPresigner) Remove(key string) error {
	path, err := p.Path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Save streams a request body into the file for key.
func (p *LocalPresigner) Save(key string, body io.Reader) error {
	path, err := p.Path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, body)
	return err
}
