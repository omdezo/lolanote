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
		presign:   s3.NewPresignClient(client),
		bucket:    cfg.R2Bucket,
		publicURL: strings.TrimSuffix(cfg.R2PublicBaseURL, "/"),
		expiry:    15 * time.Minute,
	}, nil
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
		// Private bucket: a week-long presigned GET keeps boards renderable;
		// the client re-resolves stale attachment URLs on 403.
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
