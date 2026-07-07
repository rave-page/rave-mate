// Package i18n is rave-mate's self-contained localization layer. Locale catalogs are JSON
// files embedded in the binary (locales/*.json); en is the source of truth. Lookups fall
// back active-locale → en → the key itself, so a missing translation never renders empty.
// Zero external deps - nested JSON is flattened to dotted keys, interpolation is {name}
// substitution, and plurals use per-locale CLDR cardinal rules (one/few/many/other) via
// pluralCategory. See README.md.
package i18n

import (
	"embed"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed locales/*.json
var localesFS embed.FS

const defaultLocale = "en"

var (
	mu      sync.RWMutex
	catalog = map[string]map[string]string{} // locale → flatKey → value
	active  = defaultLocale
	loaded  bool
)

// load parses every embedded locale once. Safe to call repeatedly.
func load() {
	mu.Lock()
	defer mu.Unlock()
	if loaded {
		return
	}
	loaded = true
	ents, _ := localesFS.ReadDir("locales")
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		code := strings.TrimSuffix(e.Name(), ".json")
		data, err := localesFS.ReadFile("locales/" + e.Name())
		if err != nil {
			continue
		}
		flat, err := parseCatalog(data)
		if err != nil {
			continue // malformed locale is skipped at runtime; the test catches it
		}
		catalog[code] = flat
	}
}

// parseCatalog decodes a locale JSON and flattens nested objects to dotted keys.
// A leaf must be a string; anything else is an error (the completeness test surfaces it).
func parseCatalog(data []byte) (map[string]string, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := flatten("", raw, out); err != nil {
		return nil, err
	}
	return out, nil
}

func flatten(prefix string, m map[string]any, out map[string]string) error {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch t := v.(type) {
		case string:
			out[key] = t
		case map[string]any:
			if err := flatten(key, t, out); err != nil {
				return err
			}
		default:
			return &badLeafError{key: key}
		}
	}
	return nil
}

type badLeafError struct{ key string }

func (e *badLeafError) Error() string { return "i18n: non-string leaf at key " + e.key }

// normalize reduces a locale tag to its base subtag (e.g. "de-DE" / "de_DE.UTF-8" → "de").
func normalize(tag string) string {
	t := strings.TrimSpace(tag)
	if t == "" {
		return ""
	}
	t = strings.ToLower(t)
	for _, sep := range []string{".", "@"} {
		if i := strings.IndexAny(t, sep); i >= 0 {
			t = t[:i]
		}
	}
	t = strings.ReplaceAll(t, "_", "-")
	if i := strings.Index(t, "-"); i >= 0 {
		t = t[:i]
	}
	return t
}

// SetLocale sets the active locale. Empty tag → OS locale → en. If the requested locale has
// no catalog, en is used. Call once at startup with the persisted preference, and again when
// the user picks a language. Returns the locale actually activated.
func SetLocale(tag string) string {
	load()
	code := normalize(tag)
	if code == "" {
		code = normalize(osLocale())
	}
	mu.Lock()
	defer mu.Unlock()
	if code == "" || catalog[code] == nil {
		code = defaultLocale
	}
	active = code
	return code
}

// Current returns the active locale code.
func Current() string {
	mu.RLock()
	defer mu.RUnlock()
	return active
}

// LocaleInfo is a selectable locale (code + human display name) for the language switcher.
type LocaleInfo struct {
	Code, Name string
}

// Available lists installed locales sorted by display name (en first). The display name is the
// locale's own "_meta.name"; falls back to the code.
func Available() []LocaleInfo {
	load()
	mu.RLock()
	out := make([]LocaleInfo, 0, len(catalog))
	for code, flat := range catalog {
		name := flat["_meta.name"]
		if name == "" {
			name = code
		}
		out = append(out, LocaleInfo{Code: code, Name: name})
	}
	mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Code == defaultLocale {
			return true
		}
		if out[j].Code == defaultLocale {
			return false
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// lookup resolves key against active → en, returning ("", false) if neither has it.
func lookup(key string) (string, bool) {
	mu.RLock()
	defer mu.RUnlock()
	if m := catalog[active]; m != nil {
		if v, ok := m[key]; ok {
			return v, true
		}
	}
	if active != defaultLocale {
		if m := catalog[defaultLocale]; m != nil {
			if v, ok := m[key]; ok {
				return v, true
			}
		}
	}
	return "", false
}

// T translates key with the active locale, falling back to en then to the key itself.
// Optional data interpolates {name} placeholders: T("placeholder.comingSoon", A{"name": "Peers"}).
func T(key string, data ...A) string {
	load()
	v, ok := lookup(key)
	if !ok {
		v = key // last-resort: the key, never empty
	}
	if len(data) > 0 {
		v = interpolate(v, data[0])
	}
	return v
}

// Tn is the plural variant: it picks the CLDR cardinal category for the active locale
// (pluralCategory), looks up key+"."+category, injects {count} = n, and merges any extra
// data. Falls back category → other → one → the raw key, so a locale missing a category
// (or a non-plural key) never renders empty.
func Tn(key string, n int, data ...A) string {
	load()
	cat := pluralCategory(Current(), n)
	d := A{"count": strconv.Itoa(n)}
	if len(data) > 0 {
		for k, val := range data[0] {
			d[k] = val
		}
	}
	for _, suffix := range []string{"." + cat, ".other", ".one"} {
		if v, ok := lookup(key + suffix); ok {
			return interpolate(v, d)
		}
	}
	return interpolate(key, d) // last resort: the key itself, never empty
}

// pluralCategory returns the CLDR cardinal plural category (one/few/many/other) for n in
// the given locale. Counts are non-negative integers, so the fraction-only categories are
// not produced. Locales not listed use the Germanic one/other rule (English default).
func pluralCategory(locale string, n int) string {
	if n < 0 {
		n = -n
	}
	switch locale {
	case "ru", "uk": // East-Slavic: one/few/many by the standard modulo rule
		mod10, mod100 := n%10, n%100
		switch {
		case mod10 == 1 && mod100 != 11:
			return "one"
		case mod10 >= 2 && mod10 <= 4 && !(mod100 >= 12 && mod100 <= 14):
			return "few"
		default:
			return "many"
		}
	case "ja", "zh", "ko", "th", "vi", "id": // no plural distinction
		return "other"
	case "fr": // French: 0 and 1 take the singular
		if n == 0 || n == 1 {
			return "one"
		}
		return "other"
	default: // en, de, es, … : one only for exactly 1
		if n == 1 {
			return "one"
		}
		return "other"
	}
}

// A is the named-argument map for interpolation.
type A map[string]string

// interpolate replaces every {name} in s with data[name]. Unknown placeholders are left intact.
func interpolate(s string, data A) string {
	if len(data) == 0 || !strings.Contains(s, "{") {
		return s
	}
	for k, v := range data {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}
