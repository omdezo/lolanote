// Package storage implements the presigned direct-to-storage upload flow
// (§9.10): the client asks the API to presign, then PUTs the bytes straight
// to object storage — file bytes never transit the API servers.
package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"qomranote/backend/internal/config"
	"qomranote/backend/internal/domain"
)

// R2Presigner presigns PUT/GET URLs against Cloudflare R2's S3-compatible API.
type R2Presigner struct {
	client    *s3.Client
	presign   *s3.PresignClient
	bucket    string
	publicURL string // optional public bucket / custom domain base
	expiry    time.Duration
}

var _ domain.Presigner = (*R2Presigner)(nil)

// NewR2Presigner builds the S3 client pointed at the account's R2 endpoint.
func NewR2Presigner(ctx context.Context, cfg *config.Config) (*R2Presigner, error) {
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.R2AccountID)
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.R2AccessKeyID, cfg.R2SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("r2 config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
	})
	return &R2Presigner{
		client:    client,
		presign:   s3.NewPresignClient(client),
		bucket:    cfg.R2Bucket,
		publicURL: strings.TrimSuffix(cfg.R2PublicBaseURL, "/"),
		expiry:    15 * time.Minute,
	}, nil
}

// readWindow is how long a freshly signed GET lives.
//
// Short on purpose. It is handed out per request behind an ACL check, so it only
// has to survive the image load it was minted for — and the shorter it is, the
// smaller the window in which a revoked collaborator's cached URL still opens
// the bucket. The old value was SEVEN DAYS, baked once into element content at
// upload time, which made it a bearer credential for direct bucket access that
// travelled into every board payload, every export and every viewer's IndexedDB
// mirror, and that no revocation anywhere in the product could take back.
const readWindow = 10 * time.Minute

// PresignGet mints a fresh read URL for a stored object.
func (p *R2Presigner) PresignGet(ctx context.Context, key string) (string, error) {
	if p.publicURL != "" {
		return p.publicURL + "/" + key, nil
	}
	get, err := p.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &p.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(readWindow))
	if err != nil {
		return "", fmt.Errorf("r2 presign get: %w", err)
	}
	return get.URL, nil
}

// Remove deletes a stored object.
//
// The driver had PresignPut and PresignGet and no way to delete anything, so
// "this permanently deletes all uploaded files" could not have been true on the
// production driver even if every caller had been wired: the deployment's only
// hope was a bucket lifecycle rule nobody had configured or documented.
func (p *R2Presigner) Remove(key string) error {
	_, err := p.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: &p.bucket,
		Key:    &key,
	})
	if err != nil {
		return fmt.Errorf("r2 delete %s: %w", key, err)
	}
	return nil
}

// PresignPut returns a 15-minute presigned PUT plus the read URL for the
// object. With no public bucket configured, reads get a presigned GET.
func (p *R2Presigner) PresignPut(ctx context.Context, key, contentType string, size int64) (string, string, error) {
	put, err := p.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        &p.bucket,
		Key:           &key,
		ContentType:   &contentType,
		ContentLength: &size,
	}, s3.WithPresignExpires(p.expiry))
	if err != nil {
		return "", "", fmt.Errorf("r2 presign put: %w", err)
	}

	var publicURL string
	if p.publicURL != "" {
		publicURL = p.publicURL + "/" + key
	} else {
		// The week-long signature stays ONLY as the legacy field, and only until
		// clients move to the indirection route.
		//
		// The comment that used to sit here claimed "the client re-resolves stale
		// attachment URLs on 403". No such route was ever registered and no
		// client ever handled a 403 — so on the production driver every uploaded
		// image and PDF went permanently dead on day seven: a mood board
		// assembled in July was a grid of broken images in August, and look_at
		// went blind with it. The bytes were still in the bucket; the product had
		// simply forgotten how to address them.
		//
		// A signed URL is also a bearer credential for direct bucket access, and
		// written into element content it travelled into every board payload,
		// every export and every viewer's offline mirror — so "I revoked their
		// access" was untrue for as long as the signature lasted.
		//
		// The durable answer is GET /api/v1/attachments/:id/blob, which signs per
		// request behind an ACL check: it cannot expire, and revocation is
		// immediate by construction. That route now exists (PresignGet is what it
		// calls). This value is what already-written elements still carry, and
		// shortening it now would break them faster than the fix arrives.
		get, err := p.presign.PresignGetObject(ctx, &s3.GetObjectInput{
			Bucket: &p.bucket,
			Key:    &key,
		}, s3.WithPresignExpires(7*24*time.Hour))
		if err != nil {
			return "", "", fmt.Errorf("r2 presign get: %w", err)
		}
		publicURL = get.URL
	}
	return put.URL, publicURL, nil
}
