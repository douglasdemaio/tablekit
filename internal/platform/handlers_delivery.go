package platform

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgtype"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/db/sqlcgen"
	"tablekit.de/tablekit/web/templates"
)

func (s *Server) handleDeliveryPage(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	jobs, err := s.storefrontSvc.DeliveryJobs(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("list delivery jobs", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := s.page(r, "delivery.title")
	p.Nav = "orders"
	if r.URL.Query().Get("claimed") == "1" {
		p.Notice = s.tr(r, "delivery.claimed")
	}
	if r.URL.Query().Get("released") == "1" {
		p.Notice = s.tr(r, "delivery.released")
	}
	switch r.URL.Query().Get("error") {
	case "claim":
		p.Error = s.tr(r, "delivery.error.claim")
	case "code":
		p.Error = s.tr(r, "delivery.error.code")
	case "ready":
		p.Error = s.tr(r, "delivery.error.ready")
	}
	canAssign := id.Can(auth.PermOrdersManage)
	if canAssign {
		staff, err := s.staffSvc.List(r.Context(), id.Merchant.ID)
		if err == nil {
			for _, st := range staff {
				if st.Role != sqlcgen.StaffRoleDriver || st.DisabledAt.Valid {
					continue
				}
				p.Drivers = append(p.Drivers, templates.DriverOption{ID: st.ID.String(), Name: st.Name})
			}
		}
	}
	for _, job := range jobs {
		mine := job.AssigneeID != nil && *job.AssigneeID == id.Staff.ID
		claimed := job.AssigneeID != nil
		v := templates.DeliveryView{
			ID:          job.OrderID.String(),
			Ref:         job.Ref,
			Status:      string(job.Status),
			StatusLabel: s.statusLabel(r, job.Status),
			Customer:    job.ContactName,
			Notes:       job.Notes,
			Total:       formatMoney(int64(job.Total), job.Currency),
			Payment:     s.paymentLabel(r, job.Payment),
			Assignee:    job.AssigneeName,
			Claimed:     claimed,
			Mine:        mine,
			CanClaim:    !claimed,
			CanRelease:  mine,
			CanHandoff:  mine && (job.Status == "ready" || job.Status == "out_for_delivery"),
			CanAssign:   canAssign && !mine,
		}
		if mine {
			v.Phone = job.Phone
			v.Address = strings.TrimSpace(strings.Join([]string{
				job.Address.Line1, job.Address.Line2,
				job.Address.Postcode + " " + job.Address.City,
			}, " "))
		}
		if job.ScheduledFor.IsZero() {
			v.Scheduled = formatTime(pgTime(job.PlacedAt))
		} else {
			v.Scheduled = formatTime(pgTime(job.ScheduledFor))
		}
		p.Deliveries = append(p.Deliveries, v)
	}
	s.render(w, r, http.StatusOK, templates.DeliveryPage(p))
}

func (s *Server) handleDeliveryAssign(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=claim", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=claim", http.StatusSeeOther)
		return
	}
	staffID, err := uuid.Parse(r.PostFormValue("staff_id"))
	if err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=claim", http.StatusSeeOther)
		return
	}
	if err := s.storefrontSvc.AssignDelivery(r.Context(), id.Merchant.ID, staffID, orderID); err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=claim", http.StatusSeeOther)
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "delivery.assigned", "order", orderID.String(), s.clientIP(r), map[string]any{"staff_id": staffID.String()})
	http.Redirect(w, r, "/dashboard/orders?claimed=1", http.StatusSeeOther)
}

func (s *Server) handleDeliveryClaim(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=claim", http.StatusSeeOther)
		return
	}
	if err := s.storefrontSvc.ClaimDelivery(r.Context(), id.Merchant.ID, id.Staff.ID, orderID); err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=claim", http.StatusSeeOther)
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "delivery.claimed", "order", orderID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/orders?claimed=1", http.StatusSeeOther)
}

func (s *Server) handleDeliveryRelease(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/dashboard/orders", http.StatusSeeOther)
		return
	}
	if err := s.storefrontSvc.ReleaseDelivery(r.Context(), id.Merchant.ID, orderID); err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=claim", http.StatusSeeOther)
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "delivery.released", "order", orderID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/orders?released=1", http.StatusSeeOther)
}

func pgTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}
