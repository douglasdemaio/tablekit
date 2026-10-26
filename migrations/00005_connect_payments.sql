-- +goose Up
-- +goose StatementBegin

ALTER TABLE payment_connections
    DROP COLUMN IF EXISTS publishable_key,
    DROP COLUMN IF EXISTS secret_key_enc,
    DROP COLUMN IF EXISTS webhook_secret_enc;

CREATE UNIQUE INDEX payment_connections_connected_account_idx
    ON payment_connections (connected_account_id)
    WHERE connected_account_id IS NOT NULL;

CREATE OR REPLACE FUNCTION resolve_merchant_by_payment_account(p_account text)
RETURNS TABLE (id uuid, slug text, name text, status text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT m.id, m.slug, m.name, pc.status
    FROM public.payment_connections pc
    JOIN public.merchants m ON m.id = pc.merchant_id
    WHERE pc.provider = 'stripe'
      AND pc.connected_account_id = p_account
      AND m.deleted_at IS NULL
    LIMIT 1
$$;

REVOKE ALL ON FUNCTION resolve_merchant_by_payment_account(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_merchant_by_payment_account(text) TO tablekit_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP FUNCTION IF EXISTS resolve_merchant_by_payment_account(text);
DROP INDEX IF EXISTS payment_connections_connected_account_idx;

ALTER TABLE payment_connections
    ADD COLUMN publishable_key text,
    ADD COLUMN secret_key_enc bytea,
    ADD COLUMN webhook_secret_enc bytea;

-- +goose StatementEnd
