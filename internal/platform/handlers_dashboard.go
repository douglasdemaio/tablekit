package platform

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/web/templates"
)

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	p := s.page(r, "dashboard.title")
	p.Nav = "dashboard"

	counts, err := s.orderSvc.Counts(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("order counts", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	menuCount, err := s.menuSvc.Count(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("menu count", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p.Stats = templates.DashboardStats{
		OpenOrders:  int(counts.Open),
		TodayOrders: int(counts.Today),
		Revenue:     formatMoney(counts.Total, "EUR"),
		MenuItems:   int(menuCount),
	}

	s.render(w, r, http.StatusOK, templates.DashboardPage(p))
}

func (s *Server) handleSecurityPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.securityPage(r, "", "")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, r, http.StatusOK, templates.SecurityPage(p))
}

func (s *Server) renderSecurityError(w http.ResponseWriter, r *http.Request, status int, key string) {
	p, err := s.securityPage(r, key, "")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, r, status, templates.SecurityPage(p))
}

func (s *Server) securityPage(r *http.Request, errKey, notice string) (templates.Page, error) {
	id, _ := auth.FromContext(r.Context())
	p := s.page(r, "security.title")
	p.Nav = "security"
	if errKey != "" {
		p.Error = s.tr(r, errKey)
	}
	p.Notice = notice

	passkeys, err := s.auth.ListPasskeys(r.Context(), id.Merchant.ID, id.Staff.ID)
	if err != nil {
		s.log.Error("list passkeys", "error", err)
		return p, err
	}
	for _, pk := range passkeys {
		p.Passkeys = append(p.Passkeys, templates.PasskeyView{
			ID:       pk.ID.String(),
			Name:     pk.Name,
			Created:  formatTime(pk.CreatedAt),
			LastUsed: formatOptionalTime(pk.LastUsedAt),
		})
	}

	sessions, err := s.auth.ListSessions(r.Context(), id.Merchant.ID, id.Staff.ID)
	if err != nil {
		s.log.Error("list sessions", "error", err)
		return p, err
	}
	for _, sess := range sessions {
		ip := "—"
		if sess.Ip != nil {
			ip = fmt.Sprint(sess.Ip)
		}
		ua := sess.UserAgent
		if len(ua) > 80 {
			ua = ua[:80] + "…"
		}
		p.Sessions = append(p.Sessions, templates.SessionView{
			ID:        sess.ID.String(),
			IP:        ip,
			UserAgent: ua,
			Created:   formatTime(sess.CreatedAt),
			LastSeen:  formatTime(sess.LastSeenAt),
			Current:   sess.ID == id.Session.ID,
		})
	}
	return p, nil
}

func (s *Server) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if id.Staff.TotpEnabled {
		s.renderSecurityError(w, r, http.StatusBadRequest, "security.totp_started")
		return
	}
	if _, _, err := s.auth.BeginTOTPSetup(r.Context(), id.Merchant.ID, id.Staff.ID); err != nil {
		s.log.Error("begin totp setup", "error", err)
		s.renderSecurityError(w, r, http.StatusInternalServerError, "security.error.totp_start")
		return
	}
	http.Redirect(w, r, "/setup/mfa", http.StatusSeeOther)
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if id.Role() == auth.RoleOwner {
		passkeys, err := s.auth.ListPasskeys(r.Context(), id.Merchant.ID, id.Staff.ID)
		if err != nil {
			s.renderSecurityError(w, r, http.StatusInternalServerError, "security.error.load")
			return
		}
		if len(passkeys) == 0 {
			s.renderSecurityError(w, r, http.StatusBadRequest, "security.error.owner_mfa")
			return
		}
	}
	if err := s.auth.DisableTOTP(r.Context(), id.Merchant.ID, id.Staff.ID); err != nil {
		s.log.Error("disable totp", "error", err)
		s.renderSecurityError(w, r, http.StatusInternalServerError, "security.error.totp_disable")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "auth.totp_disabled", "staff", id.Staff.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/security", http.StatusSeeOther)
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	passkeyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderSecurityError(w, r, http.StatusBadRequest, "security.error.invalid")
		return
	}
	if id.Role() == auth.RoleOwner {
		passkeys, err := s.auth.ListPasskeys(r.Context(), id.Merchant.ID, id.Staff.ID)
		if err != nil {
			s.renderSecurityError(w, r, http.StatusInternalServerError, "security.error.load")
			return
		}
		if !id.Staff.TotpEnabled && len(passkeys) <= 1 {
			s.renderSecurityError(w, r, http.StatusBadRequest, "security.error.owner_mfa")
			return
		}
	}
	if err := s.auth.DeletePasskey(r.Context(), id.Merchant.ID, id.Staff.ID, passkeyID); err != nil {
		s.log.Error("delete passkey", "error", err)
		s.renderSecurityError(w, r, http.StatusInternalServerError, "security.error.passkey")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "auth.passkey_deleted", "passkey", passkeyID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/security", http.StatusSeeOther)
}

func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderSecurityError(w, r, http.StatusBadRequest, "security.error.invalid")
		return
	}
	if err := s.auth.RevokeSession(r.Context(), id.Merchant.ID, id.Staff.ID, sessionID); err != nil {
		s.log.Error("revoke session", "error", err)
		s.renderSecurityError(w, r, http.StatusInternalServerError, "security.error.session")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "auth.session_revoked", "session", sessionID.String(), s.clientIP(r), nil)
	if sessionID == id.Session.ID {
		s.clearSessionCookies(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard/security", http.StatusSeeOther)
}

func (s *Server) handleSessionRevokeOthers(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := s.auth.RevokeOtherSessions(r.Context(), id.Merchant.ID, id.Staff.ID, id.Session.ID); err != nil {
		s.log.Error("revoke other sessions", "error", err)
		s.renderSecurityError(w, r, http.StatusInternalServerError, "security.error.sessions")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "auth.sessions_revoked_others", "staff", id.Staff.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/security", http.StatusSeeOther)
}
