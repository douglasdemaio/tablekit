-- +goose Up
-- +goose StatementBegin

CREATE TYPE staff_role AS ENUM ('owner', 'manager', 'kitchen', 'driver');
CREATE TYPE order_status AS ENUM (
    'draft', 'pending_payment', 'paid', 'accepted', 'preparing', 'ready',
    'picked_up', 'out_for_delivery', 'completed', 'cancelled', 'refunded',
    'payment_failed', 'disputed'
);
CREATE TYPE fulfilment_mode AS ENUM ('pickup', 'self_delivery', 'courier');
CREATE TYPE payment_status AS ENUM (
    'unpaid', 'pending', 'paid', 'failed', 'refunded', 'partially_refunded', 'disputed'
);
CREATE TYPE handoff_kind AS ENUM ('pickup', 'delivery');
CREATE TYPE media_kind AS ENUM ('logo', 'cover', 'menu_item');

CREATE TABLE merchants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    legal_name text,
    timezone text NOT NULL DEFAULT 'Europe/Berlin',
    locale text NOT NULL DEFAULT 'de-DE',
    currency char(3) NOT NULL DEFAULT 'EUR' CHECK (currency ~ '^[A-Z]{3}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);

CREATE TABLE merchant_domains (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    host text NOT NULL UNIQUE CHECK (host = lower(host) AND host <> ''),
    is_primary boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX merchant_domains_one_primary ON merchant_domains (merchant_id) WHERE is_primary;
CREATE INDEX merchant_domains_merchant_idx ON merchant_domains (merchant_id);

CREATE TABLE staff_users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    role staff_role NOT NULL,
    email text NOT NULL CHECK (position('@' IN email) > 1),
    name text NOT NULL DEFAULT '',
    password_hash text,
    totp_secret_enc bytea,
    totp_enabled boolean NOT NULL DEFAULT false,
    totp_last_step bigint,
    mfa_required boolean NOT NULL DEFAULT false,
    email_verified_at timestamptz,
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX staff_users_merchant_email_idx ON staff_users (merchant_id, lower(email));
CREATE INDEX staff_users_merchant_idx ON staff_users (merchant_id);

CREATE TABLE sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    staff_id uuid NOT NULL REFERENCES staff_users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    csrf_token_hash bytea NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'pending_setup', 'verified')),
    ip inet,
    user_agent text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE INDEX sessions_staff_idx ON sessions (merchant_id, staff_id);
CREATE INDEX sessions_expiry_idx ON sessions (expires_at);

CREATE TABLE passkeys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    staff_id uuid NOT NULL REFERENCES staff_users(id) ON DELETE CASCADE,
    credential_id bytea NOT NULL UNIQUE,
    credential jsonb NOT NULL,
    name text NOT NULL DEFAULT 'Passkey',
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);
CREATE INDEX passkeys_staff_idx ON passkeys (merchant_id, staff_id);

CREATE TABLE webauthn_challenges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    staff_id uuid NOT NULL REFERENCES staff_users(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('register', 'login')),
    data jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webauthn_challenges_staff_idx ON webauthn_challenges (merchant_id, staff_id);

CREATE TABLE media_assets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    kind media_kind NOT NULL,
    object_key text NOT NULL UNIQUE,
    content_type text NOT NULL,
    width integer NOT NULL CHECK (width > 0),
    height integer NOT NULL CHECK (height > 0),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    checksum bytea NOT NULL,
    created_by uuid REFERENCES staff_users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
CREATE INDEX media_assets_merchant_idx ON media_assets (merchant_id);

CREATE TABLE brand_settings (
    merchant_id uuid PRIMARY KEY REFERENCES merchants(id) ON DELETE CASCADE,
    logo_asset_id uuid REFERENCES media_assets(id) ON DELETE SET NULL,
    cover_asset_id uuid REFERENCES media_assets(id) ON DELETE SET NULL,
    primary_color text NOT NULL DEFAULT '#1f2937',
    secondary_color text NOT NULL DEFAULT '#f59e0b',
    font text NOT NULL DEFAULT 'system',
    tagline text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE menu_categories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    description text NOT NULL DEFAULT '',
    sort_order integer NOT NULL DEFAULT 0,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
CREATE INDEX menu_categories_merchant_idx ON menu_categories (merchant_id, sort_order);

CREATE TABLE menu_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    category_id uuid NOT NULL REFERENCES menu_categories(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 160),
    description text NOT NULL DEFAULT '',
    price_minor integer NOT NULL CHECK (price_minor >= 0),
    currency char(3) NOT NULL DEFAULT 'EUR' CHECK (currency ~ '^[A-Z]{3}$'),
    tax_rate_bp integer NOT NULL DEFAULT 700 CHECK (tax_rate_bp BETWEEN 0 AND 10000),
    image_asset_id uuid REFERENCES media_assets(id) ON DELETE SET NULL,
    active boolean NOT NULL DEFAULT true,
    available boolean NOT NULL DEFAULT true,
    sort_order integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
CREATE INDEX menu_items_merchant_idx ON menu_items (merchant_id, category_id, sort_order);

CREATE TABLE option_groups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    min_select integer NOT NULL DEFAULT 0 CHECK (min_select >= 0),
    max_select integer NOT NULL DEFAULT 1 CHECK (max_select >= 1 AND max_select >= min_select),
    required boolean NOT NULL DEFAULT false,
    sort_order integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
CREATE INDEX option_groups_merchant_idx ON option_groups (merchant_id);

CREATE TABLE item_options (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    group_id uuid NOT NULL REFERENCES option_groups(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    price_delta_minor integer NOT NULL DEFAULT 0,
    active boolean NOT NULL DEFAULT true,
    sort_order integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
CREATE INDEX item_options_merchant_idx ON item_options (merchant_id, group_id, sort_order);

CREATE TABLE menu_item_option_groups (
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    menu_item_id uuid NOT NULL REFERENCES menu_items(id) ON DELETE CASCADE,
    option_group_id uuid NOT NULL REFERENCES option_groups(id) ON DELETE CASCADE,
    sort_order integer NOT NULL DEFAULT 0,
    PRIMARY KEY (menu_item_id, option_group_id)
);
CREATE INDEX menu_item_option_groups_merchant_idx ON menu_item_option_groups (merchant_id);

CREATE TABLE opening_hours (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    weekday smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    opens time NOT NULL,
    closes time NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (opens <> closes)
);
CREATE INDEX opening_hours_merchant_idx ON opening_hours (merchant_id, weekday);

CREATE TABLE delivery_zones (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name text NOT NULL,
    postcode_prefixes text[] NOT NULL DEFAULT '{}',
    min_order_minor integer NOT NULL DEFAULT 0 CHECK (min_order_minor >= 0),
    delivery_fee_minor integer NOT NULL DEFAULT 0 CHECK (delivery_fee_minor >= 0),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX delivery_zones_merchant_idx ON delivery_zones (merchant_id);

CREATE TABLE customers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    email text,
    phone text,
    name text NOT NULL DEFAULT '',
    marketing_consent boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_order_at timestamptz,
    anonymized_at timestamptz,
    CHECK (email IS NOT NULL OR phone IS NOT NULL)
);
CREATE UNIQUE INDEX customers_merchant_email_idx ON customers (merchant_id, lower(email)) WHERE email IS NOT NULL;
CREATE UNIQUE INDEX customers_merchant_phone_idx ON customers (merchant_id, phone) WHERE phone IS NOT NULL;

CREATE TABLE orders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    public_ref text NOT NULL,
    customer_id uuid REFERENCES customers(id) ON DELETE SET NULL,
    status order_status NOT NULL DEFAULT 'draft',
    fulfilment fulfilment_mode NOT NULL DEFAULT 'pickup',
    payment_method text NOT NULL DEFAULT 'card' CHECK (payment_method IN ('card', 'cash')),
    payment_status payment_status NOT NULL DEFAULT 'unpaid',
    currency char(3) NOT NULL DEFAULT 'EUR' CHECK (currency ~ '^[A-Z]{3}$'),
    subtotal_minor integer NOT NULL DEFAULT 0 CHECK (subtotal_minor >= 0),
    tax_minor integer NOT NULL DEFAULT 0 CHECK (tax_minor >= 0),
    delivery_fee_minor integer NOT NULL DEFAULT 0 CHECK (delivery_fee_minor >= 0),
    total_minor integer NOT NULL DEFAULT 0 CHECK (total_minor >= 0),
    contact_name text NOT NULL DEFAULT '',
    contact_email text,
    contact_phone text,
    delivery_address jsonb,
    notes text NOT NULL DEFAULT '',
    scheduled_for timestamptz,
    placed_at timestamptz,
    accepted_at timestamptz,
    ready_at timestamptz,
    completed_at timestamptz,
    cancelled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, public_ref)
);
CREATE INDEX orders_merchant_status_idx ON orders (merchant_id, status, created_at DESC);
CREATE INDEX orders_merchant_created_idx ON orders (merchant_id, created_at DESC);

CREATE TABLE order_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    menu_item_id uuid REFERENCES menu_items(id) ON DELETE SET NULL,
    name_snapshot text NOT NULL,
    unit_price_minor integer NOT NULL CHECK (unit_price_minor >= 0),
    quantity integer NOT NULL CHECK (quantity > 0),
    options_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb,
    line_total_minor integer NOT NULL CHECK (line_total_minor >= 0)
);
CREATE INDEX order_items_order_idx ON order_items (merchant_id, order_id);

CREATE TABLE order_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    from_status order_status,
    to_status order_status,
    actor_type text NOT NULL DEFAULT 'system',
    actor_id uuid,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX order_events_order_idx ON order_events (merchant_id, order_id, created_at);

CREATE TABLE payment_connections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    provider text NOT NULL,
    connected_account_id text,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'restricted', 'disabled')),
    capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, provider)
);
CREATE INDEX payment_connections_merchant_idx ON payment_connections (merchant_id);

CREATE TABLE payment_records (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    order_id uuid REFERENCES orders(id) ON DELETE SET NULL,
    provider text NOT NULL,
    provider_ref text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('payment', 'refund')),
    amount_minor integer NOT NULL CHECK (amount_minor >= 0),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status text NOT NULL,
    raw_event_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, provider, provider_ref, kind)
);
CREATE INDEX payment_records_order_idx ON payment_records (merchant_id, order_id);

CREATE TABLE handoff_codes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    kind handoff_kind NOT NULL,
    code_hash bytea NOT NULL,
    salt bytea NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    locked_at timestamptz,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, order_id, kind)
);

CREATE TABLE couriers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    provider text NOT NULL,
    external_id text NOT NULL,
    name text NOT NULL DEFAULT '',
    phone text,
    status text NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (merchant_id, provider, external_id)
);

CREATE TABLE deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    mode fulfilment_mode NOT NULL,
    courier_id uuid REFERENCES couriers(id) ON DELETE SET NULL,
    status text NOT NULL DEFAULT 'pending',
    dispatched_at timestamptz,
    picked_up_at timestamptz,
    delivered_at timestamptz,
    proof_code_id uuid REFERENCES handoff_codes(id) ON DELETE SET NULL,
    location jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deliveries_order_idx ON deliveries (merchant_id, order_id);

CREATE TABLE audit_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    actor_type text NOT NULL,
    actor_id uuid,
    action text NOT NULL,
    target_type text NOT NULL DEFAULT '',
    target_id text NOT NULL DEFAULT '',
    ip inet,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_merchant_idx ON audit_log (merchant_id, created_at DESC);

CREATE TABLE webhook_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider text NOT NULL,
    provider_event_id text NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'processed', 'failed', 'ignored')),
    error text,
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    UNIQUE (provider, provider_event_id)
);

CREATE TABLE subscriptions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id uuid NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    stripe_customer_id text,
    stripe_subscription_id text UNIQUE,
    status text NOT NULL DEFAULT 'trialing',
    current_period_end timestamptz,
    trial_end timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE merchants ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON merchants
    USING (id = nullif(current_setting('app.merchant_id', true), '')::uuid)
    WITH CHECK (id = nullif(current_setting('app.merchant_id', true), '')::uuid);

DO $$
DECLARE
    t text;
    tenant_tables text[] := ARRAY[
        'merchant_domains', 'staff_users', 'sessions', 'passkeys', 'webauthn_challenges', 'media_assets',
        'brand_settings', 'menu_categories', 'menu_items', 'option_groups',
        'item_options', 'menu_item_option_groups', 'opening_hours', 'delivery_zones',
        'customers', 'orders', 'order_items', 'order_events', 'payment_connections',
        'payment_records', 'handoff_codes', 'couriers', 'deliveries', 'audit_log'
    ];
BEGIN
    FOREACH t IN ARRAY tenant_tables LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format($fmt$
            CREATE POLICY tenant_isolation ON %I
            USING (merchant_id = nullif(current_setting('app.merchant_id', true), '')::uuid)
            WITH CHECK (merchant_id = nullif(current_setting('app.merchant_id', true), '')::uuid)
        $fmt$, t);
    END LOOP;
END $$;

CREATE OR REPLACE FUNCTION reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'table % is append-only', TG_TABLE_NAME;
END $$;

CREATE TRIGGER order_events_append_only
    BEFORE UPDATE OR DELETE ON order_events
    FOR EACH ROW EXECUTE FUNCTION reject_mutation();
CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION reject_mutation();

CREATE OR REPLACE FUNCTION resolve_merchant_by_host(p_host text)
RETURNS TABLE (id uuid, slug text, name text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT m.id, m.slug, m.name
    FROM public.merchant_domains d
    JOIN public.merchants m ON m.id = d.merchant_id
    WHERE d.host = lower(p_host)
      AND m.deleted_at IS NULL
    LIMIT 1
$$;
REVOKE ALL ON FUNCTION resolve_merchant_by_host(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_merchant_by_host(text) TO tablekit_app;

CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END $$;

DO $$
DECLARE
    t text;
BEGIN
    FOR t IN
        SELECT table_name FROM information_schema.columns
        WHERE table_schema = 'public' AND column_name = 'updated_at'
    LOOP
        EXECUTE format(
            'CREATE TRIGGER %I_updated_at BEFORE UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION set_updated_at()',
            t, t);
    END LOOP;
END $$;

GRANT USAGE ON SCHEMA public TO tablekit_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON
    merchants, merchant_domains, staff_users, sessions, passkeys, webauthn_challenges, media_assets,
    brand_settings, menu_categories, menu_items, option_groups, item_options,
    menu_item_option_groups, opening_hours, delivery_zones, customers, orders,
    order_items, payment_connections, payment_records, handoff_codes, couriers,
    deliveries
TO tablekit_app;
GRANT SELECT, INSERT ON order_events, audit_log TO tablekit_app;

REVOKE ALL ON webhook_events, subscriptions FROM tablekit_app;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM tablekit_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP FUNCTION IF EXISTS set_updated_at() CASCADE;
DROP FUNCTION IF EXISTS reject_mutation() CASCADE;
DROP FUNCTION IF EXISTS resolve_merchant_by_host(text);
DROP TABLE IF EXISTS subscriptions;
DROP TABLE IF EXISTS webhook_events;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS deliveries;
DROP TABLE IF EXISTS couriers;
DROP TABLE IF EXISTS handoff_codes;
DROP TABLE IF EXISTS payment_records;
DROP TABLE IF EXISTS payment_connections;
DROP TABLE IF EXISTS order_events;
DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS customers;
DROP TABLE IF EXISTS delivery_zones;
DROP TABLE IF EXISTS opening_hours;
DROP TABLE IF EXISTS menu_item_option_groups;
DROP TABLE IF EXISTS item_options;
DROP TABLE IF EXISTS option_groups;
DROP TABLE IF EXISTS menu_items;
DROP TABLE IF EXISTS menu_categories;
DROP TABLE IF EXISTS brand_settings;
DROP TABLE IF EXISTS webauthn_challenges;
DROP TABLE IF EXISTS media_assets;
DROP TABLE IF EXISTS passkeys;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS staff_users;
DROP TABLE IF EXISTS merchant_domains;
DROP TABLE IF EXISTS merchants;
DROP TYPE IF EXISTS media_kind;
DROP TYPE IF EXISTS handoff_kind;
DROP TYPE IF EXISTS payment_status;
DROP TYPE IF EXISTS fulfilment_mode;
DROP TYPE IF EXISTS order_status;
DROP TYPE IF EXISTS staff_role;

-- +goose StatementEnd
