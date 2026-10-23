package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"tablekit.de/tablekit/internal/config"
)

type s3Store struct {
	client *minio.Client
	bucket string
	scheme string
}

func newS3(cfg config.StorageConfig) *s3Store {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		// Configuration was validated by the caller; a malformed endpoint here
		// degrades to the disabled store rather than crashing the server.
		return nil
	}
	scheme := "http"
	if cfg.UseSSL {
		scheme = "https"
	}
	return &s3Store{client: client, bucket: cfg.Bucket, scheme: scheme}
}

func (s *s3Store) ensureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("storage: check bucket: %w", err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("storage: create bucket: %w", err)
	}
	return nil
}

func (s *s3Store) Put(ctx context.Context, key, contentType string, body io.Reader, size int64) (Object, error) {
	if err := s.ensureBucket(ctx); err != nil {
		return Object{}, err
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, body, size, minio.PutObjectOptions{
		ContentType:  contentType,
		CacheControl: "public, max-age=31536000, immutable",
	})
	if err != nil {
		return Object{}, fmt.Errorf("storage: put object: %w", err)
	}
	return Object{Key: key, URL: s.publicURL(key)}, nil
}

func (s *s3Store) Remove(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("storage: remove object: %w", err)
	}
	return nil
}

func (s *s3Store) URL(key string) string { return s.publicURL(key) }

func (s *s3Store) publicURL(key string) string {
	base := s.client.EndpointURL()
	var b strings.Builder
	b.WriteString(s.scheme)
	b.WriteString("://")
	b.WriteString(base.Host)
	b.WriteString("/")
	b.WriteString(url.PathEscape(s.bucket))
	b.WriteString("/")
	b.WriteString(strings.TrimPrefix(key, "/"))
	return b.String()
}
