package tenant

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/db"
)

var ErrNotFound = errors.New("tenant: merchant not found")

type Merchant struct {
	ID   uuid.UUID
	Slug string
	Name string
}

type ctxKey int

const merchantKey ctxKey = iota

func WithMerchant(ctx context.Context, m Merchant) context.Context {
	return context.WithValue(ctx, merchantKey, m)
}

func FromContext(ctx context.Context) (Merchant, bool) {
	m, ok := ctx.Value(merchantKey).(Merchant)
	return m, ok
}

func MustFromContext(ctx context.Context) Merchant {
	m, ok := FromContext(ctx)
	if !ok {
		panic("tenant: merchant missing from context")
	}
	return m
}

// Middleware resolves the merchant from the exact request host. It never
// consults client-supplied tenant headers, cookies or query parameters.
func Middleware(store *db.Store, baseDomain string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := RequestHost(r)
			if host == "" || host == baseDomain || host == "www."+baseDomain {
				next.ServeHTTP(w, r)
				return
			}
			route, err := store.ResolveMerchantByHost(r.Context(), host)
			if err != nil {
				if errors.Is(err, db.ErrNotFound) {
					http.NotFound(w, r)
					return
				}
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			m := Merchant{ID: route.ID, Slug: route.Slug, Name: route.Name}
			next.ServeHTTP(w, r.WithContext(WithMerchant(r.Context(), m)))
		})
	}
}

func RequestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host
}
