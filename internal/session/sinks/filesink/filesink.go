// Package filesink writes now-playing data for OBS text/browser sources and other overlays.
// On every change it writes, atomically (temp + rename, so a half-written file is never read):
//
//   - now_playing.txt        - legacy: "artist - title" of the audible deck
//   - now_playing.json       - legacy: the single audible deck's metadata
//   - now_playing_decks.json - ALL loaded decks (per-deck fader/EQ/on-air + master pointer)
//   - now_playing_<A..D>.txt - per-deck "artist - title" (empty when that deck has no track)
//
// The decks file + per-deck text are the multi-deck upgrade: an OBS text source per deck, or a
// browser/overlay reading now_playing_decks.json. Live fader animation belongs to the browser
// overlay server (SSE) - this sink writes on meaningful change, not on every fader tick.
package filesink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

const source = "filesink"

// allDecks is the fixed deck set we keep per-deck text files for, so a deck going empty
// clears its file rather than leaving a stale value behind.
var allDecks = []string{"A", "B", "C", "D"}

// nowPlayingJSON is the legacy single-deck payload (now_playing.json).
type nowPlayingJSON struct {
	Deck        string  `json:"deck"`
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album,omitempty"`
	BPM         float64 `json:"bpm,omitempty"`
	Key         string  `json:"key,omitempty"`
	ElapsedTime float64 `json:"elapsedTime,omitempty"`
	TrackLength float64 `json:"trackLength,omitempty"`
	IsPlaying   bool    `json:"isPlaying"`
	UpdatedAt   string  `json:"updatedAt"`
}

// Sink writes now-playing files into the directory returned by dirFn. dirFn is resolved live
// (not captured once) so changing the folder in settings takes effect - toggle off/on applies
// it immediately, and a later track change applies it without any restart.
type Sink struct {
	log     *logbus.Bus
	dirFn   func() string
	lastDir string
	lastSig string // last write signature; skip redundant writes
}

// New constructs a file sink writing into dirFn() (re-resolved on every write).
func New(log *logbus.Bus, dirFn func() string) *Sink { return &Sink{log: log, dirFn: dirFn} }

// dir resolves the current output directory (falls back to "." if the resolver yields empty).
func (s *Sink) dir() string {
	if s.dirFn == nil {
		return "."
	}
	if d := s.dirFn(); d != "" {
		return d
	}
	return "."
}

// ID implements session.Sink.
func (s *Sink) ID() string { return source }

// Start writes the initial state then rewrites on every merged update until ctx cancels.
func (s *Sink) Start(ctx context.Context, m *session.Merger) error {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	ch, unsub := m.Subscribe()
	defer unsub()

	s.write(m.Snapshot())
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			s.write(m.Snapshot())
		}
	}
}

func (s *Sink) write(st session.UnifiedState) {
	ov := st.BuildOverlay(time.Now(), session.NowPlayingStaleAfter)

	// Audible deck (legacy files) - the snapshot the master pointer references.
	var np *session.DeckSnapshot
	for i := range ov.Decks {
		if ov.Decks[i].Deck == ov.Master.Deck {
			np = &ov.Decks[i]
			break
		}
	}

	legacyText := "" // empty when nothing audible
	if np != nil {
		legacyText = np.Artist + " - " + np.Title
		if np.Artist == "" {
			legacyText = np.Title
		}
	}

	// A folder change forces a write into the new dir even if content is unchanged.
	dir := s.dir()
	dirChanged := dir != s.lastDir
	if dirChanged {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			s.log.Warn(source, "make now-playing dir failed", map[string]any{"dir": dir, "error": err.Error()})
			return
		}
		s.lastDir = dir
	}

	sig := signature(ov)
	if sig == s.lastSig && !dirChanged {
		return // no meaningful change
	}
	s.lastSig = sig

	// Legacy single-deck text + JSON (the audible deck).
	if err := s.atomicWrite("now_playing.txt", []byte(legacyText)); err != nil {
		s.log.Warn(source, "write now_playing.txt failed", map[string]any{"error": err.Error()})
	}
	legacy := nowPlayingJSON{UpdatedAt: ov.UpdatedAt.UTC().Format(time.RFC3339)}
	if np != nil {
		legacy = nowPlayingJSON{
			Deck: np.Deck, Title: np.Title, Artist: np.Artist, Album: np.Album,
			BPM: np.BPM, Key: np.Key, ElapsedTime: np.ElapsedTime, TrackLength: np.TrackLength,
			IsPlaying: np.IsPlaying, UpdatedAt: legacy.UpdatedAt,
		}
	}
	if raw, err := json.MarshalIndent(legacy, "", "  "); err == nil {
		if err := s.atomicWrite("now_playing.json", raw); err != nil {
			s.log.Warn(source, "write now_playing.json failed", map[string]any{"error": err.Error()})
		}
	}

	// Multi-deck JSON (all loaded decks + faders + master pointer).
	if raw, err := json.MarshalIndent(ov, "", "  "); err == nil {
		if err := s.atomicWrite("now_playing_decks.json", raw); err != nil {
			s.log.Warn(source, "write now_playing_decks.json failed", map[string]any{"error": err.Error()})
		}
	}

	// Per-deck text (empty clears a deck that went silent/unloaded).
	byDeck := map[string]session.DeckSnapshot{}
	for _, d := range ov.Decks {
		byDeck[d.Deck] = d
	}
	for _, letter := range allDecks {
		text := ""
		if d, ok := byDeck[letter]; ok {
			text = d.Artist + " - " + d.Title
			if d.Artist == "" {
				text = d.Title
			}
		}
		name := "now_playing_" + letter + ".txt"
		if err := s.atomicWrite(name, []byte(text)); err != nil {
			s.log.Warn(source, "write "+name+" failed", map[string]any{"error": err.Error()})
		}
	}
}

// signature folds the overlay into a compact change key. Track identity + play/on-air state
// + coarse (0.1) fader buckets trigger a rewrite; sub-bucket fader jitter and the elapsed
// clock do not (the file sink isn't the live-animation channel - the overlay server is).
func signature(ov session.Overlay) string {
	var b strings.Builder
	b.WriteString("m:" + ov.Master.Deck + ";")
	for _, d := range ov.Decks {
		fmt.Fprintf(&b, "%s|%s|%s|%t|%t|%.1f;", d.Deck, d.Title, d.Artist, d.IsPlaying, d.OnAir, d.Fader)
	}
	return b.String()
}

func (s *Sink) atomicWrite(name string, data []byte) error {
	path := filepath.Join(s.dir(), name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
