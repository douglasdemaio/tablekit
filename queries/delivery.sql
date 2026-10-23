-- name: InsertDelivery :exec
INSERT INTO deliveries (merchant_id, order_id, mode, status, assignee_staff_id)
VALUES ($1::uuid, $2::uuid, 'self_delivery', 'pending', $3)
ON CONFLICT DO NOTHING;

-- name: GetDeliveryByOrder :one
SELECT * FROM deliveries
WHERE merchant_id = $1::uuid
  AND order_id = $2::uuid
ORDER BY created_at DESC
LIMIT 1;

-- name: ClaimDelivery :one
UPDATE deliveries
SET assignee_staff_id = $1, updated_at = now()
WHERE merchant_id = $2::uuid
  AND order_id = $3::uuid
  AND (assignee_staff_id IS NULL OR assignee_staff_id = $1)
RETURNING *;

-- name: ReleaseDelivery :exec
UPDATE deliveries
SET assignee_staff_id = NULL, updated_at = now()
WHERE merchant_id = $1::uuid
  AND order_id = $2::uuid;

-- name: ListDeliveryOrders :many
SELECT o.id, o.public_ref, o.status, o.fulfilment, o.payment_method, o.payment_status,
       o.currency, o.total_minor, o.contact_name, o.contact_phone, o.delivery_address,
       o.notes, o.scheduled_for, o.placed_at, o.created_at, o.updated_at,
       d.assignee_staff_id, COALESCE(su.name, '')::text AS assignee_name
FROM orders o
JOIN deliveries d ON d.order_id = o.id AND d.merchant_id = o.merchant_id
LEFT JOIN staff_users su ON su.id = d.assignee_staff_id
WHERE o.merchant_id = $1::uuid
  AND o.fulfilment = 'self_delivery'
  AND o.status NOT IN ('pending_payment', 'completed', 'cancelled', 'payment_failed', 'refunded')
ORDER BY COALESCE(o.scheduled_for, o.placed_at) ASC, o.created_at ASC;

-- name: CompleteDelivery :exec
UPDATE deliveries
SET status = 'delivered', delivered_at = now(), updated_at = now()
WHERE merchant_id = $1::uuid
  AND order_id = $2::uuid;

-- name: AssignDelivery :exec
UPDATE deliveries
SET assignee_staff_id = sqlc.arg(staff_id), updated_at = now()
WHERE merchant_id = sqlc.arg(merchant_id)
  AND order_id = sqlc.arg(order_id);
