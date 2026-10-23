-- name: GetCustomerByEmail :one
SELECT * FROM customer_accounts
WHERE lower(email) = lower($1::text)
  AND disabled_at IS NULL;

-- name: GetCustomerByID :one
SELECT * FROM customer_accounts
WHERE id = $1::uuid
  AND disabled_at IS NULL;

-- name: CreateCustomer :one
INSERT INTO customer_accounts (email)
VALUES (lower($1::text))
RETURNING *;

-- name: UpdateCustomerProfile :exec
UPDATE customer_accounts
SET name = $1, phone = $2, email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
WHERE id = $3::uuid;

-- name: TouchCustomerLogin :exec
UPDATE customer_accounts
SET last_login_at = now(), email_verified_at = COALESCE(email_verified_at, now()), updated_at = now()
WHERE id = $1::uuid;

-- name: CreateCustomerLoginCode :one
INSERT INTO customer_login_codes (email, code_hash, expires_at, ip)
VALUES (lower($1::text), $2::text, $3::timestamptz, $4)
RETURNING id, created_at;

-- name: GetLatestCustomerLoginCode :one
SELECT * FROM customer_login_codes
WHERE lower(email) = lower($1::text)
  AND used_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: IncrementCustomerLoginCodeAttempts :exec
UPDATE customer_login_codes
SET attempts = attempts + 1
WHERE id = $1::uuid;

-- name: UseCustomerLoginCode :exec
UPDATE customer_login_codes
SET used_at = now()
WHERE id = $1::uuid;

-- name: CountRecentCustomerLoginCodes :one
SELECT count(*) FROM customer_login_codes
WHERE lower(email) = lower($1::text)
  AND created_at > now() - interval '15 minutes';

-- name: CountCustomerLoginCodesForIP :one
SELECT count(*) FROM customer_login_codes
WHERE ip = $1
  AND created_at > now() - interval '15 minutes';

-- name: DeleteExpiredCustomerLoginCodes :exec
DELETE FROM customer_login_codes
WHERE created_at < now() - interval '1 day';

-- name: CreateCustomerSession :one
INSERT INTO customer_sessions (customer_id, token_hash, ip, user_agent, expires_at)
VALUES ($1::uuid, $2::bytea, $3, $4::text, $5::timestamptz)
RETURNING *;

-- name: GetCustomerSessionByTokenHash :one
SELECT * FROM customer_sessions
WHERE token_hash = $1::bytea
  AND revoked_at IS NULL
  AND expires_at > now();

-- name: TouchCustomerSession :exec
UPDATE customer_sessions
SET last_seen_at = now()
WHERE id = $1::uuid;

-- name: RevokeCustomerSessionByToken :exec
UPDATE customer_sessions
SET revoked_at = now()
WHERE token_hash = $1::bytea
  AND revoked_at IS NULL;

-- name: DeleteExpiredCustomerSessions :exec
DELETE FROM customer_sessions
WHERE expires_at < now() - interval '7 days';

-- name: ListCustomerAddresses :many
SELECT * FROM customer_addresses
WHERE customer_id = $1::uuid
ORDER BY is_default DESC, created_at ASC;

-- name: GetCustomerAddress :one
SELECT * FROM customer_addresses
WHERE id = $1::uuid
  AND customer_id = $2::uuid;

-- name: CreateCustomerAddress :one
INSERT INTO customer_addresses (
    customer_id, label, recipient, phone, line1, line2, postcode, city, country,
    delivery_notes, is_default
) VALUES (
    $1::uuid, $2::text, $3::text, $4::text, $5::text, $6::text, $7::text, $8::text,
    $9::text, $10::text, false
)
RETURNING *;

-- name: UpdateCustomerAddress :exec
UPDATE customer_addresses
SET label = $1, recipient = $2, phone = $3, line1 = $4, line2 = $5,
    postcode = $6, city = $7, country = $8, delivery_notes = $9, updated_at = now()
WHERE id = $10::uuid
  AND customer_id = $11::uuid;

-- name: DeleteCustomerAddress :exec
DELETE FROM customer_addresses
WHERE id = $1::uuid
  AND customer_id = $2::uuid;

-- name: ClearDefaultCustomerAddress :exec
UPDATE customer_addresses
SET is_default = false
WHERE customer_id = $1::uuid
  AND is_default;

-- name: SetDefaultCustomerAddress :exec
UPDATE customer_addresses
SET is_default = true
WHERE id = $1::uuid
  AND customer_id = $2::uuid;

-- name: UpsertCustomerShare :exec
INSERT INTO customer_shares (customer_id, merchant_id)
VALUES ($1::uuid, $2::uuid)
ON CONFLICT (customer_id, merchant_id) DO NOTHING;

-- name: DeleteCustomerShare :exec
DELETE FROM customer_shares
WHERE customer_id = $1::uuid
  AND merchant_id = $2::uuid;

-- name: GetCustomerShare :one
SELECT created_at FROM customer_shares
WHERE customer_id = $1::uuid
  AND merchant_id = $2::uuid;

-- name: ListCustomerShares :many
SELECT m.id, m.name, m.slug, cs.created_at
FROM customer_shares cs
JOIN merchants m ON m.id = cs.merchant_id AND m.deleted_at IS NULL
WHERE cs.customer_id = $1::uuid
ORDER BY cs.created_at DESC;

-- name: ListCustomerOrders :many
SELECT id, public_ref, status, fulfilment, payment_method, payment_status, currency,
       total_minor, placed_at, scheduled_for, created_at
FROM orders
WHERE merchant_id = $1::uuid
  AND customer_account_id = $2::uuid
ORDER BY created_at DESC
LIMIT 100;

-- name: GetCustomerOrderByRef :one
SELECT id, merchant_id, public_ref, customer_id, customer_account_id, status, fulfilment,
       payment_method, payment_status, currency, subtotal_minor, tax_minor,
       delivery_fee_minor, total_minor, contact_name, contact_email, contact_phone,
       delivery_address, notes, scheduled_for, placed_at, accepted_at, ready_at,
       completed_at, cancelled_at, created_at, updated_at
FROM orders
WHERE merchant_id = $1::uuid
  AND customer_account_id = $2::uuid
  AND public_ref = $3::text;

-- name: GetSharedCustomerProfile :one
SELECT ca.id, ca.name, ca.phone, ca.email, cs.created_at
FROM customer_shares cs
JOIN customer_accounts ca ON ca.id = cs.customer_id
WHERE cs.customer_id = $1::uuid
  AND cs.merchant_id = $2::uuid;
