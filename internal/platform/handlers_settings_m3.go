package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/db/sqlcgen"
	"tablekit.de/tablekit/internal/media"
	"tablekit.de/tablekit/internal/payments"
	"tablekit.de/tablekit/internal/storefront"
	"tablekit.de/tablekit/web/templates"
)

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (s *Server) loadSettingsExtras(w http.ResponseWriter, r *http.Request) (templates.SettingsView, bool) {
	id, _ := auth.FromContext(r.Context())
	profile, err := s.storefrontSvc.MerchantProfile(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("load merchant profile", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return templates.SettingsView{}, false
	}
	brand, err := s.storefrontSvc.Brand(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("load brand", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return templates.SettingsView{}, false
	}
	payment, err := s.storefrontSvc.PaymentConnection(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("load payment connection", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return templates.SettingsView{}, false
	}
	v := templates.SettingsView{}
	v.Profile = templates.ProfileSettingsView{
		Name: profile.Name, LegalName: profile.LegalName, Description: profile.Description, Phone: profile.Phone,
		Email: profile.Email, Street: profile.Street, Line2: profile.Line2,
		Postcode: profile.Postcode, City: profile.City, Country: profile.Country,
		Cuisine: profile.Cuisine, Timezone: profile.Timezone, Locale: profile.Locale,
		PickupEnabled: profile.PickupEnabled, SelfDeliveryEnabled: profile.SelfDeliveryEnabled,
	}
	v.Brand = templates.BrandSettingsView{
		Tagline: brand.Tagline, PrimaryColor: brand.Primary, SecondaryColor: brand.Secondary,
		AccentColor: brand.Accent, Background: brand.Background, TextColor: brand.Text,
		Font: brand.Font, Template: brand.Template,
	}
	v.Brand.LogoURL = s.assetURL(r, id.Merchant.ID, brand.LogoAssetID)
	v.Brand.CoverURL = s.assetURL(r, id.Merchant.ID, brand.CoverAssetID)
	v.Payment = templates.PaymentSettingsView{
		Available: s.cfg.StripeEnabled(),
		Connected: payment.ConnectedAccountID != "",
		Active:    payment.Status == "active",
		LiveMode:  payment.LiveMode || s.cfg.StripeLiveMode,
	}
	v.CanBrand = id.Can(auth.PermBranding)
	return v, true
}

func (s *Server) assetURL(r *http.Request, merchantID uuid.UUID, assetID *uuid.UUID) string {
	if assetID == nil {
		return ""
	}
	key, err := s.mediaSvc.ObjectKey(r.Context(), merchantID, *assetID)
	if err != nil {
		return ""
	}
	return s.mediaSvc.URL(key)
}

func (s *Server) handleSettingsProfile(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=profile", http.StatusSeeOther)
		return
	}
	current, err := s.storefrontSvc.MerchantProfile(r.Context(), id.Merchant.ID)
	if err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=profile", http.StatusSeeOther)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	tz := strings.TrimSpace(r.PostFormValue("timezone"))
	locale := r.PostFormValue("locale")
	if name == "" || len(name) > 160 || (locale != "en" && locale != "de") || len(r.PostFormValue("description")) > 2000 {
		http.Redirect(w, r, "/dashboard/settings?error=profile", http.StatusSeeOther)
		return
	}
	if _, err := time.LoadLocation(tz); err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=profile", http.StatusSeeOther)
		return
	}
	current.Name = name
	current.LegalName = strings.TrimSpace(r.PostFormValue("legal_name"))
	current.Timezone = tz
	current.Locale = locale
	current.Description = strings.TrimSpace(r.PostFormValue("description"))
	current.Phone = strings.TrimSpace(r.PostFormValue("phone"))
	current.Email = strings.TrimSpace(r.PostFormValue("email"))
	current.Street = strings.TrimSpace(r.PostFormValue("street"))
	current.Line2 = strings.TrimSpace(r.PostFormValue("line2"))
	current.Postcode = strings.TrimSpace(r.PostFormValue("postcode"))
	current.City = strings.TrimSpace(r.PostFormValue("city"))
	current.Country = strings.TrimSpace(r.PostFormValue("country"))
	current.Cuisine = strings.TrimSpace(r.PostFormValue("cuisine"))
	current.PickupEnabled = r.PostFormValue("pickup_enabled") != ""
	current.SelfDeliveryEnabled = r.PostFormValue("self_delivery_enabled") != ""
	if len(current.Description) > 2000 || len(current.Email) > 200 || len(current.Phone) > 40 {
		http.Redirect(w, r, "/dashboard/settings?error=profile", http.StatusSeeOther)
		return
	}
	if err := s.storefrontSvc.UpdateMerchantProfile(r.Context(), id.Merchant.ID, current); err != nil {
		s.log.Error("update merchant profile", "error", err)
		http.Redirect(w, r, "/dashboard/settings?error=profile", http.StatusSeeOther)
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "settings.profile_updated", "merchant", id.Merchant.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/settings?saved=profile", http.StatusSeeOther)
}

func (s *Server) handleSettingsBranding(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=branding", http.StatusSeeOther)
		return
	}
	b, err := s.storefrontSvc.Brand(r.Context(), id.Merchant.ID)
	if err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=branding", http.StatusSeeOther)
		return
	}
	colors := map[*string]string{
		&b.Primary: r.PostFormValue("primary_color"), &b.Secondary: r.PostFormValue("secondary_color"),
		&b.Accent: r.PostFormValue("accent_color"), &b.Background: r.PostFormValue("background_color"),
		&b.Text: r.PostFormValue("text_color"),
	}
	for dst, raw := range colors {
		if !hexColor.MatchString(strings.TrimSpace(raw)) {
			http.Redirect(w, r, "/dashboard/settings?error=branding", http.StatusSeeOther)
			return
		}
		*dst = strings.ToLower(strings.TrimSpace(raw))
	}
	font := r.PostFormValue("font")
	if !allowedFonts[font] {
		font = "Inter"
	}
	template := r.PostFormValue("template")
	if !allowedTemplates[template] {
		template = "classic-bistro"
	}
	b.Font = font
	b.Template = template
	b.Tagline = strings.TrimSpace(r.PostFormValue("tagline"))
	if len(b.Tagline) > 120 {
		http.Redirect(w, r, "/dashboard/settings?error=branding", http.StatusSeeOther)
		return
	}
	if err := s.storefrontSvc.UpdateBrand(r.Context(), id.Merchant.ID, b); err != nil {
		s.log.Error("update brand", "error", err)
		http.Redirect(w, r, "/dashboard/settings?error=branding", http.StatusSeeOther)
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "settings.branding_updated", "merchant", id.Merchant.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/settings?saved=branding", http.StatusSeeOther)
}

var allowedFonts = map[string]bool{
	"Inter": true, "Playfair Display": true, "Lora": true, "DM Sans": true, "Cormorant Garamond": true,
}

var allowedTemplates = map[string]bool{
	"classic-bistro": true, "modern-dark": true, "warm-tavern": true,
}

func (s *Server) handleSettingsLogo(w http.ResponseWriter, r *http.Request) {
	s.handleBrandUpload(w, r, "logo")
}
func (s *Server) handleSettingsCover(w http.ResponseWriter, r *http.Request) {
	s.handleBrandUpload(w, r, "cover")
}

func (s *Server) handleBrandUpload(w http.ResponseWriter, r *http.Request, kind string) {
	id, _ := auth.FromContext(r.Context())
	if !s.mediaSvc.Configured() {
		http.Redirect(w, r, "/dashboard/settings?error=storage", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, mediaUploadRequestLimit)
	if err := r.ParseMultipartForm(4 << 20); err != nil { // #nosec G120 -- request body is capped by MaxBytesReader
		http.Redirect(w, r, "/dashboard/settings?error=branding", http.StatusSeeOther)
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=branding", http.StatusSeeOther)
		return
	}
	defer file.Close()
	mediaKind := sqlcgen.MediaKindLogo
	if kind == "cover" {
		mediaKind = sqlcgen.MediaKindCover
	}
	asset, err := s.mediaSvc.Upload(r.Context(), id.Merchant.ID, id.Staff.ID, mediaKind, file)
	if err != nil {
		s.log.Warn("brand image upload rejected", "error", err)
		code := "branding"
		if errors.Is(err, media.ErrTooLarge) {
			code = "image_size"
		} else if errors.Is(err, media.ErrUnsupported) || errors.Is(err, media.ErrCorrupt) {
			code = "image_type"
		}
		http.Redirect(w, r, "/dashboard/settings?error="+code, http.StatusSeeOther)
		return
	}
	if err := s.storefrontSvc.SetBrandAsset(r.Context(), id.Merchant.ID, kind, asset.ID); err != nil {
		_ = s.mediaSvc.Delete(r.Context(), id.Merchant.ID, asset.ID)
		http.Redirect(w, r, "/dashboard/settings?error=branding", http.StatusSeeOther)
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "settings."+kind+"_updated", "merchant", id.Merchant.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/settings?saved=branding", http.StatusSeeOther)
}

func (s *Server) handleStripeConnectStart(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if !s.cfg.StripeEnabled() {
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	pc, err := s.storefrontSvc.PaymentConnection(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("load payment connection", "error", err)
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	profile, err := s.storefrontSvc.MerchantProfile(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("load merchant profile for stripe", "error", err)
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	client := payments.NewClient(s.cfg.StripeSecretKey)
	accountID := pc.ConnectedAccountID
	if accountID != "" && pc.LiveMode != s.cfg.StripeLiveMode {
		accountID = ""
	}
	if accountID == "" {
		if len(strings.TrimSpace(profile.Country)) != 2 {
			http.Redirect(w, r, "/dashboard/settings?error=payments_country", http.StatusSeeOther)
			return
		}
		contact := strings.TrimSpace(profile.Email)
		if contact == "" {
			contact = strings.TrimSpace(id.Staff.Email)
		}
		account, createErr := client.CreateConnectAccount(r.Context(), payments.ConnectAccountInput{
			ContactEmail: contact,
			DisplayName:  profile.Name,
			Country:      profile.Country,
		})
		if createErr != nil {
			s.log.Error("create stripe connected account", "error", createErr)
			http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
			return
		}
		accountID = account.ID
		caps, _ := json.Marshal(map[string]string{"card_payments": account.CardPaymentsStatus})
		if err := s.storefrontSvc.SaveConnectedAccount(r.Context(), id.Merchant.ID, accountID, account.Status(), caps, s.cfg.StripeLiveMode); err != nil {
			s.log.Error("save stripe connected account", "error", err)
			http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
			return
		}
	}
	link, err := s.stripeOnboardingLink(r, client, accountID)
	if err != nil {
		s.log.Error("create stripe onboarding link", "error", err)
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "settings.stripe_onboarding_started", "merchant", id.Merchant.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, link, http.StatusSeeOther)
}

func (s *Server) stripeOnboardingLink(r *http.Request, client *payments.Client, accountID string) (string, error) {
	base := fmt.Sprintf("%s://%s", s.cfg.PublicScheme, r.Host)
	return client.CreateAccountLink(r.Context(), accountID,
		base+"/dashboard/settings/payments/return",
		base+"/dashboard/settings/payments/refresh")
}

func (s *Server) syncStripeAccount(w http.ResponseWriter, r *http.Request) (*payments.ConnectAccount, bool) {
	id, _ := auth.FromContext(r.Context())
	pc, err := s.storefrontSvc.PaymentConnection(r.Context(), id.Merchant.ID)
	if err != nil || pc.ConnectedAccountID == "" || pc.LiveMode != s.cfg.StripeLiveMode {
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return nil, false
	}
	account, err := payments.NewClient(s.cfg.StripeSecretKey).GetConnectAccount(r.Context(), pc.ConnectedAccountID)
	if err != nil {
		s.log.Error("refresh stripe connected account", "error", err)
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return nil, false
	}
	caps, _ := json.Marshal(map[string]string{"card_payments": account.CardPaymentsStatus})
	if err := s.storefrontSvc.SaveConnectedAccount(r.Context(), id.Merchant.ID, account.ID, account.Status(), caps, account.LiveMode); err != nil {
		s.log.Error("save stripe account status", "error", err)
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return nil, false
	}
	return &account, true
}

func (s *Server) handleStripeConnectReturn(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if !s.cfg.StripeEnabled() {
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	if _, ok := s.syncStripeAccount(w, r); !ok {
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "settings.stripe_onboarding_refreshed", "merchant", id.Merchant.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/settings?saved=payments", http.StatusSeeOther)
}

func (s *Server) handleStripeConnectRefresh(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if !s.cfg.StripeEnabled() {
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	account, ok := s.syncStripeAccount(w, r)
	if !ok {
		return
	}
	if account.Status() == "active" {
		http.Redirect(w, r, "/dashboard/settings?saved=payments", http.StatusSeeOther)
		return
	}
	link, err := s.stripeOnboardingLink(r, payments.NewClient(s.cfg.StripeSecretKey), account.ID)
	if err != nil {
		s.log.Error("recreate stripe onboarding link", "error", err)
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "settings.stripe_onboarding_restarted", "merchant", id.Merchant.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, link, http.StatusSeeOther)
}

func (s *Server) handleThemeCSS(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantFrom(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, err := s.storefrontSvc.Brand(r.Context(), t.ID)
	if err != nil {
		b = storefront.Brand{Font: "Inter", Primary: "#1c1917", Secondary: "#78716c", Accent: "#f59e0b", Background: "#ffffff", Text: "#111111"}
	}
	font := b.Font
	if !allowedFonts[font] {
		font = "Inter"
	}
	css := fmt.Sprintf(`@import url('https://fonts.googleapis.com/css2?family=%s:wght@400;600;700&display=swap');
:root{--brand-primary:%s;--brand-secondary:%s;--brand-accent:%s;--brand-bg:%s;--brand-text:%s;--brand-font:'%s',system-ui,sans-serif}
body{background:var(--brand-bg);color:var(--brand-text);font-family:var(--brand-font)}
.brand-primary{color:var(--brand-primary)}.brand-accent{background:var(--brand-accent)}
[data-template="modern-dark"]{--brand-bg:#111111;--brand-text:#f5f5f4}
[data-template="warm-tavern"]{--brand-bg:#fdf6ec;--brand-text:#3f2a14}
`, strings.ReplaceAll(font, " ", "+"), b.Primary, b.Secondary, b.Accent, b.Background, b.Text, font)
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	_, _ = w.Write([]byte(css))
}
