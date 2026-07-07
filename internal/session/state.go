package session

import "time"

// FieldValue is a merged field value with provenance (drives the UI + debugging).
type FieldValue struct {
	Value      any       `json:"value"`
	Source     string    `json:"source"`
	TS         time.Time `json:"ts"`
	Confidence float64   `json:"confidence"`
}

// UnifiedState is a point-in-time snapshot of the merged session across all scopes.
type UnifiedState struct {
	Decks    map[string]map[string]FieldValue `json:"decks"`    // deck letter → field → value
	Channels map[string]map[string]FieldValue `json:"channels"` // channel num → field → value
	Master   map[string]FieldValue            `json:"master"`
}

// DeckField returns the merged value of a deck field, if present.
func (u UnifiedState) DeckField(deck, field string) (FieldValue, bool) {
	d, ok := u.Decks[deck]
	if !ok {
		return FieldValue{}, false
	}
	v, ok := d[field]
	return v, ok
}
