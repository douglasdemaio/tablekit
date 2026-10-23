-- +goose Up
-- New guest orders wait for staff to accept them. Adding the enum value is
-- separate from using it so the value is committed before any row needs it.
ALTER TYPE order_status ADD VALUE IF NOT EXISTS 'submitted' BEFORE 'accepted';

-- Single VAT rate per merchant; menu prices are gross and include it.
ALTER TABLE merchants
    ADD COLUMN vat_rate_bp integer NOT NULL DEFAULT 1900
    CHECK (vat_rate_bp BETWEEN 0 AND 10000);

-- +goose Down
ALTER TABLE merchants DROP COLUMN IF EXISTS vat_rate_bp;
-- 'submitted' stays in the enum: PostgreSQL cannot drop an enum value, and it
-- is harmless on a rollback.
