package i18n

import (
	"context"
	"net/http"
	"strings"
)

func contextWithLocale(r *http.Request, loc Locale) context.Context {
	return context.WithValue(r.Context(), localeKey, loc)
}

// LocaleFrom returns the locale stored on the request context, or the default.
func LocaleFrom(r *http.Request) Locale {
	if r == nil {
		return Default
	}
	if loc, ok := r.Context().Value(localeKey).(Locale); ok && loc != "" {
		return loc
	}
	return Default
}

// Negotiate picks a locale from the Accept-Language header, defaulting to
// English. It is deliberately simple: only exact language tags we support win.
func Negotiate(header string) Locale {
	for _, part := range strings.Split(header, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case strings.HasPrefix(tag, "de"):
			return German
		case strings.HasPrefix(tag, "en"):
			return English
		}
	}
	return Default
}
