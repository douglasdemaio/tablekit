-- +goose Up
-- +goose StatementBegin

-- Customer accounts are platform-level: one login works on every restaurant
-- subdomain. They are deliberately outside tenant RLS and are only accessed
-- through the authenticated customer session.
CREATE TABLE customer_accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL CHECK (position('@' IN email) > 1 AND length(email) <= 254),
    name text NOT NULL DEFAULT '' CHECK (length(name) <= 120),
    phone text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
    email_verified_at timestamptz,
    last_login_at timestamptz,
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX customer_accounts_email_idx ON customer_accounts (lower(email));

CREATE TABLE customer_addresses (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES customer_accounts(id) ON DELETE CASCADE,
    label text NOT NULL DEFAULT '' CHECK (length(label) <= 60),
    recipient text NOT NULL DEFAULT '' CHECK (length(recipient) <= 120),
    phone text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
    line1 text NOT NULL CHECK (length(line1) BETWEEN 1 AND 200),
    line2 text NOT NULL DEFAULT '' CHECK (length(line2) <= 200),
    postcode text NOT NULL CHECK (length(postcode) BETWEEN 1 AND 12),
    city text NOT NULL CHECK (length(city) BETWEEN 1 AND 120),
    country text NOT NULL DEFAULT 'DE' CHECK (length(country) BETWEEN 2 AND 2),
    delivery_notes text NOT NULL DEFAULT '' CHECK (length(delivery_notes) <= 500),
    is_default boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX customer_addresses_customer_idx ON customer_addresses (customer_id, created_at);
CREATE UNIQUE INDEX customer_addresses_one_default
    ON customer_addresses (customer_id) WHERE is_default;

CREATE TABLE customer_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES customer_accounts(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    ip inet,
    user_agent text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX customer_sessions_customer_idx ON customer_sessions (customer_id);
CREATE INDEX customer_sessions_expiry_idx ON customer_sessions (expires_at);

CREATE TABLE customer_login_codes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL,
    code_hash text NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    ip inet,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX customer_login_codes_email_idx ON customer_login_codes (lower(email), created_at DESC);

CREATE TABLE customer_shares (
    customer_id uuid NOT NULL REFERENCES customer_accounts(id) ON DELETE CASCADE,
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (customer_id, merchant_id)
);
CREATE INDEX customer_shares_merchant_idx ON customer_shares (merchant_id);

ALTER TABLE orders
    ADD COLUMN customer_account_id uuid REFERENCES customer_accounts(id) ON DELETE SET NULL;
CREATE INDEX orders_customer_account_idx ON orders (merchant_id, customer_account_id, created_at DESC);

ALTER TABLE deliveries
    ADD COLUMN assignee_staff_id uuid REFERENCES staff_users(id) ON DELETE SET NULL;
CREATE INDEX deliveries_assignee_idx ON deliveries (merchant_id, assignee_staff_id);

ALTER TABLE menu_items
    ADD COLUMN sold_out_until timestamptz,
    ADD COLUMN sold_out_reason text NOT NULL DEFAULT '';
CREATE INDEX menu_items_sold_out_idx ON menu_items (merchant_id, sold_out_until);

ALTER TABLE merchants
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN phone text NOT NULL DEFAULT '',
    ADD COLUMN email text NOT NULL DEFAULT '',
    ADD COLUMN street text NOT NULL DEFAULT '',
    ADD COLUMN line2 text NOT NULL DEFAULT '',
    ADD COLUMN postcode text NOT NULL DEFAULT '',
    ADD COLUMN city text NOT NULL DEFAULT '',
    ADD COLUMN country text NOT NULL DEFAULT 'DE',
    ADD COLUMN cuisine text NOT NULL DEFAULT '',
    ADD COLUMN pickup_enabled boolean NOT NULL DEFAULT true,
    ADD COLUMN self_delivery_enabled boolean NOT NULL DEFAULT true;

ALTER TABLE brand_settings
    ADD COLUMN template text NOT NULL DEFAULT 'classic-bistro',
    ADD COLUMN accent_color text NOT NULL DEFAULT '#f59e0b',
    ADD COLUMN background_color text NOT NULL DEFAULT '#ffffff',
    ADD COLUMN text_color text NOT NULL DEFAULT '#111111';

ALTER TABLE payment_connections
    ADD COLUMN publishable_key text,
    ADD COLUMN secret_key_enc bytea,
    ADD COLUMN webhook_secret_enc bytea,
    ADD COLUMN livemode boolean NOT NULL DEFAULT false;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    customer_accounts, customer_addresses, customer_sessions,
    customer_login_codes, customer_shares
TO tablekit_app;

GRANT SELECT, INSERT, UPDATE ON webhook_events TO tablekit_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE payment_connections
    DROP COLUMN IF EXISTS livemode,
    DROP COLUMN IF EXISTS webhook_secret_enc,
    DROP COLUMN IF EXISTS secret_key_enc,
    DROP COLUMN IF EXISTS publishable_key;
ALTER TABLE brand_settings
    DROP COLUMN IF EXISTS text_color,
    DROP COLUMN IF EXISTS background_color,
    DROP COLUMN IF EXISTS accent_color,
    DROP COLUMN IF EXISTS template;
ALTER TABLE merchants
    DROP COLUMN IF EXISTS self_delivery_enabled,
    DROP COLUMN IF EXISTS pickup_enabled,
    DROP COLUMN IF EXISTS cuisine,
    DROP COLUMN IF EXISTS country,
    DROP COLUMN IF EXISTS city,
    DROP COLUMN IF EXISTS postcode,
    DROP COLUMN IF EXISTS line2,
    DROP COLUMN IF EXISTS street,
    DROP COLUMN IF EXISTS email,
    DROP COLUMN IF EXISTS phone,
    DROP COLUMN IF EXISTS description;
ALTER TABLE menu_items
    DROP COLUMN IF EXISTS sold_out_reason,
    DROP COLUMN IF EXISTS sold_out_until;
ALTER TABLE deliveries DROP COLUMN IF EXISTS assignee_staff_id;
ALTER TABLE orders DROP COLUMN IF EXISTS customer_account_id;
DROP TABLE IF EXISTS customer_shares;
DROP TABLE IF EXISTS customer_login_codes;
DROP TABLE IF EXISTS customer_sessions;
DROP TABLE IF EXISTS customer_addresses;
DROP TABLE IF EXISTS customer_accounts;
-- +goose StatementEnd
