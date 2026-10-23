-- name: CreateMediaAsset :one
INSERT INTO media_assets (merchant_id, kind, object_key, content_type, width, height, size_bytes, checksum, created_by)
VALUES ($1::uuid, $2::media_kind, $3::text, $4::text, $5::integer, $6::integer, $7::bigint, $8::bytea, $9::uuid)
RETURNING id, object_key;

-- name: GetMediaAsset :one
SELECT * FROM media_assets
WHERE id = $1::uuid
  AND deleted_at IS NULL;

-- name: SoftDeleteMediaAsset :exec
UPDATE media_assets
SET deleted_at = now()
WHERE id = $1::uuid;
