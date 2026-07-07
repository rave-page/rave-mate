// Package overlayobs is a third overlay render mode: instead of writing files (filesink) or
// serving a browser source (overlayserver), it drives OBS directly over obs-websocket v5.
// rave-mate creates/updates a pair of inputs per loaded deck - a Text input (title/artist/
// bpm/key/elapsed) and an Image input (cover art) - inside the current program scene, and
// positions them with a simple stacked layout. Each deck thus appears in OBS with no browser
// source at all.
//
// It is a session.Sink. It mirrors the overlayserver's cued-not-played gate (a deck shows
// only once its track has gone on-air) and throttles OBS traffic (push only on meaningful
// change, min one update interval apart). OBS may be absent/disconnected at any time - every
// OBS call is best-effort: on error the tick is abandoned and retried next change, never
// blocking the merger or crashing.
//
// Wiring note: New takes an OBSClient interface (satisfied by *obs.Client). The concrete
// obs-websocket connection lives in the featurehost "obs" child; the parent wires a connected
// *obs.Client (or a proxy implementing OBSClient) in. A nil client = no-op sink.
package overlayobs

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/obs"
	"rave.page/mate/internal/overlayart"
	"rave.page/mate/internal/session"
)

const source = "overlayobs"

// inputNamePrefix is the shared prefix for every input this sink owns, so they're easy to spot
// (and clean up) in OBS and never collide with the user's own sources.
const inputNamePrefix = "RaveMate Deck "

// OBSClient is the obs-websocket surface overlayobs needs. *obs.Client satisfies it; the
// parent may instead pass a proxy that forwards to the featurehost obs child.
type OBSClient interface {
	GetCurrentProgramScene(ctx context.Context) (string, error)
	GetInputList(ctx context.Context, kind string) ([]obs.InputInfo, error)
	CreateInput(ctx context.Context, p obs.CreateInputParams) (int, error)
	SetInputSettings(ctx context.Context, inputName string, settings map[string]any, overlay bool) error
	GetSceneItemID(ctx context.Context, sceneName, sourceName string) (int, error)
	SetSceneItemEnabled(ctx context.Context, sceneName string, itemID int, enabled bool) error
	SetSceneItemTransform(ctx context.Context, sceneName string, itemID int, transform map[string]any) error
}

// layout describes the stacked default placement (logical px on the OBS canvas).
type layout struct {
	MarginX, MarginY float64 // top-left of the first deck card
	ArtSize          float64 // art square edge
	Gap              float64 // gap between art and text, and between stacked cards' rows
	RowHeight        float64 // vertical stride between decks
}

func defaultLayout() layout {
	return layout{MarginX: 40, MarginY: 40, ArtSize: 120, Gap: 16, RowHeight: 140}
}

// artPos / textPos return the canvas position for deck slot i (0-based, top→down).
func (l layout) artPos(i int) (x, y float64) {
	return l.MarginX, l.MarginY + float64(i)*l.RowHeight
}

func (l layout) textPos(i int) (x, y float64) {
	return l.MarginX + l.ArtSize + l.Gap, l.MarginY + float64(i)*l.RowHeight
}

// gateEntry mirrors overlayserver's cued-not-played gate.
type gateEntry struct {
	key       string
	everOnAir bool
}

// deckInputs caches the resolved scene-item ids for one deck's two inputs in the active scene.
// textOn/artOn/placed* track the LAST-APPLIED visibility + position so we only push those when they
// actually change - re-asserting every tick fought the user's manual hide/move in OBS.
type deckInputs struct {
	textItem int
	artItem  int
	hasText  bool
	hasArt   bool
	shown    bool // currently enabled in OBS
	textOn   bool // last-applied text-item visibility
	artOn    bool // last-applied art-item visibility
	placedT  bool // text transform applied once (don't re-position over a manual move)
	placedA  bool // art transform applied once
}

// Sink drives OBS directly. Implements session.Sink.
type Sink struct {
	log    *logbus.Bus
	client OBSClient
	art    *overlayart.Resolver

	textKind string // OBS input kind for text (GOOS-dependent default)
	imageK3  string // OBS input kind for images
	layout   layout
	minGap   time.Duration // min interval between OBS update passes
	reqTO    time.Duration // per-request timeout

	// pump-goroutine-only state (no lock needed):
	scene    string
	known    map[string]*deckInputs // deck → its inputs in s.scene
	gate     map[string]*gateEntry
	lastSig  string
	lastPush time.Time

	mu sync.Mutex // guards SetClient swap vs pump read of client
}

// Option configures the sink.
type Option func(*Sink)

// WithTextKind overrides the OBS text input kind (default text_gdiplus_v3 on Windows,
// text_ft2_source_v2 elsewhere).
func WithTextKind(kind string) Option { return func(s *Sink) { s.textKind = kind } }

// WithImageKind overrides the OBS image input kind (default image_source).
func WithImageKind(kind string) Option { return func(s *Sink) { s.imageK3 = kind } }

// WithLayout overrides the stacked layout geometry.
func WithLayout(l layout) Option { return func(s *Sink) { s.layout = l } }

// WithMinInterval sets the min gap between OBS update passes (default 1s).
func WithMinInterval(d time.Duration) Option { return func(s *Sink) { s.minGap = d } }

// New builds the OBS-driving overlay sink. client may be nil (no-op until SetClient).
func New(log *logbus.Bus, client OBSClient, art *overlayart.Resolver, opts ...Option) *Sink {
	s := &Sink{
		log:      log,
		client:   client,
		art:      art,
		textKind: defaultTextKind(),
		imageK3:  "image_source",
		layout:   defaultLayout(),
		minGap:   time.Second,
		reqTO:    5 * time.Second,
		known:    map[string]*deckInputs{},
		gate:     map[string]*gateEntry{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// defaultTextKind picks the platform-native OBS text source kind.
func defaultTextKind() string {
	if runtime.GOOS == "windows" {
		return "text_gdiplus_v3"
	}
	return "text_ft2_source_v2"
}

// SetClient swaps the OBS client at runtime (e.g. reconnect). Safe from any goroutine.
func (s *Sink) SetClient(c OBSClient) {
	s.mu.Lock()
	s.client = c
	s.mu.Unlock()
}

func (s *Sink) curClient() OBSClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// ID implements session.Sink.
func (s *Sink) ID() string { return source }

// Start subscribes to the merger and pushes deck inputs to OBS on meaningful change until ctx
// cancels. Errors talking to OBS are non-fatal (disconnected is normal).
func (s *Sink) Start(ctx context.Context, m *session.Merger) error {
	ch, unsub := m.Subscribe()
	defer unsub()

	// Prime once, then react to updates (throttled).
	s.tick(ctx, m.Snapshot())
	for {
		select {
		case <-ctx.Done():
			s.teardown()
			return nil
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			s.tick(ctx, m.Snapshot())
		}
	}
}

// tick rebuilds the gated overlay and pushes it to OBS on meaningful change, throttled so OBS
// is never hit more than once per minGap. Runs only on the Start goroutine - pump-only state
// needs no lock. signature() buckets elapsed to whole seconds, so a steady clock yields at most
// ~1 change/sec/deck; minGap coalesces bursts on top of that.
func (s *Sink) tick(ctx context.Context, st session.UnifiedState) {
	ov := st.BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
	decks := s.applyGate(ov.Decks)

	sig := signature(decks)
	if sig == s.lastSig {
		return // nothing meaningful changed
	}
	if !s.lastPush.IsZero() && time.Since(s.lastPush) < s.minGap {
		return // changed, but pushed too recently - a later update carries it forward
	}

	client := s.curClient()
	if client == nil {
		return // disconnected: no-op, retry on next change
	}

	if err := s.push(ctx, client, decks); err != nil {
		// OBS likely went away - drop cached scene/item state so a reconnect re-resolves.
		s.log.Debug(source, "obs push failed", map[string]any{"error": err.Error()})
		s.scene = ""
		s.known = map[string]*deckInputs{}
		return
	}
	s.lastSig = sig
	s.lastPush = time.Now()
}

// push ensures the current scene's per-deck inputs match decks: shown decks get their inputs
// created/updated/positioned/enabled; decks no longer shown get their inputs disabled.
func (s *Sink) push(ctx context.Context, client OBSClient, decks []session.DeckSnapshot) error {
	rctx, cancel := context.WithTimeout(ctx, s.reqTO)
	defer cancel()

	scene, err := client.GetCurrentProgramScene(rctx)
	if err != nil {
		return fmt.Errorf("get scene: %w", err)
	}
	if scene == "" {
		return nil
	}
	if scene != s.scene {
		// scene changed: forget cached item ids (they're per-scene).
		s.scene = scene
		s.known = map[string]*deckInputs{}
	}

	shown := map[string]bool{}
	for i, d := range decks {
		shown[d.Deck] = true
		if err := s.applyDeck(rctx, client, scene, i, d); err != nil {
			return err
		}
	}
	// Disable inputs for decks we previously showed but no longer do.
	for deck, di := range s.known {
		if shown[deck] || !di.shown {
			continue
		}
		s.disableDeck(rctx, client, scene, deck, di)
	}
	return nil
}

// applyDeck creates (if needed) + updates + positions + enables one deck's text & art inputs.
func (s *Sink) applyDeck(ctx context.Context, client OBSClient, scene string, slot int, d session.DeckSnapshot) error {
	di := s.known[d.Deck]
	if di == nil {
		di = &deckInputs{}
		s.known[d.Deck] = di
	}

	// ── text input ──
	textName := TextInputName(d.Deck)
	if !di.hasText {
		id, err := s.ensureInput(ctx, client, scene, textName, s.textKind, map[string]any{"text": deckText(d)})
		if err != nil {
			return err
		}
		di.textItem, di.hasText = id, true
	} else {
		if err := client.SetInputSettings(ctx, textName, map[string]any{"text": deckText(d)}, true); err != nil {
			return err
		}
	}
	if !di.placedT { // position once, then leave it (respect a manual move)
		tx, ty := s.layout.textPos(slot)
		if err := client.SetSceneItemTransform(ctx, scene, di.textItem, posTransform(tx, ty)); err != nil {
			return err
		}
		di.placedT = true
	}
	if !di.textOn { // enable once, then leave it (respect a manual hide)
		if err := client.SetSceneItemEnabled(ctx, scene, di.textItem, true); err != nil {
			return err
		}
		di.textOn = true
	}

	// ── art input (best-effort: a missing cover just leaves the art input absent) ──
	if path, ok := s.art.Ensure(ctx, d); ok {
		artName := ArtInputName(d.Deck)
		if !di.hasArt {
			id, err := s.ensureInput(ctx, client, scene, artName, s.imageK3, map[string]any{"file": path})
			if err != nil {
				return err
			}
			di.artItem, di.hasArt = id, true
		} else {
			if err := client.SetInputSettings(ctx, artName, map[string]any{"file": path}, true); err != nil {
				return err
			}
		}
		if !di.placedA {
			ax, ay := s.layout.artPos(slot)
			if err := client.SetSceneItemTransform(ctx, scene, di.artItem, posTransform(ax, ay)); err != nil {
				return err
			}
			di.placedA = true
		}
		if !di.artOn {
			if err := client.SetSceneItemEnabled(ctx, scene, di.artItem, true); err != nil {
				return err
			}
			di.artOn = true
		}
	} else if di.hasArt && di.artOn {
		_ = client.SetSceneItemEnabled(ctx, scene, di.artItem, false) // no art now: hide it
		di.artOn = false
	}

	di.shown = true
	return nil
}

// disableDeck hides both of a deck's inputs (track unloaded / gated out). Only toggles items that
// are currently on, and records it so applyDeck re-enables them when the deck comes back.
func (s *Sink) disableDeck(ctx context.Context, client OBSClient, scene, deck string, di *deckInputs) {
	if di.hasText && di.textOn {
		_ = client.SetSceneItemEnabled(ctx, scene, di.textItem, false)
		di.textOn = false
	}
	if di.hasArt && di.artOn {
		_ = client.SetSceneItemEnabled(ctx, scene, di.artItem, false)
		di.artOn = false
	}
	di.shown = false
	_ = deck
}

// ensureInput returns the sceneItemId for inputName in scene, creating the input if absent.
// If the input already exists (e.g. left over from a prior run) it's reused via GetSceneItemId
// and its settings updated.
func (s *Sink) ensureInput(ctx context.Context, client OBSClient, scene, inputName, kind string, settings map[string]any) (int, error) {
	id, err := client.GetSceneItemID(ctx, scene, inputName)
	if err == nil {
		// existing input in this scene: refresh its settings.
		if uerr := client.SetInputSettings(ctx, inputName, settings, true); uerr != nil {
			return 0, uerr
		}
		return id, nil
	}
	// Not in this scene - create it (CreateInput also adds it as a scene item).
	id, cerr := client.CreateInput(ctx, obs.CreateInputParams{
		SceneName:        scene,
		InputName:        inputName,
		InputKind:        kind,
		InputSettings:    settings,
		SceneItemEnabled: true,
	})
	if cerr == nil {
		return id, nil
	}
	// CreateInput can fail because the input exists globally but in another scene; in that case
	// add it to this scene as a new item. Fall back to GetSceneItemId one more time.
	if id2, gerr := client.GetSceneItemID(ctx, scene, inputName); gerr == nil {
		return id2, nil
	}
	return 0, fmt.Errorf("ensure input %q: %w", inputName, cerr)
}

// teardown best-effort hides every input we created (OBS keeps them; user can delete). Called
// on Start ctx cancel (feature toggled off).
func (s *Sink) teardown() {
	client := s.curClient()
	if client == nil || s.scene == "" {
		return
	}
	debuglog.Go(s.log, source, func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.reqTO)
		defer cancel()
		for deck, di := range s.known {
			s.disableDeck(ctx, client, s.scene, deck, di)
		}
	})
}

// applyGate hides decks whose current track has never gone on-air (mirrors overlayserver).
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

// ── pure helpers (unit-tested) ──

// TextInputName / ArtInputName derive the OBS input names for a deck letter.
func TextInputName(deck string) string { return inputNamePrefix + deck + " Text" }
func ArtInputName(deck string) string  { return inputNamePrefix + deck + " Art" }

// posTransform builds an obs-websocket SceneItemTransform with just a position.
func posTransform(x, y float64) map[string]any {
	return map[string]any{"positionX": x, "positionY": y}
}

// deckText renders a deck's multi-line OBS text block. Lines: title / artist / "bpm • key •
// elapsed" (empty fields dropped so a line is never just bullets).
func deckText(d session.DeckSnapshot) string {
	var lines []string
	if t := strings.TrimSpace(d.Title); t != "" {
		lines = append(lines, t)
	}
	if a := strings.TrimSpace(d.Artist); a != "" {
		lines = append(lines, a)
	}
	var meta []string
	if d.BPM > 0 {
		meta = append(meta, fmt.Sprintf("%.0f BPM", d.BPM))
	}
	if k := strings.TrimSpace(d.Key); k != "" {
		meta = append(meta, k)
	}
	meta = append(meta, fmtElapsed(d.ElapsedTime))
	lines = append(lines, strings.Join(meta, "  •  "))
	return strings.Join(lines, "\n")
}

// fmtElapsed renders seconds as m:ss.
func fmtElapsed(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	total := int(sec)
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// signature folds the gated decks into a compact change key. Track identity + on-air + slot +
// whole-second elapsed + the rendered text trigger a push; sub-second jitter does not.
func signature(decks []session.DeckSnapshot) string {
	var b strings.Builder
	for i, d := range decks {
		fmt.Fprintf(&b, "%d:%s|%s|%t|%d;", i, d.Deck, d.ArtKey, d.OnAir, int(d.ElapsedTime))
	}
	return b.String()
}
