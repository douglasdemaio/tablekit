package platform

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/a-h/templ"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/customer"
	"tablekit.de/tablekit/internal/i18n"
	"tablekit.de/tablekit/internal/tenant"
	"tablekit.de/tablekit/web/templates"
)

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := component.Render(r.Context(), w); err != nil {
		s.log.Error("render template", "error", err)
	}
}

func (s *Server) page(r *http.Request, titleKey string) templates.Page {
	loc := i18n.LocaleFrom(r)
	p := templates.NewPage(loc, titleKey)
	p.BaseDomain = s.cfg.BaseDomain
	p.SessionTTL = humanDuration(loc, s.cfg.SessionTTL)
	if v, err := r.Cookie(s.csrfCookieName()); err == nil {
		p.CSRFToken = v.Value
	}
	if m, ok := tenant.FromContext(r.Context()); ok {
		p.MerchantName = m.Name
		p.MerchantSlug = m.Slug
	}
	if id, ok := auth.FromContext(r.Context()); ok {
		p.SignedIn = true
		p.StaffName = id.Staff.Name
		if p.StaffName == "" {
			p.StaffName = id.Staff.Email
		}
		p.StaffRole = roleLabel(loc, id.Role())
		p.TOTPEnabled = id.Staff.TotpEnabled
		p.CanStaff = id.Can(auth.PermStaffManage)
		p.CanSettings = id.Can(auth.PermSettings)
		p.CanOrders = id.Can(auth.PermOrdersView)
		p.CanMenu = id.Can(auth.PermMenuManage)
		p.CanDelivery = id.Can(auth.PermDelivery)
		p.CanSecurity = id.Can(auth.PermSecurity)
	}
	if id, ok := customer.FromContext(r.Context()); ok {
		p.CustomerSignedIn = true
		p.CustomerName = id.Account.Name
		if p.CustomerName == "" {
			p.CustomerName = id.Account.Email
		}
	}
	return p
}

func roleLabel(loc i18n.Locale, r auth.Role) string {
	switch r {
	case auth.RoleOwner:
		return i18n.Raw(loc, "role.owner")
	case auth.RoleManager:
		return i18n.Raw(loc, "role.manager")
	case auth.RoleKitchen:
		return i18n.Raw(loc, "role.kitchen")
	case auth.RoleDriver:
		return i18n.Raw(loc, "role.driver")
	default:
		return string(r)
	}
}

func humanDuration(loc i18n.Locale, d time.Duration) string {
	if d%time.Hour == 0 {
		hours := int(d.Hours())
		if hours == 1 {
			return i18n.Raw(loc, "time.hour")
		}
		return i18n.Raw(loc, "time.hours", "n", itoa(hours))
	}
	return d.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func (s *Server) tr(r *http.Request, key string, args ...string) string {
	return i18n.For(i18n.LocaleFrom(r)).T(key, args...)
}

func (s *Server) logRequest(r *http.Request, status int, start time.Time) {
	lvl := slog.LevelInfo
	if status >= 500 {
		lvl = slog.LevelError
	} else if status >= 400 {
		lvl = slog.LevelWarn
	}
	args := []any{
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if m, ok := tenant.FromContext(r.Context()); ok {
		args = append(args, "merchant_id", m.ID)
	}
	if id, ok := auth.FromContext(r.Context()); ok {
		args = append(args, "staff_id", id.Staff.ID)
	}
	s.log.Log(r.Context(), lvl, "request", args...)
}
