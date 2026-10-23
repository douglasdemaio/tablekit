-- name: GetStaffByEmail :one
SELECT * FROM staff_users
WHERE lower(email) = lower($1::text)
  AND disabled_at IS NULL;

-- name: GetStaffByID :one
SELECT * FROM staff_users
WHERE id = $1::uuid;

-- name: ListStaff :many
SELECT * FROM staff_users
ORDER BY created_at;

-- name: CreateStaff :one
INSERT INTO staff_users (merchant_id, role, email, name, password_hash, mfa_required)
VALUES ($1::uuid, $2, lower($3::text), $4::text, $5, $6::boolean)
RETURNING *;

-- name: UpdateStaffPassword :exec
UPDATE staff_users
SET password_hash = $1
WHERE id = $2::uuid;

-- name: SetStaffTOTPSecret :exec
UPDATE staff_users
SET totp_secret_enc = $1,
    totp_enabled = false
WHERE id = $2::uuid;

-- name: EnableStaffTOTP :exec
UPDATE staff_users
SET totp_enabled = true
WHERE id = $1::uuid;

-- name: DisableStaffTOTP :exec
UPDATE staff_users
SET totp_enabled = false,
    totp_secret_enc = NULL
WHERE id = $1::uuid;

-- name: DisableStaff :exec
UPDATE staff_users
SET disabled_at = now()
WHERE id = $1::uuid;

-- name: CreateSession :one
INSERT INTO sessions (
    merchant_id, staff_id, token_hash, csrf_token_hash, state, ip, user_agent, expires_at
) VALUES (
    $1::uuid, $2::uuid, $3::bytea, $4::bytea, $5::text, $6, $7::text, $8::timestamptz
)
RETURNING *;

-- name: GetSessionByTokenHash :one
SELECT * FROM sessions
WHERE token_hash = $1::bytea
  AND revoked_at IS NULL
  AND expires_at > now();

-- name: GetSessionByID :one
SELECT * FROM sessions
WHERE id = $1::uuid
  AND revoked_at IS NULL;

-- name: SetSessionState :exec
UPDATE sessions
SET state = $1::text,
    expires_at = $2::timestamptz
WHERE id = $3::uuid;

-- name: TouchSession :exec
UPDATE sessions
SET last_seen_at = now()
WHERE id = $1::uuid;

-- name: ListSessionsForStaff :many
SELECT id, state, COALESCE(host(ip)::text, '') AS ip, user_agent, created_at, last_seen_at, expires_at
FROM sessions
WHERE staff_id = $1::uuid
  AND revoked_at IS NULL
  AND expires_at > now()
ORDER BY last_seen_at DESC;

-- name: RevokeSession :exec
UPDATE sessions
SET revoked_at = now()
WHERE id = $1::uuid
  AND revoked_at IS NULL;

-- name: RevokeOtherSessions :exec
UPDATE sessions
SET revoked_at = now()
WHERE staff_id = $1::uuid
  AND id <> $2::uuid
  AND revoked_at IS NULL;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions
WHERE expires_at < now() - interval '7 days';

-- name: CreatePasskey :one
INSERT INTO passkeys (merchant_id, staff_id, credential_id, credential, name)
VALUES ($1::uuid, $2::uuid, $3::bytea, $4::jsonb, $5::text)
RETURNING *;

-- name: ListPasskeysForStaff :many
SELECT id, name, created_at, last_used_at
FROM passkeys
WHERE staff_id = $1::uuid
ORDER BY created_at;

-- name: GetPasskeyByCredentialID :one
SELECT * FROM passkeys
WHERE credential_id = $1::bytea;

-- name: TouchPasskey :exec
UPDATE passkeys
SET last_used_at = now()
WHERE id = $1::uuid;

-- name: DeletePasskey :exec
DELETE FROM passkeys
WHERE id = $1::uuid
  AND staff_id = $2::uuid;

-- name: InsertAuditLog :exec
INSERT INTO audit_log (merchant_id, actor_type, actor_id, action, target_type, target_id, ip, metadata)
VALUES ($1::uuid, $2::text, $3, $4::text, $5::text, $6::text, $7, $8::jsonb);

-- name: SetStaffTOTPStep :exec
UPDATE staff_users
SET totp_last_step = $1::bigint
WHERE id = $2::uuid;

-- name: RotateSession :exec
UPDATE sessions
SET token_hash = $1::bytea,
    csrf_token_hash = $2::bytea,
    state = 'verified',
    expires_at = $3::timestamptz
WHERE id = $4::uuid;

-- name: UpdatePasskeyCredential :exec
UPDATE passkeys
SET credential = $1::jsonb,
    last_used_at = now()
WHERE id = $2::uuid;

-- name: CreateWebAuthnChallenge :one
INSERT INTO webauthn_challenges (merchant_id, staff_id, kind, data, expires_at)
VALUES ($1::uuid, $2::uuid, $3::text, $4::jsonb, $5::timestamptz)
RETURNING id, merchant_id, staff_id, kind, data, expires_at, created_at;

-- name: GetWebAuthnChallenge :one
SELECT id, merchant_id, staff_id, kind, data, expires_at, created_at
FROM webauthn_challenges
WHERE id = $1::uuid
  AND kind = $2::text
  AND expires_at > now();

-- name: DeleteWebAuthnChallenge :exec
DELETE FROM webauthn_challenges
WHERE id = $1::uuid;

-- name: DeleteExpiredWebAuthnChallenges :exec
DELETE FROM webauthn_challenges
WHERE expires_at < now() - interval '1 hour';

-- name: ListPasskeyCredentialsForStaff :many
SELECT * FROM passkeys
WHERE staff_id = $1::uuid
ORDER BY created_at;

-- name: RevokeOwnSession :exec
UPDATE sessions
SET revoked_at = now()
WHERE id = $1::uuid
  AND staff_id = $2::uuid
  AND revoked_at IS NULL;

-- name: UpdateStaffAccess :exec
UPDATE staff_users
SET name = $1::text, role = $2::staff_role, mfa_required = $3::boolean, updated_at = now()
WHERE id = $4::uuid;

-- name: EnableStaff :exec
UPDATE staff_users
SET disabled_at = NULL, updated_at = now()
WHERE id = $1::uuid;

-- name: RevokeAllStaffSessions :exec
UPDATE sessions
SET revoked_at = now()
WHERE staff_id = $1::uuid
  AND revoked_at IS NULL;

-- name: ListActiveSessionsForMerchant :many
SELECT s.id, s.staff_id, s.state, COALESCE(s.ip::text, '') AS ip_text, s.user_agent,
       s.created_at, s.last_seen_at, s.expires_at,
       u.name AS staff_name, u.email AS staff_email, u.role AS staff_role
FROM sessions s
JOIN staff_users u ON u.id = s.staff_id
WHERE s.revoked_at IS NULL
  AND s.expires_at > now()
ORDER BY s.last_seen_at DESC;
