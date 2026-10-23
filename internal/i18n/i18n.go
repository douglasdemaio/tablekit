// Package i18n holds user-facing strings. English is the default and only
// locale today, but every string is looked up by key so additional locales can
// be added without touching templates or handlers.
package i18n

import "net/http"

type Locale string

const (
	English Locale = "en"
	German  Locale = "de"
)

// Default is used when no locale is otherwise determined.
const Default = English

var catalogs = map[Locale]map[string]string{
	English: en,
	German:  de,
}

type ctxKey int

const localeKey ctxKey = iota

// WithLocale stores the active locale on the context.
func WithLocale(r *http.Request, loc Locale) *http.Request {
	return r.WithContext(contextWithLocale(r, loc))
}

// T looks up a key for the locale on the request context, falling back to the
// default locale and then to the key itself.
func T(r *http.Request, key string) string {
	return Localize(LocaleFrom(r), key)
}

// Localize resolves a key for an explicit locale.
func Localize(loc Locale, key string) string {
	if c, ok := catalogs[loc]; ok {
		if v, ok := c[key]; ok {
			return v
		}
	}
	if v, ok := catalogs[Default][key]; ok {
		return v
	}
	return key
}
