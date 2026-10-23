package platform

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/customer"
	"tablekit.de/tablekit/internal/db/sqlcgen"
	"tablekit.de/tablekit/internal/tenant"
	"tablekit.de/tablekit/web/templates"
)

func (s *Server) handleCustomerLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := customer.FromContext(r.Context()); ok {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther) // #nosec G710 -- safeNext only permits local paths
		return
	}
	p := s.page(r, "account.login_title")
	p.Nav = "account"
	p.Login = templates.CustomerLoginView{
		Next:      safeNext(r.URL.Query().Get("next")),
		DevNotice: s.cfg.SMTPHost == "",
	}
	s.render(w, r, http.StatusOK, templates.CustomerLoginPage(p))
}

func (s *Server) handleCustomerLoginRequest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderCustomerLoginError(w, r, "account.error.invalid")
		return
	}
	next := safeNext(r.PostFormValue("next"))
	email := strings.TrimSpace(r.PostFormValue("email"))
	if !s.auth.Limiter.Allow("customer-code:ip:"+s.clientIP(r), 10, 10*time.Minute) {
		s.renderCustomerLoginError(w, r, "account.error.toomany")
		return
	}
	if err := s.customerSvc.RequestCode(r.Context(), email, s.clientIP(r)); err != nil {
		switch {
		case errors.Is(err, customer.ErrRateLimited):
			s.renderCustomerLoginError(w, r, "account.error.toomany")
		case errors.Is(err, customer.ErrInvalidInput):
			s.renderCustomerLoginError(w, r, "account.error.email")
		default:
			s.log.Error("request customer code", "error", err)
			s.renderCustomerLoginError(w, r, "account.error.send")
		}
		return
	}
	p := s.page(r, "account.check_email")
	p.Nav = "account"
	p.Notice = s.tr(r, "account.code_sent")
	p.Login = templates.CustomerLoginView{
		Next:      next,
		DevNotice: s.cfg.SMTPHost == "",
		Email:     email,
	}
	s.render(w, r, http.StatusOK, templates.CustomerLoginPage(p))
}

func (s *Server) handleCustomerLoginVerify(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderCustomerLoginError(w, r, "account.error.invalid")
		return
	}
	next := safeNext(r.PostFormValue("next"))
	email := strings.TrimSpace(r.PostFormValue("email"))
	if !s.auth.Limiter.Allow("customer-verify:ip:"+s.clientIP(r), 20, 10*time.Minute) {
		s.renderCustomerLoginError(w, r, "account.error.toomany")
		return
	}
	res, err := s.customerSvc.VerifyCode(r.Context(), email, r.PostFormValue("code"), s.clientIP(r), r.UserAgent())
	if err != nil {
		if errors.Is(err, customer.ErrInvalidCode) {
			s.renderCustomerLoginError(w, r, "account.error.code")
		} else {
			s.log.Error("verify customer code", "error", err)
			s.renderCustomerLoginError(w, r, "account.error.invalid")
		}
		return
	}
	s.setCustomerCookie(w, res.Token, res.Expires)
	http.Redirect(w, r, next, http.StatusSeeOther) // #nosec G710 -- next is validated by safeNext
}

func (s *Server) handleCustomerLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.customerCookieName()); err == nil {
		_ = s.customerSvc.Logout(r.Context(), c.Value)
	}
	s.clearCustomerCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) renderCustomerLoginError(w http.ResponseWriter, r *http.Request, key string) {
	p := s.page(r, "account.login_title")
	p.Nav = "account"
	p.Error = s.tr(r, key)
	p.Login = templates.CustomerLoginView{DevNotice: s.cfg.SMTPHost == ""}
	s.render(w, r, http.StatusBadRequest, templates.CustomerLoginPage(p))
}

func (s *Server) handleAccountPage(w http.ResponseWriter, r *http.Request) {
	id, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login?next=%2Faccount", http.StatusSeeOther)
		return
	}
	m, _ := tenant.FromContext(r.Context())
	addresses, err := s.customerSvc.ListAddresses(r.Context(), id.Account.ID)
	if err != nil {
		s.log.Error("list addresses", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	shares, err := s.customerSvc.Shares(r.Context(), id.Account.ID)
	if err != nil {
		s.log.Error("list shares", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := s.page(r, "account.title")
	p.Nav = "account"
	p.Account = templates.AccountView{
		Name:       id.Account.Name,
		Email:      id.Account.Email,
		Phone:      id.Account.Phone,
		Addresses:  s.addressViews(addresses),
		SharedHere: s.customerSvc.HasShared(r.Context(), id.Account.ID, m.ID),
		Restaurant: m.Name,
		Customer:   true,
	}
	for _, sh := range shares {
		p.Account.Shares = append(p.Account.Shares, templates.ShareView{
			MerchantID: sh.ID.String(),
			Name:       sh.Name,
			URL:        s.cfg.PublicScheme + "://" + sh.Slug + "." + s.cfg.BaseDomain + "/",
			Created:    formatTime(sh.CreatedAt),
		})
	}
	if r.URL.Query().Get("saved") == "1" {
		p.Notice = s.tr(r, "account.saved")
	}
	s.render(w, r, http.StatusOK, templates.AccountPage(p))
}

func (s *Server) handleAccountProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	if err := s.customerSvc.UpdateProfile(r.Context(), id.Account.ID, r.PostFormValue("name"), r.PostFormValue("phone")); err != nil {
		http.Redirect(w, r, "/account?error=profile", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/account?saved=1", http.StatusSeeOther)
}

func (s *Server) handleAccountAddressCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login", http.StatusSeeOther)
		return
	}
	in, ok := parseAddressForm(w, r)
	if !ok {
		http.Redirect(w, r, "/account?error=address", http.StatusSeeOther)
		return
	}
	if _, err := s.customerSvc.CreateAddress(r.Context(), id.Account.ID, in); err != nil {
		http.Redirect(w, r, "/account?error=address", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/account?saved=1#addresses", http.StatusSeeOther)
}

func (s *Server) handleAccountAddressUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login", http.StatusSeeOther)
		return
	}
	addressID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/account?error=address", http.StatusSeeOther)
		return
	}
	in, ok := parseAddressForm(w, r)
	if !ok {
		http.Redirect(w, r, "/account?error=address", http.StatusSeeOther)
		return
	}
	if err := s.customerSvc.UpdateAddress(r.Context(), id.Account.ID, addressID, in); err != nil {
		http.Redirect(w, r, "/account?error=address", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/account?saved=1#addresses", http.StatusSeeOther)
}

func (s *Server) handleAccountAddressDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login", http.StatusSeeOther)
		return
	}
	addressID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/account?error=address", http.StatusSeeOther)
		return
	}
	if err := s.customerSvc.DeleteAddress(r.Context(), id.Account.ID, addressID); err != nil {
		http.Redirect(w, r, "/account?error=address", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/account?saved=1#addresses", http.StatusSeeOther)
}

func (s *Server) handleAccountAddressDefault(w http.ResponseWriter, r *http.Request) {
	id, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login", http.StatusSeeOther)
		return
	}
	addressID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/account?error=address", http.StatusSeeOther)
		return
	}
	if err := s.customerSvc.SetDefaultAddress(r.Context(), id.Account.ID, addressID); err != nil {
		http.Redirect(w, r, "/account?error=address", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/account?saved=1#addresses", http.StatusSeeOther)
}

func (s *Server) handleAccountShareRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login", http.StatusSeeOther)
		return
	}
	merchantID, err := uuid.Parse(chi.URLParam(r, "merchant"))
	if err != nil {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	if err := s.customerSvc.RevokeShare(r.Context(), id.Account.ID, merchantID); err != nil {
		http.Redirect(w, r, "/account?error=share", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/account?saved=1", http.StatusSeeOther)
}

func (s *Server) handleAccountOrders(w http.ResponseWriter, r *http.Request) {
	id, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login?next=%2Faccount%2Forders", http.StatusSeeOther)
		return
	}
	m, _ := tenant.FromContext(r.Context())
	rows, err := s.customerSvc.Orders(r.Context(), m.ID, id.Account.ID)
	if err != nil {
		s.log.Error("list customer orders", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := s.page(r, "account.orders_title")
	p.Nav = "account"
	for _, row := range rows {
		v := templates.AccountOrderView{
			Ref:         row.PublicRef,
			StatusLabel: s.statusLabel(r, row.Status),
			Fulfilment:  s.fulfilmentLabel(r, row.Fulfilment),
			Payment:     s.paymentLabel(r, row.PaymentStatusEnum),
			Total:       formatMoney(int64(row.TotalMinor), row.Currency),
			Placed:      formatTime(row.CreatedAt),
		}
		if row.ScheduledFor.Valid {
			v.Scheduled = formatTime(row.ScheduledFor)
		}
		p.AccountOrders = append(p.AccountOrders, v)
	}
	s.render(w, r, http.StatusOK, templates.AccountOrdersPage(p))
}

func (s *Server) handleAccountOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := customer.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/account/login", http.StatusSeeOther)
		return
	}
	m, _ := tenant.FromContext(r.Context())
	ref := chi.URLParam(r, "ref")
	row, err := s.customerSvc.OrderByRef(r.Context(), m.ID, id.Account.ID, ref)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	order, err := s.storefrontSvc.OrderByRef(r.Context(), m.ID, ref)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p := s.page(r, "account.order_title")
	p.Nav = "account"
	v := templates.AccountOrderView{
		Ref:         row.PublicRef,
		Status:      s.statusLabel(r, row.Status),
		StatusLabel: s.statusLabel(r, row.Status),
		Fulfilment:  s.fulfilmentLabel(r, row.Fulfilment),
		Payment:     s.paymentLabel(r, row.PaymentStatusEnum),
		Total:       formatMoney(int64(row.TotalMinor), row.Currency),
		Subtotal:    formatMoney(int64(row.SubtotalMinor), row.Currency),
		Fee:         formatMoney(int64(row.DeliveryFeeMinor), row.Currency),
		Tax:         formatMoney(int64(row.TaxMinor), row.Currency),
		Placed:      formatTime(row.CreatedAt),
		Pickup:      row.Fulfilment == "pickup",
		ContactName: row.ContactName,
	}
	if row.ScheduledFor.Valid {
		v.Scheduled = formatTime(row.ScheduledFor)
	}
	if c, err := r.Cookie(pickupCodeCookie); err == nil {
		prefix := ref + ":"
		if strings.HasPrefix(c.Value, prefix) {
			v.Code = strings.TrimPrefix(c.Value, prefix)
		}
	}
	for _, line := range order.Items {
		v.Items = append(v.Items, templates.ConfirmationLine{
			Name: line.NameSnapshot, Quantity: line.Quantity,
			Total: formatMoney(int64(line.LineTotalMinor), row.Currency),
		})
	}
	p.AccountOrder = v
	s.render(w, r, http.StatusOK, templates.AccountOrderPage(p))
}

func (s *Server) addressViews(rows []sqlcgen.CustomerAddress) []templates.AddressView {
	out := make([]templates.AddressView, 0, len(rows))
	for _, a := range rows {
		out = append(out, templates.AddressView{
			ID: a.ID.String(), Label: a.Label, Recipient: a.Recipient, Phone: a.Phone,
			Line1: a.Line1, Line2: a.Line2, Postcode: a.Postcode, City: a.City,
			Country: a.Country, Notes: a.DeliveryNotes, IsDefault: a.IsDefault,
			Summary: strings.TrimSpace(a.Line1 + " " + a.Line2 + ", " + a.Postcode + " " + a.City),
		})
	}
	return out
}

func parseAddressForm(w http.ResponseWriter, r *http.Request) (customer.AddressInput, bool) {
	if err := r.ParseForm(); err != nil {
		return customer.AddressInput{}, false
	}
	return customer.AddressInput{
		Label:         r.PostFormValue("label"),
		Recipient:     r.PostFormValue("recipient"),
		Phone:         r.PostFormValue("phone"),
		Line1:         r.PostFormValue("line1"),
		Line2:         r.PostFormValue("line2"),
		Postcode:      r.PostFormValue("postcode"),
		City:          r.PostFormValue("city"),
		Country:       r.PostFormValue("country"),
		DeliveryNotes: r.PostFormValue("delivery_notes"),
		IsDefault:     r.PostFormValue("is_default") == "1",
	}, true
}

func safeNext(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.Contains(v, "\\") {
		return "/account"
	}
	return v
}
