// Package traktor receives Traktor Pro 4 metadata POSTs on 127.0.0.1:8080 and fans
// them out to subscribers, holding an in-memory snapshot for late attachers. Port of
// electron/src/main/traktor.ts. Optional raw-payload jsonl log for schema discovery.
package traktor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
)

const source = "traktor"

// EventType enumerates the fan-out event kinds (1:1 with the in-process bus).
type EventType string

const (
	DeckLoaded    EventType = "deck.loaded"
	DeckUpdate    EventType = "deck.update"
	ChannelUpdate EventType = "channel.update"
	MasterClock   EventType = "master.clock"
)

// Event is one normalized Traktor state change.
type Event struct {
	Type    EventType
	Deck    string // "A".."D" for deck events
	Channel string // "1".."4" for channel events
	State   map[string]any
}

// Snapshot is the full current mixer state.
type Snapshot struct {
	Decks    map[string]map[string]any `json:"decks"`
	Channels map[string]map[string]any `json:"channels"`
	Master   map[string]any            `json:"master"`
}

var (
	reDeckLoaded = regexp.MustCompile(`^/deckLoaded/([A-D])$`)
	reDeckUpdate = regexp.MustCompile(`^/updateDeck/([A-D])$`)
	reChannel    = regexp.MustCompile(`^/updateChannel/([1-4])$`)
)

// Server is the Traktor metadata listener.
type Server struct {
	log     *logbus.Bus
	mon     *logbus.Bus // HTTP-ingest monitor (Traktor tab); nil = off
	addr    string
	logPath string

	logging   atomic.Bool
	listening atomic.Bool

	mu       sync.RWMutex
	decks    map[string]map[string]any
	channels map[string]map[string]any
	master   map[string]any

	monMu    sync.Mutex
	monLast  map[string]time.Time  // route → last emitted line
	monPend  map[string]monPending // route → buffered latest payload
	monTimer bool

	subMu   sync.Mutex
	subs    map[int]chan Event
	nextSub int

	srv *http.Server
}

// New constructs a listener bound to addr (e.g. "127.0.0.1:8080"). logPath enables the
// raw-payload jsonl; pass "" to disable. logging gates whether payloads are written.
func New(log *logbus.Bus, addr, logPath string, logging bool) *Server {
	s := &Server{
		log:      log,
		addr:     addr,
		logPath:  logPath,
		decks:    map[string]map[string]any{"A": {}, "B": {}, "C": {}, "D": {}},
		channels: map[string]map[string]any{"1": {}, "2": {}, "3": {}, "4": {}},
		master:   map[string]any{},
		subs:     map[int]chan Event{},
		monLast:  map[string]time.Time{},
		monPend:  map[string]monPending{},
	}
	s.logging.Store(logging)
	return s
}

// SetMonitor attaches an HTTP-ingest monitor bus (the Traktor monitor view subscribes).
func (s *Server) SetMonitor(mon *logbus.Bus) { s.mon = mon }

// monInterval caps monitor lines at ~4/route/sec: still a live view in the Traktor tab,
// but no longer one child→daemon frame per ingest POST (deck ticks arrive many times/sec).
const monInterval = 250 * time.Millisecond

type monPending struct {
	payload map[string]any
	n       int // lines coalesced into this one
}

// monitor logs one ingest line (route + a compact payload summary) to the monitor bus,
// throttled per route to monInterval, latest payload wins (a burst's final state still
// shows via the trailing flush; the coalesced count is surfaced on the line).
func (s *Server) monitor(route string, payload map[string]any) {
	if s.mon == nil {
		return
	}
	s.monMu.Lock()
	now := time.Now()
	if p, ok := s.monPend[route]; ok {
		s.monPend[route] = monPending{payload: payload, n: p.n + 1}
		s.monMu.Unlock()
		return
	}
	if now.Sub(s.monLast[route]) < monInterval {
		s.monPend[route] = monPending{payload: payload}
		s.armMonFlushLocked()
		s.monMu.Unlock()
		return
	}
	s.monLast[route] = now
	s.monMu.Unlock()
	s.monEmit(route, payload, 0)
}

// armMonFlushLocked schedules one trailing flush per window (monMu held).
func (s *Server) armMonFlushLocked() {
	if s.monTimer {
		return
	}
	s.monTimer = true
	time.AfterFunc(monInterval, s.monFlush)
}

// monFlush emits buffered routes whose window elapsed; re-arms while any remain.
func (s *Server) monFlush() {
	s.monMu.Lock()
	s.monTimer = false
	now := time.Now()
	type line struct {
		route   string
		payload map[string]any
		n       int
	}
	var due []line
	rearm := false
	for route, p := range s.monPend {
		if now.Sub(s.monLast[route]) < monInterval {
			rearm = true
			continue
		}
		delete(s.monPend, route)
		s.monLast[route] = now
		due = append(due, line{route, p.payload, p.n})
	}
	if rearm {
		s.armMonFlushLocked()
	}
	s.monMu.Unlock()
	for _, l := range due {
		s.monEmit(l.route, l.payload, l.n)
	}
}

// monEmit writes one monitor line; coalesced = extra lines folded into it.
func (s *Server) monEmit(route string, payload map[string]any, coalesced int) {
	fields := map[string]any{"keys": len(payload)}
	if coalesced > 0 {
		fields["coalesced"] = coalesced
	}
	s.mon.Info(route, summarizePayload(payload), fields)
}

// Start binds the listener and serves until ctx is cancelled. The bind happens
// synchronously so a port-in-use error is reported deterministically (the Electron
// client also uses :8080 - only one Traktor bridge can own it). Blocks until shutdown.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		s.log.Error(source, "listener bind failed (port in use? Electron client may own it)", map[string]any{"addr": s.addr, "error": err.Error()})
		if s.mon != nil {
			s.mon.Error("bind", "listener bind failed - "+s.addr+" in use (Electron client owns it?). No Traktor HTTP data will arrive.", map[string]any{"error": err.Error()})
		}
		return err
	}
	s.listening.Store(true)
	s.log.Info(source, "listening", map[string]any{"addr": s.addr, "logging": s.logging.Load(), "logFile": s.logPath})
	if s.mon != nil {
		s.mon.Info("bind", "listening on "+s.addr+" - waiting for Traktor deck/channel/master POSTs", nil)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	errCh := make(chan error, 1)
	debuglog.Go(s.log, source, func() { errCh <- s.srv.Serve(ln) })

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
		s.listening.Store(false)
		return nil
	case err := <-errCh:
		s.listening.Store(false)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		s.log.Error(source, "listener error", map[string]any{"error": err.Error()})
		return err
	}
}

// Listening reports whether the HTTP listener is currently bound.
func (s *Server) Listening() bool { return s.listening.Load() }

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	payload := parseJSON(raw)
	s.appendPayloadLog(r.URL.Path, payload)
	s.monitor(r.URL.Path, payload)

	switch {
	case reDeckLoaded.MatchString(r.URL.Path):
		deck := reDeckLoaded.FindStringSubmatch(r.URL.Path)[1]
		// deck.loaded resets the deck before merging (parity w/ electron).
		st := cloneMerge(map[string]any{}, payload)
		st["loadedAt"] = time.Now().UnixMilli()
		s.setDeck(deck, st)
		s.emit(Event{Type: DeckLoaded, Deck: deck, State: st})
	case reDeckUpdate.MatchString(r.URL.Path):
		deck := reDeckUpdate.FindStringSubmatch(r.URL.Path)[1]
		st := s.mergeDeck(deck, payload)
		s.emit(Event{Type: DeckUpdate, Deck: deck, State: st})
	case reChannel.MatchString(r.URL.Path):
		ch := reChannel.FindStringSubmatch(r.URL.Path)[1]
		st := s.mergeChannel(ch, payload)
		s.emit(Event{Type: ChannelUpdate, Channel: ch, State: st})
	case r.URL.Path == "/updateMasterClock":
		st := s.mergeMaster(payload)
		s.emit(Event{Type: MasterClock, State: st})
	default:
		// Unknown route: still ACK 200 so Traktor doesn't back off; captured in log.
		s.log.Debug(source, "unknown route", map[string]any{"path": r.URL.Path})
	}
	w.WriteHeader(http.StatusOK)
}

// ── state mutation (each returns a copy of the resulting state) ──────────────

func (s *Server) setDeck(deck string, st map[string]any) {
	s.mu.Lock()
	s.decks[deck] = st
	s.mu.Unlock()
}

func (s *Server) mergeDeck(deck string, p map[string]any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decks[deck] = cloneMerge(s.decks[deck], p)
	return cloneMerge(map[string]any{}, s.decks[deck])
}

func (s *Server) mergeChannel(ch string, p map[string]any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels[ch] = cloneMerge(s.channels[ch], p)
	return cloneMerge(map[string]any{}, s.channels[ch])
}

func (s *Server) mergeMaster(p map[string]any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.master = cloneMerge(s.master, p)
	return cloneMerge(map[string]any{}, s.master)
}

// Snapshot returns a deep-ish copy of current mixer state.
func (s *Server) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := func(m map[string]map[string]any) map[string]map[string]any {
		out := make(map[string]map[string]any, len(m))
		for k, v := range m {
			out[k] = cloneMerge(map[string]any{}, v)
		}
		return out
	}
	return Snapshot{Decks: cp(s.decks), Channels: cp(s.channels), Master: cloneMerge(map[string]any{}, s.master)}
}

// ── subscriptions ────────────────────────────────────────────────────────────

// Subscribe returns a channel of future events + an unsubscribe func. Buffered;
// drops on overflow so a slow consumer can't stall the HTTP handler.
func (s *Server) Subscribe() (<-chan Event, func()) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	id := s.nextSub
	s.nextSub++
	ch := make(chan Event, 128)
	s.subs[id] = ch
	return ch, func() {
		s.subMu.Lock()
		defer s.subMu.Unlock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
	}
}

func (s *Server) emit(e Event) {
	s.subMu.Lock()
	chans := make([]chan Event, 0, len(s.subs))
	for _, ch := range s.subs {
		chans = append(chans, ch)
	}
	s.subMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- e:
		default:
		}
	}
}

// ── payload logging ──────────────────────────────────────────────────────────

// SetLogging toggles raw-payload jsonl writing at runtime.
func (s *Server) SetLogging(on bool) { s.logging.Store(on) }

// LogPath returns the jsonl path (may be "").
func (s *Server) LogPath() string { return s.logPath }

func (s *Server) appendPayloadLog(path string, payload map[string]any) {
	if !s.logging.Load() || s.logPath == "" {
		return
	}
	entry := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339), "url": path, "payload": payload}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return // diagnostic only - never block delivery
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}

func parseJSON(b []byte) map[string]any {
	if len(b) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// summarizePayload renders a compact, deterministic "k=v" line for the monitor, favouring
// the high-signal fields (title/artist/bpm/playing) and capping length so a row stays terse.
func summarizePayload(p map[string]any) string {
	if len(p) == 0 {
		return "(empty)"
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return payloadRank(keys[i]) < payloadRank(keys[j]) || (payloadRank(keys[i]) == payloadRank(keys[j]) && keys[i] < keys[j])
	})
	var b strings.Builder
	for i, k := range keys {
		if i >= 6 {
			fmt.Fprintf(&b, " +%d", len(keys)-i)
			break
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		v := fmt.Sprintf("%v", p[k])
		if len(v) > 28 {
			v = v[:27] + "…"
		}
		fmt.Fprintf(&b, "%s=%s", k, v)
	}
	return b.String()
}

// payloadRank sorts the informative fields first in a monitor summary.
func payloadRank(k string) int {
	switch strings.ToLower(k) {
	case "title":
		return 0
	case "artist":
		return 1
	case "bpm":
		return 2
	case "isplaying", "playing", "play":
		return 3
	}
	return 9
}

// cloneMerge returns a new map = base overlaid with overlay.
func cloneMerge(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	maps.Copy(out, base)
	maps.Copy(out, overlay)
	return out
}
