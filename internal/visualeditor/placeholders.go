package visualeditor

import "regexp"

// Provider resolves a placeholder key to a string. Value returns ok=false for unknown keys.
type Provider interface {
	Value(key string) (string, bool)
}

// Known built-in placeholder keys (documented for the UI). Additional keys come from a
// document's static Vars map or a live host provider.
var KnownPlaceholders = []string{
	"track.title", "track.artist", "track.bpm", "track.key", "time", "date",
}

// placeholderRE matches {key} where key is [A-Za-z0-9_.-]+.
var placeholderRE = regexp.MustCompile(`\{([A-Za-z0-9_.\-]+)\}`)

// Substitute replaces {key} tokens in s using p. Unknown keys are left as the literal
// {key} so unresolved placeholders stay visible while editing. A nil provider is a no-op.
func Substitute(s string, p Provider) string {
	if p == nil || s == "" {
		return s
	}
	return placeholderRE.ReplaceAllStringFunc(s, func(tok string) string {
		key := tok[1 : len(tok)-1]
		if v, ok := p.Value(key); ok {
			return v
		}
		return tok
	})
}

// MapProvider is a static key→value provider.
type MapProvider map[string]string

// Value implements Provider.
func (m MapProvider) Value(k string) (string, bool) { v, ok := m[k]; return v, ok }

// ChainProvider tries each provider in order, first hit wins.
type ChainProvider []Provider

// Value implements Provider.
func (c ChainProvider) Value(k string) (string, bool) {
	for _, p := range c {
		if p == nil {
			continue
		}
		if v, ok := p.Value(k); ok {
			return v, true
		}
	}
	return "", false
}

// docProvider is the document's static Vars as a Provider.
func (d *Document) varsProvider() Provider {
	if len(d.Vars) == 0 {
		return nil
	}
	return MapProvider(d.Vars)
}
