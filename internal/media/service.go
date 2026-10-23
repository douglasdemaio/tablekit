// Package media handles image uploads for menu items and branding. Uploads are
// validated, decoded (which both verifies the format and strips metadata) and
// re-encoded to normalized JPEGs before being stored as immutable objects.
package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"

	"github.com/google/uuid"
	xdraw "golang.org/x/image/draw"

	// Decoders registered for image.Decode.
	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/db/sqlcgen"
	"tablekit.de/tablekit/internal/storage"
)

const (
	// MaxUploadBytes caps the raw upload size.
	MaxUploadBytes = 8 << 20
	// MaxDimension guards against decompression bombs.
	MaxDimension = 12000
	// ThumbWidth is the width of the generated preview.
	ThumbWidth  = 480
	JPEGQuality = 85
)

var (
	ErrTooLarge    = errors.New("media: file too large")
	ErrUnsupported = errors.New("media: unsupported image format")
	ErrCorrupt     = errors.New("media: corrupt or unreadable image")
)

// Service stores and records media assets.
type Service struct {
	store *db.Store
	blob  storage.Store
}

func NewService(store *db.Store, blob storage.Store) *Service {
	return &Service{store: store, blob: blob}
}

// Configured reports whether an object store is available.
func (s *Service) Configured() bool {
	_, disabled := s.blob.(storage.Disabled)
	return !disabled
}

// Asset is a stored media asset.
type Asset struct {
	ID        uuid.UUID
	ObjectKey string
	URL       string
	Width     int
	Height    int
}

// Upload validates, normalizes and stores an image, recording a media_assets row.
func (s *Service) Upload(ctx context.Context, merchantID, staffID uuid.UUID, kind sqlcgen.MediaKindEnum, r io.Reader) (Asset, error) {
	limited := io.LimitReader(r, MaxUploadBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return Asset{}, fmt.Errorf("read upload: %w", err)
	}
	if len(raw) > MaxUploadBytes {
		return Asset{}, ErrTooLarge
	}

	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Asset{}, ErrUnsupported
	}
	bounds := src.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 || bounds.Dx() > MaxDimension || bounds.Dy() > MaxDimension {
		return Asset{}, ErrCorrupt
	}

	var main bytes.Buffer
	if err := jpeg.Encode(&main, src, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return Asset{}, fmt.Errorf("encode image: %w", err)
	}

	thumb := resize(src, ThumbWidth)
	var thumbBuf bytes.Buffer
	if err := jpeg.Encode(&thumbBuf, thumb, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return Asset{}, fmt.Errorf("encode thumbnail: %w", err)
	}

	sum := sha256.Sum256(main.Bytes())
	key := fmt.Sprintf("merchants/%s/%s/%s.jpg", merchantID, kind, uuid.NewString())

	if _, err := s.blob.Put(ctx, key, "image/jpeg", bytes.NewReader(main.Bytes()), int64(main.Len())); err != nil {
		return Asset{}, err
	}
	if _, err := s.blob.Put(ctx, thumbKey(key), "image/jpeg", bytes.NewReader(thumbBuf.Bytes()), int64(thumbBuf.Len())); err != nil {
		return Asset{}, err
	}

	asset := Asset{
		ObjectKey: key,
		URL:       s.blob.URL(key),
		Width:     bounds.Dx(),
		Height:    bounds.Dy(),
	}
	err = s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		if bounds.Dx() > math.MaxInt32 || bounds.Dy() > math.MaxInt32 {
			return fmt.Errorf("image dimensions too large")
		}
		row, err := q.CreateMediaAsset(ctx, sqlcgen.CreateMediaAssetParams{
			Column1: merchantID,
			Column2: kind,
			Column3: key,
			Column4: "image/jpeg",
			Column5: int32(bounds.Dx()), // #nosec G115 -- checked above
			Column6: int32(bounds.Dy()), // #nosec G115 -- checked above
			Column7: int64(main.Len()),
			Column8: sum[:],
			Column9: staffID,
		})
		if err != nil {
			return err
		}
		asset.ID = row.ID
		return nil
	})
	if err != nil {
		_ = s.blob.Remove(ctx, key)
		_ = s.blob.Remove(ctx, thumbKey(key))
		return Asset{}, err
	}
	return asset, nil
}

// Delete soft-deletes an asset record and removes its stored objects.
func (s *Service) Delete(ctx context.Context, merchantID, assetID uuid.UUID) error {
	var key string
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		asset, err := q.GetMediaAsset(ctx, assetID)
		if err != nil {
			return err
		}
		key = asset.ObjectKey
		return q.SoftDeleteMediaAsset(ctx, assetID)
	})
	if err != nil {
		return err
	}
	_ = s.blob.Remove(ctx, key)
	_ = s.blob.Remove(ctx, thumbKey(key))
	return nil
}

// URL returns the public URL for a stored asset key.
func (s *Service) URL(objectKey string) string { return s.blob.URL(objectKey) }

// ThumbURL returns the preview URL for a stored asset key.
func (s *Service) ThumbURL(objectKey string) string { return s.blob.URL(thumbKey(objectKey)) }

// ObjectKey looks up the stored object key for an asset.
func (s *Service) ObjectKey(ctx context.Context, merchantID, assetID uuid.UUID) (string, error) {
	var key string
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		asset, err := q.GetMediaAsset(ctx, assetID)
		if err != nil {
			return err
		}
		key = asset.ObjectKey
		return nil
	})
	return key, err
}

func thumbKey(key string) string {
	return key + "_thumb.jpg"
}

func resize(src image.Image, maxWidth int) image.Image {
	b := src.Bounds()
	if b.Dx() <= maxWidth {
		return src
	}
	ratio := float64(maxWidth) / float64(b.Dx())
	dstW := maxWidth
	dstH := int(float64(b.Dy()) * ratio)
	if dstH < 1 {
		dstH = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}
