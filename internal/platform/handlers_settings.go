package platform

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/auth"
	"tablekit.de/tablekit/internal/storefront"
	"tablekit.de/tablekit/web/templates"
)

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	settings, hours, zones, err := s.storefrontSvc.LoadSettings(r.Context(), id.Merchant.ID)
	if err != nil {
		s.log.Error("load settings", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := s.page(r, "settings.title")
	p.Nav = "settings"
	switch r.URL.Query().Get("saved") {
	case "vat", "hours":
		p.Notice = s.tr(r, "settings.saved")
	case "zone":
		p.Notice = s.tr(r, "settings.zone_saved")
	case "zone_deleted":
		p.Notice = s.tr(r, "settings.zone_deleted")
	}
	switch r.URL.Query().Get("error") {
	case "vat":
		p.Error = s.tr(r, "settings.error.vat")
	case "hours":
		p.Error = s.tr(r, "settings.error.hours")
	case "zone":
		p.Error = s.tr(r, "settings.error.zone")
	}
	p.Settings = s.settingsView(r, settings, hours, zones)
	s.render(w, r, http.StatusOK, templates.SettingsPage(p))
}

func (s *Server) handleSettingsVAT(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=vat", http.StatusSeeOther)
		return
	}
	bp, ok := parsePercent(r.PostFormValue("vat"))
	if !ok {
		http.Redirect(w, r, "/dashboard/settings?error=vat", http.StatusSeeOther)
		return
	}
	if err := s.storefrontSvc.UpdateVatRate(r.Context(), id.Merchant.ID, bp); err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=vat", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard/settings?saved=vat", http.StatusSeeOther)
}

func (s *Server) handleSettingsHours(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=hours", http.StatusSeeOther)
		return
	}
	hours := make([]storefront.Hour, 0, 7)
	for day := 0; day < 7; day++ {
		prefix := "day_" + strconv.Itoa(day)
		if r.PostFormValue(prefix+"_enabled") == "" {
			continue
		}
		opens := r.PostFormValue(prefix + "_open")
		closes := r.PostFormValue(prefix + "_close")
		if opens == "" || closes == "" {
			http.Redirect(w, r, "/dashboard/settings?error=hours", http.StatusSeeOther)
			return
		}
		hours = append(hours, storefront.Hour{Weekday: day, Opens: opens, Closes: closes})
	}
	if err := s.storefrontSvc.ReplaceHours(r.Context(), id.Merchant.ID, hours); err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=hours", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard/settings?saved=hours", http.StatusSeeOther)
}

func (s *Server) handleSettingsZoneCreate(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=zone", http.StatusSeeOther)
		return
	}
	minOrder, okMin := parsePrice(r.PostFormValue("min_order"))
	fee, okFee := parsePrice(r.PostFormValue("fee"))
	if !okMin || !okFee {
		http.Redirect(w, r, "/dashboard/settings?error=zone", http.StatusSeeOther)
		return
	}
	prefixes := splitPrefixes(r.PostFormValue("prefixes"))
	err := s.storefrontSvc.CreateZone(r.Context(), id.Merchant.ID, r.PostFormValue("name"), prefixes, minOrder, fee)
	if err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=zone", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard/settings?saved=zone", http.StatusSeeOther)
}

func (s *Server) handleSettingsZoneUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	zoneID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=zone", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=zone", http.StatusSeeOther)
		return
	}
	minOrder, okMin := parsePrice(r.PostFormValue("min_order"))
	fee, okFee := parsePrice(r.PostFormValue("fee"))
	if !okMin || !okFee {
		http.Redirect(w, r, "/dashboard/settings?error=zone", http.StatusSeeOther)
		return
	}
	active := r.PostFormValue("active") != ""
	prefixes := splitPrefixes(r.PostFormValue("prefixes"))
	err = s.storefrontSvc.UpdateZone(r.Context(), id.Merchant.ID, zoneID, r.PostFormValue("name"), prefixes, minOrder, fee, active)
	if err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=zone", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard/settings?saved=zone", http.StatusSeeOther)
}

func (s *Server) handleSettingsZoneDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	zoneID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Redirect(w, r, "/dashboard/settings?error=zone", http.StatusSeeOther)
		return
	}
	if err := s.storefrontSvc.DeleteZone(r.Context(), id.Merchant.ID, zoneID); err != nil {
		s.log.Warn("delete delivery zone", "error", err)
		http.Redirect(w, r, "/dashboard/settings?error=zone", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/dashboard/settings?saved=zone_deleted", http.StatusSeeOther)
}

func (s *Server) settingsView(r *http.Request, settings storefront.Settings, hours []storefront.Hour, zones []storefront.Zone) templates.SettingsView {
	byDay := make(map[int]storefront.Hour, len(hours))
	for _, h := range hours {
		byDay[h.Weekday] = h
	}
	view := templates.SettingsView{
		VATPercent: formatPercent(settings.VatRateBp),
	}
	for day := 0; day < 7; day++ {
		row := templates.HourRow{
			Weekday: day,
			Label:   s.tr(r, "day."+strconv.Itoa(day)),
			Opens:   "11:00",
			Closes:  "22:00",
		}
		if h, ok := byDay[day]; ok {
			row.Enabled = true
			row.Opens = h.Opens
			row.Closes = h.Closes
		}
		view.Hours = append(view.Hours, row)
	}
	for _, z := range zones {
		view.Zones = append(view.Zones, templates.ZoneSettingView{
			ID:       z.ID.String(),
			Name:     z.Name,
			Prefixes: strings.Join(z.Prefixes, ", "),
			MinOrder: priceInput(z.MinOrderMinor),
			Fee:      priceInput(z.DeliveryFeeMin),
			Active:   z.Active,
		})
	}
	return view
}

func parsePercent(raw string) (int32, bool) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", "."))
	raw = strings.TrimSuffix(raw, "%")
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 || value > 100 {
		return 0, false
	}
	return int32(value*100 + 0.5), true
}

func formatPercent(bp int32) string {
	if bp%100 == 0 {
		return strconv.Itoa(int(bp / 100))
	}
	return fmt.Sprintf("%.2f", float64(bp)/100)
}

func splitPrefixes(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
