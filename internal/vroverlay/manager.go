package vroverlay

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/netstats"
	"rave.page/mate/internal/obscontrol"
	"rave.page/mate/internal/perfmon"
	"rave.page/mate/internal/procstat"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/version"
	"rave.page/mate/internal/vrbind"
	"rave.page/mate/internal/vrstats"
)

const (
	logTag   = "vroverlay"
	tickRate = 100 * time.Millisecond // ~10 fps overlay refresh
	ringCap  = 100
	panelW   = 640
	panelH   = 480
)

// Manager renders VR overlays from bus events. It subscribes to twitch.chat/event (local OR a
// peer's), keeps message rings, and on a tick renders each enabled overlay → the VR runtime.
type Manager struct {
	log    *logbus.Bus
	bus    Bus
	cfg    func() config.VROverlayFeature
	mutate func(func(*config.VROverlayFeature)) // apply+persist a config edit (in-VR editor)
	rt     Runtime
	beat   func() // liveness ping to the featurehost host (subprocess mode); may be nil

	mu             sync.Mutex
	chat           []twitch.Event
	alerts         []twitch.Event
	viewers        twitch.ViewerInfo           // latest polled viewer count/state
	chatters       twitch.ChatterInfo          // latest polled chatter list
	obsInst        map[string]obsInstanceEntry // node id → latest OBS status (for obs overlays + control)
	hidden         map[string]bool             // overlay id → user-toggled hidden
	contentHidden  bool                        // global show/hide all content overlays (wrist short-click)
	pendEditToggle bool                        // editor.toggle bind requested off-thread → consumed on the VR goroutine
	rend           *Renderer
	created        map[string]bool          // overlay key → EnsureOverlay done
	sig            map[string]string        // overlay key → last-rendered content signature (skip re-upload)
	lastTf         map[string]Transform     // overlay key → last-applied transform (skip redundant SetTransform)
	shown          map[string]bool          // overlay key → last-applied visibility (skip redundant Show)
	texUploads     int                      // overlay texture uploads since the last perf sample
	health         vrHealth                 // connected-session health (reconnect when the runtime dies w/o a Quit event)
	edit           *editor                  // in-VR editor (nil unless the runtime implements Editor)
	motion         *motion                  // VR motion record/playback → VRChat OSC (nil unless Editor)
	procSampler    procstat.Sampler         // process self-footprint sampler (CPU%/RSS, delta-based)
	bindDisp       *vrbind.Dispatcher       // keybind dispatcher (VR action slots → app actions); may be nil
	bindsFn        func() []vrbind.Bind     // current user binds; may be nil
	camPaths       func() []CamPathItem     // in-VR camera-path picker source (current world first); may be nil
	loadCamPath    func(string) error       // load a camera path into VRChat over OSC; may be nil
	camPathGeom    func(string) CamPathGeom // path geometry for the in-world 3D preview; may be nil

	// Per-world layouts (worldlayout.go). VR-goroutine-only state (tick + menu build share it there).
	worldSrc    func() (id, name string, ok bool) // current VRChat world (vrctools timeline); may be nil
	lastWorldID string                            // last observed world (change detect; survives VR reconnects)
	suggest     *worldSuggest                     // pending notify-mode layout suggestion
	toastMsg    string                            // head-locked toast text ("" = none)
	toastUntil  time.Time
	toastEnsure bool
	toastShow   cachedBool
	toastTf     cachedTf
	toastSig    string

	// Live-stats overlay data sources (perf/network/timing overlay kinds). Both may be nil (subprocess
	// host / no wiring) → the overlay renders a "waiting for data" placeholder. statsNext throttles the
	// per-overlay view rebuild + texture re-render to ~2 Hz (SetTexture is expensive).
	statsPerf func() []perfmon.Sample  // app+system CPU/RAM 1 Hz ring (perfmon.Monitor.Snapshot)
	statsNet  func() netstats.Snapshot // peer/API byte-rate + per-peer RTT series (netstats.Sampler.Snapshot)
	statsNext map[string]time.Time     // overlay key → earliest next stats view rebuild (2 Hz cap)

	// Perf instrumentation (perf.go): own locking/atomics, not guarded by m.mu.
	renderStat loopStat       // 100ms render tick duration
	inputStat  loopStat       // ~90Hz handleActions/pointer loop duration
	castStat   loopStat       // pointerCastHand duration (touch pre-pass + ray)
	perfC      vrPerfCounters // pointer-cast + texture-upload counters
}

// CamPathItem is one selectable camera path for the in-VR menu.
type CamPathItem struct{ Label, File string }

// CamPathGeom is a camera path's geometry for the in-VR 3D preview: world positions + per-point speed
// (for colour) + per-point duration (seconds to the next keyframe, for real-speed playback). Parallel.
type CamPathGeom struct {
	Pts [][3]float32
	Spd []float32
	Dur []float32
}

// obsInstanceEntry is a known instance's OBS status + when it was last heard from.
type obsInstanceEntry struct {
	inst obscontrol.Instance
	seen time.Time
}

// New builds the manager over a runtime (NewRuntime()) + the bus. bus may be nil (then no content).
// mutate applies+persists a config edit from the in-VR editor (nil = read-only).
func New(log *logbus.Bus, bus Bus, rt Runtime, cfg func() config.VROverlayFeature, mutate func(func(*config.VROverlayFeature))) *Manager {
	return &Manager{
		log: log, bus: bus, cfg: cfg, mutate: mutate, rt: rt,
		hidden:     map[string]bool{},
		created:    map[string]bool{},
		sig:        map[string]string{},
		lastTf:     map[string]Transform{},
		shown:      map[string]bool{},
		statsNext:  map[string]time.Time{},
		obsInst:    map[string]obsInstanceEntry{},
		renderStat: loopStat{budgetMs: 100},
		inputStat:  loopStat{budgetMs: 11},
	}
}

// Available reports whether the VR runtime is live (true only after Start connects to SteamVR on a
// `vr` build).
func (m *Manager) Available() bool { return m.rt.Available() }

// ToggleHidden flips an overlay's user-hidden state (wire to a hotkey / MIDI / UI button).
func (m *Manager) ToggleHidden(id string) {
	m.mu.Lock()
	m.hidden[id] = !m.hidden[id]
	m.mu.Unlock()
}

// SetHidden sets an overlay's user-hidden state explicitly (for show/hide binds).
func (m *Manager) SetHidden(id string, hidden bool) {
	m.mu.Lock()
	m.hidden[id] = hidden
	m.mu.Unlock()
}

// ToggleAllOverlays flips global show/hide of all content overlays (the "overlays.toggle" bind action -
// same effect as a summon tap). Safe from any goroutine (contentHidden guarded by m.mu).
func (m *Manager) ToggleAllOverlays() {
	m.mu.Lock()
	m.contentHidden = !m.contentHidden
	m.mu.Unlock()
}

// RequestEditorToggle asks the editor to open/close the in-world menu (the "editor.toggle" bind action).
// The editor's e.on lives on the single VR goroutine, so we set a pending flag it consumes there rather
// than mutating it off-thread (a MIDI/bus dispatch runs on another goroutine).
func (m *Manager) RequestEditorToggle() {
	m.mu.Lock()
	m.pendEditToggle = true
	m.mu.Unlock()
}

// takeEditToggle reads + clears a pending editor-toggle request (called on the VR goroutine).
func (m *Manager) takeEditToggle() bool {
	m.mu.Lock()
	p := m.pendEditToggle
	m.pendEditToggle = false
	m.mu.Unlock()
	return p
}

// SetBindDispatcher wires the keybind dispatcher + a current-binds accessor so VR action slots
// (read in the editor tick) fire app actions.
func (m *Manager) SetBindDispatcher(d *vrbind.Dispatcher, binds func() []vrbind.Bind) {
	m.bindDisp, m.bindsFn = d, binds
}

// SetCamPathProvider wires the in-VR camera-path picker: list returns paths (current world first),
// load loads one into VRChat over OSC, geom returns a path's world geometry for the 3D in-world
// preview (geom may be nil to disable the preview).
func (m *Manager) SetCamPathProvider(list func() []CamPathItem, load func(string) error, geom func(string) CamPathGeom) {
	m.camPaths, m.loadCamPath, m.camPathGeom = list, load, geom
}

// SetBeat wires a liveness ping (featurehost Runtime.Beat) fired from the VR work loop, so a wedged
// cgo/OpenVR call is detected by the host's heartbeat monitor and the child gets restarted.
func (m *Manager) SetBeat(fn func()) { m.beat = fn }

// doBeat pings the host if wired (coalesced upstream - per-tick calls are free).
func (m *Manager) doBeat() {
	if m.beat != nil {
		m.beat()
	}
}

// SetStatsProviders wires the live-stats overlay data sources: perf = app+system CPU/RAM 1 Hz ring
// (perfmon.Monitor.Snapshot), net = peer/API byte-rate + per-peer RTT series (netstats.Sampler.Snapshot).
// Either may be nil (then those overlay kinds show a "waiting for data" placeholder). VR frame timing
// comes from the runtime directly (PerfStats), so it needs no wiring here.
func (m *Manager) SetStatsProviders(perf func() []perfmon.Sample, net func() netstats.Snapshot) {
	m.statsPerf, m.statsNet = perf, net
}

// fireSlots dispatches each pressed slot (edges bitmask, bit i = slot i+1) to its bound action.
func (m *Manager) fireSlots(edges uint32) {
	if edges == 0 || m.bindDisp == nil || m.bindsFn == nil {
		return
	}
	binds := m.bindsFn()
	for i := 0; i < len(vrbind.VRActionSlots()); i++ {
		if edges&(1<<uint(i)) != 0 {
			m.bindDisp.FireVR(binds, vrbind.VRActionSlots()[i])
		}
	}
}

// InputDiag returns a human-readable dump of the SteamVR Input action state (manifest loaded?
// per-action live state + what each action is bound to) for debugging why a binding does nothing.
func (m *Manager) InputDiag() string {
	ed, ok := m.rt.(Editor)
	if !ok {
		return "VR overlay runtime unavailable (non-vr build or SteamVR not connected)"
	}
	diag := fmt.Sprintf("build %s (#%d) %s\n", version.Version, version.BuildNum(), version.Commit) + ed.InputDiag()
	feat := m.cfg()
	diag += fmt.Sprintf("config: summonOn=%v summonButton=%q tapHides=%v stickMoveOnly=%v editHand=%s\n",
		feat.SummonOn, feat.ResolvedSummonButton(), feat.SummonTapHides, feat.StickMoveOnly, feat.ResolvedEditHand())
	if feat.SummonOn && ed.ActionBinding(actSummon) == "" {
		diag += "WARN: summon enabled but bound to NOTHING - menu keybind dead. Set the button in settings (A/X | B/Y) or bind 'summon' in SteamVR.\n"
	}
	if m.edit != nil { // append the live pointer/editor state + interaction ring so a remote can SEE it
		diag += "\n\npointer/editor state:\n" + m.edit.debugState()
		if evts := m.edit.recentEvents(); evts != "" {
			diag += "\nrecent interaction events (newest last):\n" + evts
		}
	}
	return diag
}

// BindingStatus reports whether SteamVR has usable controller bindings for rave-mate (live). Returns
// BindingNotReady on a non-vr build or before SteamVR/input is up; BindingUnbound when a stale custom
// binding leaves every action unbound (so summon/pointer/grab silently do nothing) - the UI surfaces it.
func (m *Manager) BindingStatus() BindingStatus {
	ed, ok := m.rt.(Editor)
	if !ok {
		return BindingNotReady
	}
	return ed.BindingStatus()
}

// OpenBindingUI opens SteamVR's controller-binding screen (if the runtime is an Editor).
func (m *Manager) OpenBindingUI() error {
	ed, ok := m.rt.(Editor)
	if !ok {
		return fmt.Errorf("VR not available")
	}
	return ed.OpenBindingUI()
}

// ActionBinding returns the human-readable physical inputs SteamVR binds to an action path (e.g.
// ActionToggleEditor / ActionToggleOverlays); "" when unbound or VR unavailable (non-vr build /
// SteamVR down). The UI polls this to show each action's live bind state.
func (m *Manager) ActionBinding(action string) string {
	ed, ok := m.rt.(Editor)
	if !ok {
		return ""
	}
	return ed.ActionBinding(action)
}

// reconnectWait is how long the supervise loop waits between SteamVR connect attempts.
const reconnectWait = 5 * time.Second

// Start is a supervise loop: wait for SteamVR → run while connected → on SteamVR quit (or error)
// tear down cleanly and wait again. So enabling the module before SteamVR is up, SteamVR closing,
// and SteamVR restarting are all handled without crashing or giving up. ctx cancel exits.
func (m *Manager) Start(ctx context.Context) error {
	rend, err := NewRenderer(1)
	if err != nil {
		return err
	}
	m.rend = rend
	defer rend.Close()
	if ed, ok := m.rt.(Editor); ok && m.mutate != nil {
		m.edit = &editor{m: m, ed: ed, menuSig: map[string]string{},
			menuInter: map[string]bool{}, menuMh: map[string]int{}, menuItems: map[string][]MenuItem{}, menuBuiltAt: map[string]time.Time{},
			menuShown: map[string]menuSnap{}, menuTexWH: map[string][2]int{}, contentInter: map[string]bool{},
			menuRowsHi: map[string]int{}}
		m.motion = newMotion(m.log, ed.TrackerPoses,
			func() string { return m.cfg().ResolvedOSCAddr() },
			func() string { return m.cfg().ResolvedVMCAddr() },
			func() bool { return m.cfg().VMCLive })
	}
	logged := false
	for ctx.Err() == nil {
		m.doBeat()
		// Never start SteamVR ourselves - only attach if the user already has it running. Otherwise
		// VR_Init(Overlay) would relaunch SteamVR the moment the user closes it.
		if !steamvrRunning() {
			if !logged {
				m.log.Info(logTag, "VR overlays idle - SteamVR not running (won't launch it; start SteamVR to connect)", nil)
				logged = true
			}
			if !sleepCtx(ctx, reconnectWait) {
				break
			}
			continue
		}
		_ = m.rt.Init()
		if !m.rt.Available() {
			if !logged {
				m.log.Info(logTag, "VR overlays waiting for SteamVR (start it, or it's a non-vr build)", nil)
				logged = true
			}
			if !sleepCtx(ctx, reconnectWait) {
				break
			}
			continue
		}
		logged = false
		m.log.Info(logTag, "VR overlays connected", nil)
		m.runConnected(ctx)
		m.rt.Shutdown()
		m.resetSession()
		m.log.Info(logTag, "VR overlays disconnected (SteamVR closed) - waiting", nil)
	}
	m.rt.Shutdown()
	return nil
}

// runConnected ticks the overlays until ctx is cancelled or SteamVR signals quit.
func (m *Manager) runConnected(ctx context.Context) {
	m.maybeRegisterApp()
	m.initInput()
	var unsub []func()
	if m.bus != nil {
		unsub = append(unsub,
			m.bus.Subscribe(twitch.TopicChat, func(e eventbus.Event) { m.onEvent(e, true) }),
			m.bus.Subscribe(twitch.TopicEvent, func(e eventbus.Event) { m.onEvent(e, false) }),
			m.bus.Subscribe(twitch.TopicViewers, m.onViewers),
			m.bus.Subscribe(twitch.TopicChatters, m.onChatters),
			m.bus.Subscribe(obscontrol.TopicStatus, m.onObsStatus),
		)
	}
	defer func() {
		for _, u := range unsub {
			u()
		}
	}()
	t := time.NewTicker(tickRate)
	defer t.Stop()
	perfT := time.NewTicker(time.Second) // VR perf telemetry sample/publish cadence
	defer perfT.Stop()
	motionT := time.NewTicker(33 * time.Millisecond) // ~30 Hz motion capture / OSC playback
	defer motionT.Stop()
	inputT := time.NewTicker(11 * time.Millisecond) // ~90 Hz SteamVR Input pump - catch click/double-click
	defer inputT.Stop()                             // edges (a 10fps overlay tick misses sub-100ms pulses)
	if m.motion != nil {
		defer m.motion.close()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-perfT.C:
			m.publishPerf()
		case <-inputT.C:
			if m.edit != nil { // same goroutine as tick() → single-threaded OpenVR access
				t0 := time.Now()
				feat := m.cfg()
				m.edit.handleActions(feat, HandFromString(feat.ResolvedEditHand()))
				m.inputStat.observe(time.Since(t0))
				// Interaction edge → reconcile the editor NOW (~11ms) instead of waiting for
				// the 100ms overlay tick: menu opens/navs/clicks paint within one input frame.
				// Everything inside tick is signature/changed-gated, so a dirty pass costs
				// only what actually changed. Content overlays stay on the slow tick.
				if m.edit.consumeDirty() {
					r0 := time.Now()
					m.edit.tick(feat)
					m.renderStat.observe(time.Since(r0))
				}
			}
		case <-motionT.C:
			if m.motion != nil {
				m.motion.tick() // same goroutine as tick() → single-threaded OpenVR access
			}
		case <-t.C:
			m.doBeat()
			if q := m.rt.PollQuit(); q != QuitNone { // session-fatal event - stop calling into it; supervise loop reconnects
				m.log.Warn(logTag, "VR session ending - reconnecting", map[string]any{"reason": q.String()})
				return
			}
			t0 := time.Now()
			m.tick()
			m.renderStat.observe(time.Since(t0))
			if m.health.dead() { // runtime died without a Quit event (every call failing) → reconnect
				m.log.Warn(logTag, "VR overlay session unresponsive - every runtime call failing; reconnecting", map[string]any{"failedTicks": m.health.consecFail})
				return
			}
		}
	}
}

// publishPerf samples VR perf telemetry + the overlay-app state and broadcasts it on the bus so a
// monitoring instance (or `rave-mate ctl vrperf`) can see frame timing / drops remotely.
func (m *Manager) publishPerf() {
	ps, ok := m.rt.PerfStats()
	if !ok || m.bus == nil {
		return
	}
	feat := m.cfg()
	for _, o := range feat.Overlays {
		if o.Enabled {
			ps.Overlays++
		}
	}
	if m.edit != nil {
		ps.EditorOpen = m.edit.on
	}
	m.mu.Lock()
	ps.TexUploads = m.texUploads
	m.texUploads = 0
	pst := m.procSampler.Sample()
	m.mu.Unlock()
	m.perfC.lastTexRate.Store(int64(ps.TexUploads))
	ps.ProcCPUPct = pst.CPUPercent
	ps.ProcRSSMB = pst.RSSMB
	ps.ProcHeapMB = pst.HeapMB
	ps.ProcGoros = pst.Goroutines
	ps.ProcNumGC = pst.NumGC
	if raw, err := json.Marshal(ps); err == nil {
		m.bus.Publish(vrstats.TopicPerf, raw)
	}
}

// resetSession clears per-connection overlay state so a reconnect recreates everything cleanly.
func (m *Manager) resetSession() {
	m.created = map[string]bool{}
	m.sig = map[string]string{}
	m.lastTf = map[string]Transform{}
	m.shown = map[string]bool{}
	m.statsNext = map[string]time.Time{}
	m.health.reset()
	m.toastEnsure, m.toastShow, m.toastTf, m.toastSig = false, cachedBool{}, cachedTf{}, "" // recreate on reconnect
	if m.edit != nil {
		m.edit.resetSession()
	}
}

// maybeRegisterApp registers/unregisters the SteamVR auto-launch manifest per config.AutoStart.
func (m *Manager) maybeRegisterApp() {
	want := m.cfg().AutoStart
	path, err := writeManifest()
	if err != nil {
		if want {
			m.log.Warn(logTag, "could not write vrmanifest", map[string]any{"error": err.Error()})
		}
		return
	}
	if err := m.rt.RegisterApp(path, vrAppKey, want); err != nil {
		m.log.Warn(logTag, "SteamVR app registration failed", map[string]any{"error": err.Error()})
		return
	}
	m.log.Info(logTag, "SteamVR overlay app registered", map[string]any{"autoStart": want})
}

// initInput writes + loads the SteamVR Input action manifest (the OVRAS approach - IVRInput). Always
// loaded so the controls work out of the box AND rave-mate appears in SteamVR's controller-binding UI
// for rebinding. IVRInput fans inputs out to every app, so this does NOT take controllers from the
// game; the safe defaults (grip=grab, stick=push/pull) only act while the editor is open. The SUMMON
// action (hold A/X to open, tap to show/hide) is bound to the configured face button; a long hold
// avoids conflicting with quick in-game presses. Best-effort: dashboard tab still works if input fails.
func (m *Manager) initInput() {
	ed, ok := m.rt.(Editor)
	if !ok {
		return
	}
	path, err := writeInputManifest(m.cfg().ResolvedSummonButton())
	if err != nil {
		m.log.Warn(logTag, "could not write VR input manifest", map[string]any{"error": err.Error()})
		return
	}
	if ed.InputInit(path) {
		m.log.Info(logTag, "SteamVR Input actions loaded (rebind in SteamVR bindings, or the in-app button)", nil)
		// Binding health is evaluated on the LIVE tick (the settings UI re-polls BindingStatus every 2s).
		// Do NOT check it here: SteamVR loads the active binding ASYNCHRONOUSLY after SetActionManifestPath,
		// so a read one pump later false-reports "unbound" even for a fully-bound custom profile - that was
		// the bogus "no controller bindings" warning. The live-tick BindingStatus (bActive) is authoritative.
	} else {
		m.log.Info(logTag, "SteamVR Input actions unavailable - using wrist/menu controls", nil)
	}
}

// sleepCtx waits d or until ctx is cancelled; returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (m *Manager) onEvent(e eventbus.Event, chat bool) {
	var ev twitch.Event
	if json.Unmarshal(e.Data, &ev) != nil {
		return
	}
	m.mu.Lock()
	if chat {
		m.chat = appendRing(m.chat, ev)
	} else {
		m.alerts = appendRing(m.alerts, ev)
	}
	m.mu.Unlock()
}

func (m *Manager) onViewers(e eventbus.Event) {
	var vi twitch.ViewerInfo
	if json.Unmarshal(e.Data, &vi) != nil {
		return
	}
	m.mu.Lock()
	m.viewers = vi
	m.mu.Unlock()
}

func (m *Manager) onChatters(e eventbus.Event) {
	var ci twitch.ChatterInfo
	if json.Unmarshal(e.Data, &ci) != nil {
		return
	}
	m.mu.Lock()
	m.chatters = ci
	m.mu.Unlock()
}

func (m *Manager) onObsStatus(e eventbus.Event) {
	var st obscontrol.Status
	if json.Unmarshal(e.Data, &st) != nil || st.ID == "" {
		return
	}
	m.mu.Lock()
	m.obsInst[st.ID] = obsInstanceEntry{
		inst: obscontrol.Instance{Node: e.Origin, Local: e.Local, Status: st},
		seen: time.Now(),
	}
	m.mu.Unlock()
}

// obsInstances returns the known OBS instances (fresh within 6s), local first then by label. Caller
// must NOT hold m.mu.
func (m *Manager) obsInstances() []obscontrol.Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.obsInstancesLocked()
}

// obsInstancesLocked is obsInstances with the lock already held (used from tick/linesFor).
func (m *Manager) obsInstancesLocked() []obscontrol.Instance {
	now := time.Now()
	var out []obscontrol.Instance
	for node, e := range m.obsInst {
		if now.Sub(e.seen) > 6*time.Second {
			delete(m.obsInst, node)
			continue
		}
		out = append(out, e.inst)
	}
	sortInstances(out)
	return out
}

// SendObsCmd publishes an OBS control command on the bus (the targeted instance executes it).
func (m *Manager) SendObsCmd(cmd obscontrol.Cmd) {
	if m.bus == nil {
		return
	}
	if raw, err := json.Marshal(cmd); err == nil {
		m.bus.Publish(obscontrol.TopicCmd, raw)
	}
}

func sortInstances(s []obscontrol.Instance) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0; j-- {
			a, b := s[j-1], s[j]
			less := a.Local != b.Local && a.Local || (a.Local == b.Local && a.Label < b.Label)
			if less {
				break
			}
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// tick reconciles the configured overlays with the runtime. Transform / visibility / texture are
// only pushed to OpenVR when they actually change (per-key caches + content signatures) - re-sending
// every tick adds needless compositor + GPU load and tanks the host VR app's framerate.
func (m *Manager) tick() {
	feat := m.cfg()

	// Health probe: count OpenVR reconcile calls + failures this tick so runConnected can detect a
	// runtime that died without a Quit event (every call failing) and force a clean reconnect.
	attempts, fails := 0, 0
	rtErr := func(err error) {
		attempts++
		if err != nil {
			fails++
		}
	}

	wantKeys := map[string]bool{}
	for _, o := range feat.Overlays {
		if !o.Enabled {
			continue
		}
		key := "page.rave.mate." + o.ID
		wantKeys[key] = true
		if !m.created[key] {
			err := m.rt.EnsureOverlay(key, "rave-mate "+o.Type)
			rtErr(err)
			if err != nil {
				m.log.Warn(logTag, "ensure overlay failed", map[string]any{"id": o.ID, "error": err.Error()})
				continue
			}
			m.created[key] = true
		}
		stats := isStatsType(o.Type)
		m.mu.Lock()
		hidden := m.hidden[o.ID]
		var lines []Line
		if !stats {
			lines = m.linesFor(o)
		}
		m.mu.Unlock()

		// Transform: skip while the editor drags it; otherwise only when changed.
		if m.edit == nil || !m.edit.isGrabbing(key) {
			tf := transformOf(o)
			if last, ok := m.lastTf[key]; !ok || last != tf {
				rtErr(m.rt.SetTransform(key, tf))
				m.lastTf[key] = tf
			}
		}
		// Visibility: locked (AlwaysShow) overlays ignore the global hide; empty content overlays with
		// the placeholder disabled stay hidden. Stats overlays always have content. Only Show on change.
		vis := !hidden && (!m.contentHidden || o.AlwaysShow) && (stats || len(lines) > 0)
		if last, ok := m.shown[key]; !ok || last != vis {
			rtErr(m.rt.Show(key, vis))
			m.shown[key] = vis
		}
		// Texture: only when the rendered content (or bg opacity) changes + the overlay is visible.
		// In edit mode every content overlay gets a brand outline so the user sees edit mode is live;
		// the flag is in the signature so the texture re-uploads on the edit-mode transition.
		if vis {
			if stats {
				m.renderStatsTexture(o, key, rtErr)
			} else {
				bg := o.ResolvedBgOpacity()
				outline := m.edit != nil && m.edit.editMode
				grabbed := m.edit != nil && m.edit.isGrabbing(key)
				s := linesSig(lines) + fmt.Sprintf("|bg%.2f|edit%v|grab%v", bg, outline, grabbed)
				if m.sig[key] != s {
					img := m.rend.Panel(lines, panelW, panelH, bg)
					m.editBorder(img, key)
					err := m.rt.SetTexture(key, img)
					rtErr(err)
					if err == nil {
						m.sig[key] = s
						m.mu.Lock()
						m.texUploads++
						m.mu.Unlock()
						m.perfC.texTotal.Add(1)
					}
				}
			}
		}
	}
	// Destroy overlays no longer wanted.
	for key := range m.created {
		if !wantKeys[key] {
			_ = m.rt.DestroyOverlay(key)
			delete(m.created, key)
			delete(m.sig, key)
			delete(m.lastTf, key)
			delete(m.shown, key)
			delete(m.statsNext, key)
		}
	}
	m.tickWorldLayout(feat) // per-world layout auto-apply/notify (worldlayout.go)
	m.driveToast()
	if m.edit != nil {
		m.edit.tick(feat)
	}
	m.health.observe(attempts, fails)
}

// linesSig is a cheap content signature for a rendered panel (name|text|rgba per line). Equal
// signatures → identical pixels → skip the GPU texture upload.
func linesSig(lines []Line) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Name)
		b.WriteByte('|')
		b.WriteString(l.Text)
		b.WriteByte('|')
		if l.Color != nil {
			r, g, bl, a := l.Color.RGBA()
			fmt.Fprintf(&b, "%d,%d,%d,%d", r, g, bl, a)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// linesFor builds the render lines for an overlay from its type + the rings (caller holds the lock).
func (m *Manager) linesFor(o config.VROverlay) []Line {
	n := o.ResolvedMaxMessages()
	switch o.Type {
	case "obs":
		return m.obsLines()
	case "viewers":
		return m.viewerLines()
	case "viewerlist":
		return m.viewerListLines(n)
	case "alerts":
		if src := lastN(m.alerts, n); len(src) > 0 {
			return eventsToLines(src)
		}
		if o.HidePlaceholder {
			return nil
		}
		return samplePreview("alerts", n)
	default: // chat
		if src := lastN(m.chat, n); len(src) > 0 {
			return eventsToLines(src)
		}
		if o.HidePlaceholder {
			return nil
		}
		return samplePreview("chat", n)
	}
}

func eventsToLines(src []twitch.Event) []Line {
	out := make([]Line, 0, len(src))
	for _, ev := range src {
		out = append(out, lineOf(ev))
	}
	return out
}

var colMuted = color.NRGBA{R: 150, G: 150, B: 160, A: 255}

// obsLines renders the OBS cockpit: per connected instance, stream/record state + bitrate/health.
func (m *Manager) obsLines() []Line {
	insts := m.obsInstancesLocked()
	if len(insts) == 0 {
		return []Line{
			{Text: "OBS - no instance connected", Color: colMuted},
			{Text: "live: STREAM 6200 kbps 12:34", Color: colMint},
			{Text: "live: REC 05:12", Color: colHot},
		}
	}
	var out []Line
	for _, in := range insts {
		label := in.Label
		if label == "" {
			label = in.Node
		}
		if in.Local {
			label += " (this PC)"
		}
		out = append(out, Line{Text: label, Color: colName})
		if !in.Connected {
			out = append(out, Line{Text: "  OBS offline", Color: colMuted})
			continue
		}
		if in.Streaming {
			s := fmt.Sprintf("  STREAM %s  %d kbps", clock(in.StreamSec), in.BitrateKbps)
			if in.Reconnecting {
				s += " (reconnecting)"
			}
			out = append(out, Line{Text: s, Color: colMint})
			out = append(out, Line{Text: fmt.Sprintf("  net %.0f%%  drop %.1f%%", in.Congestion*100, in.DropPct()), Color: healthColor(in.Congestion)})
		} else {
			out = append(out, Line{Text: "  stream offline", Color: colMuted})
		}
		if in.Recording {
			out = append(out, Line{Text: fmt.Sprintf("  REC %s", clock(in.RecSec)), Color: colHot})
		} else {
			out = append(out, Line{Text: "  not recording", Color: colMuted})
		}
	}
	return out
}

// viewerLines renders the Twitch viewer count + live state.
func (m *Manager) viewerLines() []Line {
	v := m.viewers
	if !v.Live && v.ViewerCount == 0 {
		return []Line{
			{Text: "OFFLINE", Color: colMuted},
			{Text: "0 viewers", Color: colName},
		}
	}
	state := "OFFLINE"
	col := colMuted
	if v.Live {
		state = "LIVE"
		col = colMint
	}
	out := []Line{
		{Text: fmt.Sprintf("%s viewers", commaInt(v.ViewerCount)), Color: colName},
		{Text: state, Color: col},
	}
	if v.GameName != "" {
		out = append(out, Line{Text: v.GameName, Color: colMuted})
	}
	return out
}

// viewerListLines renders the current chatter list (capped at n names).
func (m *Manager) viewerListLines(n int) []Line {
	c := m.chatters
	if c.Total == 0 && len(c.Names) == 0 {
		return []Line{
			{Text: "VIEWERS (0)", Color: colName},
			{Text: "raver_99", Color: colText},
			{Text: "bassqueen", Color: colText},
			{Text: "neonkid", Color: colText},
		}
	}
	out := []Line{{Text: fmt.Sprintf("VIEWERS (%d)", c.Total), Color: colName}}
	names := c.Names
	if n > 0 && len(names) > n {
		names = names[:n]
	}
	for _, nm := range names {
		out = append(out, Line{Text: nm, Color: colText})
	}
	if c.Total > len(names) {
		out = append(out, Line{Text: fmt.Sprintf("+%d more", c.Total-len(names)), Color: colMuted})
	}
	return out
}

// clock formats seconds as H:MM:SS or M:SS.
func clock(sec float64) string {
	s := int(sec)
	h, m, ss := s/3600, (s%3600)/60, s%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, ss)
	}
	return fmt.Sprintf("%d:%02d", m, ss)
}

// healthColor greens a healthy stream, ambers light congestion, reds heavy.
func healthColor(congestion float64) color.Color {
	switch {
	case congestion >= 0.5:
		return colHot
	case congestion >= 0.2:
		return color.NRGBA{R: 0xFF, G: 0xB5, B: 0x47, A: 255}
	default:
		return colMint
	}
}

// commaInt formats n with thousands separators.
func commaInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b []byte
	pre := len(s) % 3
	if pre > 0 {
		b = append(b, s[:pre]...)
	}
	for i := pre; i < len(s); i += 3 {
		if len(b) > 0 {
			b = append(b, ',')
		}
		b = append(b, s[i:i+3]...)
	}
	return string(b)
}

var (
	colMint = color.NRGBA{R: 8, G: 247, B: 155, A: 255}
	colHot  = color.NRGBA{R: 255, G: 62, B: 138, A: 255}
)

func lineOf(ev twitch.Event) Line {
	// ASCII markers (Orbitron has no emoji glyphs).
	switch ev.Kind {
	case twitch.KindChat:
		return Line{Name: nameOf(ev), Text: ev.Text, Color: chatColor(ev.Color)}
	case twitch.KindFollow:
		return Line{Text: "+ " + nameOf(ev) + " followed", Color: colName}
	case twitch.KindCheer:
		return Line{Text: "* " + nameOf(ev) + " cheered", Color: colMint}
	default:
		return Line{Text: "* " + nameOf(ev) + " subscribed", Color: colHot}
	}
}

// samplePreview returns placeholder lines (fake chatters + Twitch emote names) so an overlay can be
// positioned/styled before going live. Real messages replace these as soon as they arrive.
func samplePreview(typ string, n int) []Line {
	if typ == "alerts" {
		all := []Line{
			{Text: "+ raver_99 followed", Color: colName},
			{Text: "* djfan subscribed (Tier 1)", Color: colHot},
			{Text: "* bassqueen gifted 5 subs", Color: colHot},
			{Text: "* neonkid cheered 500 bits", Color: colMint},
		}
		return tailLines(all, n)
	}
	all := []Line{
		{Name: "raver_99", Text: "this set goes so hard Kappa", Color: chatColor("#FF7F50")},
		{Name: "bassqueen", Text: "PogChamp drop incoming", Color: chatColor("#1E90FF")},
		{Name: "neonkid", Text: "<3 <3 <3 vibes", Color: chatColor("#32CD32")},
		{Name: "mod_sam", Text: "welcome everyone LUL", Color: chatColor("#9146FF")},
		{Name: "glowstick", Text: "what track is this?? PauseChamp", Color: chatColor("#FF69B4")},
		{Name: "preview", Text: "sample chat - go live to replace", Color: colName},
	}
	return tailLines(all, n)
}

func tailLines(l []Line, n int) []Line {
	if n <= 0 || len(l) <= n {
		return l
	}
	return l[len(l)-n:]
}

func nameOf(ev twitch.Event) string {
	if ev.UserName != "" {
		return ev.UserName
	}
	return ev.UserLogin
}

func chatColor(hex string) color.Color {
	if len(hex) == 7 && hex[0] == '#' {
		var r, g, b uint8
		if n, _ := parseHex(hex, &r, &g, &b); n {
			return color.NRGBA{R: r, G: g, B: b, A: 255}
		}
	}
	return colName
}

func parseHex(s string, r, g, b *uint8) (bool, error) {
	var rr, gg, bb int
	for i, p := range []*int{&rr, &gg, &bb} {
		v, ok := hexByte(s[1+i*2], s[2+i*2])
		if !ok {
			return false, nil
		}
		*p = v
	}
	*r, *g, *b = uint8(rr), uint8(gg), uint8(bb)
	return true, nil
}

func hexByte(a, b byte) (int, bool) {
	hi, ok1 := hexNibble(a)
	lo, ok2 := hexNibble(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	return hi*16 + lo, true
}

func hexNibble(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

func transformOf(o config.VROverlay) Transform {
	return Transform{
		Snap: HandFromString(o.SnapTo),
		X:    o.X, Y: o.Y, Z: o.Z,
		Yaw: o.Yaw, Pitch: o.Pitch, Roll: o.Roll,
		WidthM:  o.ResolvedWidthM(),
		Opacity: o.ResolvedOpacity(),
	}
}

func appendRing(r []twitch.Event, ev twitch.Event) []twitch.Event {
	r = append(r, ev)
	if len(r) > ringCap {
		r = r[len(r)-ringCap:]
	}
	return r
}

func lastN(r []twitch.Event, n int) []twitch.Event {
	if n <= 0 || len(r) <= n {
		return r
	}
	return r[len(r)-n:]
}
