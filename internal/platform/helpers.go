package platform

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"tablekit.de/tablekit/internal/db/sqlcgen"
	"tablekit.de/tablekit/internal/tenant"
)

func mustMerchant(r *http.Request) tenant.Merchant {
	m, _ := tenant.FromContext(r.Context())
	return m
}

func tenantFrom(r *http.Request) (tenant.Merchant, bool) {
	return tenant.FromContext(r.Context())
}

var displayLocation = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		return time.Local
	}
	return loc
}()

func formatTime(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return "—"
	}
	return ts.Time.In(displayLocation).Format("02 Jan 15:04")
}

func formatOptionalTime(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return "—"
	}
	return formatTime(ts)
}

var currencySymbols = map[string]string{
	"EUR": "€",
	"USD": "$",
	"GBP": "£",
	"CHF": "CHF ",
}

// formatMoney renders minor units (cents) as a currency string.
func formatMoney(minor int64, currency string) string {
	symbol, ok := currencySymbols[strings.ToUpper(currency)]
	if !ok {
		symbol = strings.ToUpper(currency) + " "
	}
	neg := minor < 0
	if neg {
		minor = -minor
	}
	whole := minor / 100
	cents := minor % 100
	out := symbol + strconv.FormatInt(whole, 10) + "." + pad2(cents)
	if neg {
		return "-" + out
	}
	return out
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// priceInput renders minor units as a plain decimal string for edit fields.
func priceInput(minor int32) string {
	return formatDecimal(int64(minor))
}

func formatDecimal(minor int64) string {
	negative := minor < 0
	if negative {
		minor = -minor
	}
	out := strconv.FormatInt(minor/100, 10) + "." + pad2(minor%100)
	if negative {
		return "-" + out
	}
	return out
}

// parsePrice accepts "12.30", "12,30" or "12" and returns minor units.
func parsePrice(raw string) (int32, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, "€", "")
	raw = strings.ReplaceAll(raw, " ", "")
	raw = strings.ReplaceAll(raw, ",", ".")
	if raw == "" {
		return 0, false
	}
	parts := strings.SplitN(raw, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || whole < 0 {
		return 0, false
	}
	var cents int64
	if len(parts) == 2 {
		frac := parts[1]
		switch len(frac) {
		case 0:
			frac = "0"
		case 1:
			frac += "0"
		case 2:
		default:
			frac = frac[:2]
		}
		cents, err = strconv.ParseInt(frac, 10, 32)
		if err != nil {
			return 0, false
		}
	}
	total := whole*100 + cents
	if total > 1_000_000 {
		return 0, false
	}
	return int32(total), true
}

func (s *Server) statusLabel(r *http.Request, st sqlcgen.OrderStatusEnum) string {
	return s.tr(r, "orders.status."+string(st))
}

func (s *Server) paymentLabel(r *http.Request, ps sqlcgen.PaymentStatusEnum) string {
	return s.tr(r, "payment."+string(ps))
}

func (s *Server) fulfilmentLabel(r *http.Request, f sqlcgen.FulfilmentModeEnum) string {
	switch f {
	case sqlcgen.FulfilmentModePickup:
		return s.tr(r, "orders.pickup")
	case sqlcgen.FulfilmentModeSelfDelivery:
		return s.tr(r, "orders.self_delivery")
	case sqlcgen.FulfilmentModeCourier:
		return s.tr(r, "orders.courier")
	default:
		return string(f)
	}
}

func actionStyle(st sqlcgen.OrderStatusEnum) string {
	switch st {
	case sqlcgen.OrderStatusCompleted:
		return "primary"
	case sqlcgen.OrderStatusCancelled:
		return "danger"
	default:
		return "default"
	}
}
