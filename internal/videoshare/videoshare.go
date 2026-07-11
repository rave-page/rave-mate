// Package videoshare is a render mode that publishes each loaded deck's now-playing card as a
// live video frame over the OS-native GPU/IPC video-sharing API, so any compatible receiver
// (OBS, Resolume, TouchDesigner, VRChat, vMix, …) can pull the deck visuals straight from
// memory - no file, no browser source, no window capture.
//
// One named sender per deck ("RaveMate Deck A" …) carries that deck's card; a receiver picks the
// deck it wants by sender name. Frames are the same card the PNG + browser overlays draw
// (internal/deckcard), with the same cued-not-played gate (a deck appears only once on-air).
//
// The transport is platform-specific and selected at build time:
//
//	-tags spout     → Windows, Spout2 (DirectX 11 shared texture)
//	-tags syphon    → macOS, Syphon (Metal/OpenGL shared texture)
//	-tags pipewire  → Linux, PipeWire (SPA video node)
//	(no tag)        → no-op backend: the sink runs but publishes nothing.
//
// The native backends need their platform SDK present at build time, so the default build ships
// the no-op backend and a real one is opted into per platform. See SUPPLY_CHAIN.md.
package videoshare

import (
	"context"
	"image"
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

const source = "videoshare"

// SinkID is the aggregator sink ID (settings auto-apply restarts address it).
const SinkID = source

// SenderName is the published name for a deck's video sender (what a receiver selects).
func SenderName(deck string) string { return "RaveMate Deck " + deck }

// Sender is the platform video-sharing backend. One Sink owns one Sender; the Sender manages a
// named publisher per deck internally. All calls come from the Sink's single Start goroutine, so
// implementations need no internal locking for Send/Remove/Close ordering.
type Sender interface {
	// Send creates (first call for a deck) or updates the named sender for deck with frame img
	// (NRGBA, deckcard.Width×Height). The image is owned by the caller after return.
	Send(deck string, img *image.NRGBA) error
	// Remove tears down a deck's sender (track unloaded / gated out). No-op if absent.
	Remove(deck string) error
	// Close tears down every sender. The Sink calls this once on shutdown.
	Close()
}

// gateEntry mirrors overlayserver's cued-not-played gate.
type gateEntry struct {
	key       string
	everOnAir bool
}

// Sink publishes per-deck video frames over the platform backend. Implements session.Sink.
type Sink struct {
	log     *logbus.Bus
	art     *overlayart.Resolver
	wave    *waveform.Resolver
	waveFn  func() config.OverlayWaveformFeature
	scaleFn func() int            // supersample factor (re-read at Start) for crisp large display
	style   *overlaystyle.Watcher // shared overlay-style.json (gradients, per-band colours)

	// Start-goroutine-only state (no lock):
	scale  int // resolved render scale for this Start lifetime
	gate   map[string]*gateEntry
	sigs   map[string]string           // deck → last sent signature
	sent   map[string]bool             // deck → currently has a live sender
	clocks map[string]*deckclock.Clock // deck → velocity-PLL playback clock (smooth scroll)
}

// New builds the video-share sink. art resolves cover thumbnails (may be nil). wave resolves
// per-track peak overviews + waveFn supplies the live waveform-panel settings (both may be nil).
// scaleFn returns the supersample factor (1..8) so the card renders crisp when a receiver displays
// it large; nil = 1×. The transport is chosen at build time (see package doc); no tag = no-op.
func New(log *logbus.Bus, art *overlayart.Resolver, wave *waveform.Resolver, waveFn func() config.OverlayWaveformFeature, scaleFn func() int, stylePath string) *Sink {
	return &Sink{
		log:     log,
		art:     art,
		wave:    wave,
		waveFn:  waveFn,
		scaleFn: scaleFn,
		style:   overlaystyle.NewWatcher(stylePath),
		gate:    map[string]*gateEntry{},
		sigs:    map[string]string{},
		sent:    map[string]bool{},
		clocks:  map[string]*deckclock.Clock{},
	}
}

// ID implements session.Sink.
func (s *Sink) ID() string { return source }

// Backend reports the compiled-in transport label ("Spout", "Syphon", "PipeWire", or "none").
func Backend() string { return backendName }

// Start opens the platform backend then publishes deck frames on every merged update until ctx
// cancels. A backend that can't open (missing runtime, no GPU) degrades to no-op without failing
// the sink - the rest of the session keeps running.
func (s *Sink) Start(ctx context.Context, m *session.Merger) error {
	sender, err := newSender(s.log)
	if err != nil {
		s.log.Warn(source, "video-share backend unavailable; sink idle", map[string]any{"backend": backendName, "error": err.Error()})
		sender = noopSender{}
	} else {
		s.log.Info(source, "video-share backend ready", map[string]any{"backend": backendName})
	}
	defer sender.Close()

	s.scale = 1
	if s.scaleFn != nil {
		if sc := s.scaleFn(); sc > 0 {
			s.scale = sc
		}
	}
	f, err := deckcard.LoadFacesScale(float64(s.scale))
	if err != nil {
		return err
	}
	defer f.Close()

	ch, unsub := m.Subscribe()
	defer unsub()

	// Frame ticker for the scrolling waveform: merger updates alone are too sparse for smooth
	// scroll, so when the waveform panel is on we re-render at ~30fps (the signature throttle keeps
	// non-waveform decks from re-sending). Idle when the feature is off - merger updates drive it.
	fps := time.NewTicker(33 * time.Millisecond)
	defer fps.Stop()

	s.publish(ctx, sender, f, m.Snapshot())
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			s.publish(ctx, sender, f, m.Snapshot())
		case <-fps.C:
			if s.waveFn != nil && s.waveFn().Enabled {
				s.publish(ctx, sender, f, m.Snapshot())
			}
		}
	}
}

// publish renders the gated decks and pushes/removes each deck's frame (throttled by signature).
func (s *Sink) publish(ctx context.Context, sender Sender, f *deckcard.Faces, st session.UnifiedState) {
	now := time.Now()
	ov := st.BuildOverlay(now, session.NowPlayingStaleAfter)
	visible := s.applyGate(ov.Decks)

	byDeck := map[string]session.DeckSnapshot{}
	for _, d := range visible {
		byDeck[d.Deck] = d
	}
	for _, letter := range []string{"A", "B", "C", "D"} {
		d, ok := byDeck[letter]
		if !ok {
			if s.sent[letter] {
				if err := sender.Remove(letter); err != nil {
					s.log.Debug(source, "remove sender failed", map[string]any{"deck": letter, "error": err.Error()})
				}
				s.sent[letter] = false
				delete(s.sigs, letter)
			}
			continue
		}
		// Resolve art first (cheap on a cache hit) so the signature reflects whether the cover is
		// actually available - the cover resolves a beat after the track loads, and without this the
		// throttle would keep the first (placeholder) frame forever.
		artPath, hasArt := "", false
		if s.art != nil {
			if p, ok := s.art.Ensure(ctx, d); ok {
				artPath, hasArt = p, true
			}
		}
		wo := s.waveOpts(d, now)
		sig := signature(d, hasArt, wo)
		if sig == s.sigs[letter] && s.sent[letter] {
			continue // no meaningful change
		}
		var art image.Image
		if hasArt {
			art = decodeJPEG(artPath)
		}
		img := deckcard.RenderScaled(f, d, art, wo, float64(s.scale))
		if err := sender.Send(letter, img); err != nil {
			s.log.Debug(source, "send frame failed", map[string]any{"deck": letter, "error": err.Error()})
			continue
		}
		s.sigs[letter] = sig
		s.sent[letter] = true
	}
}

// waveOpts builds the combined waveform-panel options for a deck (zero value when disabled).
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
	// Smooth scroll: velocity-PLL per deck off the RAW elapsed reading (not the snapping interpolator).
	clk := s.clocks[d.Deck]
	if clk == nil {
		clk = &deckclock.Clock{}
		s.clocks[d.Deck] = clk
	}
	wo.Position = clk.Tick(d.ElapsedTime, d.IsPlaying, wo.Duration, now)
	return wo
}

// applyGate hides decks whose current track has never been on-air (mirrors overlayserver).
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
			delete(s.gate, deck)
		}
	}
	return out
}

// signature folds a deck card into a compact change key (same buckets as pngsink). With the
// waveform panel on, the interpolated position (0.1s) + peaks-ready join it so each frame ticks.
func signature(d session.DeckSnapshot, hasArt bool, wo deckcard.WaveOpts) string {
	var b strings.Builder
	b.WriteString(d.Deck)
	b.WriteByte('|')
	b.WriteString(d.Title)
	b.WriteByte('|')
	b.WriteString(d.Artist)
	b.WriteByte('|')
	b.WriteString(d.Key)
	writeF(&b, d.BPM, 0)
	writeB(&b, d.IsPlaying)
	writeB(&b, d.OnAir)
	writeB(&b, hasArt)
	writeF(&b, float64(int(d.ElapsedTime)), 0)
	writeF(&b, d.TrackLength, 0)
	writeF(&b, d.Fader, 1)
	writeB(&b, d.HasMixer)
	writeF(&b, d.EQHigh, 1)
	writeF(&b, d.EQMid, 1)
	writeF(&b, d.EQLow, 1)
	writeF(&b, d.Filter, 1)
	if wo.Enabled {
		writeB(&b, true)
		writeF(&b, wo.Position, 2) // 0.01s → re-send at the ~30fps ticker rate for a smooth scroll
		writeB(&b, len(wo.Peaks) > 0)
	}
	return b.String()
}

// noopSender is the fallback used when a backend fails to open at runtime.
type noopSender struct{}

func (noopSender) Send(string, *image.NRGBA) error { return nil }
func (noopSender) Remove(string) error             { return nil }
func (noopSender) Close()                          {}
