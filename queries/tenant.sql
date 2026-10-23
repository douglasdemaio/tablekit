-- name: GetCurrentMerchant :one
SELECT id, name, slug, legal_name, timezone, locale, currency, created_at, updated_at, deleted_at FROM merchants;

-- name: UpdateMerchantProfile :exec
UPDATE merchants
SET name = $1,
    legal_name = $2,
    timezone = $3,
    locale = $4
WHERE id = $5;
