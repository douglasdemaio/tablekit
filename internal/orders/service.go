// Package orders contains the staff-facing order queue logic: listing open
// orders and moving them through the fulfilment lifecycle. It never touches
// payment state; order.paid is set only from a verified provider webhook.
package orders

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/db/sqlcgen"
)

var (
	ErrNotFound          = errors.New("orders: not found")
	ErrInvalidTransition = errors.New("orders: invalid status transition")
)

type Service struct {
	store *db.Store
}

func NewService(store *db.Store) *Service { return &Service{store: store} }

// OpenOrder is an order together with its line items for display.
type OpenOrder struct {
	Order sqlcgen.Order
	Items []sqlcgen.OrderItem
}

// allowed maps the current status to the statuses staff may move it to. Cash
// and card orders both start after creation, so both pending_payment and paid
// may be accepted; the payment fields themselves are never changed here.
var allowed = map[sqlcgen.OrderStatusEnum][]sqlcgen.OrderStatusEnum{
	// Guest orders arrive as submitted and wait for staff to accept.
	sqlcgen.OrderStatusEnum("submitted"): {sqlcgen.OrderStatusAccepted, sqlcgen.OrderStatusCancelled},
	sqlcgen.OrderStatusDraft:             {sqlcgen.OrderStatusAccepted, sqlcgen.OrderStatusCancelled},
	sqlcgen.OrderStatusPendingPayment:    {sqlcgen.OrderStatusAccepted, sqlcgen.OrderStatusCancelled},
	sqlcgen.OrderStatusPaid:              {sqlcgen.OrderStatusAccepted, sqlcgen.OrderStatusCancelled},
	sqlcgen.OrderStatusAccepted:          {sqlcgen.OrderStatusPreparing, sqlcgen.OrderStatusCancelled},
	sqlcgen.OrderStatusPreparing:         {sqlcgen.OrderStatusReady, sqlcgen.OrderStatusCancelled},
	sqlcgen.OrderStatusReady: {
		sqlcgen.OrderStatusPickedUp, sqlcgen.OrderStatusOutForDelivery,
		sqlcgen.OrderStatusCompleted, sqlcgen.OrderStatusCancelled,
	},
	sqlcgen.OrderStatusPickedUp:       {sqlcgen.OrderStatusCompleted},
	sqlcgen.OrderStatusOutForDelivery: {sqlcgen.OrderStatusCompleted},
}

// AllowedFrom returns the statuses staff may move an order to.
func AllowedFrom(from sqlcgen.OrderStatusEnum) []sqlcgen.OrderStatusEnum {
	return allowed[from]
}

// CanTransition reports whether from -> to is a permitted staff transition.
func CanTransition(from, to sqlcgen.OrderStatusEnum) bool {
	for _, candidate := range allowed[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

func (s *Service) ListOpen(ctx context.Context, merchantID uuid.UUID) ([]OpenOrder, error) {
	var out []OpenOrder
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		rows, err := q.ListOpenOrders(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			items, err := q.ListOrderItems(ctx, row.ID)
			if err != nil {
				return err
			}
			out = append(out, OpenOrder{Order: row, Items: items})
		}
		return nil
	})
	return out, err
}

// Transition validates the requested status change and records an event.
func (s *Service) Transition(ctx context.Context, merchantID, staffID, orderID uuid.UUID, to sqlcgen.OrderStatusEnum) error {
	return s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		current, err := q.GetOrderByID(ctx, orderID)
		if err != nil {
			return ErrNotFound
		}
		if !CanTransition(current.Status, to) {
			return ErrInvalidTransition
		}
		if _, err := q.UpdateOrderStatus(ctx, sqlcgen.UpdateOrderStatusParams{Status: to, ID: orderID}); err != nil {
			return err
		}
		actor := staffID
		return q.InsertOrderEvent(ctx, sqlcgen.InsertOrderEventParams{
			MerchantID: merchantID,
			OrderID:    orderID,
			ToStatus:   &to,
			ActorID:    &actor,
		})
	})
}

type Counts struct {
	Open  int64
	Today int64
	Total int64
}

func (s *Service) Counts(ctx context.Context, merchantID uuid.UUID) (Counts, error) {
	var c Counts
	err := s.store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		open, err := q.CountOpenOrders(ctx)
		if err != nil {
			return err
		}
		today, err := q.CountOrdersToday(ctx)
		if err != nil {
			return err
		}
		revenue, err := q.SumRevenueToday(ctx)
		if err != nil {
			return err
		}
		c = Counts{Open: open, Today: today, Total: revenue}
		return nil
	})
	return c, err
}
