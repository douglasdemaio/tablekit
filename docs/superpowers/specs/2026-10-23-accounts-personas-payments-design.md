# Accounts, personas, branding and payments — design (Milestone 3)

Status: approved, implementation starting.

## Goals

Turn tablekit from a staff tool with guest checkout into a complete ordering
platform:

- one customer account that works across restaurant subdomains;
- a customer address book that can be shared per restaurant;
- customer order history for the restaurant being visited;
- separate, permission-driven staff personas;
- restaurant profile and branding on each subdomain;
- card payments direct to the restaurant's own Stripe account.

Hard rules still apply: tablekit never holds, routes or splits customer money,
never stores card data, and only a verified provider webhook may mark an order
paid by card. Prices are still recalculated server-side.

## 1. Identity model

### Customer accounts (platform-wide)

New tables outside tenant RLS:

- `customer_accounts` — id, email (unique, lowercase), name, phone,
  last_login_at, created_at, updated_at, disabled_at.
- `customer_addresses` — id, customer_id, label, recipient, phone, line1,
  line2, postcode, city, country, delivery_notes, is_default.
  One default per customer enforced with a partial unique index.
- `customer_sessions` — id, customer_id, token_hash, ip, user_agent,
  created_at, last_seen_at, expires_at, revoked_at.
- `customer_login_codes` — id, email, code_hash, salt, attempts, expires_at,
  used_at, created_at, ip.
- `customer_shares` — customer_id, merchant_id, created_at; unique pair.

`orders` gains `customer_account_id uuid REFERENCES customer_accounts(id)`.
The existing tenant-scoped `customers` table remains for a later CRM feature
and is not used by Milestone 3.

All customer data access is scoped by the authenticated customer id. Staff
handlers never query the global customer tables; they render only the contact
details stored on the order, plus the shared profile when a share exists.

### Staff identities

Unchanged (`staff_users`, `sessions`). A person may be both a customer and a
staff member with separate credentials.

## 2. Customer login — email one-time code

Routes:

| Method | Path | Purpose |
| --- | --- | --- |
| GET/POST | `/account/login` | Request a code by email |
| POST | `/account/login/verify` | Verify code, create session |
| POST | `/account/logout` | Revoke session |
| GET | `/account` | Profile, addresses, shared restaurants |
| GET | `/account/orders` | Own orders for the current restaurant |
| POST | `/account/profile` | Update name/phone |
| POST | `/account/addresses` | Create address |
| POST | `/account/addresses/{id}` | Update address |
| POST | `/account/addresses/{id}/delete` | Delete address |
| POST | `/account/addresses/{id}/default` | Make default |
| POST | `/account/shares/{merchant}/revoke` | Revoke a share |

Rules:

- Code: 6 digits, 10-minute expiry, hashed with Argon2id plus salt, max 5
  attempts, single use.
- Rate limits by email and IP for both request and verification.
- Request responses never reveal whether an email exists; unknown emails are
  created on first successful verification.
- SMTP configured through environment; in development without SMTP the code is
  written to the server log and the page states that.
- Session cookie `tk_customer` is set with `Domain=<base domain>`, HttpOnly,
  SameSite=Lax, Secure in production, 30-day lifetime, so one login works on
  every restaurant subdomain. Apex `/account` also works.
- CSRF uses the existing double-submit middleware.
- No customer MFA in this milestone.

## 3. Personas, permissions and navigation

| Persona | Visible areas |
| --- | --- |
| Customer | Storefront menu, checkout, own orders for the current restaurant, account/profile/addresses/shares. Never dashboard, staff, settings, security or staff order queue. |
| Owner | Dashboard, orders, menu, staff, security, settings. |
| Manager | Dashboard, orders, menu, staff, security, settings. |
| Kitchen | Dashboard, orders, full menu editing including sold-out controls. |
| Driver | Dashboard, delivery jobs only, menu editing. No staff/settings/security. |

- Menu editing permission is extended to kitchen and driver.
- Navigation is generated from permissions; the current hardcoded nav is
  replaced by permission-aware items plus a separate customer shell.
- A staff member with a customer session sees the customer shell on the
  storefront and the staff shell on the dashboard. The footer links to staff
  login.

### Driver flow

- Driver order list contains only `self_delivery` orders, not pickup/kitchen
  work.
- Address and phone are hidden until the driver claims the job.
- `POST /dashboard/orders/{id}/claim` assigns `deliveries.assignee_staff_id`
  and reveals the address to that driver.
- Managers may reassign or release a delivery.
- Delivery handoff uses a `delivery` kind handoff code generated at placement,
  entered by the driver at drop-off. Cash deliveries become paid on handoff;
  card deliveries are already paid.

## 4. Customer checkout

- Browsing is public; placing an order requires a customer session. Unauthenticated
  users are sent to `/account/login?next=/checkout`.
- The cart remains client-side (`tk_cart_<slug>`); the server recalculates all
  prices.
- Checkout offers saved addresses with the default selected, plus a new-address
  form. Delivery requires name, phone, postcode/address and an active zone.
- Timing: ASAP or a 30-minute slot within opening hours for the next 7 days,
  stored in `orders.scheduled_for`. The server validates the slot.
- “Share my saved profile with this restaurant” checkbox, default off. The
  order always carries the contact details and address needed for that order.
- Confirmation and history are accessible only to the owning customer.
- Bank/cash payment: pay in person, completed at handoff.

## 5. Restaurant profile and branding

Merchant fields added: description, phone, email, street, line 2, postcode,
city, country, cuisine, pickup_enabled, self_delivery_enabled.

Branding (`brand_settings` additions): template, accent color, background
color, text color; existing logo, cover, tagline, primary/secondary colors and
font are wired up.

- Templates: Classic Bistro, Solana Vibes, Cyber Snack, Modern Minimal, Street
  Food, Fine Dining, Custom (same definitions as sporkit-site).
- Fonts: curated Google Fonts list with safe stacks and fallback to system
  fonts.
- Logo and cover uploads reuse the existing media pipeline and `media_kind`
  values `logo` and `cover`.
- Dynamic branding is served from `GET /theme.css`, scoped to the tenant
  request, defining CSS variables and template rules. No inline styles, so the
  existing CSP stays intact apart from allowing Google Fonts
  (`fonts.googleapis.com` for style, `fonts.gstatic.com` for font).
- Storefront shows logo, cover, description, cuisine, address and contact.
- Settings becomes sections: Profile, Branding, Opening hours, Delivery zones,
  VAT, Payments.

## 6. Payments — Stripe direct charges

- Credentials live in `payment_connections` (provider `stripe`): publishable
  key, encrypted secret key, encrypted webhook secret, livemode flag, status.
- Owner/manager pastes keys in settings; secrets are encrypted with the
  existing sealer and never rendered back.
- Cash is always available. Card is available only when Stripe is configured.
- Card order flow:
  1. Order is created as `pending_payment` with `payment_method = card`.
  2. Tablekit creates a Stripe Checkout Session with the restaurant's secret
     key (direct charge, no platform fee).
  3. Customer is redirected to Stripe. Success returns to
     `/orders/{ref}` showing “payment processing” until the webhook arrives.
  4. `POST /webhooks/stripe` verifies `Stripe-Signature` with the merchant's
     webhook secret, records the event idempotently, and on
     `checkout.session.completed` / `payment_intent.succeeded` sets payment
     paid and moves the order into the queue.
  5. Failed payments mark the order `payment_failed`.
- Only the webhook path sets card orders paid.
- Refunds are performed in the restaurant's Stripe dashboard.
- No Stripe SDK is used (offline build): REST calls with `net/http` and
  HMAC-SHA256 webhook verification with the standard library.

## 7. Menu sold-out state

- `menu_items.sold_out_until timestamptz` plus `sold_out_reason text`.
- Effective availability: `available AND (sold_out_until IS NULL OR now() >= sold_out_until)`.
- Presets: rest of today (until closing), evening service, until tomorrow's
  opening, custom date/time. Items return automatically; staff can clear the
  state.
- Storefront shows “Sold out” and the expected return time.

## 8. Data migration

`00003_accounts_personas.sql` adds:

- global customer tables and indexes;
- `orders.customer_account_id`;
- `deliveries.assignee_staff_id`;
- `menu_items.sold_out_until`, `sold_out_reason`;
- merchant profile columns;
- brand template/color columns;
- Stripe credential columns on `payment_connections`;
- grants for the new tables.

Existing tenant RLS remains unchanged. Global customer tables are intentionally
platform-level; the app scopes them by customer session.

## 9. Tests

Unit:

- OTP generation/hash/expiry/attempt limits;
- address default enforcement and validation;
- slot generation and validation against opening hours;
- permission matrix for owner/manager/kitchen/driver/customer;
- theme CSS generation and font allow-list;
- Stripe webhook signature verification and event parsing;
- menu sold-out effective availability.

Integration (test DSNs):

- request code → verify → session created; unknown email becomes an account;
- login required for checkout;
- saved address used on an order; sharing created/revoked;
- cash order lifecycle; card order pending → webhook paid;
- driver cannot see address before claim and can after claim;
- kitchen/driver can edit menu and set sold-out;
- customer cannot access dashboard/staff/settings/security;
- customer order history limited to the current restaurant.

Manual smoke:

- log in as owner, manager, kitchen, driver and customer;
- verify navigation differs;
- check branding/theme on the storefront;
- place a cash order and a Stripe test order when keys are available.

## Open risks

- SMTP and live Stripe calls cannot be exercised offline; dev log fallback and
  unit tests cover the local code paths.
- Google Fonts requires browser network access; system-font fallbacks are used
  when unavailable.
- Extending the customer session cookie to the base domain means any tenant
  subdomain receives it; the cookie is HttpOnly and same-site, and sessions are
  revocable.
