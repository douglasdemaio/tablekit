-- name: GetStorefrontSettings :one
SELECT m.id, m.name, m.slug, m.currency, m.timezone, m.vat_rate_bp,
       COALESCE(bs.tagline, '')::text AS tagline,
       COALESCE(bs.primary_color, '#1f2937')::text AS primary_color,
       COALESCE(bs.secondary_color, '#f59e0b')::text AS secondary_color
FROM merchants m
LEFT JOIN brand_settings bs ON bs.merchant_id = m.id
WHERE m.id = $1::uuid
  AND m.deleted_at IS NULL;

-- name: ListOpeningHours :many
SELECT weekday, opens, closes
FROM opening_hours
ORDER BY weekday, opens;

-- name: DeleteOpeningHours :exec
DELETE FROM opening_hours;

-- name: InsertOpeningHour :exec
INSERT INTO opening_hours (merchant_id, weekday, opens, closes)
VALUES ($1::uuid, $2::smallint, $3::time, $4::time);

-- name: ListStorefrontCategories :many
SELECT id, name, description, sort_order
FROM menu_categories
WHERE deleted_at IS NULL
  AND active
ORDER BY sort_order ASC, name ASC;

-- name: ListStorefrontItems :many
SELECT mi.id, mi.category_id, mi.name, mi.description, mi.price_minor, mi.currency,
       mi.available, mi.sort_order, COALESCE(ma.object_key, '')::text AS image_key
FROM menu_items mi
LEFT JOIN media_assets ma ON ma.id = mi.image_asset_id AND ma.deleted_at IS NULL
WHERE mi.deleted_at IS NULL
  AND mi.active
ORDER BY mi.sort_order ASC, mi.name ASC;

-- name: ListDeliveryZones :many
SELECT id, name, postcode_prefixes, min_order_minor, delivery_fee_minor, active
FROM delivery_zones
ORDER BY name ASC;

-- name: CreateDeliveryZone :one
INSERT INTO delivery_zones (merchant_id, name, postcode_prefixes, min_order_minor, delivery_fee_minor)
VALUES ($1::uuid, $2::text, $3::text[], $4::integer, $5::integer)
RETURNING id;

-- name: UpdateDeliveryZone :exec
UPDATE delivery_zones
SET name = $1, postcode_prefixes = $2::text[], min_order_minor = $3,
    delivery_fee_minor = $4, active = $5, updated_at = now()
WHERE id = $6::uuid;

-- name: DeleteDeliveryZone :exec
DELETE FROM delivery_zones
WHERE id = $1::uuid;

-- name: UpdateVatRate :exec
UPDATE merchants
SET vat_rate_bp = $1, updated_at = now()
WHERE id = $2::uuid;

-- name: CreateGuestOrder :one
INSERT INTO orders (merchant_id, public_ref, status, fulfilment, payment_method,
                    payment_status, currency, subtotal_minor, tax_minor,
                    delivery_fee_minor, total_minor, contact_name,
                    delivery_address, notes, placed_at)
VALUES ($1::uuid, $2::text, 'submitted'::order_status, $3::fulfilment_mode,
        'cash', 'unpaid', $4::char(3), $5, $6, $7, $8, $9, $10, $11, now())
RETURNING id, public_ref, created_at;

-- name: InsertGuestOrderItem :exec
INSERT INTO order_items (merchant_id, order_id, menu_item_id, name_snapshot,
                         unit_price_minor, quantity, line_total_minor)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::text, $5::integer, $6::integer, $7::integer);

-- name: InsertGuestOrderEvent :exec
INSERT INTO order_events (merchant_id, order_id, event_type, to_status, actor_type, metadata)
VALUES ($1::uuid, $2::uuid, 'order_placed', 'submitted'::order_status, 'guest', '{}'::jsonb);

-- name: InsertPickupCode :exec
INSERT INTO handoff_codes (merchant_id, order_id, kind, code_hash, salt, expires_at)
VALUES ($1::uuid, $2::uuid, 'pickup', $3::bytea, $4::bytea, $5::timestamptz);

-- name: GetOrderByPublicRef :one
SELECT id, merchant_id, public_ref, customer_id, status, fulfilment, payment_method,
       payment_status, currency, subtotal_minor, tax_minor, delivery_fee_minor, total_minor,
       contact_name, contact_email, contact_phone, delivery_address, notes, scheduled_for,
       placed_at, accepted_at, ready_at, completed_at, cancelled_at, created_at, updated_at
FROM orders
WHERE public_ref = $1;

-- name: GetHandoffCode :one
SELECT id, code_hash, salt, attempts, used_at, expires_at
FROM handoff_codes
WHERE order_id = $1::uuid
  AND kind = 'pickup';

-- name: UseHandoffCode :exec
UPDATE handoff_codes
SET used_at = now()
WHERE id = $1::uuid;

-- name: IncrementHandoffAttempts :exec
UPDATE handoff_codes
SET attempts = attempts + 1
WHERE id = $1::uuid;

-- name: MarkOrderCollected :one
UPDATE orders
SET status = 'completed'::order_status,
    payment_status = 'paid',
    completed_at = now(),
    updated_at = now()
WHERE id = $1::uuid
RETURNING id, status, payment_status;

-- name: InsertStaffOrderEvent :exec
INSERT INTO order_events (merchant_id, order_id, event_type, to_status, actor_type, actor_id, metadata)
VALUES ($1::uuid, $2::uuid, $3::text, $4, 'staff', $5::uuid, $6::jsonb);
