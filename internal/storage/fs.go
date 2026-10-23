package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FS is a filesystem-backed store for local development. Objects are served by
// the application under /media/.
type FS struct {
	dir string
}

// NewFS creates a filesystem store rooted at dir.
func NewFS(dir string) *FS { return &FS{dir: dir} }

func (f *FS) path(key string) (string, error) {
	clean := filepath.Clean("/" + key)
	if strings.Contains(clean, "..") {
		return "", fmt.Errorf("storage: invalid key %q", key)
	}
	return filepath.Join(f.dir, clean), nil
}

func (f *FS) Put(_ context.Context, key, _ string, body io.Reader, _ int64) (Object, error) {
	p, err := f.path(key)
	if err != nil {
		return Object{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return Object{}, fmt.Errorf("storage: mkdir: %w", err)
	}
	tmp := p + ".tmp"
	file, err := os.Create(tmp) // #nosec G304 -- key is server-generated and cleaned by path()
	if err != nil {
		return Object{}, fmt.Errorf("storage: create: %w", err)
	}
	if _, err := io.Copy(file, body); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return Object{}, fmt.Errorf("storage: write: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return Object{}, err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return Object{}, fmt.Errorf("storage: rename: %w", err)
	}
	return Object{Key: key, URL: f.URL(key)}, nil
}

func (f *FS) Remove(_ context.Context, key string) error {
	p, err := f.path(key)
	if err != nil {
		return err
	}
	_ = os.Remove(p)
	return nil
}

func (f *FS) URL(key string) string { return "/media/" + strings.TrimPrefix(key, "/") }

func (f *FS) Dir() string { return f.dir }
