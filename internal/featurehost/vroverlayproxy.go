package featurehost

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/netstats"
	"rave.page/mate/internal/obscontrol"
	"rave.page/mate/internal/perfmon"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/vrbind"
	"rave.page/mate/internal/vroverlay"
)

// vrPush cadences + mirror sizing.
const (
	vrPushTick     = time.Second     // pusher loop period (stats + change detection)
	vrCamPathTicks = 5               // campath list refresh every N ticks (geom = file IO)
	vrRingCap      = 50              // chat/alert replay ring (re-pushed on child restart)
	vrObsFresh     = 6 * time.Second // obs-status replay freshness (matches Manager's window)
	vrHeartbeat    = 45 * time.Second
	// vr child restart budget: ~10 respawns without a 5-min-stable run = the GPU/driver is broken;
	// stop feeding it fresh OpenVR sessions (each cold start uploads N 640×480 textures - amplifying
	// a TDR loop). The 60s default stableAfter reset let a fault every ~70s respawn at 1s forever.
	vrMaxAttempts = 10
	vrStableAfter = 5 * time.Minute
)

// VROverlayDeps is everything the vr child needs from the daemon: config access, the mesh bus,
// keybind dispatch, camera paths (vrctools), the current-world source, and stats providers.
type VROverlayDeps struct {
	Cfg         func() config.VROverlayFeature
	Mutate      func(func(*config.VROverlayFeature)) // persist an in-VR config edit
	Bus         *eventbus.Bus
	FireBind    func(vrbind.Bind)                  // daemon-side action dispatch (OBS/STT/app groups)
	LoadCamPath func(string) error                 // vrctools OSC load (+ auto-backup)
	CamPaths    func() []vroverlay.CamPathItem     // list, current world first
	CamPathGeom func(string) vroverlay.CamPathGeom // preview geometry (may be nil)
	World       func() (id, name string, ok bool)  // current VRChat world (may be nil)
	StatsPerf   func() []perfmon.Sample            // perfmon ring (may be nil)
	StatsNet    func() netstats.Snapshot           // net sampler (may be nil)
}

// vrSeenEvent is a mirrored bus event + when it arrived (obs-status freshness).
type vrSeenEvent struct {
	ev   vrBusEvent
	seen time.Time
}

// VrOverlayProxy is the daemon-side stand-in for the subprocessed VR overlay stack. It implements
// vroverlay.Surface (UI / keybinds / ctl vrinput / remote-vrinput keep working unchanged), bridges
// the eventbus both ways, and re-pushes FULL desired state (config, world, camera paths, stats,
// recent bus content) whenever the child (re)spawns - so a crash costs one restart, not the set.
type VrOverlayProxy struct {
	host *Host
	log  *logbus.Bus
	deps VROverlayDeps
	send func(event string, data any) error // host.Send; test hook

	mu      sync.Mutex
	avail   bool
	binding vroverlay.BindingStatus
	chat    []vrBusEvent           // twitch chat replay ring
	alerts  []vrBusEvent           // twitch event replay ring
	lastVal map[string]vrBusEvent  // viewers/chatters last value per topic
	obsSt   map[string]vrSeenEvent // obs status per origin node
	cfgSig  string                 // last-pushed config signature (change detect + echo suppression)
	camSig  string
	world   vrWorldEvent
	haveW   bool
	ticks   int

	cancel context.CancelFunc
	unsub  []func()
}

// NewVrOverlayProxy builds the proxy + its supervising host (child spawns on Start).
func NewVrOverlayProxy(log *logbus.Bus, deps VROverlayDeps) (*VrOverlayProxy, error) {
	p := &VrOverlayProxy{log: log, deps: deps, lastVal: map[string]vrBusEvent{}, obsSt: map[string]vrSeenEvent{}}
	h, err := New(Options{
		Name:             "vr",
		Log:              log,
		Init:             func() any { return vrInit{Config: deps.Cfg()} },
		HeartbeatTimeout: vrHeartbeat,   // a wedged cgo/OpenVR call stops beats → force-restart
		MaxAttempts:      vrMaxAttempts, // GPU-fault loop: stop respawning into a broken driver
		StableAfter:      vrStableAfter, // a TDR every ~70s must burn the budget, not reset it
		OnReady:          p.onReady,
		OnDown:           p.onDown,
		OnEvent: map[string]func(json.RawMessage){
			vrEvState:   p.onState,
			vrEvBus:     p.onChildBus,
			vrEvAction:  p.onAction,
			vrEvCamLoad: p.onCamLoad,
			vrEvConfig:  p.onConfigEdit,
		},
	})
	if err != nil {
		return nil, err
	}
	p.host = h
	p.send = h.Send
	return p, nil
}

// Host exposes the supervising host (SetNotifier, Stats).
func (p *VrOverlayProxy) Host() *Host { return p.host }

// GPUReset forwards an OS-logged display-driver reset (TDR) to the child so it rebuilds its OpenVR
// session in place (the gpurecover.OnGPUReset consumer). Best-effort: a down child reinits on
// respawn anyway.
func (p *VrOverlayProxy) GPUReset(detail string) {
	_ = p.send(vrEvGpuReset, vrGpuResetEvent{Detail: detail})
}

// Start begins supervising the child + the state pusher + the bus bridge. Idempotent per module
// start; Stop reverses it.
func (p *VrOverlayProxy) Start(ctx context.Context) error {
	if err := p.host.Start(ctx); err != nil {
		return err
	}
	pctx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	p.subscribeBus()
	debuglog.Go(p.log, "feature:vr", func() { p.pusher(pctx) })
	return nil
}

// Stop tears down the bus bridge, the pusher, and the child.
func (p *VrOverlayProxy) Stop() {
	p.mu.Lock()
	cancel, unsub := p.cancel, p.unsub
	p.cancel, p.unsub = nil, nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, u := range unsub {
		u()
	}
	p.host.Stop()
}

// subscribeBus mirrors + forwards the overlay-content topics (Twitch chat/alerts/viewers/chatters
// + OBS status) into the child, preserving Origin/Local.
func (p *VrOverlayProxy) subscribeBus() {
	if p.deps.Bus == nil {
		return
	}
	sub := func(topic string) func() {
		return p.deps.Bus.Subscribe(topic, func(e eventbus.Event) { p.onBusEvent(topic, e) })
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unsub != nil { // already subscribed (restart without Stop)
		return
	}
	p.unsub = []func(){
		sub(twitch.TopicChat), sub(twitch.TopicEvent),
		sub(twitch.TopicViewers), sub(twitch.TopicChatters),
		sub(obscontrol.TopicStatus),
	}
}

// onBusEvent records a content event in the replay mirror and forwards it live.
func (p *VrOverlayProxy) onBusEvent(topic string, e eventbus.Event) {
	ev := vrBusEvent{Topic: topic, Origin: e.Origin, Local: e.Local, Data: e.Data}
	p.mu.Lock()
	switch topic {
	case twitch.TopicChat:
		p.chat = appendVrRing(p.chat, ev)
	case twitch.TopicEvent:
		p.alerts = appendVrRing(p.alerts, ev)
	case obscontrol.TopicStatus:
		p.obsSt[e.Origin] = vrSeenEvent{ev: ev, seen: time.Now()}
	default:
		p.lastVal[topic] = ev
	}
	p.mu.Unlock()
	if p.host.Running() {
		_ = p.send(vrEvBus, ev)
	}
}

func appendVrRing(r []vrBusEvent, ev vrBusEvent) []vrBusEvent {
	r = append(r, ev)
	if len(r) > vrRingCap {
		r = r[len(r)-vrRingCap:]
	}
	return r
}

// ── child → daemon events ──

func (p *VrOverlayProxy) onState(data json.RawMessage) {
	var st vrStateEvent
	if json.Unmarshal(data, &st) != nil {
		return
	}
	p.mu.Lock()
	p.avail = st.Available
	p.binding = vroverlay.BindingStatus(st.Binding)
	p.mu.Unlock()
}

// onDown clears mirrored VR state (child crashed/stopped - SteamVR link is gone with it).
func (p *VrOverlayProxy) onDown() {
	p.mu.Lock()
	p.avail = false
	p.binding = vroverlay.BindingNotReady
	p.mu.Unlock()
}

// onChildBus republishes a child-produced event (vr perf telemetry, OBS commands) on the mesh bus.
func (p *VrOverlayProxy) onChildBus(data json.RawMessage) {
	var e vrBusEvent
	if json.Unmarshal(data, &e) != nil || e.Topic == "" || p.deps.Bus == nil {
		return
	}
	p.deps.Bus.Publish(e.Topic, e.Data)
}

// onAction fires a daemon-side keybind action requested from VR (OBS control, STT, app groups).
func (p *VrOverlayProxy) onAction(data json.RawMessage) {
	var a vrActionEvent
	if json.Unmarshal(data, &a) != nil || p.deps.FireBind == nil {
		return
	}
	p.deps.FireBind(vrbind.Bind{Action: vrbind.ActionID(a.Action), Target: a.Target})
}

// onCamLoad loads a camera path into VRChat (file IO + OSC - off the reader goroutine).
func (p *VrOverlayProxy) onCamLoad(data json.RawMessage) {
	var c vrCamLoadEvent
	if json.Unmarshal(data, &c) != nil || p.deps.LoadCamPath == nil {
		return
	}
	debuglog.Go(p.log, "feature:vr", func() {
		if err := p.deps.LoadCamPath(c.File); err != nil {
			p.log.Warn("feature:vr", "camera-path load failed", map[string]any{"file": c.File, "error": err.Error()})
		}
	})
}

// onConfigEdit persists an in-VR editor edit (full replacement - declarative, last writer wins).
// Inline on the reader goroutine so successive edits persist in order.
func (p *VrOverlayProxy) onConfigEdit(data json.RawMessage) {
	var c config.VROverlayFeature
	if json.Unmarshal(data, &c) != nil || p.deps.Mutate == nil {
		return
	}
	p.deps.Mutate(func(f *config.VROverlayFeature) { *f = c })
	p.mu.Lock()
	p.cfgSig = string(data) // suppress the echo push (child already has this state)
	p.mu.Unlock()
}

// ── full-state (re)push ──

// onReady re-pushes everything a fresh child needs beyond its init config: current world, camera
// paths, stats, and the recent bus content (chat/alerts/viewers/chatters/obs) so overlays don't
// blank after a crash-restart.
func (p *VrOverlayProxy) onReady() {
	p.mu.Lock()
	p.cfgSig = sigOf(p.deps.Cfg()) // init params carried this config; don't re-push until it changes
	p.camSig, p.haveW, p.ticks = "", false, 0
	chat := append([]vrBusEvent(nil), p.chat...)
	alerts := append([]vrBusEvent(nil), p.alerts...)
	last := make([]vrBusEvent, 0, len(p.lastVal))
	for _, ev := range p.lastVal {
		last = append(last, ev)
	}
	obs := make([]vrBusEvent, 0, len(p.obsSt))
	for _, se := range p.obsSt {
		if time.Since(se.seen) <= vrObsFresh {
			obs = append(obs, se.ev)
		}
	}
	p.mu.Unlock()

	p.pushWorld()
	p.pushCamPaths()
	p.pushStats()
	for _, batch := range [][]vrBusEvent{chat, alerts, last, obs} {
		for _, ev := range batch {
			_ = p.send(vrEvBus, ev)
		}
	}
}

// pusher streams config edits, world changes, campath list changes, and 1 Hz stats while ready.
func (p *VrOverlayProxy) pusher(ctx context.Context) {
	t := time.NewTicker(vrPushTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !p.host.Running() {
			continue
		}
		p.pushConfig()
		p.pushWorld()
		p.mu.Lock()
		p.ticks++
		camDue := p.ticks%vrCamPathTicks == 0
		p.mu.Unlock()
		if camDue {
			p.pushCamPaths()
		}
		p.pushStats()
	}
}

func sigOf(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

// pushConfig sends the full config when it changed daemon-side (UI edits, layout saves).
func (p *VrOverlayProxy) pushConfig() {
	cfg := p.deps.Cfg()
	sig := sigOf(cfg)
	p.mu.Lock()
	changed := sig != "" && sig != p.cfgSig
	if changed {
		p.cfgSig = sig
	}
	p.mu.Unlock()
	if changed {
		_ = p.send(vrEvConfig, cfg)
	}
}

// pushWorld sends the current VRChat world on change (per-world layouts).
func (p *VrOverlayProxy) pushWorld() {
	if p.deps.World == nil {
		return
	}
	id, name, ok := p.deps.World()
	cur := vrWorldEvent{ID: id, Name: name, OK: ok}
	p.mu.Lock()
	changed := !p.haveW || cur != p.world
	p.world, p.haveW = cur, true
	p.mu.Unlock()
	if changed {
		_ = p.send(vrEvWorld, cur)
	}
}

// pushCamPaths sends the full camera-path list + geometry when the list changes (geom = file IO,
// so it's rebuilt only on list-signature change).
func (p *VrOverlayProxy) pushCamPaths() {
	if p.deps.CamPaths == nil {
		return
	}
	list := p.deps.CamPaths()
	sig := ""
	for _, it := range list {
		sig += it.Label + "\x00" + it.File + "\n"
	}
	p.mu.Lock()
	changed := sig != p.camSig
	if changed {
		p.camSig = sig
	}
	p.mu.Unlock()
	if !changed {
		return
	}
	ev := vrCamPathsEvent{Items: make([]vrCamPathItem, 0, len(list))}
	for _, it := range list {
		item := vrCamPathItem{Label: it.Label, File: it.File}
		if p.deps.CamPathGeom != nil {
			g := p.deps.CamPathGeom(it.File)
			item.Pts, item.Spd, item.Dur = g.Pts, g.Spd, g.Dur
		}
		ev.Items = append(ev.Items, item)
	}
	_ = p.send(vrEvCamPaths, ev)
}

// pushStats sends the perf ring + net snapshot, only while a live-stats overlay is enabled.
func (p *VrOverlayProxy) pushStats() {
	if !vrStatsWanted(p.deps.Cfg()) {
		return
	}
	ev := vrStatsEvent{}
	if p.deps.StatsPerf != nil {
		if s := p.deps.StatsPerf(); len(s) > vrStatsSpan {
			ev.Perf = s[len(s)-vrStatsSpan:]
		} else {
			ev.Perf = s
		}
	}
	if p.deps.StatsNet != nil {
		w := netToWire(p.deps.StatsNet())
		ev.Net = &w
	}
	_ = p.send(vrEvStats, ev)
}

// vrStatsSpan caps the pushed perf ring to what the stats graphs draw.
const vrStatsSpan = 120

// vrStatsWanted reports whether any enabled overlay renders live stats.
func vrStatsWanted(cfg config.VROverlayFeature) bool {
	for _, o := range cfg.Overlays {
		if o.Enabled && vroverlay.IsStatsKind(o.Type) {
			return true
		}
	}
	return false
}

// ── vroverlay.Surface (daemon-facing control plane) ──

var _ vroverlay.Surface = (*VrOverlayProxy)(nil)

// Available reports whether the child is up AND connected to SteamVR (mirrored state).
func (p *VrOverlayProxy) Available() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.avail
}

// BindingStatus returns the child's mirrored SteamVR binding health.
func (p *VrOverlayProxy) BindingStatus() vroverlay.BindingStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.binding
}

// call runs one child request with a bounded wait.
func (p *VrOverlayProxy) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.host.Call(ctx, method, params)
}

// fire sends a no-response control request off-thread (keybind handlers must not block on the pipe).
func (p *VrOverlayProxy) fire(method string, params any) {
	debuglog.Go(p.log, "feature:vr", func() { _, _ = p.call(method, params, 2*time.Second) })
}

// OpenBindingUI opens SteamVR's controller-binding screen (in the child).
func (p *VrOverlayProxy) OpenBindingUI() error {
	_, err := p.call(vrMethOpenBindingUI, nil, 3*time.Second)
	return err
}

// ActionBinding returns the physical inputs bound to an action path ("" when unbound/down).
func (p *VrOverlayProxy) ActionBinding(action string) string {
	raw, err := p.call(vrMethActionBinding, vrActionParam{Action: action}, time.Second)
	if err != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// InputDiag returns the child's SteamVR Input diagnostic (ctl vrinput / remote-vrinput).
func (p *VrOverlayProxy) InputDiag() string {
	raw, err := p.call(vrMethInputDiag, nil, 5*time.Second)
	if err != nil {
		return "VR overlay subprocess not running (" + err.Error() + ")"
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// ToggleAllOverlays flips global show/hide of all content overlays.
func (p *VrOverlayProxy) ToggleAllOverlays() { p.fire(vrMethToggleAll, nil) }

// ToggleHidden flips one overlay's user-hidden state.
func (p *VrOverlayProxy) ToggleHidden(id string) { p.fire(vrMethToggleHidden, vrIDParam{ID: id}) }

// SetHidden sets one overlay's user-hidden state.
func (p *VrOverlayProxy) SetHidden(id string, hidden bool) {
	p.fire(vrMethSetHidden, vrIDParam{ID: id, Hidden: hidden})
}

// RequestEditorToggle opens/closes the in-world menu.
func (p *VrOverlayProxy) RequestEditorToggle() { p.fire(vrMethEditorToggle, nil) }

// PerfProbe returns the child's VR perf section for `ctl perf`.
func (p *VrOverlayProxy) PerfProbe() string {
	raw, err := p.call(vrMethPerfProbe, nil, time.Second)
	if err != nil {
		return "vr subprocess: " + err.Error()
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
