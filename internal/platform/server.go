package platform

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/webauthn"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/config"
	"tablekit.de/tablekit/internal/crypto"
	"tablekit.de/tablekit/internal/db"
	"tablekit.de/tablekit/internal/media"
	"tablekit.de/tablekit/internal/menu"
	"tablekit.de/tablekit/internal/orders"
	"tablekit.de/tablekit/internal/staff"
	"tablekit.de/tablekit/internal/storage"
	"tablekit.de/tablekit/internal/storefront"
	"tablekit.de/tablekit/internal/tenant"
	"tablekit.de/tablekit/web"
)

type Server struct {
	cfg           *config.Config
	log           *slog.Logger
	store         *db.Store
	sealer        *crypto.Sealer
	auth          *auth.Service
	sessions      *auth.SessionManager
	orderSvc      *orders.Service
	menuSvc       *menu.Service
	staffSvc      *staff.Service
	mediaSvc      *media.Service
	storefrontSvc *storefront.Service
	handler       http.Handler
}

func New(cfg *config.Config, log *slog.Logger, store *db.Store, sealer *crypto.Sealer) (*Server, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.WebAuthnRPID,
		RPDisplayName: cfg.WebAuthnRPName,
		RPOrigins:     cfg.WebAuthnOrigins,
	})
	if err != nil {
		return nil, fmt.Errorf("configure webauthn: %w", err)
	}
	s := &Server{
		cfg:           cfg,
		log:           log,
		store:         store,
		sealer:        sealer,
		auth:          auth.NewService(store, sealer, cfg, wa, log),
		sessions:      auth.NewSessionManager(store, cfg),
		orderSvc:      orders.NewService(store),
		menuSvc:       menu.NewService(store),
		staffSvc:      staff.NewService(store),
		mediaSvc:      media.NewService(store, storage.New(cfg)),
		storefrontSvc: storefront.NewService(store),
	}
	if !cfg.StorageEnabled() {
		log.Warn("media storage not configured; image uploads are disabled")
	}
	s.handler = s.chain(s.routes())
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) chain(mux http.Handler) http.Handler {
	h := s.accessLog(mux)
	h = s.ensureCSRF(h)
	h = s.loadSession(h)
	h = tenant.Middleware(s.store, s.cfg.BaseDomain)(h)
	h = s.limitBody(h)
	h = s.securityHeaders(h)
	h = s.locale(h)
	h = s.recoverer(h)
	h = s.requestID(h)
	return h
}

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()

	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	r.Handle("/static/*", http.StripPrefix("/static/", web.StaticHandler()))

	if s.cfg.MediaDir != "" {
		fs := http.FileServer(http.Dir(s.cfg.MediaDir))
		r.Handle("/media/*", http.StripPrefix("/media/", fs))
	}

	r.Get("/", s.handleLanding)

	r.Group(func(r chi.Router) {
		r.Use(s.requireMerchant)
		r.Get("/checkout", s.handleCheckoutPage)
		r.Post("/orders", s.handlePlaceOrder)
		r.Get("/orders/{ref}", s.handleOrderConfirmation)

		r.Get("/login", s.handleLoginPage)
		r.Post("/login", s.handleLogin)
		r.Get("/login/mfa", s.handleMFAPage)
		r.Post("/login/mfa", s.handleMFA)
		r.Post("/login/passkey/begin", s.handlePasskeyLoginBegin)
		r.Post("/login/passkey/finish", s.handlePasskeyLoginFinish)
		r.Post("/logout", s.handleLogout)

		r.With(s.requireSetup).Get("/setup/mfa", s.handleSetupPage)
		r.With(s.requireSetup).Post("/setup/totp/verify", s.handleSetupTOTPVerify)
		r.With(s.requireSetup).Post("/setup/passkey/begin", s.handlePasskeyRegisterBegin)
		r.With(s.requireSetup).Post("/setup/passkey/finish", s.handlePasskeyRegisterFinish)

		r.With(s.requireVerified).Get("/dashboard", s.handleDashboard)
		r.With(s.requirePermission(auth.PermOrdersView)).Get("/dashboard/orders", s.handleOrdersPage)
		r.With(s.requirePermission(auth.PermOrdersManage)).Post("/dashboard/orders/{id}/status", s.handleOrderStatus)
		r.With(s.requirePermission(auth.PermOrdersManage)).Post("/dashboard/orders/{id}/handoff", s.handleOrderHandoff)
		r.With(s.requirePermission(auth.PermMenuManage)).Get("/dashboard/menu", s.handleMenuPage)
		r.With(s.requirePermission(auth.PermMenuManage)).Post("/dashboard/menu/categories", s.handleMenuCreateCategory)
		r.With(s.requirePermission(auth.PermMenuManage)).Post("/dashboard/menu/categories/{id}/items", s.handleMenuCreateItem)
		r.With(s.requirePermission(auth.PermMenuManage)).Post("/dashboard/menu/items/{id}", s.handleMenuUpdateItem)
		r.With(s.requirePermission(auth.PermMenuManage)).Post("/dashboard/menu/items/{id}/availability", s.handleMenuToggleAvailability)
		r.With(s.requirePermission(auth.PermMenuManage)).Post("/dashboard/menu/items/{id}/delete", s.handleMenuDeleteItem)
		r.With(s.requirePermission(auth.PermMenuManage)).Post("/dashboard/menu/items/{id}/image", s.handleMenuUploadImage)
		r.With(s.requirePermission(auth.PermMenuManage)).Post("/dashboard/menu/items/{id}/image/delete", s.handleMenuDeleteImage)

		r.With(s.requirePermission(auth.PermStaffManage)).Get("/dashboard/staff", s.handleStaffPage)
		r.With(s.requirePermission(auth.PermStaffManage)).Post("/dashboard/staff", s.handleStaffCreate)
		r.With(s.requirePermission(auth.PermStaffManage)).Post("/dashboard/staff/{id}", s.handleStaffUpdate)
		r.With(s.requirePermission(auth.PermStaffManage)).Post("/dashboard/staff/{id}/password", s.handleStaffResetPassword)
		r.With(s.requirePermission(auth.PermStaffManage)).Post("/dashboard/staff/{id}/disable", s.handleStaffDisable)
		r.With(s.requirePermission(auth.PermStaffManage)).Post("/dashboard/staff/{id}/enable", s.handleStaffEnable)
		r.With(s.requirePermission(auth.PermStaffManage)).Post("/dashboard/staff/sessions/{id}/revoke", s.handleStaffSessionRevoke)
		r.With(s.requirePermission(auth.PermStaffManage)).Post("/dashboard/staff/{id}/sessions/revoke", s.handleStaffSessionsRevoke)

		r.With(s.requirePermission(auth.PermSettings)).Get("/dashboard/settings", s.handleSettingsPage)
		r.With(s.requirePermission(auth.PermSettings)).Post("/dashboard/settings/vat", s.handleSettingsVAT)
		r.With(s.requirePermission(auth.PermSettings)).Post("/dashboard/settings/hours", s.handleSettingsHours)
		r.With(s.requirePermission(auth.PermSettings)).Post("/dashboard/settings/zones", s.handleSettingsZoneCreate)
		r.With(s.requirePermission(auth.PermSettings)).Post("/dashboard/settings/zones/{id}", s.handleSettingsZoneUpdate)
		r.With(s.requirePermission(auth.PermSettings)).Post("/dashboard/settings/zones/{id}/delete", s.handleSettingsZoneDelete)

		r.With(s.requireVerified).Get("/dashboard/security", s.handleSecurityPage)

		r.With(s.requirePermission(auth.PermSecurity)).Post("/dashboard/security/totp/begin", s.handleTOTPBegin)
		r.With(s.requirePermission(auth.PermSecurity)).Post("/dashboard/security/totp/disable", s.handleTOTPDisable)
		r.With(s.requirePermission(auth.PermSecurity)).Post("/dashboard/security/passkeys/begin", s.handlePasskeyRegisterBegin)
		r.With(s.requirePermission(auth.PermSecurity)).Post("/dashboard/security/passkeys/finish", s.handlePasskeyRegisterFinish)
		r.With(s.requirePermission(auth.PermSecurity)).Post("/dashboard/security/passkeys/{id}/delete", s.handlePasskeyDelete)
		r.With(s.requirePermission(auth.PermSecurity)).Post("/dashboard/security/sessions/{id}/revoke", s.handleSessionRevoke)
		r.With(s.requirePermission(auth.PermSecurity)).Post("/dashboard/security/sessions/revoke-others", s.handleSessionRevokeOthers)
	})

	return r
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("database unavailable\n"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}
