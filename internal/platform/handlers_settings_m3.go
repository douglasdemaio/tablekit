package platform

import (
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
		Configured: payment.Status == "active", PublishableKey: payment.PublishableKey, LiveMode: payment.LiveMode,
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

func (s *Server) handleSettingsPayments(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	current, err := s.storefrontSvc.PaymentConnection(r.Context(), id.Merchant.ID)
	if err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	publishable := strings.TrimSpace(r.PostFormValue("publishable_key"))
	secret := strings.TrimSpace(r.PostFormValue("secret_key"))
	webhook := strings.TrimSpace(r.PostFormValue("webhook_secret"))
	secretEnc, webhookEnc := current.SecretKeyEnc, current.WebhookSecretEnc
	live := current.LiveMode
	if !strings.HasPrefix(publishable, "pk_") || len(publishable) > 200 {
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	if secret != "" {
		if !strings.HasPrefix(secret, "sk_") || len(secret) > 200 {
			http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
			return
		}
		sealed, err := s.sealer.Seal([]byte(secret))
		if err != nil {
			http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
			return
		}
		secretEnc = sealed
		live = strings.HasPrefix(secret, "sk_live_")
	}
	if webhook != "" {
		if !strings.HasPrefix(webhook, "whsec_") || len(webhook) > 200 {
			http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
			return
		}
		sealed, err := s.sealer.Seal([]byte(webhook))
		if err != nil {
			http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
			return
		}
		webhookEnc = sealed
	}
	if secretEnc == nil || webhookEnc == nil {
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	if err := s.storefrontSvc.SavePaymentConnection(r.Context(), id.Merchant.ID, publishable, secretEnc, webhookEnc, live); err != nil {
		s.log.Error("save payment connection", "error", err)
		http.Redirect(w, r, "/dashboard/settings?error=payments", http.StatusSeeOther)
		return
	}
	s.audit(r, id.Merchant.ID, "staff", &id.Staff.ID, "settings.payments_updated", "merchant", id.Merchant.ID.String(), s.clientIP(r), nil)
	http.Redirect(w, r, "/dashboard/settings?saved=payments", http.StatusSeeOther)
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
