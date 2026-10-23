# tablekit

Open-source (MIT) ordering platform for independent restaurants. Guests order
directly from the restaurant; tablekit never touches the money. Payment for
card orders flows through the restaurant's own payment provider account.

Status: Milestone 2A (guest storefront, cart, checkout, pickup handoff).

## Design rules

- No customer funds are ever held, routed, or split by tablekit.
- No card data is stored; card status changes only on a verified provider
  webhook and order `paid` is set only from such a webhook.
- Prices are always recalculated on the server from stored menu data.
- Every tenant table is protected by PostgreSQL row-level security (RLS). The
  application role must not be a superuser and must not have `BYPASSRLS`; the
  server refuses to start otherwise.

## Local development

Requirements: Go 1.27+, Podman or Docker, Node.js (for Tailwind), `psql`.

```bash
# 1. Start a local Postgres and create the app role
podman run -d --name tablekit-pg -p 55432:5432 \
  -e POSTGRES_PASSWORD=devpass -e POSTGRES_DB=tablekit postgres:16-alpine
PGPASSWORD=devpass psql -h localhost -p 55432 -U postgres -d tablekit <<'SQL'
CREATE ROLE tablekit_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD 'tablekit_dev_app';
GRANT CONNECT ON DATABASE tablekit TO tablekit_app;
GRANT USAGE ON SCHEMA public TO tablekit_app;
SQL

# 2. Configure the environment
cp .env.example .env
export TABLEKIT_ENCRYPTION_KEY="$(openssl rand -base64 32)"
set -a && source .env && set +a

# 3. Migrate, seed a merchant + owner, then run
make migrate-up
make bootstrap
make run
```

For local subdomain routing, point a host at loopback and use a tenant host:

```bash
echo '127.0.0.1 bamberg-test.tk.local' | sudo tee -a /etc/hosts
# then open http://bamberg-test.tk.local:8080/login
```

Set `TABLEKIT_BASE_DOMAIN=tk.local` for local development. The apex
(`tk.local`) and `www.tk.local` are reserved for the platform; tenants live on
their own subdomains. Owners must complete MFA setup on first login.

## Container deployment

```bash
export TABLEKIT_OWNER_PASSWORD='...'
export TABLEKIT_APP_PASSWORD='...'
export TABLEKIT_ENCRYPTION_KEY="$(openssl rand -base64 32)"
export TABLEKIT_BASE_DOMAIN='example.de'
export TABLEKIT_WEBAUTHN_RP_ID='example.de'
export TABLEKIT_WEBAUTHN_ORIGINS='https://example.de'
podman compose -f deploy/compose.yaml up --build
```

`deploy/compose.yaml` runs Postgres, a one-shot `migrate` service, and the app.
Put a TLS-terminating reverse proxy in front and enable
`TABLEKIT_TRUST_PROXY=true`.

## Layout

- `cmd/server` — HTTP server.
- `cmd/migrate` — goose migration runner (embedded SQL).
- `cmd/bootstrap` — seed one merchant and owner (development/self-hosting).
- `internal/auth` — passwords (Argon2id), TOTP, WebAuthn, sessions, RBAC.
- `internal/db` — pool, tenant transaction helpers, RLS backstop, sqlc output.
- `internal/tenant` — host-to-merchant resolution middleware.
- `internal/storefront` — public menu, opening hours, delivery zones, guest orders.
- `internal/platform` — routing, middleware, handlers, view rendering.
- `migrations` — versioned schema, RLS policies and grants.
- `queries` — sqlc query sources.
- `web` — templ views, Tailwind input, static assets.

## Testing

```bash
export TABLEKIT_TEST_DATABASE_URL='postgres://tablekit_app:tablekit_dev_app@localhost:55432/tablekit?sslmode=disable'
export TABLEKIT_TEST_OWNER_DATABASE_URL='postgres://postgres:devpass@localhost:55432/tablekit?sslmode=disable'
make test
```

The suite includes RLS isolation tests that prove one merchant cannot read or
write another merchant's rows through the application role.
