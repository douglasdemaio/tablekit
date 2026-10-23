-- name: ListMenuCategories :many
SELECT id, merchant_id, name, description, sort_order, active, created_at, updated_at, deleted_at
FROM menu_categories
WHERE deleted_at IS NULL
ORDER BY sort_order ASC, name ASC;

-- name: ListMenuItems :many
SELECT id, merchant_id, category_id, name, description, price_minor, currency, tax_rate_bp,
       image_asset_id, active, available, sold_out_until, sold_out_reason, sort_order, created_at, updated_at, deleted_at
FROM menu_items
WHERE deleted_at IS NULL
ORDER BY sort_order ASC, name ASC;

-- name: CreateMenuCategory :one
INSERT INTO menu_categories (merchant_id, name, sort_order)
VALUES ($1, $2, $3)
RETURNING id, name;

-- name: CreateMenuItem :one
INSERT INTO menu_items (merchant_id, category_id, name, price_minor, currency)
VALUES ($1, $2, $3, $4, 'EUR')
RETURNING id;

-- name: UpdateMenuItem :exec
UPDATE menu_items
SET name = $1, price_minor = $2, updated_at = now()
WHERE id = $3;

-- name: GetMenuItem :one
SELECT * FROM menu_items
WHERE id = $1::uuid
  AND deleted_at IS NULL;

-- name: SetMenuItemImage :exec
UPDATE menu_items
SET image_asset_id = $1, updated_at = now()
WHERE id = $2;

-- name: SetMenuItemAvailability :exec
UPDATE menu_items
SET available = $1, updated_at = now()
WHERE id = $2;

-- name: SetMenuItemSoldOut :exec
UPDATE menu_items
SET sold_out_until = $1, sold_out_reason = $2, updated_at = now()
WHERE id = $3;


-- name: SoftDeleteMenuItem :exec
UPDATE menu_items
SET deleted_at = now(), updated_at = now()
WHERE id = $1;

-- name: CountMenuItems :one
SELECT count(*) FROM menu_items WHERE deleted_at IS NULL;
