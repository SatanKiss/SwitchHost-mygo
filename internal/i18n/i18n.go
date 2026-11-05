// Package i18n is the localisation layer of the pure-Go SwitchHosts build.
//
// It deliberately has no dependency on a browser, a JavaScript bundle, a
// gettext catalog on disk or cgo: the catalogs are Go maps compiled into the
// binary, and the operating-system locale is read with Go's own syscall
// package. The native UI calls T/F for every string it draws, so switching
// language needs nothing more than rebuilding the next frame and the native
// menus.
//
// Key convention: the English source text IS the key. That keeps call sites
// readable, makes English the built-in fallback (no English catalog is needed
// at all) and lets a parser-based test prove that zh / zh_hant never miss a
// string. Format keys keep Go's fmt verbs, e.g. "Saved %s"; translations may
// reorder them with indexed verbs ("%[2]s" / "%[1]s") when a language needs
// a different word order.
package i18n

import (
	"fmt"
	"strings"
)

// Locale is one of the three languages this build ships.
type Locale string

const (
	En     Locale = "en"
	Zh     Locale = "zh"
	ZhHant Locale = "zh_hant"
)

// SystemValue is the value written to internal/config.json when the user
// picks "follow the operating system". It is a v5-compatible locale field:
// the Tauri build treats an unknown/empty locale as English.
const SystemValue = "system"

// All returns the supported locales in the order shown by Preferences.
func All() []Locale { return []Locale{En, Zh, ZhHant} }

func IsSupported(l Locale) bool {
	return l == En || l == Zh || l == ZhHant
}

// String returns the value stored in config.json.
func (l Locale) String() string {
	if IsSupported(l) {
		return string(l)
	}
	return string(En)
}

// Name is the endonym shown in the language picker. It is intentionally never
// translated, so the picker is usable even when the current language is wrong.
func (l Locale) Name() string {
	switch l {
	case Zh:
		return "简体中文"
	case ZhHant:
		return "繁體中文"
	default:
		return "English"
	}
}

// Normalize maps a v5 / OS locale string onto a supported Locale. It returns
// "" for "follow the system" and for languages this build does not ship.
//
// The aliases match both src-tauri/src/i18n.rs (normalize_locale) and
// src/common/i18n/index.ts (languages), so a config.json written by the Tauri
// build selects the same language here. Chinese script tags are honoured:
// zh-Hant / zh-TW / zh-HK / zh-MO are Traditional, everything else Chinese
// (including bare zh and zh-CN) is Simplified.
func Normalize(s string) Locale {
	key := strings.ToLower(strings.TrimSpace(s))
	key = strings.ReplaceAll(key, "-", "_")
	if key == "" {
		return ""
	}
	// POSIX locales may carry an encoding, e.g. "zh_CN.UTF-8" or "zh_CN@euc".
	if i := strings.IndexAny(key, ".@"); i >= 0 {
		key = key[:i]
	}
	parts := strings.Split(key, "_")
	switch parts[0] {
	case "zh", "cn":
		for _, p := range parts[1:] {
			switch p {
			case "hant", "tw", "hk", "mo", "cht", "hanttw", "hanthk", "hantmo":
				return ZhHant
			}
		}
		return Zh
	case "en":
		return En
	default:
		return ""
	}
}

// System reports whether a raw config value means "follow the OS".
func System(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "system", "auto", "default", "os":
		return true
	}
	return false
}

// Resolve turns the raw internal/config.json "locale" value into the locale
// the UI must use. Empty/system means detect from the OS; an explicitly set
// but unsupported language (French, Japanese, ...) falls back to English,
// never to a guessed Chinese, because the user made a deliberate choice.
func Resolve(configured string) Locale {
	if l := Normalize(configured); l != "" {
		return l
	}
	if System(configured) {
		if l := Detect(); IsSupported(l) {
			return l
		}
	}
	return En
}

// Detect reads the current user's display language from the OS. It never
// returns an unsupported locale; callers can use it directly.
func Detect() Locale {
	if l := Normalize(detectOSLocale()); IsSupported(l) {
		return l
	}
	return En
}

// ---------------------------------------------------------------------------
// Catalogs and translation
// ---------------------------------------------------------------------------

// catalogs are keyed by the English source string. En is intentionally
// absent: T falls back to the key, which is the English text itself.
var catalogs = map[Locale]map[string]string{
	Zh:     zhCatalog,
	ZhHant: zhHantCatalog,
}

// T translates one key for a locale. Missing entries fall back to English,
// which makes a partially translated catalog safe instead of showing ids.
func T(l Locale, key string) string {
	if l == En {
		return key
	}
	if c, ok := catalogs[l]; ok {
		if s, ok := c[key]; ok && s != "" {
			return s
		}
	}
	return key
}

// F translates a format key and applies fmt.Sprintf to the translation.
func F(l Locale, key string, args ...any) string {
	return fmt.Sprintf(T(l, key), args...)
}

// Has reports whether a locale has an explicit translation for key. English
// always has one (the key itself).
func Has(l Locale, key string) bool {
	if l == En {
		return true
	}
	c, ok := catalogs[l]
	if !ok {
		return false
	}
	s, ok := c[key]
	return ok && s != ""
}

// Keys returns every explicit catalog key for a locale, sorted. English
// returns an empty slice because its catalog is implicit.
func Keys(l Locale) []string {
	c := catalogs[l]
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// Translator is a mutable locale handle shared by every frame of one window.
// All methods are main-thread only, like the rest of uiapp.App state.
type Translator struct {
	locale Locale
}

func NewTranslator(l Locale) *Translator {
	if !IsSupported(l) {
		l = En
	}
	return &Translator{locale: l}
}

func (t *Translator) Locale() Locale { return t.locale }

func (t *Translator) SetLocale(l Locale) {
	if IsSupported(l) {
		t.locale = l
	}
}

func (t *Translator) T(key string) string { return T(t.locale, key) }

func (t *Translator) F(key string, args ...any) string { return F(t.locale, key, args...) }

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
