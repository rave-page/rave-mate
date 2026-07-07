package session

import "time"

// NowPlaying is the derived audible track across all decks - the single "what's playing
// right now" used by the file sink and the recorder. Source is the deck letter it came
// from; Fields are that deck's merged values.
type NowPlaying struct {
	Deck   string
	Fields map[string]FieldValue
	Score  float64 // audibility score (fader-weighted); 0 if silent
}

// deckChannel maps a deck letter to its mixer channel (Traktor default A→1 … D→4).
var deckChannel = map[string]string{"A": "1", "B": "2", "C": "3", "D": "4"}

// NowPlayingStaleAfter bounds how old a "playing" deck's data may be before it counts as
// silent. A source that dies mid-play (app killed, connection gone) leaves isPlaying=true
// behind forever, while a deck that's really playing refreshes elapsed/meta every second -
// so stale deck state must not keep the recorder/now-playing "live" indefinitely.
const NowPlayingStaleAfter = 2 * time.Minute

// DeriveNowPlaying picks the audible deck: among decks with isPlaying=true and fresh data
// (≤ NowPlayingStaleAfter old), the one with the highest channel fader (ties broken by
// greater elapsed time). Returns ok=false when nothing is audibly playing.
func (u UnifiedState) DeriveNowPlaying() (NowPlaying, bool) {
	return u.DeriveNowPlayingAt(time.Now(), NowPlayingStaleAfter)
}

// DeriveNowPlayingAt is DeriveNowPlaying with an explicit clock + staleness window
// (maxAge <= 0 disables the staleness check).
func (u UnifiedState) DeriveNowPlayingAt(now time.Time, maxAge time.Duration) (NowPlaying, bool) {
	var best NowPlaying
	bestScore := -1.0
	for deck, fields := range u.Decks {
		if b, _ := boolVal(fields, FieldIsPlaying); !b {
			continue
		}
		if maxAge > 0 && deckStale(fields, now, maxAge) {
			continue
		}
		fader := 1.0 // assume audible if we have no channel fader data
		if ch, ok := deckChannel[deck]; ok {
			if f, has := floatVal(u.Channels[ch], FieldFader); has {
				fader = f
			}
		}
		elapsed, _ := floatVal(fields, FieldElapsedTime)
		score := fader*1000 + elapsed/1e6 // fader dominates; elapsed breaks ties
		if score > bestScore {
			bestScore = score
			best = NowPlaying{Deck: deck, Fields: fields, Score: fader}
		}
	}
	if bestScore < 0 {
		return NowPlaying{}, false
	}
	return best, true
}

// deckStale reports whether a deck's merged data has gone quiet: its newest field TS is
// older than maxAge. Zero timestamps (synthetic states, tests) are never stale.
func deckStale(fields map[string]FieldValue, now time.Time, maxAge time.Duration) bool {
	var newest time.Time
	for _, fv := range fields {
		if fv.TS.After(newest) {
			newest = fv.TS
		}
	}
	return !newest.IsZero() && now.Sub(newest) > maxAge
}

func boolVal(m map[string]FieldValue, f string) (bool, bool) {
	if m == nil {
		return false, false
	}
	fv, ok := m[f]
	if !ok {
		return false, false
	}
	b, ok := fv.Value.(bool)
	return b, ok
}

func floatVal(m map[string]FieldValue, f string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	fv, ok := m[f]
	if !ok {
		return 0, false
	}
	switch n := fv.Value.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// StringField returns a string-typed merged field value (empty if absent/other type).
func StringField(m map[string]FieldValue, f string) string {
	if m == nil {
		return ""
	}
	if fv, ok := m[f]; ok {
		if s, ok := fv.Value.(string); ok {
			return s
		}
	}
	return ""
}
