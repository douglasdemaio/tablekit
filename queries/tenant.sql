-- name: GetCurrentMerchant :one
SELECT id, name, slug, legal_name, timezone, locale, currency, created_at, updated_at, deleted_at FROM merchants;

-- name: UpdateMerchantProfile :exec
UPDATE merchants
SET name = $1,
    legal_name = $2,
    timezone = $3,
    locale = $4
WHERE id = $5;

-- name: GetMerchantFull :one
SELECT id, name, slug, legal_name, timezone, locale, currency, vat_rate_bp,
       description, phone, email, street, line2, postcode, city, country, cuisine,
       pickup_enabled, self_delivery_enabled
FROM merchants
WHERE id = $1::uuid
  AND deleted_at IS NULL;

-- name: UpdateMerchantFull :exec
UPDATE merchants
SET name = sqlc.arg(name),
    legal_name = sqlc.arg(legal_name),
    timezone = sqlc.arg(timezone),
    locale = sqlc.arg(locale),
    vat_rate_bp = sqlc.arg(vat_rate_bp),
    description = sqlc.arg(description),
    phone = sqlc.arg(phone),
    email = sqlc.arg(email),
    street = sqlc.arg(street),
    line2 = sqlc.arg(line2),
    postcode = sqlc.arg(postcode),
    city = sqlc.arg(city),
    country = sqlc.arg(country),
    cuisine = sqlc.arg(cuisine),
    pickup_enabled = sqlc.arg(pickup_enabled),
    self_delivery_enabled = sqlc.arg(self_delivery_enabled),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: GetBrandSettings :one
SELECT merchant_id, logo_asset_id, cover_asset_id, primary_color, secondary_color,
       font, tagline, template, accent_color, background_color, text_color
FROM brand_settings
WHERE merchant_id = $1::uuid;

-- name: UpdateBrandSettings :exec
INSERT INTO brand_settings (
    merchant_id, primary_color, secondary_color, font, tagline, template,
    accent_color, background_color, text_color
) VALUES (
    sqlc.arg(merchant_id), sqlc.arg(primary_color), sqlc.arg(secondary_color),
    sqlc.arg(font), sqlc.arg(tagline), sqlc.arg(template), sqlc.arg(accent_color),
    sqlc.arg(background_color), sqlc.arg(text_color)
)
ON CONFLICT (merchant_id) DO UPDATE SET
    primary_color = EXCLUDED.primary_color,
    secondary_color = EXCLUDED.secondary_color,
    font = EXCLUDED.font,
    tagline = EXCLUDED.tagline,
    template = EXCLUDED.template,
    accent_color = EXCLUDED.accent_color,
    background_color = EXCLUDED.background_color,
    text_color = EXCLUDED.text_color,
    updated_at = now();

-- name: SetBrandLogo :exec
UPDATE brand_settings SET logo_asset_id = $1, updated_at = now() WHERE merchant_id = $2::uuid;

-- name: SetBrandCover :exec
UPDATE brand_settings SET cover_asset_id = $1, updated_at = now() WHERE merchant_id = $2::uuid;

-- name: GetPaymentConnection :one
SELECT id, merchant_id, provider, connected_account_id, status, capabilities, livemode
FROM payment_connections
WHERE merchant_id = $1::uuid
  AND provider = 'stripe';

-- name: UpsertPaymentConnection :exec
INSERT INTO payment_connections (
    merchant_id, provider, connected_account_id, status, capabilities, livemode
) VALUES (
    sqlc.arg(merchant_id), 'stripe', sqlc.arg(connected_account_id)::text,
    sqlc.arg(status), sqlc.arg(capabilities)::jsonb, sqlc.arg(livemode)
)
ON CONFLICT (merchant_id, provider) DO UPDATE SET
    connected_account_id = EXCLUDED.connected_account_id,
    status = EXCLUDED.status,
    capabilities = EXCLUDED.capabilities,
    livemode = EXCLUDED.livemode,
    updated_at = now();

-- name: CreatePaymentRecord :exec
INSERT INTO payment_records (merchant_id, order_id, provider, provider_ref, kind, amount_minor, currency, status)
VALUES ($1::uuid, $2::uuid, 'stripe', $3::text, 'payment', $4, $5::char(3), 'pending')
ON CONFLICT DO NOTHING;

-- name: GetPaymentRecordByRef :one
SELECT id, merchant_id, order_id, provider, provider_ref, kind, amount_minor,
       currency, status, created_at
FROM payment_records
WHERE merchant_id = $1::uuid
  AND provider = 'stripe'
  AND provider_ref = $2::text;

-- name: MarkPaymentRecordPaid :exec
UPDATE payment_records
SET status = 'paid', updated_at = now()
WHERE id = $1::uuid;

-- name: MarkPaymentRecordFailed :exec
UPDATE payment_records
SET status = 'failed', updated_at = now()
WHERE id = $1::uuid;

-- name: MarkOrderPaymentPaid :one
UPDATE orders
SET payment_status = 'paid',
    status = CASE WHEN status = 'pending_payment' THEN 'submitted'::order_status ELSE status END,
    updated_at = now()
WHERE id = $1::uuid
RETURNING id, public_ref, status, payment_status;

-- name: MarkOrderPaymentFailed :exec
UPDATE orders
SET payment_status = 'failed',
    status = 'payment_failed'::order_status,
    updated_at = now()
WHERE id = $1::uuid
  AND payment_status <> 'paid';

-- name: InsertWebhookEvent :one
INSERT INTO webhook_events (provider, provider_event_id, event_type, payload, status)
VALUES ('stripe', $1::text, $2::text, $3::jsonb, 'received')
ON CONFLICT (provider, provider_event_id) DO NOTHING
RETURNING id;

-- name: MarkWebhookEvent :exec
UPDATE webhook_events
SET status = $1, error = $2, processed_at = now()
WHERE id = $3::bigint;
