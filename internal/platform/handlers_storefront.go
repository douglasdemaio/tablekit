package platform

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/customer"
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
	cid, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login?next=%2Fcheckout", http.StatusSeeOther)
		return
	}
	sf, err := s.storefrontSvc.Load(r.Context(), m.ID)
	if err != nil {
		s.log.Error("load storefront", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	addresses, err := s.customerSvc.ListAddresses(r.Context(), cid.Account.ID)
	if err != nil {
		s.log.Error("list checkout addresses", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := s.page(r, "checkout.title")
	p.Nav = "checkout"
	p.Checkout = templates.CheckoutView{
		Name:        sf.Settings.Name,
		Open:        sf.Open,
		Currency:    sf.Settings.Currency,
		Zones:       s.zoneViews(sf.Zones, sf.Settings.Currency),
		SignedIn:    true,
		Customer:    displayName(cid.Account.Name, cid.Account.Email),
		Email:       cid.Account.Email,
		Phone:       cid.Account.Phone,
		Addresses:   s.addressViews(addresses),
		Slots:       s.slotViews(sf, time.Now()),
		CardEnabled: sf.Settings.CardEnabled,
		SharedHere:  s.customerSvc.HasShared(r.Context(), cid.Account.ID, m.ID),
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
	cid, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login?next=%2Fcheckout", http.StatusSeeOther)
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
	address := storefront.DeliveryAddress{
		Recipient: firstNonEmpty(r.PostFormValue("recipient"), cid.Account.Name),
		Phone:     firstNonEmpty(r.PostFormValue("delivery_phone"), r.PostFormValue("phone"), cid.Account.Phone),
		Line1:     r.PostFormValue("line1"),
		Line2:     r.PostFormValue("line2"),
		Postcode:  r.PostFormValue("postcode"),
		City:      r.PostFormValue("city"),
		Country:   firstNonEmpty(r.PostFormValue("country"), "DE"),
		Notes:     r.PostFormValue("delivery_notes"),
	}
	if addressID := r.PostFormValue("address_id"); addressID != "" && fulfilment == sqlcgen.FulfilmentModeSelfDelivery {
		if aid, parseErr := uuid.Parse(addressID); parseErr == nil {
			if saved, getErr := s.customerSvc.Address(r.Context(), cid.Account.ID, aid); getErr == nil {
				address = storefront.DeliveryAddress{
					Recipient: saved.Recipient, Phone: saved.Phone, Line1: saved.Line1,
					Line2: saved.Line2, Postcode: saved.Postcode, City: saved.City,
					Country: saved.Country, Notes: saved.DeliveryNotes,
				}
			}
		}
		if address.Line1 == "" {
			s.renderCheckoutError(w, r, m.ID, "checkout.error.address")
			return
		}
	}
	scheduled := time.Time{}
	if raw := strings.TrimSpace(r.PostFormValue("scheduled_for")); raw != "" && raw != "asap" {
		loc := time.Now().Location()
		if profile, pErr := s.storefrontSvc.MerchantProfile(r.Context(), m.ID); pErr == nil {
			if tz, tzErr := time.LoadLocation(profile.Timezone); tzErr == nil {
				loc = tz
			}
		}
		if t, parseErr := time.ParseInLocation("2006-01-02T15:04", raw, loc); parseErr == nil {
			scheduled = t
		} else {
			s.renderCheckoutError(w, r, m.ID, "checkout.error.slot")
			return
		}
	}
	payment := r.PostFormValue("payment")
	if payment != "card" {
		payment = "cash"
	}
	placed, err := s.storefrontSvc.PlaceOrder(r.Context(), m.ID, storefront.OrderInput{
		CustomerID:   cid.Account.ID,
		Name:         firstNonEmpty(r.PostFormValue("name"), cid.Account.Name),
		Email:        cid.Account.Email,
		Phone:        firstNonEmpty(pickupPhone(fulfilment, address), cid.Account.Phone),
		Fulfilment:   fulfilment,
		Payment:      payment,
		Address:      address,
		Notes:        r.PostFormValue("notes"),
		ScheduledFor: scheduled,
		ShareProfile: r.PostFormValue("share_profile") == "1",
		Lines:        lines,
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
	if placed.Card {
		checkoutURL, payErr := s.createStripeCheckout(r, m, cid, placed)
		if payErr != nil {
			s.log.Error("create stripe checkout", "error", payErr)
			s.renderCheckoutError(w, r, m.ID, "checkout.error.payment")
			return
		}
		http.Redirect(w, r, checkoutURL, http.StatusSeeOther) // #nosec G710 -- validated Stripe checkout host in createStripeCheckout
		return
	}
	if placed.Pickup {
		http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows deployment config
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
	if cid, ok := customer.FromContext(r.Context()); ok {
		if _, err := s.customerSvc.OrderByRef(r.Context(), m.ID, cid.Account.ID, ref); err != nil {
			http.NotFound(w, r)
			return
		}
	} else if order, err := s.storefrontSvc.OrderByRef(r.Context(), m.ID, ref); err != nil || order.Order.CustomerAccountID != nil {
		http.Redirect(w, r, "/account/login?next="+url.QueryEscape("/orders/"+ref), http.StatusSeeOther)
		return
	}
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
	order, orderErr := s.storefrontSvc.OrderByID(r.Context(), id.Merchant.ID, orderID)
	if orderErr != nil {
		http.Redirect(w, r, "/dashboard/orders?error=notfound", http.StatusSeeOther)
		return
	}
	if id.Role() == auth.RoleDriver && order.Fulfilment != sqlcgen.FulfilmentModeSelfDelivery {
		http.Redirect(w, r, "/dashboard/orders?error=notfound", http.StatusSeeOther)
		return
	}
	if id.Role() == auth.RoleDriver {
		job, jobErr := s.storefrontSvc.DeliveryFor(r.Context(), id.Merchant.ID, orderID)
		if jobErr != nil || job.AssigneeStaffID == nil || *job.AssigneeStaffID != id.Staff.ID {
			http.Redirect(w, r, "/dashboard/orders?error=notfound", http.StatusSeeOther)
			return
		}
	}
	var verifyErr error
	if order.Fulfilment == sqlcgen.FulfilmentModeSelfDelivery {
		verifyErr = s.storefrontSvc.VerifyDelivery(r.Context(), id.Merchant.ID, id.Staff.ID, orderID, r.PostFormValue("code"))
	} else {
		verifyErr = s.storefrontSvc.VerifyPickup(r.Context(), id.Merchant.ID, id.Staff.ID, orderID, r.PostFormValue("code"))
	}
	if err := verifyErr; err != nil {
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
	cid, signed := customer.FromContext(r.Context())
	view := templates.CheckoutView{
		Name:        sf.Settings.Name,
		Template:    sf.Settings.Template,
		Open:        sf.Open,
		Currency:    sf.Settings.Currency,
		Zones:       s.zoneViews(sf.Zones, sf.Settings.Currency),
		SignedIn:    signed,
		Slots:       s.slotViews(sf, time.Now()),
		CardEnabled: sf.Settings.CardEnabled,
	}
	if signed {
		view.Customer = displayName(cid.Account.Name, cid.Account.Email)
		view.Email = cid.Account.Email
		view.Phone = cid.Account.Phone
		view.SharedHere = s.customerSvc.HasShared(r.Context(), cid.Account.ID, merchantID)
		if addresses, err := s.customerSvc.ListAddresses(r.Context(), cid.Account.ID); err == nil {
			view.Addresses = s.addressViews(addresses)
		}
	}
	p.Checkout = view
	s.render(w, r, http.StatusBadRequest, templates.CheckoutPage(p))
}

func (s *Server) storefrontView(r *http.Request, sf storefront.Storefront) templates.StorefrontView {
	address := strings.TrimSpace(strings.Join([]string{sf.Settings.Street, sf.Settings.Line2, sf.Settings.Postcode + " " + sf.Settings.City}, " "))
	view := templates.StorefrontView{
		Name:         sf.Settings.Name,
		Tagline:      sf.Settings.Tagline,
		Description:  sf.Settings.Description,
		Cuisine:      sf.Settings.Cuisine,
		Address:      strings.TrimSpace(address),
		Phone:        sf.Settings.Phone,
		Email:        sf.Settings.Email,
		Currency:     sf.Settings.Currency,
		PrimaryColor: sf.Settings.PrimaryColor,
		AccentColor:  sf.Settings.AccentColor,
		Template:     sf.Settings.Template,
		Open:         sf.Open,
		Zones:        s.zoneViews(sf.Zones, sf.Settings.Currency),
	}
	if sf.Settings.LogoKey != "" {
		view.LogoURL = s.mediaSvc.URL(sf.Settings.LogoKey)
	}
	if sf.Settings.CoverKey != "" {
		view.CoverURL = s.mediaSvc.URL(sf.Settings.CoverKey)
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
			if !it.Available {
				iv.SoldOutText = s.tr(r, "storefront.sold_out")
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

func (s *Server) slotViews(sf storefront.Storefront, now time.Time) []templates.SlotView {
	loc := sf.Settings.Timezone
	now = now.In(loc)
	out := make([]templates.SlotView, 0, 48)
	for _, slot := range storefront.Slots(sf.Hours, now, 7*24*2) {
		out = append(out, templates.SlotView{
			Value: slot.Format("2006-01-02T15:04"),
			Label: slot.Format("Mon 02 Jan 15:04"),
		})
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func pickupPhone(f sqlcgen.FulfilmentModeEnum, a storefront.DeliveryAddress) string {
	if f == sqlcgen.FulfilmentModeSelfDelivery {
		return a.Phone
	}
	return ""
}

func displayName(name, email string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return email
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
