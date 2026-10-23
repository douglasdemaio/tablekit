package platform

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/crypto"
	"tablekit.de/tablekit/internal/i18n"
	"tablekit.de/tablekit/internal/logging"
	"tablekit.de/tablekit/internal/tenant"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := crypto.RandomToken(12)
		if err != nil {
			id = fmt.Sprintf("%d", time.Now().UnixNano())
		}
		w.Header().Set("X-Request-Id", id)
		ctx := logging.WithRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) locale(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loc := i18n.Default
		if c, err := r.Cookie("tk_locale"); err == nil {
			loc = i18n.Locale(c.Value)
		} else {
			loc = i18n.Negotiate(r.Header.Get("Accept-Language"))
		}
		next.ServeHTTP(w, i18n.WithLocale(r, loc))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "error", rec, "stack", string(debug.Stack()))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if s.cfg.IsProduction() {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			limit := int64(1 << 20)
			if isMediaUploadPath(r.URL.Path) {
				limit = mediaUploadRequestLimit
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

func isMediaUploadPath(path string) bool {
	return strings.HasSuffix(path, "/image") && strings.HasPrefix(path, "/dashboard/menu/items/")
}

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, ok := tenant.FromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(s.sessionCookieName())
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		session, staff, err := s.sessions.Load(r.Context(), m.ID, cookie.Value)
		if err != nil {
			s.clearSessionCookies(w)
			next.ServeHTTP(w, r)
			return
		}
		id := auth.Identity{Merchant: m, Staff: staff, Session: session}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
	})
}

func (s *Server) ensureCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(s.csrfCookieName())
		if err != nil || cookie.Value == "" {
			token, genErr := crypto.RandomToken(32)
			if genErr != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			cookie = &http.Cookie{
				Name:     s.csrfCookieName(),
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				Secure:   s.cfg.CookieSecure,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   int(s.cfg.SessionTTL.Seconds()),
			}
			http.SetCookie(w, cookie)
		}

		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		if !s.validOrigin(r) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		submitted := r.Header.Get("X-CSRF-Token")
		if submitted == "" {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				if err := r.ParseMultipartForm(4 << 20); err == nil {
					submitted = r.FormValue("csrf_token")
				}
			} else if err := r.ParseForm(); err == nil {
				submitted = r.PostFormValue("csrf_token")
			}
		}
		if submitted == "" || subtle.ConstantTimeCompare([]byte(submitted), []byte(cookie.Value)) != 1 {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if id, ok := auth.FromContext(r.Context()); ok {
			if !crypto.TokensEqual(submitted, id.Session.CsrfTokenHash) {
				http.Error(w, "invalid csrf token", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		s.logRequest(r, rec.status, start)
	})
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func (s *Server) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) requireMerchant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := tenant.FromContext(r.Context()); !ok {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireVerified(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := tenant.FromContext(r.Context()); !ok {
			http.NotFound(w, r)
			return
		}
		id, ok := auth.FromContext(r.Context())
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		switch id.Session.State {
		case "verified":
			next.ServeHTTP(w, r)
		case "pending":
			http.Redirect(w, r, "/login/mfa", http.StatusSeeOther)
		default:
			http.Redirect(w, r, "/setup/mfa", http.StatusSeeOther)
		}
	})
}

func (s *Server) requirePermission(perm auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return s.requireVerified(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := auth.FromContext(r.Context())
			if !id.Can(perm) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

func (s *Server) requireSetup(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := tenant.FromContext(r.Context()); !ok {
			http.NotFound(w, r)
			return
		}
		id, ok := auth.FromContext(r.Context())
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if id.Session.State == "pending" {
			http.Redirect(w, r, "/login/mfa", http.StatusSeeOther)
			return
		}
		if id.Session.State == "verified" && id.Staff.TotpEnabled {
			http.Redirect(w, r, "/dashboard/security", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sessionCookieName() string {
	if s.cfg.CookieSecure {
		return "__Host-tk_session"
	}
	return "tk_session"
}

func (s *Server) csrfCookieName() string {
	if s.cfg.CookieSecure {
		return "__Host-tk_csrf"
	}
	return "tk_csrf"
}

func (s *Server) setSessionCookies(w http.ResponseWriter, token, csrf string, verified bool) {
	maxAge := int(s.cfg.SessionTTL.Seconds())
	if !verified {
		maxAge = int(s.cfg.MFAPendingTTL.Seconds())
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.sessionCookieName(),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     s.csrfCookieName(),
		Value:    csrf,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func (s *Server) clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{s.sessionCookieName(), s.csrfCookieName()} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
		})
	}
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	return r.RemoteAddr
}
