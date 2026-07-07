// Package overlayserver serves a live multi-deck OBS overlay over loopback HTTP. It is a
// session.Sink: while enabled it runs an HTTP server on 127.0.0.1 that an OBS Browser source
// points at. The page renders one card per loaded deck (cover art, title/artist/BPM/key,
// elapsed, and a live fader/EQ meter), gets live updates via Server-Sent Events (so faders
// animate smoothly without disk churn), and supports a drag-to-position layout editor whose
// per-deck positions persist to disk.
//
// rave-mate's UI rule is "native, no webview" - that governs rave-mate's OWN UI. This package
// serves HTML/JS as OUTPUT consumed by OBS's browser (an explicit, user-requested overlay
// channel), not as rave-mate's interface.
package overlayserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"encoding/binary"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/overlayart"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/waveform"
)

const (
	source    = "overlayserver"
	minFlush  = 16 * time.Millisecond // min gap between SSE pushes (~60 Hz) - low latency, bounded
	clientBuf = 8
)

// gateEntry tracks whether a deck's current track has ever been on-air (faded in). A track
// that's only cued (loaded, fader down) stays hidden until it goes on-air once.
type gateEntry struct {
	key       string // current track identity; reset everOnAir when it changes
	everOnAir bool
}

// Sink runs the overlay HTTP server while enabled.
type Sink struct {
	log         *logbus.Bus
	portFn      func() int
	art         *overlayart.Resolver
	wave        *waveform.Resolver
	waveFn      func() config.OverlayWaveformFeature
	layoutPath  string
	stylePath   string
	presetsPath string

	mu       sync.RWMutex
	latest   session.Overlay // retains per-deck Path (not serialized) for /art
	styleRaw json.RawMessage // cached overlay-style.json, pushed live over SSE (no manual reload)
	layRaw   json.RawMessage // cached overlay-layout.json, pushed live over SSE

	gate map[string]*gateEntry // pump-goroutine only; cued-not-played gate

	subMu   sync.Mutex
	subs    map[int]chan []byte
	nextSub int
}

// New builds the overlay server. portFn is resolved live; art resolves cover thumbnails; wave
// resolves per-track peak overviews + waveFn supplies the waveform-panel settings (both may be
// nil); layoutPath is where the drag-editor persists deck positions (style persists alongside it).
func New(log *logbus.Bus, portFn func() int, art *overlayart.Resolver, wave *waveform.Resolver, waveFn func() config.OverlayWaveformFeature, layoutPath string) *Sink {
	return &Sink{
		log: log, portFn: portFn, art: art, wave: wave, waveFn: waveFn, layoutPath: layoutPath,
		stylePath:   filepath.Join(filepath.Dir(layoutPath), "overlay-style.json"),
		presetsPath: filepath.Join(filepath.Dir(layoutPath), "overlay-presets.json"),
		subs:        map[int]chan []byte{},
		gate:        map[string]*gateEntry{},
	}
}

// ID implements session.Sink.
func (s *Sink) ID() string { return source }

// Start runs the HTTP server + the update pump until ctx is cancelled (feature toggled off).
func (s *Sink) Start(ctx context.Context, m *session.Merger) error {
	port := s.portFn()
	s.rebuild(m.Snapshot())
	s.mu.Lock()
	s.styleRaw = readJSON(s.stylePath)
	s.layRaw = readJSON(s.layoutPath)
	s.mu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/deck/", s.handleIndex) // single-deck per-URL view for separate OBS sources
	mux.HandleFunc("/state", s.handleState)
	mux.HandleFunc("/events", s.handleEvents)
	mux.HandleFunc("/art/", s.handleArt)
	mux.HandleFunc("/peaks/", s.handlePeaks)
	mux.HandleFunc("/layout", s.handleLayout)
	mux.HandleFunc("/style", s.handleStyle)
	mux.HandleFunc("/presets", s.handlePresets)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("overlay listen :%d: %w", port, err)
	}
	s.log.Info(source, "overlay server up", map[string]any{"url": fmt.Sprintf("http://127.0.0.1:%d/", port)})

	debuglog.Go(s.log, source, func() { s.pump(ctx, m) })

	errc := make(chan error, 1)
	debuglog.Go(s.log, source, func() { errc <- srv.Serve(ln) })

	select {
	case <-ctx.Done():
		sh, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
		return nil
	case err := <-errc:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// pump pushes the latest overlay to SSE clients with low latency: it flushes immediately on a
// merged update when idle, otherwise schedules a single trailing flush at the minFlush (~60 Hz)
// boundary. So a MIDI fader/EQ move interrupts and renders within ~16 ms, without flooding.
func (s *Sink) pump(ctx context.Context, m *session.Merger) {
	ch, unsub := m.Subscribe()
	defer unsub()

	var lastFlush time.Time
	pending := false
	var timer *time.Timer
	var timerC <-chan time.Time
	flush := func() {
		s.rebuild(m.Snapshot())
		s.broadcast(s.stateJSON())
		lastFlush = time.Now()
		pending = false
	}
	flush() // prime
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			if since := time.Since(lastFlush); since >= minFlush {
				flush()
			} else if !pending {
				pending = true
				timer = time.NewTimer(minFlush - since)
				timerC = timer.C
			}
		case <-timerC:
			timerC = nil
			if pending {
				flush()
			}
		}
	}
}

func (s *Sink) rebuild(st session.UnifiedState) {
	ov := st.BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
	ov.Decks = s.applyGate(ov.Decks)
	s.mu.Lock()
	s.latest = ov
	s.mu.Unlock()
}

// applyGate hides decks whose current track has never been on-air (cued but not yet faded in).
// Once a track goes on-air it stays shown until a different track loads on that deck. Runs only
// on the pump goroutine, so s.gate needs no lock.
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
			delete(s.gate, deck) // track unloaded - forget so a reload re-gates
		}
	}
	return out
}

// waveWire is the waveform-panel config the page reads to drive its canvas (mirrors the native
// renderer's options). Sent inside each SSE payload so a config change applies on the next flush.
type waveWire struct {
	Enabled     bool    `json:"enabled"`
	ZoomSeconds float64 `json:"zoomSeconds"`
	PlayheadPct float64 `json:"playheadPct"`
	WaveColor   string  `json:"waveColor"`
	WaveOpacity float64 `json:"waveOpacity"`
	BgColor     string  `json:"bgColor"`
	BgOpacity   float64 `json:"bgOpacity"`
}

// statePayload embeds the overlay (decks/master/updatedAt flatten to top level) + the waveform
// config - backward-compatible: the page still reads `.decks`, now also `.waveform`. style/layout
// carry the live overlay-style.json / overlay-layout.json so non-edit overlays (the OBS source)
// apply edits without a manual reload.
type statePayload struct {
	session.Overlay
	Waveform waveWire        `json:"waveform"`
	Style    json.RawMessage `json:"style,omitempty"`
	Layout   json.RawMessage `json:"layout,omitempty"`
}

// readJSON loads an opaque-JSON store file, defaulting to "{}" when missing/empty.
func readJSON(path string) json.RawMessage {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func (s *Sink) stateJSON() []byte {
	s.mu.RLock()
	ov := s.latest
	st, ly := s.styleRaw, s.layRaw
	s.mu.RUnlock()
	wc := waveWire{}
	if s.waveFn != nil {
		f := s.waveFn()
		wc = waveWire{
			Enabled: f.Enabled, ZoomSeconds: f.ResolvedZoomSeconds(), PlayheadPct: f.ResolvedPlayheadPct(),
			WaveColor: f.ResolvedWaveColor(), WaveOpacity: f.ResolvedWaveOpacity(),
			BgColor: f.ResolvedBgColor(), BgOpacity: f.ResolvedBgOpacity(),
		}
	}
	raw, err := json.Marshal(statePayload{Overlay: ov, Waveform: wc, Style: st, Layout: ly})
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// ── HTTP handlers ──────────────────────────────────────────────────────────────

func (s *Sink) handleIndex(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	ok := p == "/" || p == "/overlay" || strings.HasPrefix(p, "/deck/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, must-revalidate") // always serve the latest overlay (OBS/browser caching bit us)
	_, _ = w.Write(overlayHTML)                                  // page reads the path itself for single-deck mode
}

func (s *Sink) handleState(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.stateJSON())
}

func (s *Sink) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	id, ch := s.addSub()
	defer s.removeSub(id)

	// Prime with the current state so a fresh OBS source paints immediately.
	writeSSE(w, s.stateJSON())
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, data)
			flusher.Flush()
		}
	}
}

// handleArt resolves + serves the cached cover thumbnail for /art/<artKey>. Looks the key up
// in the current overlay to recover the track's file path for extraction.
func (s *Sink) handleArt(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/art/")
	key = strings.TrimSuffix(key, ".jpg")
	if key == "" {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	var deck session.DeckSnapshot
	found := false
	for _, d := range s.latest.Decks {
		if d.ArtKey == key {
			deck, found = d, true
			break
		}
	}
	s.mu.RUnlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	// Decouple extraction from the request context: the browser re-requests /art every SSE tick
	// and each new request cancels the previous one - using r.Context() would kill the in-flight
	// ffmpeg extraction mid-run (→ miss → negative-cache → covers stalled for ~90s). Use an
	// independent timeout instead so a started extraction always completes.
	ectx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	path, ok := s.art.Ensure(ectx, deck)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, path)
}

// handlePeaks serves the waveform overview for /peaks/<artKey>.bin as raw bytes:
// [uint32 LE durationMs][uint8 peak buckets]. 404 until the decode lands (the page retries on the
// next track tick, like /art). Looks the key up in the current overlay to recover the file path.
func (s *Sink) handlePeaks(w http.ResponseWriter, r *http.Request) {
	if s.wave == nil {
		http.NotFound(w, r)
		return
	}
	key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/peaks/"), ".bin")
	if key == "" {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	var deck session.DeckSnapshot
	found := false
	for _, d := range s.latest.Decks {
		if d.ArtKey == key {
			deck, found = d, true
			break
		}
	}
	s.mu.RUnlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	p, ok := s.wave.Get(deck) // non-blocking; kicks off a background decode on a miss
	if !ok {
		http.NotFound(w, r)
		return
	}
	hdr := make([]byte, 4)
	binary.LittleEndian.PutUint32(hdr, p.DurationMs)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(hdr)
	_, _ = w.Write(p.Data)
}

// handleLayout reads (GET) or persists (POST) the drag-editor's deck positions as opaque JSON.
func (s *Sink) handleLayout(w http.ResponseWriter, r *http.Request) { s.jsonFile(w, r, s.layoutPath) }

// handleStyle reads (GET) or persists (POST) the overlay style (card opacity, colors) as JSON.
func (s *Sink) handleStyle(w http.ResponseWriter, r *http.Request) { s.jsonFile(w, r, s.stylePath) }

// handlePresets reads (GET) or persists (POST) the reusable gradient/colour preset library as JSON
// - durable on disk + shared across clients (localStorage was per-browser + lost on cache refresh).
func (s *Sink) handlePresets(w http.ResponseWriter, r *http.Request) { s.jsonFile(w, r, s.presetsPath) }

// jsonFile is a GET/POST opaque-JSON store backing the layout + style endpoints.
func (s *Sink) jsonFile(w http.ResponseWriter, r *http.Request, path string) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		raw, err := os.ReadFile(path)
		if err != nil {
			_, _ = w.Write([]byte("{}"))
			return
		}
		_, _ = w.Write(raw)
	case http.MethodPost:
		var body json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, body, 0o644); err != nil {
			http.Error(w, "write failed", http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			http.Error(w, "save failed", http.StatusInternalServerError)
			return
		}
		// Update the live cache + push to every open overlay so edits apply with no manual reload.
		// (Presets are a library, not live state - they don't broadcast.)
		live := false
		s.mu.Lock()
		switch path {
		case s.stylePath:
			s.styleRaw = append(json.RawMessage(nil), body...)
			live = true
		case s.layoutPath:
			s.layRaw = append(json.RawMessage(nil), body...)
			live = true
		}
		s.mu.Unlock()
		if live {
			s.broadcast(s.stateJSON())
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── SSE subscriber registry ──────────────────────────────────────────────────

func (s *Sink) addSub() (int, chan []byte) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	id := s.nextSub
	s.nextSub++
	ch := make(chan []byte, clientBuf)
	s.subs[id] = ch
	return id, ch
}

func (s *Sink) removeSub(id int) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if ch, ok := s.subs[id]; ok {
		delete(s.subs, id)
		close(ch)
	}
}

func (s *Sink) broadcast(data []byte) {
	s.subMu.Lock()
	chans := make([]chan []byte, 0, len(s.subs))
	for _, ch := range s.subs {
		chans = append(chans, ch)
	}
	s.subMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- data:
		default: // slow client - drop this frame, next flush carries the latest
		}
	}
}

func writeSSE(w http.ResponseWriter, data []byte) {
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
}
