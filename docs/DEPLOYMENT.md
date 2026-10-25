# Deployment checklist

This is a concrete checklist for self-hosting tablekit. Follow in order; anything marked [REQUIRED] must be done before exposing to the internet.

## 1) Prerequisites
- [REQUIRED] Linux host with Docker/Podman.
- [REQUIRED] PostgreSQL 16 (managed or self-hosted). Do **not** use `BYPASSRLS`.
- [REQUIRED] TLS-terminating reverse proxy in front of the app (Caddy, Nginx, Traefik).
- [REQUIRED] A public base domain (e.g. `example.com`) and DNS for tenant subdomains (`*.example.com`).
- [REQUIRED] Outbound email (SMTP) for customer login codes and notifications.
- [REQUIRED] S3-compatible object storage for merchant logos/covers (or disable uploads).

## 2) Secrets and configuration
- [REQUIRED] Generate a strong `TABLEKIT_ENCRYPTION_KEY` (32 bytes, base64). Store in a secret manager/KMS. **Never** commit it. Back it up safely; losing it cannot be recovered.
- [REQUIRED] Create least-privilege DB roles: `tablekit_owner` (migrations/owner-only ops) and `tablekit_app` (`NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS`). App role must not bypass RLS.
- [REQUIRED] Set `TABLEKIT_ENV=production`, `TABLEKIT_PUBLIC_SCHEME=https`, `TABLEKIT_COOKIE_SECURE=true`, `TABLEKIT_TRUST_PROXY=true` (only if behind a trusted proxy).
- [REQUIRED] Configure WebAuthn: `TABLEKIT_WEBAUTHN_RP_ID=<your-domain>`, `TABLEKIT_WEBAUTHN_ORIGINS=https://<your-domain>` (and tenant origins as needed).
- [REQUIRED] SMTP configured (see `internal/email`) or document that codes are logged (dev only).
- [REQUIRED] Stripe (if enabling card payments): `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET` (per merchant or platform config as implemented). Webhooks must go to `POST /webhooks/stripe` over HTTPS and signature verification must remain enforced.

## 3) Database
- [REQUIRED] Run migrations with the owner role: `tablekit-migrate up` (or `make migrate-up`).
- [REQUIRED] Create the first merchant + owner with `tablekit-bootstrap` (or equivalent). Change the bootstrap password immediately and require MFA setup.
- [REQUIRED] Verify RLS: run `make test` against a real DB or the RLS tests; app role must be unable to read other merchants' data.
- [REQUIRED] Set up automated backups (logical/physical), test restore procedure, and document RPO/RTO.

## 4) Build and deploy
- [REQUIRED] Build container images (see `Dockerfile`). The image runs as non-root (`tablekit`).
- [REQUIRED] Use the provided `deploy/compose.yaml` as a starting point or equivalent orchestration (K8s/systemd).
- [REQUIRED] Put TLS reverse proxy in front. Only forward required headers; set `TABLEKIT_TRUST_PROXY=true`.
- [REQUIRED] Configure health/readiness: liveness `GET /healthz`, readiness `GET /readyz` (checks DB). Use them in orchestrator.
- [REQUIRED] Restrict network egress/ingress as appropriate; do not expose Postgres/MinIO publicly.

## 5) Security hardening
- [REQUIRED] Rotate all default/dev passwords.
- [REQUIRED] Enforce MFA for privileged staff (owner/manager). Policy is in place; do not disable.
- [REQUIRED] Review CORS/origin checks (CSRF/origin validation is enforced; keep secure cookies).
- [REQUIRED] Audit logging is in place for sensitive actions; ensure logs are shipped/retained per policy.
- [REQUIRED] Keep dependencies updated; run CI security checks (gosec/gitleaks) in release pipeline.
- [REQUIRED] Rate limiting: review auth/setup endpoints and consider edge rate limits.

## 6) Go-live verification
- [REQUIRED] Tenant resolution works for `*.{BASE_DOMAIN}` and apex is reserved.
- [REQUIRED] Staff login + MFA setup works; customer OTP login works (email delivered).
- [REQUIRED] Place cash order end-to-end (menu → cart → checkout → confirmation).
- [REQUIRED] Place card order in test mode → webhook marks `paid` → idempotent on replay.
- [REQUIRED] Delivery/pickup handoff codes verified; orders transition correctly.
- [REQUIRED] RLS isolation verified; no card data stored.
- [REQUIRED] Backups/restores tested; rollback plan documented.
