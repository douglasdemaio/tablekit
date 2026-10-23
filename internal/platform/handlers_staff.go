package platform

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/i18n"
	"tablekit.de/tablekit/internal/staff"
	"tablekit.de/tablekit/web/templates"
)

func (s *Server) handleStaffPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.staffPage(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if r.URL.Query().Get("saved") == "1" {
		p.Notice = s.tr(r, "staff.saved")
	}
	s.render(w, r, http.StatusOK, templates.StaffPage(p))
}

func (s *Server) renderStaffError(w http.ResponseWriter, r *http.Request, key string) {
	p, err := s.staffPage(r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p.Error = s.tr(r, key)
	s.render(w, r, http.StatusBadRequest, templates.StaffPage(p))
}

func (s *Server) staffPage(r *http.Request) (templates.Page, error) {
	id, _ := auth.FromContext(r.Context())
	p := s.page(r, "staff.title")
	p.Nav = "staff"

	employees, err := s.staffSvc.List(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("list staff", "error", err)
		return p, err
	}
	for _, u := range employees {
		p.Staff = append(p.Staff, templates.StaffView{
			ID:         u.ID.String(),
			Name:       u.Name,
			Email:      u.Email,
			Role:       string(u.Role),
			RoleLabel:  roleLabel(i18n.LocaleFrom(r), auth.Role(u.Role)),
			Disabled:   u.DisabledAt.Valid,
			MFAEnabled: u.TotpEnabled,
			IsSelf:     u.ID == id.Staff.ID,
		})
	}

	sessions, err := s.staffSvc.ActiveSessions(r.Context(), id.Merchant.ID, id.Session.ID)
	if err != nil {
		s.log.Error("list team sessions", "error", err)
		return p, err
	}
	for _, sess := range sessions {
		p.TeamSessions = append(p.TeamSessions, templates.TeamSessionView{
			ID:        sess.ID.String(),
			StaffName: sess.StaffName,
			Email:     sess.Email,
			RoleLabel: roleLabel(i18n.LocaleFrom(r), sess.Role),
			IP:        displayOrDash(sess.IP),
			UserAgent: truncate(sess.UserAgent, 80),
			LastSeen:  sess.LastSeen,
			Current:   sess.Current,
		})
	}
	return p, nil
}

func (s *Server) handleStaffCreate(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	password := r.PostFormValue("password")
	role, err := auth.ParseRole(r.PostFormValue("role"))
	if err != nil {
		s.renderStaffError(w, r, "staff.error.role")
		return
	}
	if _, err := s.staffSvc.Create(r.Context(), id.Merchant.ID, email, name, password, role); err != nil {
		s.log.Warn("create staff rejected", "error", err)
		if errors.Is(err, staff.ErrInvalidInput) && len(password) < staff.MinPasswordLength {
			s.renderStaffError(w, r, "staff.error.password")
		} else {
			s.renderStaffError(w, r, "staff.error.invalid")
		}
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "staff.created", "staff", email, s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/staff?saved=1", http.StatusSeeOther)
}

func (s *Server) handleStaffUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	staffID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	role, err := auth.ParseRole(r.PostFormValue("role"))
	if err != nil {
		s.renderStaffError(w, r, "staff.error.role")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	mfaRequired := r.PostFormValue("mfa_required") == "1"
	if err := s.staffSvc.Update(r.Context(), id.Merchant.ID, staffID, name, role, mfaRequired); err != nil {
		s.log.Warn("update staff rejected", "error", err)
		if err == staff.ErrLastOwner {
			s.renderStaffError(w, r, "staff.error.last_owner")
			return
		}
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "staff.updated", "staff", staffID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/staff?saved=1", http.StatusSeeOther)
}

func (s *Server) handleStaffResetPassword(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	staffID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	if err := s.staffSvc.ResetPassword(r.Context(), id.Merchant.ID, staffID, r.PostFormValue("password")); err != nil {
		s.log.Warn("reset password rejected", "error", err)
		s.renderStaffError(w, r, "staff.error.password")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "staff.password_reset", "staff", staffID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/staff?saved=1", http.StatusSeeOther)
}

func (s *Server) handleStaffDisable(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	staffID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	if err := s.staffSvc.SetDisabled(r.Context(), id.Merchant.ID, staffID, true); err != nil {
		if err == staff.ErrLastOwner {
			s.renderStaffError(w, r, "staff.error.last_owner")
			return
		}
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "staff.disabled", "staff", staffID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/staff?saved=1", http.StatusSeeOther)
}

func (s *Server) handleStaffEnable(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	staffID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	if err := s.staffSvc.SetDisabled(r.Context(), id.Merchant.ID, staffID, false); err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "staff.enabled", "staff", staffID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/staff?saved=1", http.StatusSeeOther)
}

func (s *Server) handleStaffSessionRevoke(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	if err := s.staffSvc.RevokeSession(r.Context(), id.Merchant.ID, sessionID); err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "staff.session_revoked", "session", sessionID.String(), s.clientIP(r), nil)
	if sessionID == id.Session.ID {
		s.clearSessionCookies(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard/staff?saved=1", http.StatusSeeOther)
}

func (s *Server) handleStaffSessionsRevoke(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	staffID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	if err := s.staffSvc.RevokeStaffSessions(r.Context(), id.Merchant.ID, staffID); err != nil {
		s.renderStaffError(w, r, "staff.error.invalid")
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "staff.sessions_revoked", "staff", staffID.String(), s.clientIP(r), nil)
	if staffID == id.Staff.ID {
		s.clearSessionCookies(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard/staff?saved=1", http.StatusSeeOther)
}

func displayOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
