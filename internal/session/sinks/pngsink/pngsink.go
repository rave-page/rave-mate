// Package pngsink renders one PNG card per loaded deck into a folder, so OBS Image sources (one
// per deck) can display each deck natively - no browser, no cgo.
//
// It is one render mode alongside the browser overlay server (overlayserver), the obs-websocket
// renderer (overlayobs) and the GPU/IPC video-share sink (videoshare): same data model
// (session.Overlay), same cued-not-played gate (a deck shows only once it's been faded in /
// on-air), same card renderer (internal/deckcard). The output here is a flat file per deck:
//
//   - deck_A.png … deck_D.png - transparent-background card for each visible deck. A deck that
//     unloads (or was never on-air) has its PNG removed.
//
// Writes are atomic (temp + rename) and throttled: a card is only re-rendered when its content
// meaningfully changed (track identity, play/on-air, coarse fader/EQ buckets, whole-second
// elapsed).
package pngsink

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/deckcard"
	"rave.page/mate/internal/deckclock"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/overlayart"
	"rave.page/mate/internal/overlaystyle"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/waveform"
)

const source = "pngsink"

// allDecks is the fixed deck set; a deck going empty removes its PNG rather than leaving a stale
// card behind.
var allDecks = []string{"A", "B", "C", "D"}

// card size mirrors the shared renderer (kept as local names for the tests + size checks).
const (
	cardW = deckcard.Width
	cardH = deckcard.Height
)

// faces aliases the shared renderer's font set; loadFaces builds it (one per Start goroutine).
type faces = deckcard.Faces

func loadFaces() (*faces, error) { return deckcard.LoadFaces() }

// gateEntry mirrors overlayserver's cued-not-played gate: a track that's only cued (loaded,
// fader down) stays hidden until it goes on-air once; reset on track change.
type gateEntry struct {
	key       string
	everOnAir bool
}

// Sink renders per-deck PNG cards into the directory returned by dirFn. dirFn is resolved live
// (not captured once) so a folder change in settings applies on the next write without a restart.
type Sink struct {
	log    *logbus.Bus
	dirFn  func() string
	art    *overlayart.Resolver
	wave   *waveform.Resolver
	waveFn func() config.OverlayWaveformFeature
	style  *overlaystyle.Watcher // shared overlay-style.json (gradients, per-band colours)

	// Start-goroutine-only state (no lock): the subscribe loop is single-threaded.
	lastDir   string
	gate      map[string]*gateEntry
	sigs      map[string]string           // deck → last rendered signature
	written   map[string]bool             // deck → PNG currently on disk
	clocks    map[string]*deckclock.Clock // deck → velocity-PLL playback clock (smooth scroll)
	lastFrame time.Time                   // last waveform-frame write (caps the PNG frame rate)
}

// New constructs a PNG sink writing one card per deck into dirFn() (re-resolved on every write).
// art resolves cover thumbnails (cached JPEGs); may be nil. wave resolves per-track peak overviews
// + waveFn supplies the live waveform-panel settings; both may be nil (no waveform panel).
func New(log *logbus.Bus, dirFn func() string, art *overlayart.Resolver, wave *waveform.Resolver, waveFn func() config.OverlayWaveformFeature, stylePath string) *Sink {
	return &Sink{
		log:     log,
		dirFn:   dirFn,
		art:     art,
		wave:    wave,
		waveFn:  waveFn,
		style:   overlaystyle.NewWatcher(stylePath),
		gate:    map[string]*gateEntry{},
		sigs:    map[string]string{},
		written: map[string]bool{},
		clocks:  map[string]*deckclock.Clock{},
	}
}

// ID implements session.Sink.
func (s *Sink) ID() string { return source }

// dir resolves the current output directory (falls back to "." if empty).
func (s *Sink) dir() string {
	if s.dirFn == nil {
		return "."
	}
	if d := s.dirFn(); d != "" {
		return d
	}
	return "."
}

// Start renders the initial state then re-renders on every merged update until ctx cancels.
// Blocking (mirrors filesink): the subscribe loop owns all mutable state.
func (s *Sink) Start(ctx context.Context, m *session.Merger) error {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	f, err := loadFaces()
	if err != nil {
		return err
	}
	defer f.Close()

	ch, unsub := m.Subscribe()
	defer unsub()

	s.render(ctx, f, m.Snapshot())
	// When the waveform panel is on, also render at ~30fps so it scrolls smoothly (event-driven
	// updates alone are ~1/s). renderDecks time-throttles to this rate; NB this rewrites the PNGs
	// continuously while a deck plays - heavier disk I/O, the cost of a moving file-based overlay.
	fps := time.NewTicker(33 * time.Millisecond)
	defer fps.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			s.render(ctx, f, m.Snapshot())
		case <-fps.C:
			if s.waveFn != nil && s.waveFn().Enabled {
				s.render(ctx, f, m.Snapshot())
			}
		}
	}
}

// render builds the overlay from the merged state then writes/removes each card.
func (s *Sink) render(ctx context.Context, f *faces, st session.UnifiedState) {
	now := time.Now()
	ov := st.BuildOverlay(now, session.NowPlayingStaleAfter)
	s.renderDecks(ctx, f, ov.Decks, now)
}

// renderDecks applies the cued-not-played gate then writes a PNG for each visible deck (throttled
// by signature) and removes the PNG of any deck that's gone.
func (s *Sink) renderDecks(ctx context.Context, f *faces, decks []session.DeckSnapshot, now time.Time) {
	visible := s.applyGate(decks)

	// Waveform live → the panel scrolls every frame, so bypass the per-deck signature throttle but
	// cap the write rate (~30fps) so frequent merger updates (EQ moves) don't write faster than that.
	wfLive := s.waveFn != nil && s.waveFn().Enabled
	if wfLive {
		if now.Sub(s.lastFrame) < 30*time.Millisecond {
			return
		}
		s.lastFrame = now
	}

	dir := s.dir()
	dirChanged := dir != s.lastDir
	if dirChanged {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			s.log.Warn(source, "make deck-png dir failed", map[string]any{"dir": dir, "error": err.Error()})
			return
		}
		s.lastDir = dir
		s.sigs = map[string]string{} // force re-render into the new dir
	}

	byDeck := map[string]session.DeckSnapshot{}
	for _, d := range visible {
		byDeck[d.Deck] = d
	}

	for _, letter := range allDecks {
		d, ok := byDeck[letter]
		if !ok {
			s.clear(letter)
			continue
		}
		// Resolve art first (cheap on a cache hit) so the signature reflects whether the cover is
		// actually available - it resolves a beat after the track loads; otherwise the throttle
		// would keep the first (placeholder) frame forever.
		artPath, hasArt := "", false
		if s.art != nil {
			if p, ok := s.art.Ensure(ctx, d); ok {
				artPath, hasArt = p, true
			}
		}
		wo := s.waveOpts(d, now)
		sig := signature(d, hasArt, wo)
		if !wfLive && sig == s.sigs[letter] && s.written[letter] && !dirChanged {
			continue // no meaningful change (when the waveform's live it moves every frame → always draw)
		}
		img := deckcard.RenderScaled(f, d, decodeArt(artPath), wo, 1)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			s.log.Warn(source, "encode deck png failed", map[string]any{"deck": letter, "error": err.Error()})
			continue
		}
		if err := s.atomicWrite("deck_"+letter+".png", buf.Bytes()); err != nil {
			s.log.Warn(source, "write deck png failed", map[string]any{"deck": letter, "error": err.Error()})
			continue
		}
		s.sigs[letter] = sig
		s.written[letter] = true
	}
}

// waveOpts builds the combined waveform-panel options for a deck (zero value when disabled):
// live interpolated position + cached peaks (when ready) at the configured zoom + playhead.
func (s *Sink) waveOpts(d session.DeckSnapshot, now time.Time) deckcard.WaveOpts {
	if s.waveFn == nil {
		return deckcard.WaveOpts{}
	}
	cfg := s.waveFn()
	if !cfg.Enabled {
		return deckcard.WaveOpts{}
	}
	wo := deckcard.WaveOpts{
		Enabled:      true,
		Duration:     d.TrackLength,
		ZoomSeconds:  cfg.ResolvedZoomSeconds(),
		PlayheadFrac: cfg.ResolvedPlayheadPct(),
		WaveColor:    cfg.ResolvedWaveColor(),
		WaveOpacity:  cfg.ResolvedWaveOpacity(),
		BgColor:      cfg.ResolvedBgColor(),
		BgOpacity:    cfg.ResolvedBgOpacity(),
	}
	if s.wave != nil {
		if p, ok := s.wave.Get(d); ok {
			wo.Peaks = p.Data
			wo.Duration = float64(p.DurationMs) / 1000
			wo.PeaksKey = d.ArtKey // stable per-track key for the smoothed-envelope cache
		}
	}
	wo.ApplyStyle(s.style.Get(), cfg.ResolvedWaveColor(), cfg.ResolvedBgColor()) // style.json overrides config
	clk := s.clocks[d.Deck]
	if clk == nil {
		clk = &deckclock.Clock{}
		s.clocks[d.Deck] = clk
	}
	wo.Position = clk.Tick(d.ElapsedTime, d.IsPlaying, wo.Duration, now)
	return wo
}

// clear removes a deck's PNG when it unloads or is gated out.
func (s *Sink) clear(letter string) {
	delete(s.sigs, letter)
	if !s.written[letter] {
		return
	}
	path := filepath.Join(s.dir(), "deck_"+letter+".png")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.log.Warn(source, "remove deck png failed", map[string]any{"deck": letter, "error": err.Error()})
	}
	s.written[letter] = false
}

// applyGate hides decks whose current track has never been on-air (cued but not yet faded in).
// Once on-air it stays shown until a different track loads on that deck. Single-goroutine (the
// Start loop), so s.gate needs no lock. Mirrors overlayserver.
func (s *Sink) applyGate(decks []session.DeckSnapshot) []session.DeckSnapshot {
	out := decks[:0:0]
	seen := map[string]bool{}
	for _, d := range decks {
		seen[d.Deck] = true
		e := s.gate[d.Deck]
		if e == nil || e.key != d.ArtKey {
			e = &gateEntry{key: d.ArtKey}
			s.gate[d.Deck] = e
		}
		if d.OnAir {
			e.everOnAir = true
		}
		if e.everOnAir {
			out = append(out, d)
		}
	}
	for deck := range s.gate {
		if !seen[deck] {
			delete(s.gate, deck) // unloaded - forget so a reload re-gates
		}
	}
	return out
}

func (s *Sink) atomicWrite(name string, data []byte) error {
	path := filepath.Join(s.dir(), name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// decodeArt decodes the cached cover JPEG at path (the resolver already normalized to JPEG).
// "" or a decode error → nil (deckcard draws the placeholder).
func decodeArt(path string) image.Image {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	img, err := jpeg.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

// signature folds a deck card into a compact change key: track identity + play/on-air + coarse
// (0.1) fader/EQ buckets + whole-second elapsed + whether art is available. Sub-bucket jitter
// doesn't re-render; the clock advances at most once per second. With the waveform panel on, the
// interpolated position (0.1s) + peaks-ready join the key so the scroll advances each update.
func signature(d session.DeckSnapshot, hasArt bool, wo deckcard.WaveOpts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%.0f|%s|%t|%t|%t|",
		d.Deck, d.Title, d.Artist, d.BPM, d.Key, d.IsPlaying, d.OnAir, hasArt)
	fmt.Fprintf(&b, "%d|%.0f|", int(d.ElapsedTime), d.TrackLength)
	fmt.Fprintf(&b, "f%.1f|m%t|h%.1f|m%.1f|l%.1f|x%.1f",
		d.Fader, d.HasMixer, d.EQHigh, d.EQMid, d.EQLow, d.Filter)
	if wo.Enabled {
		fmt.Fprintf(&b, "|w%.1f|p%t|z%.1f|y%.2f", wo.Position, len(wo.Peaks) > 0, wo.ZoomSeconds, wo.PlayheadFrac)
	}
	return b.String()
}
