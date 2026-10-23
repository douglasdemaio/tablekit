package i18n

import "strings"

// Translator renders keys for a fixed locale with placeholder substitution.
type Translator struct {
	locale Locale
}

// For returns a Translator bound to the given locale.
func For(loc Locale) Translator { return Translator{locale: loc} }

// Locale returns the bound locale.
func (t Translator) Locale() Locale { return t.locale }

// Tag returns the HTML lang tag (e.g. "en").
func (t Translator) Tag() string { return string(t.locale) }

// T resolves key and substitutes {name}-style placeholders from args. Args are
// alternating key/value pairs, e.g. T("login.subtitle", "merchant", "Joes").
func (t Translator) T(key string, args ...string) string {
	s := Localize(t.locale, key)
	if len(args) == 0 {
		return s
	}
	for i := 0; i+1 < len(args); i += 2 {
		s = strings.ReplaceAll(s, "{"+args[i]+"}", args[i+1])
	}
	return s
}

// Raw returns the translated string with no sub-locale bound. Convenience for
// call sites that already know the locale.
func Raw(loc Locale, key string, args ...string) string {
	return For(loc).T(key, args...)
}
