package i18n

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// loadRaw parses every embedded locale directly (bypasses the runtime skip-on-error) so the
// tests see malformed files instead of silently ignoring them.
func loadRaw(t *testing.T) map[string]map[string]string {
	t.Helper()
	ents, err := localesFS.ReadDir("locales")
	if err != nil {
		t.Fatalf("read locales dir: %v", err)
	}
	out := map[string]map[string]string{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		code := strings.TrimSuffix(e.Name(), ".json")
		data, err := localesFS.ReadFile("locales/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		flat, err := parseCatalog(data)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		out[code] = flat
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// TestLocalesWellFormed is the hard guard: every locale parses, en (source of truth) is
// non-empty, and no non-en locale carries STALE keys (keys absent from en = a typo or a
// removed string). These are real bugs, so they FAIL the build.
func TestLocalesWellFormed(t *testing.T) {
	cats := loadRaw(t)
	en := cats[defaultLocale]
	if len(en) == 0 {
		t.Fatalf("en.json is the source of truth but has no keys")
	}
	for code, flat := range cats {
		if code == defaultLocale {
			continue
		}
		var stale []string
		for k := range flat {
			if _, ok := en[k]; !ok {
				stale = append(stale, k)
			}
		}
		if len(stale) > 0 {
			sort.Strings(stale)
			t.Errorf("%s.json has %d key(s) not present in en.json (stale/typo): %s",
				code, len(stale), strings.Join(stale, ", "))
		}
	}
}

// TestTranslationCompleteness reports, per non-en locale, which en keys are still untranslated
// (they fall back to English at runtime - not a bug). It is informational by default so
// `go test ./...` stays green with an intentionally-partial locale; set I18N_STRICT=1 to make
// missing keys a hard failure (use in a "full-coverage" CI lane).
func TestTranslationCompleteness(t *testing.T) {
	cats := loadRaw(t)
	en := cats[defaultLocale]
	enKeys := sortedKeys(en)
	strict := os.Getenv("I18N_STRICT") != ""

	for code, flat := range cats {
		if code == defaultLocale {
			continue
		}
		var missing []string
		for _, k := range enKeys {
			if _, ok := flat[k]; !ok {
				missing = append(missing, k)
			}
		}
		covered := len(enKeys) - len(missing)
		pct := 100
		if len(enKeys) > 0 {
			pct = covered * 100 / len(enKeys)
		}
		t.Logf("%s: %d/%d keys translated (%d%%)", code, covered, len(enKeys), pct)
		if len(missing) > 0 {
			t.Logf("%s: %d untranslated key(s) (fall back to en): %s",
				code, len(missing), strings.Join(missing, ", "))
			if strict {
				t.Errorf("%s: %d untranslated key(s) with I18N_STRICT set", code, len(missing))
			}
		}
	}
}

func TestFallbackAndInterpolation(t *testing.T) {
	SetLocale("de")
	if got := T("tab.settings"); got != "Einstellungen" {
		t.Errorf("de tab.settings = %q, want Einstellungen", got)
	}
	// brand.tagline stays in English across every locale (brand string) → returns the en value.
	if got := T("brand.tagline"); got != "Local worker & bridge for rave.page" {
		t.Errorf("de brand.tagline = %q, want the English brand string", got)
	}
	// unknown key → the key itself, never empty.
	if got := T("does.not.exist"); got != "does.not.exist" {
		t.Errorf("missing key = %q, want the key back", got)
	}
	if got := Tn("track", 1); got != "1 Titel" {
		t.Errorf("Tn(1) = %q, want 1 Titel", got)
	}
	if got := Tn("track", 5); got != "5 Titel" {
		t.Errorf("Tn(5) = %q", got)
	}
	SetLocale("en")
	if got := Tn("track", 3); got != "3 tracks" {
		t.Errorf("en Tn(3) = %q", got)
	}
	if got := T("placeholder.comingSoon", A{"name": "Peers"}); got != "Coming soon - the Peers controls will render here." {
		t.Errorf("interpolation = %q", got)
	}
}

// TestPluralCategory pins the per-locale CLDR cardinal rules Tn depends on.
func TestPluralCategory(t *testing.T) {
	cases := []struct {
		loc  string
		n    int
		want string
	}{
		{"en", 1, "one"}, {"en", 0, "other"}, {"en", 2, "other"},
		{"de", 1, "one"}, {"de", 5, "other"},
		{"es", 1, "one"}, {"es", 21, "other"},
		{"fr", 0, "one"}, {"fr", 1, "one"}, {"fr", 2, "other"},
		{"ja", 1, "other"}, {"ja", 2, "other"}, {"ja", 5, "other"},
		// Russian/Ukrainian: 1,21,31→one; 2-4,22-24→few; 0,5-20,25-30,11-14→many
		{"ru", 1, "one"}, {"ru", 21, "one"}, {"ru", 11, "many"},
		{"ru", 2, "few"}, {"ru", 4, "few"}, {"ru", 22, "few"}, {"ru", 12, "many"},
		{"ru", 5, "many"}, {"ru", 0, "many"}, {"ru", 25, "many"}, {"ru", 100, "many"},
		{"uk", 1, "one"}, {"uk", 3, "few"}, {"uk", 14, "many"}, {"uk", 111, "many"},
	}
	for _, c := range cases {
		if got := pluralCategory(c.loc, c.n); got != c.want {
			t.Errorf("pluralCategory(%q,%d) = %q, want %q", c.loc, c.n, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"de-DE":       "de",
		"de_DE.UTF-8": "de",
		"EN":          "en",
		"en-US":       "en",
		"C":           "c",
		"":            "",
		"pt_BR@euro":  "pt",
	} {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
