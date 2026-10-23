// Package storage provides an S3-compatible object store for media assets.
package storage

import (
	"context"
	"errors"
	"io"

	"tablekit.de/tablekit/internal/config"
)

// ErrNotConfigured is returned when no object store credentials are present.
var ErrNotConfigured = errors.New("storage: not configured")

// Object is a stored object and its public URL.
type Object struct {
	Key string
	URL string
}

// Store stores and retrieves immutable media objects.
type Store interface {
	// Put stores body under key with the given content type and returns the URL.
	Put(ctx context.Context, key, contentType string, body io.Reader, size int64) (Object, error)
	// Remove deletes an object; missing objects are not an error.
	Remove(ctx context.Context, key string) error
	// URL returns the public URL for a stored object key.
	URL(key string) string
}

// New builds the configured store, or a no-op store when unconfigured. In
// development, TABLEKIT_MEDIA_DIR selects a filesystem backend.
func New(cfg *config.Config) Store {
	if cfg.MediaDir != "" {
		return NewFS(cfg.MediaDir)
	}
	if !cfg.StorageEnabled() {
		return Disabled{}
	}
	if store := newS3(cfg.Storage); store != nil {
		return store
	}
	return Disabled{}
}

// Disabled is a Store that always reports ErrNotConfigured.
type Disabled struct{}

func (Disabled) Put(context.Context, string, string, io.Reader, int64) (Object, error) {
	return Object{}, ErrNotConfigured
}

func (Disabled) Remove(context.Context, string) error { return ErrNotConfigured }

func (Disabled) URL(string) string { return "" }
