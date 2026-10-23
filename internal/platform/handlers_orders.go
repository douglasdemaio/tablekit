package platform

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/db/sqlcgen"
	"tablekit.de/tablekit/internal/orders"
	"tablekit.de/tablekit/web/templates"
)

func (s *Server) handleOrdersPage(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if id.Role() == auth.RoleDriver {
		s.handleDeliveryPage(w, r, id)
		return
	}
	p := s.page(r, "orders.title")
	p.Nav = "orders"

	if ref := r.URL.Query().Get("updated"); ref != "" {
		p.Notice = s.tr(r, "orders.updated", "ref", ref)
	}
	switch r.URL.Query().Get("error") {
	case "transition":
		p.Error = s.tr(r, "orders.error.transition")
	case "code":
		p.Error = s.tr(r, "orders.error.code")
	case "ready":
		p.Error = s.tr(r, "orders.error.ready")
	case "notfound":
		p.Error = s.tr(r, "orders.error.notfound")
	}
	if r.URL.Query().Get("handoff") == "1" {
		p.Notice = s.tr(r, "orders.handoff_done")
	}

	open, err := s.orderSvc.ListOpen(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("list open orders", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	for _, item := range open {
		o := item.Order
		view := templates.OrderView{
			ID:          o.ID.String(),
			Ref:         o.PublicRef,
			Status:      string(o.Status),
			StatusLabel: s.statusLabel(r, o.Status),
			Payment:     s.paymentLabel(r, o.PaymentStatusEnum),
			Fulfilment:  s.fulfilmentLabel(r, o.Fulfilment),
			Customer:    o.ContactName,
			Total:       formatMoney(int64(o.TotalMinor), o.Currency),
			Placed:      formatTime(placedAt(o)),
			Notes:       o.Notes,
			CanHandoff:  o.Fulfilment == sqlcgen.FulfilmentModePickup && o.Status == sqlcgen.OrderStatusReady,
		}
		for _, line := range item.Items {
			view.Items = append(view.Items, templates.OrderItemView{
				Name:     line.NameSnapshot,
				Quantity: line.Quantity,
				Total:    formatMoney(int64(line.LineTotalMinor), o.Currency),
			})
		}
		for _, target := range orders.AllowedFrom(o.Status) {
			view.Actions = append(view.Actions, templates.OrderAction{
				Key:   string(target),
				Label: s.tr(r, "orders.action."+string(target)),
				Path:  "/dashboard/orders/" + o.ID.String() + "/status",
				Style: actionStyle(target),
			})
		}
		p.Orders = append(p.Orders, view)
	}

	s.render(w, r, http.StatusOK, templates.OrdersPage(p))
}

func (s *Server) handleOrderStatus(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=transition", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=transition", http.StatusSeeOther)
		return
	}
	to := sqlcgen.OrderStatusEnum(r.PostFormValue("to"))
	if err := s.orderSvc.Transition(r.Context(), id.Merchant.ID, id.Staff.ID, orderID, to); err != nil {
		s.log.Warn("order transition rejected", "error", err, "to", to)
		http.Redirect(w, r, "/dashboard/orders?error=transition", http.StatusSeeOther)
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "order.status_changed", "order", orderID.String(), s.clientIP(r), map[string]any{"to": string(to)})
	http.Redirect(w, r, "/dashboard/orders?updated="+orderID.String(), http.StatusSeeOther) // #nosec G710 -- fixed local path with UUID
}

func placedAt(o sqlcgen.Order) pgtype.Timestamptz {
	if o.PlacedAt.Valid {
		return o.PlacedAt
	}
	return o.CreatedAt
}
