// Package menu contains staff-facing menu management: categories, items and
// availability. Prices are stored in minor units (cents) and never trusted
// from the client at order time.
package menu

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/db/sqlcgen"
)

var (
	ErrNotFound     = errors.New("menu: not found")
	ErrInvalidInput = errors.New("menu: invalid input")
)

type Service struct {
	store *db.Store
}

func NewService(store *db.Store) *Service { return &Service{store: store} }

type Category struct {
	Category sqlcgen.MenuCategory
	Items    []sqlcgen.MenuItem
}

func (s *Service) List(ctx context.Context, merchantID uuid.UUID) ([]Category, error) {
	var out []Category
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		categories, err := q.ListMenuCategories(ctx)
		if err != nil {
			return err
		}
		items, err := q.ListMenuItems(ctx)
		if err != nil {
			return err
		}
		byCategory := make(map[uuid.UUID][]sqlcgen.MenuItem)
		for _, it := range items {
			byCategory[it.CategoryID] = append(byCategory[it.CategoryID], it)
		}
		for _, c := range categories {
			out = append(out, Category{Category: c, Items: byCategory[c.ID]})
		}
		return nil
	})
	return out, err
}

func (s *Service) Count(ctx context.Context, merchantID uuid.UUID) (int64, error) {
	var n int64
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		var err error
		n, err = q.CountMenuItems(ctx)
		return err
	})
	return n, err
}

func (s *Service) CreateCategory(ctx context.Context, merchantID uuid.UUID, name string) error {
	if name == "" {
		return ErrInvalidInput
	}
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		_, err := q.CreateMenuCategory(ctx, sqlcgen.CreateMenuCategoryParams{MerchantID: merchantID, Name: name})
		return err
	})
}

func (s *Service) CreateItem(ctx context.Context, merchantID, categoryID uuid.UUID, name string, priceMinor int32) error {
	if name == "" || priceMinor < 0 {
		return ErrInvalidInput
	}
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		_, err := q.CreateMenuItem(ctx, sqlcgen.CreateMenuItemParams{
			MerchantID: merchantID,
			CategoryID: categoryID,
			Name:       name,
			PriceMinor: priceMinor,
		})
		return err
	})
}

func (s *Service) UpdateItem(ctx context.Context, merchantID, itemID uuid.UUID, name string, priceMinor int32) error {
	if name == "" || priceMinor < 0 {
		return ErrInvalidInput
	}
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.UpdateMenuItem(ctx, sqlcgen.UpdateMenuItemParams{Name: name, PriceMinor: priceMinor, ID: itemID})
	})
}

func (s *Service) SetAvailability(ctx context.Context, merchantID, itemID uuid.UUID, available bool) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.SetMenuItemAvailability(ctx, sqlcgen.SetMenuItemAvailabilityParams{Available: available, ID: itemID})
	})
}

func (s *Service) DeleteItem(ctx context.Context, merchantID, itemID uuid.UUID) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.SoftDeleteMenuItem(ctx, itemID)
	})
}

// SetImage attaches a media asset to a menu item; a nil assetID clears it.
func (s *Service) SetImage(ctx context.Context, merchantID, itemID uuid.UUID, assetID *uuid.UUID) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.SetMenuItemImage(ctx, sqlcgen.SetMenuItemImageParams{ImageAssetID: assetID, ID: itemID})
	})
}

// Item returns a single menu item.
func (s *Service) Item(ctx context.Context, merchantID, itemID uuid.UUID) (sqlcgen.MenuItem, error) {
	var out sqlcgen.MenuItem
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		item, err := q.GetMenuItem(ctx, itemID)
		if err != nil {
			return err
		}
		out = item
		return nil
	})
	return out, err
}
