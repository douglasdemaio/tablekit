package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"tablekit.de/tablekit/internal/config"
)

func TestEnsureCSRFExposesTokenOnFirstRequest(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	var token string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		token = csrfFromContext(r.Context())
	})
	req := httptest.NewRequest(http.MethodGet, "/account/login", nil)
	rec := httptest.NewRecorder()
	s.ensureCSRF(next).ServeHTTP(rec, req)

	if token == "" {
		t.Fatal("new CSRF token was not added to the request context")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != token {
		t.Fatalf("cookie = %+v, want token %q", cookies, token)
	}
}

func TestPageUsesCSRFTokenFromContext(t *testing.T) {
	s := &Server{cfg: &config.Config{BaseDomain: "tk.local"}}
	req := httptest.NewRequest(http.MethodGet, "/account/login", nil)
	req = req.WithContext(contextWithCSRF(req, "first-request-token"))
	if got := s.page(req, "account.login_title").CSRFToken; got != "first-request-token" {
		t.Fatalf("page CSRF token = %q", got)
	}
}

func contextWithCSRF(r *http.Request, token string) context.Context {
	return context.WithValue(r.Context(), csrfContextKey{}, token)
}
