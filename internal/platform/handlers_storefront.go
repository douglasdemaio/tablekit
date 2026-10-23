package platform

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/db/sqlcgen"
	"tablekit.de/tablekit/internal/storefront"
	"tablekit.de/tablekit/internal/tenant"
	"tablekit.de/tablekit/web/templates"
)

const pickupCodeCookie = "tk_pickup"

func (s *Server) handleStorefront(w http.ResponseWriter, r *http.Request) {
	m, ok := tenant.FromContext(r.Context())
	if !ok {
		http.NotFound(w, r)
		return
	}
	sf, err := s.storefrontSvc.Load(r.Context(), m.ID)
	if err != nil {
		if errors.Is(err, storefront.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.log.Error("load storefront", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := s.page(r, "storefront.title")
	p.Title = sf.Settings.Name
	p.Storefront = s.storefrontView(r, sf)
	s.render(w, r, http.StatusOK, templates.StorefrontPage(p))
}

func (s *Server) handleCheckoutPage(w http.ResponseWriter, r *http.Request) {
	m, ok := tenantFrom(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	sf, err := s.storefrontSvc.Load(r.Context(), m.ID)
	if err != nil {
		s.log.Error("load storefront", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := s.page(r, "checkout.title")
	p.Checkout = templates.CheckoutView{
		Name:     sf.Settings.Name,
		Open:     sf.Open,
		Currency: sf.Settings.Currency,
		Zones:    s.zoneViews(sf.Zones, sf.Settings.Currency),
	}
	s.render(w, r, http.StatusOK, templates.CheckoutPage(p))
}

func (s *Server) handlePlaceOrder(w http.ResponseWriter, r *http.Request) {
	m, ok := tenantFrom(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.auth.Limiter.Allow("order:ip:"+s.clientIP(r), 10, 10*time.Minute) {
		s.renderCheckoutError(w, r, m.ID, "checkout.error.toomany")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderCheckoutError(w, r, m.ID, "checkout.error.invalid")
		return
	}
	lines, ok := parseCart(r.PostFormValue("cart"))
	if !ok || len(lines) == 0 {
		s.renderCheckoutError(w, r, m.ID, "checkout.error.empty")
		return
	}
	fulfilment := sqlcgen.FulfilmentModePickup
	switch r.PostFormValue("fulfilment") {
	case "pickup":
	case "delivery":
		fulfilment = sqlcgen.FulfilmentModeSelfDelivery
	default:
		s.renderCheckoutError(w, r, m.ID, "checkout.error.invalid")
		return
	}
	placed, err := s.storefrontSvc.PlaceOrder(r.Context(), m.ID, storefront.OrderInput{
		Name:       r.PostFormValue("name"),
		Fulfilment: fulfilment,
		Postcode:   r.PostFormValue("postcode"),
		Address:    r.PostFormValue("address"),
		Notes:      r.PostFormValue("notes"),
		Lines:      lines,
	})
	if err != nil {
		s.log.Warn("guest order rejected", "error", err)
		switch {
		case errors.Is(err, storefront.ErrClosed):
			s.renderCheckoutError(w, r, m.ID, "checkout.error.closed")
		case errors.Is(err, storefront.ErrEmptyCart):
			s.renderCheckoutError(w, r, m.ID, "checkout.error.empty")
		case errors.Is(err, storefront.ErrItemUnavailable):
			s.renderCheckoutError(w, r, m.ID, "checkout.error.unavailable")
		case errors.Is(err, storefront.ErrDeliveryUnavailable):
			s.renderCheckoutError(w, r, m.ID, "checkout.error.delivery")
		case errors.Is(err, storefront.ErrMinOrder):
			s.renderCheckoutError(w, r, m.ID, "checkout.error.min_order")
		default:
			s.renderCheckoutError(w, r, m.ID, "checkout.error.invalid")
		}
		return
	}
	if placed.Pickup {
		http.SetCookie(w, &http.Cookie{
			Name:     pickupCodeCookie,
			Value:    placed.PublicRef + ":" + placed.PickupCode,
			Path:     "/orders/" + placed.PublicRef,
			HttpOnly: true,
			Secure:   s.cfg.CookieSecure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   15 * 60,
		})
	}
	http.Redirect(w, r, "/orders/"+placed.PublicRef, http.StatusSeeOther)
}

func (s *Server) handleOrderConfirmation(w http.ResponseWriter, r *http.Request) {
	m, ok := tenantFrom(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ref := chi.URLParam(r, "ref")
	order, err := s.storefrontSvc.OrderByRef(r.Context(), m.ID, ref)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p := s.page(r, "order.title")
	p.Title = s.tr(r, "order.title")
	view := templates.ConfirmationView{
		Ref:        order.Order.PublicRef,
		Status:     s.statusLabel(r, order.Order.Status),
		Name:       order.Order.ContactName,
		Fulfilment: s.fulfilmentLabel(r, order.Order.Fulfilment),
		Pickup:     order.Order.Fulfilment == sqlcgen.FulfilmentModePickup,
		Subtotal:   formatMoney(int64(order.Order.SubtotalMinor), order.Order.Currency),
		Fee:        formatMoney(int64(order.Order.DeliveryFeeMinor), order.Order.Currency),
		Tax:        formatMoney(int64(order.Order.TaxMinor), order.Order.Currency),
		Total:      formatMoney(int64(order.Order.TotalMinor), order.Order.Currency),
	}
	if c, err := r.Cookie(pickupCodeCookie); err == nil {
		prefix := order.Order.PublicRef + ":"
		if strings.HasPrefix(c.Value, prefix) {
			view.Code = strings.TrimPrefix(c.Value, prefix)
		}
	}
	for _, line := range order.Items {
		view.Items = append(view.Items, templates.ConfirmationLine{
			Name:     line.NameSnapshot,
			Quantity: line.Quantity,
			Total:    formatMoney(int64(line.LineTotalMinor), order.Order.Currency),
		})
	}
	p.Confirmation = view
	s.render(w, r, http.StatusOK, templates.OrderPage(p))
}

func (s *Server) handleOrderHandoff(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	orderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=code", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/orders?error=code", http.StatusSeeOther)
		return
	}
	if err := s.storefrontSvc.VerifyPickup(r.Context(), id.Merchant.ID, id.Staff.ID, orderID, r.PostFormValue("code")); err != nil {
		switch {
		case errors.Is(err, storefront.ErrNotReady):
			http.Redirect(w, r, "/dashboard/orders?error=ready", http.StatusSeeOther)
		case errors.Is(err, storefront.ErrNotFound):
			http.Redirect(w, r, "/dashboard/orders?error=notfound", http.StatusSeeOther)
		default:
			http.Redirect(w, r, "/dashboard/orders?error=code", http.StatusSeeOther)
		}
		return
	}
	http.Redirect(w, r, "/dashboard/orders?handoff=1", http.StatusSeeOther)
}

func (s *Server) renderCheckoutError(w http.ResponseWriter, r *http.Request, merchantID uuid.UUID, key string) {
	sf, err := s.storefrontSvc.Load(r.Context(), merchantID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := s.page(r, key)
	p.Checkout = templates.CheckoutView{
		Name:     sf.Settings.Name,
		Open:     sf.Open,
		Currency: sf.Settings.Currency,
		Zones:    s.zoneViews(sf.Zones, sf.Settings.Currency),
	}
	s.render(w, r, http.StatusBadRequest, templates.CheckoutPage(p))
}

func (s *Server) storefrontView(r *http.Request, sf storefront.Storefront) templates.StorefrontView {
	view := templates.StorefrontView{
		Name:         sf.Settings.Name,
		Tagline:      sf.Settings.Tagline,
		Currency:     sf.Settings.Currency,
		PrimaryColor: sf.Settings.PrimaryColor,
		Open:         sf.Open,
		Zones:        s.zoneViews(sf.Zones, sf.Settings.Currency),
	}
	for _, h := range sf.Hours {
		view.Hours = append(view.Hours, templates.HourView{
			Day:   s.tr(r, "day."+strconv.Itoa(h.Weekday)),
			Range: h.Opens + "–" + h.Closes,
		})
	}
	for _, c := range sf.Menu {
		cat := templates.StorefrontCategoryView{Name: c.Name, Description: c.Description}
		for _, it := range c.Items {
			iv := templates.StorefrontItemView{
				ID:          it.ID.String(),
				Name:        it.Name,
				Description: it.Description,
				Price:       formatMoney(int64(it.PriceMinor), sf.Settings.Currency),
				Available:   it.Available,
			}
			if it.ImageKey != "" {
				iv.ImageURL = s.mediaSvc.URL(it.ImageKey)
				iv.ThumbURL = s.mediaSvc.ThumbURL(it.ImageKey)
			}
			cat.Items = append(cat.Items, iv)
		}
		view.Menu = append(view.Menu, cat)
	}
	return view
}

func (s *Server) zoneViews(zones []storefront.Zone, currency string) []templates.ZoneView {
	out := make([]templates.ZoneView, 0, len(zones))
	for _, z := range zones {
		if !z.Active {
			continue
		}
		out = append(out, templates.ZoneView{
			Name:      z.Name,
			Postcodes: strings.Join(z.Prefixes, ", "),
			Fee:       formatMoney(int64(z.DeliveryFeeMin), currency),
			MinOrder:  formatMoney(int64(z.MinOrderMinor), currency),
		})
	}
	return out
}

type cartLine struct {
	ID  string `json:"id"`
	Qty int    `json:"qty"`
}

func parseCart(raw string) ([]storefront.CartLine, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 8192 {
		return nil, false
	}
	var payload []cartLine
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, false
	}
	if len(payload) == 0 || len(payload) > storefront.MaxItemsPerOrder {
		return nil, false
	}
	lines := make([]storefront.CartLine, 0, len(payload))
	for _, l := range payload {
		id, err := uuid.Parse(l.ID)
		if err != nil || l.Qty < 1 || l.Qty > storefront.MaxQuantityPerItem {
			return nil, false
		}
		lines = append(lines, storefront.CartLine{ItemID: id, Quantity: l.Qty})
	}
	return lines, true
}
