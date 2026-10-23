# Guest ordering — design (Milestone 2)

Status: 2A implemented. 2B–2D not started.

## Goals

Guests order from the restaurant's own page on its tenant subdomain. tablekit
never holds, routes or splits customer money. Every price is recalculated on
the server from stored menu data, and payment state only changes from staff
action (cash) or a verified provider webhook (card, Milestone 2B).

## Confirmed decisions

| Topic | Decision |
| --- | --- |
| Payments | Cash in person from day one; online card from day one of 2B. |
| Provider (2B) | Stripe standard accounts; each restaurant uses its own keys. |
| Fulfilment | Pickup, self-delivery, courier. Courier pool in 2D. |
| Ordering surface | Web pickup/delivery only. No dine-in or QR ordering. |
| Customer identity | Name only plus a 6-character pickup code. No customer account. |
| Delivery | Zones by postcode prefix with fee and minimum order. |
| Timing | ASAP only; ordering is blocked outside opening hours. |
| Lifecycle | `submitted` → `accepted` → `preparing` → `ready` → handoff → `completed` |
| Item options | Plain items only; option tables remain unused. |
| VAT | Gross prices; one merchant rate (`vat_rate_bp`, default 19%). |

## Decomposition

- **2A (this milestone)** — public storefront, cart, checkout, order placement,
  pickup code, opening hours, delivery zones, staff accept/handoff.
- **2B** — Stripe standard accounts, checkout sessions, webhook-driven payment
  status, refunds from the provider dashboard.
- **2C** — delivery operations: courier assignment for the restaurant's own
  staff, dispatch states, delivery confirmation.
- **2D** — tablekit-run courier pool. Open question: paying pooled couriers
  requires routing money, which conflicts with the hard rule; resolve before
  building.

## 2A routes (tenant host)

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/` | Storefront (apex `/` remains the platform landing page) |
| GET | `/checkout` | Checkout form |
| POST | `/orders` | Place a guest order |
| GET | `/orders/{ref}` | Confirmation and status |
| GET | `/dashboard/settings` | VAT, opening hours, delivery zones |
| POST | `/dashboard/settings/vat` | Update VAT rate |
| POST | `/dashboard/settings/hours` | Replace weekly hours |
| POST | `/dashboard/settings/zones` | Create zone |
| POST | `/dashboard/settings/zones/{id}` | Update zone |
| POST | `/dashboard/settings/zones/{id}/delete` | Delete zone |
| POST | `/dashboard/orders/{id}/handoff` | Verify pickup code and complete |

## Data

Migration `00002_storefront.sql`:

- `order_status` gains `submitted` (BEFORE `accepted`). PostgreSQL cannot drop
  an enum value, so the down migration leaves it.
- `merchants.vat_rate_bp integer NOT NULL DEFAULT 1900 CHECK (0..10000)`.

Existing `opening_hours`, `delivery_zones`, `handoff_codes`, `brand_settings`
and `order_events` are reused. Guest orders have `customer_id = NULL` and only
`contact_name`.

## Checkout rules

- Cart lives in `localStorage` under `tk_cart_<slug>`; the server ignores it
  except as a list of item IDs and quantities.
- Quantities are capped at 20 per line and 50 lines per order.
- Items must exist, be active and available; prices come from the database.
- Pickup needs only a name. Delivery needs a postcode matching an active zone
  and a subtotal at or above that zone's minimum; the delivery fee is added to
  the total.
- Ordering is refused outside opening hours unless no hours are configured.
  Overnight windows (closes < opens) keep the previous day open past midnight.
- VAT is computed from the gross total:
  `tax = round(total * rate / (10000 + rate))`.
- Public reference is 12 uppercase hex characters; pickup code is 6 uppercase
  hex characters stored as `sha256(salt || code)` with a random 16-byte salt.
- The plaintext pickup code is delivered once through an HttpOnly cookie scoped
  to `/orders/<ref>` (15 minutes) and shown on the confirmation page.
- Wrong pickup codes increment `attempts`; the code locks after 5 failures.
- Handoff requires status `ready`; success sets `completed` and `paid` and
  writes an `order_events` row. This is the only place cash orders become paid.

## Security

- Public `POST /orders` is bounded by the shared IP limiter (10 per 10 min).
- CSRF is enforced on all POSTs, including the guest checkout form.
- All queries run through tenant-scoped transactions; RLS remains the backstop.
- Public order lookup is by unguessable reference only; the confirmation page
  does not leak other orders.

## Staff additions

- New permission `settings.manage` (owner + manager) and a Settings nav entry.
- Orders queue shows submitted guest orders and a pickup-code form on ready
  pickup orders.
- New i18n keys in English and German.

## Testing

- Unit: opening-hours windows (including overnight), zone prefix matching,
  included VAT.
- Integration (needs test DSNs): place pickup order, verify totals and items,
  reject handoff before ready, reject wrong code, complete with the right code;
  reject delivery outside a zone.
- Manual smoke: storefront load, guest order, confirmation code, delivery
  postcode acceptance/rejection.
