package session

import (
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OnAirFaderThreshold is the channel-fader level above which a playing deck counts as
// audible ("on air"). Below it the track is loaded/cued but not in the mix.
const OnAirFaderThreshold = 0.02

// deckOrder is the stable display order of decks in an Overlay.
var deckOrder = []string{"A", "B", "C", "D"}

// DeckSnapshot is one deck's flattened state for overlay renderers (file sink, browser
// overlay, native PNG, obs-websocket). All mixer levels are 0..1. A deck appears in an
// Overlay only when a track is loaded (Title non-empty) and its data is fresh.
type DeckSnapshot struct {
	Deck        string  `json:"deck"` // "A".."D"
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album,omitempty"`
	Genre       string  `json:"genre,omitempty"`
	Key         string  `json:"key,omitempty"`
	BPM         float64 `json:"bpm,omitempty"`
	ElapsedTime float64 `json:"elapsedTime"`           // seconds
	TrackLength float64 `json:"trackLength,omitempty"` // seconds
	IsPlaying   bool    `json:"isPlaying"`

	// ElapsedAt is when ElapsedTime was last observed (the merge timestamp). The overlay
	// renderers interpolate live position = ElapsedTime + (now-ElapsedAt) while playing, so the
	// waveform scrolls smoothly between sparse source updates; a fresh reading (incl. after a
	// backspin / beat-jump) snaps it. Not serialized - the browser interpolates from SSE arrival.
	ElapsedAt time.Time `json:"-"`
	OnAir     bool      `json:"onAir"` // playing AND fader above OnAirFaderThreshold (or fader unknown)

	Fader    float64 `json:"fader"` // 0..1 (0 if unknown - see HasFader)
	HasFader bool    `json:"hasFader"`
	// EQ/filter: 0..1, 0.5 ≈ neutral. NOT omitempty - a killed band (0) is a real value, and
	// dropping it would make the overlay read "absent" as neutral and snap the curve.
	EQHigh   float64 `json:"eqHigh"`
	EQMid    float64 `json:"eqMid"`
	EQLow    float64 `json:"eqLow"`
	Filter   float64 `json:"filter"`
	HasMixer bool    `json:"hasMixer"` // EQ/filter data present (fader tracked separately via HasFader)
	Cue      bool    `json:"cue,omitempty"`

	Path   string `json:"-"`                // local file path (art resolution); never serialized to overlays
	ArtKey string `json:"artKey,omitempty"` // stable key for art caching/lookup (hash of path|artist|title)
}

// LivePosition interpolates the current playback position (seconds) from the last elapsed
// observation: while playing it advances with wall-clock between sparse source updates; a fresh
// reading snaps it (handles backspin / beat-jump). Drift is capped (a stalled feed shouldn't run
// away). Clamped to [0, TrackLength] when the length is known.
func (d DeckSnapshot) LivePosition(now time.Time) float64 {
	pos := d.ElapsedTime
	if d.IsPlaying && !d.ElapsedAt.IsZero() {
		if dt := now.Sub(d.ElapsedAt).Seconds(); dt > 0 && dt < 30 {
			pos += dt
		}
	}
	if pos < 0 {
		pos = 0
	}
	if d.TrackLength > 0 && pos > d.TrackLength {
		pos = d.TrackLength
	}
	return pos
}

// MasterSnapshot is the derived "what's audibly playing right now" pointer + master clock.
type MasterSnapshot struct {
	Deck string  `json:"deck,omitempty"` // audible deck letter ("" = silent)
	BPM  float64 `json:"bpm,omitempty"`  // master BPM if known
}

// Overlay is the multi-deck snapshot every overlay renderer consumes. Decks holds only
// decks with a loaded, fresh track, in A→D order.
type Overlay struct {
	Decks     []DeckSnapshot `json:"decks"`
	Master    MasterSnapshot `json:"master"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// BuildOverlay flattens the merged state into a renderer-ready multi-deck snapshot. A deck
// is included when it has a loaded track (non-empty title) and its data is fresh
// (≤ maxAge old; maxAge <= 0 disables the freshness gate). Mixer levels are pulled from the
// deck's mapped channel (A→1 … D→4).
func (u UnifiedState) BuildOverlay(now time.Time, maxAge time.Duration) Overlay {
	ov := Overlay{UpdatedAt: now}
	// Derive the playing decks once (all-decks scan + per-deck stale/fader reads + sort) and reuse
	// for both the db-fallback branch and the master pointer, instead of DeriveNowPlayingAt twice.
	playing := u.DerivePlayingDecksAt(now, maxAge)
	npDeck, npOK := "", false
	for _, d := range playing {
		if d.Audible {
			npDeck, npOK = d.Deck, true
			break
		}
	}
	// Fallback for sources that report play-state but no track metadata (e.g. rekordbox via MIDI:
	// play/cue only, no title). The audible playing deck inherits the master-scope latest play from
	// the master.db poll (~60s lag) so the overlay still shows the live deck + a best-effort title.
	dbFallbackDeck := ""
	if StringField(u.Master, FieldTitle) != "" && npOK && StringField(u.Decks[npDeck], FieldTitle) == "" {
		dbFallbackDeck = npDeck
	}
	for _, deck := range deckOrder {
		fields := u.Decks[deck]
		if fields == nil {
			continue
		}
		title := StringField(fields, FieldTitle)
		fromDB := false
		if strings.TrimSpace(title) == "" {
			if deck != dbFallbackDeck {
				continue // nothing loaded
			}
			title, fromDB = StringField(u.Master, FieldTitle), true
		}
		if maxAge > 0 && deckStale(fields, now, maxAge) {
			continue
		}
		ds := DeckSnapshot{
			Deck:   deck,
			Title:  title,
			Artist: StringField(fields, FieldArtist),
			Album:  StringField(fields, FieldAlbum),
			Genre:  StringField(fields, FieldGenre),
			Key:    StringField(fields, FieldKey),
			Path:   StringField(fields, FieldPath),
		}
		ds.BPM, _ = floatVal(fields, FieldBPM)
		if fromDB { // backfill missing metadata from the master.db latest play
			ds.Artist = orStr(ds.Artist, StringField(u.Master, FieldArtist))
			ds.Key = orStr(ds.Key, StringField(u.Master, FieldKey))
			ds.Path = orStr(ds.Path, StringField(u.Master, FieldPath))
			if ds.BPM == 0 {
				ds.BPM, _ = floatVal(u.Master, FieldBPM)
			}
		}
		ds.ElapsedTime, _ = floatVal(fields, FieldElapsedTime)
		if fv, ok := fields[FieldElapsedTime]; ok {
			ds.ElapsedAt = fv.TS
		}
		ds.TrackLength, _ = floatVal(fields, FieldTrackLength)
		ds.IsPlaying, _ = boolVal(fields, FieldIsPlaying)

		// Neutral defaults: a loaded deck starts at flat EQ + filter off until a mixer source updates
		// it, so a fresh load / partial MIDI never reads 0 (= full cut). Fader unknown → renderers
		// treat it as full (unity) via HasFader.
		ds.EQLow, ds.EQMid, ds.EQHigh, ds.Filter = 0.5, 0.5, 0.5, 0.5

		if ch, ok := deckChannel[deck]; ok {
			chFields := u.Channels[ch]
			if f, has := floatVal(chFields, FieldFader); has {
				ds.Fader, ds.HasFader = f, true
			}
			if v, has := floatVal(chFields, FieldEQHigh); has {
				ds.EQHigh, ds.HasMixer = v, true
			}
			if v, has := floatVal(chFields, FieldEQMid); has {
				ds.EQMid, ds.HasMixer = v, true
			}
			if v, has := floatVal(chFields, FieldEQLow); has {
				ds.EQLow, ds.HasMixer = v, true
			}
			if v, has := floatVal(chFields, FieldFilter); has {
				ds.Filter, ds.HasMixer = v, true
			}
			if b, has := boolVal(chFields, FieldCue); has {
				ds.Cue = b
			}
		}
		// On air: playing and either audible by fader, or fader unknown (assume audible).
		ds.OnAir = ds.IsPlaying && (!ds.HasFader || ds.Fader > OnAirFaderThreshold)
		ds.ArtKey = artKey(ds.Path, ds.Artist, ds.Title)
		ov.Decks = append(ov.Decks, ds)
	}
	if npOK {
		ov.Master.Deck = npDeck
	}
	if bpm, ok := floatVal(u.Master, FieldBPM); ok {
		ov.Master.BPM = bpm
	}
	sort.SliceStable(ov.Decks, func(i, j int) bool { return ov.Decks[i].Deck < ov.Decks[j].Deck })
	return ov
}

// orStr returns a if non-empty, else b (metadata backfill helper).
func orStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// artKey is a stable, filesystem-safe key identifying a track's artwork. Prefers the file
// path (one image per file); falls back to artist|title. Empty when nothing identifies it.
func artKey(path, artist, title string) string {
	seed := strings.TrimSpace(path)
	if seed == "" {
		seed = strings.ToLower(strings.TrimSpace(artist) + "|" + strings.TrimSpace(title))
	}
	if seed == "|" || seed == "" {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	return strconv.FormatUint(h.Sum64(), 16)
}
