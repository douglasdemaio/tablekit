-- name: ListOpenOrders :many
SELECT id, merchant_id, public_ref, customer_id, status, fulfilment, payment_method,
       payment_status, currency, subtotal_minor, tax_minor, delivery_fee_minor, total_minor,
       contact_name, contact_email, contact_phone, delivery_address, notes, scheduled_for,
       placed_at, accepted_at, ready_at, completed_at, cancelled_at, created_at, updated_at
FROM orders
WHERE status NOT IN ('completed', 'cancelled', 'refunded', 'payment_failed', 'disputed')
ORDER BY created_at ASC
LIMIT 100;

-- name: GetOrderByID :one
SELECT id, merchant_id, public_ref, customer_id, status, fulfilment, payment_method,
       payment_status, currency, subtotal_minor, tax_minor, delivery_fee_minor, total_minor,
       contact_name, contact_email, contact_phone, delivery_address, notes, scheduled_for,
       placed_at, accepted_at, ready_at, completed_at, cancelled_at, created_at, updated_at
FROM orders
WHERE id = $1;

-- name: ListOrderItems :many
SELECT id, merchant_id, order_id, menu_item_id, name_snapshot, unit_price_minor, quantity,
       options_snapshot, line_total_minor
FROM order_items
WHERE order_id = $1
ORDER BY id ASC;

-- name: UpdateOrderStatus :one
UPDATE orders
SET     status = $1,
    accepted_at  = CASE WHEN $1 = 'accepted'::order_status  THEN now() ELSE accepted_at  END,
    ready_at     = CASE WHEN $1 = 'ready'::order_status     THEN now() ELSE ready_at     END,
    completed_at = CASE WHEN $1 = 'completed'::order_status THEN now() ELSE completed_at END,
    cancelled_at = CASE WHEN $1 = 'cancelled'::order_status THEN now() ELSE cancelled_at END,
    updated_at = now()
WHERE id = $2
RETURNING id, status;

-- name: InsertOrderEvent :exec
INSERT INTO order_events (merchant_id, order_id, event_type, to_status, actor_type, actor_id, metadata)
VALUES ($1, $2, 'status_changed', $3, 'staff', $4, '{}'::jsonb);

-- name: CountOpenOrders :one
SELECT count(*) FROM orders
WHERE status NOT IN ('completed', 'cancelled', 'refunded', 'payment_failed', 'disputed');

-- name: CountOrdersToday :one
SELECT count(*) FROM orders
WHERE created_at >= date_trunc('day', now());

-- name: SumRevenueToday :one
SELECT COALESCE(sum(total_minor), 0)::bigint FROM orders
WHERE created_at >= date_trunc('day', now())
  AND status NOT IN ('cancelled', 'refunded', 'payment_failed');
