package platform

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/tenant"
	"tablekit.de/tablekit/web/templates"
)

func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenant.FromContext(r.Context()); ok {
		s.handleStorefront(w, r)
		return
	}
	s.render(w, r, http.StatusOK, templates.Landing(s.page(r, "tablekit")))
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if id, ok := auth.FromContext(r.Context()); ok {
		switch id.Session.State {
		case "verified":
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		case "pending":
			http.Redirect(w, r, "/login/mfa", http.StatusSeeOther)
			return
		default:
			http.Redirect(w, r, "/setup/mfa", http.StatusSeeOther)
			return
		}
	}
	s.render(w, r, http.StatusOK, templates.LoginPage(s.page(r, "login.title")))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderLoginError(w, r, http.StatusBadRequest, "login.error.invalid")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	password := r.PostFormValue("password")
	if email == "" || password == "" || len(email) > 254 || len(password) > 1024 {
		s.renderLoginError(w, r, http.StatusBadRequest, "login.error.missing")
		return
	}

	ip := s.clientIP(r)
	if !s.auth.Limiter.Allow("login:ip:"+ip, 10, 15*time.Minute) ||
		!s.auth.Limiter.Allow("login:email:"+email, 5, 15*time.Minute) {
		s.renderLoginError(w, r, http.StatusTooManyRequests, "login.error.toomany")
		return
	}

	m := mustMerchant(r)
	result, err := s.auth.LoginPassword(r.Context(), m.ID, email, password, ip, r.UserAgent())
	if err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			s.renderLoginError(w, r, http.StatusTooManyRequests, "login.error.toomany")
			return
		}
		s.auditLoginFailure(r, "invalid_credentials")
		s.renderLoginError(w, r, http.StatusUnauthorized, "login.error.bad")
		return
	}

	s.setSessionCookies(w, result.Token, result.CSRFToken, result.Session.State == "verified")
	s.audit(r, m.ID, "staff", &result.Staff.ID, "auth.login", "staff", result.Staff.ID.String(), ip, nil)
	http.Redirect(w, r, result.Next, http.StatusSeeOther)
}

func (s *Server) handleMFAPage(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	switch id.Session.State {
	case "verified":
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	case "pending_setup":
		http.Redirect(w, r, "/setup/mfa", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, templates.MFAPage(s.page(r, "mfa.title")))
}

func (s *Server) handleMFA(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if id.Session.State != "pending" {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderMFAError(w, r, http.StatusBadRequest, "login.error.invalid")
		return
	}
	code := strings.TrimSpace(r.PostFormValue("code"))
	if len(code) < 6 || len(code) > 12 {
		s.renderMFAError(w, r, http.StatusBadRequest, "mfa.error.length")
		return
	}
	if !s.auth.Limiter.Allow("mfa:session:"+id.Session.ID.String(), 5, 5*time.Minute) {
		s.renderMFAError(w, r, http.StatusTooManyRequests, "login.error.toomany")
		return
	}
	result, err := s.auth.VerifyLoginTOTP(r.Context(), id.Merchant.ID, id.Session, code)
	if err != nil {
		s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "auth.mfa_failed", "staff", id.Staff.ID.String(), s.clientIP(r), nil)
		s.renderMFAError(w, r, http.StatusUnauthorized, "mfa.error.bad")
		return
	}
	s.setSessionCookies(w, result.Token, result.CSRFToken, true)
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "auth.mfa", "staff", id.Staff.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if id, ok := auth.FromContext(r.Context()); ok {
		if err := s.auth.RevokeSession(r.Context(), id.Merchant.ID, id.Staff.ID, id.Session.ID); err != nil {
			s.log.Error("revoke session on logout", "error", err)
		}
		s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "auth.logout", "session", id.Session.ID.String(), s.clientIP(r), nil)
	}
	s.clearSessionCookies(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	secret, uri, err := s.auth.TOTPSetupState(r.Context(), id.Merchant.ID, id.Staff.ID)
	if err != nil {
		secret, uri, err = s.auth.BeginTOTPSetup(r.Context(), id.Merchant.ID, id.Staff.ID)
		if err != nil {
			s.renderSetupError(w, r, http.StatusInternalServerError, "setup.error.begin")
			return
		}
	}
	s.renderSetup(w, r, http.StatusOK, secret, uri, "", "")
}

func (s *Server) handleSetupTOTPVerify(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderSetupError(w, r, http.StatusBadRequest, "login.error.invalid")
		return
	}
	code := strings.TrimSpace(r.PostFormValue("code"))
	if len(code) < 6 || len(code) > 12 {
		s.renderCurrentSetup(w, r, http.StatusBadRequest, "mfa.error.length")
		return
	}
	if !s.auth.Limiter.Allow("setup-mfa:session:"+id.Session.ID.String(), 8, 10*time.Minute) {
		s.renderSetupError(w, r, http.StatusTooManyRequests, "login.error.toomany")
		return
	}

	if id.Session.State == "verified" {
		if err := s.auth.ConfirmTOTPSetup(r.Context(), id.Merchant.ID, id.Staff.ID, code); err != nil {
			s.renderCurrentSetup(w, r, http.StatusUnauthorized, "mfa.error.bad")
			return
		}
		s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "auth.totp_enabled", "staff", id.Staff.ID.String(), s.clientIP(r), nil)
		http.Redirect(w, r, "/dashboard/security", http.StatusSeeOther)
		return
	}

	if err := s.auth.ConfirmTOTPSetup(r.Context(), id.Merchant.ID, id.Staff.ID, code); err != nil {
		s.renderCurrentSetup(w, r, http.StatusUnauthorized, "mfa.error.bad")
		return
	}
	result, err := s.auth.UpgradeSession(r.Context(), id.Merchant.ID, id.Session.ID)
	if err != nil {
		s.renderSetupError(w, r, http.StatusInternalServerError, "setup.error.session")
		return
	}
	s.setSessionCookies(w, result.Token, result.CSRFToken, true)
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "auth.totp_enabled", "staff", id.Staff.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": s.tr(r, "error.not_signed_in")})
		return
	}
	begin, err := s.auth.BeginPasskeyRegistration(r.Context(), id.Merchant.ID, id.Staff.ID)
	if err != nil {
		s.log.Error("begin passkey registration", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.tr(r, "passkey.error.start")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge_id": begin.ChallengeID.String(),
		"options":      begin.Options,
	})
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": s.tr(r, "error.not_signed_in")})
		return
	}
	var body struct {
		ChallengeID string          `json:"challenge_id"`
		Credential  json.RawMessage `json:"credential"`
		Name        string          `json:"name"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "login.error.invalid")})
		return
	}
	challengeID, err := uuid.Parse(body.ChallengeID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "login.error.invalid")})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	if err := s.auth.FinishPasskeyRegistration(r.Context(), id.Merchant.ID, id.Staff.ID, challengeID, body.Credential, name, r); err != nil {
		s.log.Warn("finish passkey registration", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "passkey.error.register")})
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "auth.passkey_added", "staff", id.Staff.ID.String(), s.clientIP(r), nil)

	if id.Session.State != "verified" {
		result, err := s.auth.UpgradeSession(r.Context(), id.Merchant.ID, id.Session.ID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": s.tr(r, "setup.error.session")})
			return
		}
		s.setSessionCookies(w, result.Token, result.CSRFToken, true)
		writeJSON(w, http.StatusOK, map[string]string{"next": "/dashboard"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"next": ""})
}

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	m := mustMerchant(r)
	var body struct {
		Email string `json:"email"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "login.error.invalid")})
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || len(email) > 254 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "login.error.email")})
		return
	}
	ip := s.clientIP(r)
	if !s.auth.Limiter.Allow("passkey-begin:ip:"+ip, 10, 15*time.Minute) ||
		!s.auth.Limiter.Allow("passkey-begin:email:"+email, 10, 15*time.Minute) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": s.tr(r, "login.error.toomany")})
		return
	}
	begin, err := s.auth.BeginPasskeyLogin(r.Context(), m.ID, email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "passkey.error.available")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge_id": begin.ChallengeID.String(),
		"options":      begin.Options,
	})
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	m := mustMerchant(r)
	var body struct {
		ChallengeID string          `json:"challenge_id"`
		Credential  json.RawMessage `json:"credential"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "login.error.invalid")})
		return
	}
	challengeID, err := uuid.Parse(body.ChallengeID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "login.error.invalid")})
		return
	}
	ip := s.clientIP(r)
	if !s.auth.Limiter.Allow("passkey-finish:ip:"+ip, 10, 15*time.Minute) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": s.tr(r, "login.error.toomany")})
		return
	}
	result, err := s.auth.FinishPasskeyLogin(r.Context(), m.ID, challengeID, body.Credential, r)
	if err != nil {
		s.log.Warn("passkey login failed", "error", err)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": s.tr(r, "passkey.error.login")})
		return
	}
	if result.Session.State == "verified" {
		s.setSessionCookies(w, result.Token, result.CSRFToken, true)
	} else {
		s.setSessionCookies(w, result.Token, result.CSRFToken, false)
	}
	s.audit(r, m.ID, "staff", &result.Staff.ID, "auth.passkey_login", "staff", result.Staff.ID.String(), ip, nil)
	writeJSON(w, http.StatusOK, map[string]string{"next": result.Next})
}

func (s *Server) renderLoginError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	p := s.page(r, "login.title")
	p.Error = s.tr(r, msg)
	s.render(w, r, status, templates.LoginPage(p))
}

func (s *Server) renderMFAError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	p := s.page(r, "mfa.title")
	p.Error = s.tr(r, msg)
	s.render(w, r, status, templates.MFAPage(p))
}

func (s *Server) renderSetupError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	p := s.page(r, "setup.title")
	p.Error = s.tr(r, msg)
	s.render(w, r, status, templates.SetupPage(p))
}

func (s *Server) renderCurrentSetup(w http.ResponseWriter, r *http.Request, status int, msg string) {
	id, _ := auth.FromContext(r.Context())
	secret, uri, err := s.auth.TOTPSetupState(r.Context(), id.Merchant.ID, id.Staff.ID)
	if err != nil {
		s.renderSetupError(w, r, http.StatusInternalServerError, "setup.error.load")
		return
	}
	s.renderSetup(w, r, status, secret, uri, msg, "")
}

func (s *Server) renderSetup(w http.ResponseWriter, r *http.Request, status int, secret, uri, errMsg, notice string) {
	p := s.page(r, "setup.title")
	p.SetupSecret = secret
	p.SetupURI = uri
	p.QRDataURI = qrDataURI(uri)
	p.Error = errMsg
	p.Notice = notice
	s.render(w, r, status, templates.SetupPage(p))
}

func qrDataURI(uri string) string {
	png, err := qrcode.Encode(uri, qrcode.Medium, 320)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

func (s *Server) audit(r *http.Request, merchantID uuid.UUID, actorType string, actorID *uuid.UUID, action, targetType, targetID, ip string, meta map[string]any) {
	if err := s.auth.Audit(r.Context(), merchantID, actorType, actorID, action, targetType, targetID, ip, meta); err != nil {
		s.log.Error("write audit log", "action", action, "error", err)
	}
}

func (s *Server) auditLoginFailure(r *http.Request, reason string) {
	m, ok := tenantFrom(r)
	if !ok {
		return
	}
	s.audit(r, m.ID, "anonymous", nil, "auth.login_failed", "staff", "", s.clientIP(r), map[string]any{"reason": reason})
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("unexpected trailing data")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
