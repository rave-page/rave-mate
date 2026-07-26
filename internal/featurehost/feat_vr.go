package featurehost

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/netstats"
	"rave.page/mate/internal/perfmon"
	"rave.page/mate/internal/vrbind"
	"rave.page/mate/internal/vroverlay"
)

func init() { Register("vr", func() Feature { return &vrFeature{} }) }

// vrInit is the init wire config: the VROverlay config snapshot the child starts from (re-read from
// live daemon config on every (re)spawn); live edits stream in as vrEvConfig pushes afterwards.
type vrInit struct {
	Config config.VROverlayFeature `json:"config"`
}

// vrFeature hosts the FULL VR overlay stack (OpenVR init, overlays, wrist strip, paged menu, ray
// pointer, in-VR editor, motion, world layouts) in its own subprocess - a cgo/OpenVR/GPU fault kills
// only this child; the host restarts it and the daemon re-pushes desired state. SteamVR closing is
// NOT fatal: the Manager's own supervise loop waits and re-inits inside the child. On a non-vr build
// the stub runtime keeps the manager idle ("waiting for SteamVR / non-vr build") instead of crashing.
type vrFeature struct {
	rt      *Runtime
	mgr     *vroverlay.Manager
	bus     *vrChildBus
	dirtyCh chan struct{} // cap 1: wakes flushConfig on the clean→dirty transition

	mu       sync.Mutex
	snap     config.VROverlayFeature
	cfgDirty bool // an in-VR edit changed snap since the last emit (coalesced flush - see mutate/flushConfig)
	world    vrWorldEvent
	cam      []vrCamPathItem
	perf     []perfmon.Sample
	net      netstats.Snapshot
	stats    bool // a stats push arrived (else overlays show "waiting for data")
}

func (f *vrFeature) Init(params json.RawMessage, rt *Runtime) error {
	var p vrInit
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return err
		}
	}
	f.rt = rt
	f.snap = p.Config
	f.dirtyCh = make(chan struct{}, 1)
	f.bus = &vrChildBus{emit: func(topic string, data json.RawMessage) {
		rt.Emit(vrEvBus, vrBusEvent{Topic: topic, Data: data})
	}}
	f.mgr = vroverlay.New(rt.Log, f.bus, vroverlay.NewRuntime(), f.cfgSnap, f.mutate)
	f.mgr.SetBeat(rt.Beat)
	f.mgr.SetBindDispatcher(f.dispatcher(), func() []vrbind.Bind { return f.cfgSnap().Binds })
	f.mgr.SetCamPathProvider(f.camList, f.camLoad, f.camGeom)
	f.mgr.SetWorldSource(f.worldSrc)
	f.mgr.SetStatsProviders(f.perfStats, f.netStats)
	rt.Log.Info("feature:vr", "vr overlay subprocess up", map[string]any{"overlays": len(p.Config.Overlays)})
	return nil
}

// dispatcher routes VR slot / quick-button actions: overlay-local ones act on the in-child manager;
// everything else (OBS control, STT, app groups, …) forwards to the daemon as a vrEvAction event.
func (f *vrFeature) dispatcher() *vrbind.Dispatcher {
	d := vrbind.NewDispatcher()
	for _, a := range vrbind.Actions() {
		switch a.ID {
		case vrbind.ActEditorToggle:
			d.Register(a.ID, func(string) { f.mgr.RequestEditorToggle() })
		case vrbind.ActOverlaysToggle:
			d.Register(a.ID, func(string) { f.mgr.ToggleAllOverlays() })
		case vrbind.ActOverlayToggle:
			d.Register(a.ID, func(t string) { f.mgr.ToggleHidden(t) })
		case vrbind.ActOverlayShow:
			d.Register(a.ID, func(t string) { f.mgr.SetHidden(t, false) })
		case vrbind.ActOverlayHide:
			d.Register(a.ID, func(t string) { f.mgr.SetHidden(t, true) })
		default:
			id := a.ID
			d.Register(id, func(t string) { f.rt.Emit(vrEvAction, vrActionEvent{Action: string(id), Target: t}) })
		}
	}
	return d
}

// cfgSnap returns the current config snapshot (value copy; slice backing shared like in-proc).
func (f *vrFeature) cfgSnap() config.VROverlayFeature {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

// mutate applies an in-VR editor edit to the LOCAL snapshot immediately (overlays read snap every render
// tick, so movement stays smooth) but only marks it dirty - the actual daemon emit is coalesced by
// flushConfig. Continuous edits (thumbstick nudge / slider drag fire at the 90 Hz input tick) would
// otherwise emit a full-config JSON 90×/s; the daemon persists each to disk, the stdio pipe backs up,
// Emit blocks the child's render/beat goroutine, and the host force-restarts it (every overlay vanishes).
func (f *vrFeature) mutate(fn func(*config.VROverlayFeature)) {
	f.mu.Lock()
	fn(&f.snap)
	first := !f.cfgDirty
	f.cfgDirty = true
	f.mu.Unlock()
	if first { // wake the flusher on the clean→dirty transition only
		select {
		case f.dirtyCh <- struct{}{}:
		default:
		}
	}
}

// flushConfig emits the coalesced config to the daemon at ≤10 Hz plus a trailing flush on shutdown, so
// a burst of in-VR edits costs at most one full-config write per 100 ms (was one per 90 Hz frame).
// Event-armed: the ticker runs only while edits are in flight - no 10 Hz idle wakeups.
func (f *vrFeature) flushConfig(ctx context.Context) {
	t := time.NewTicker(100 * time.Millisecond)
	t.Stop() // armed by the first edit
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			f.flushConfigOnce() // trailing flush - don't drop the last edit of a drag
			return
		case <-f.dirtyCh: // burst started: open a 100 ms coalescing window
			t.Reset(100 * time.Millisecond)
		case <-t.C:
			if !f.flushConfigOnce() {
				t.Stop() // burst over - idle until the next edit
			}
		}
	}
}

// flushConfigOnce emits the snapshot if dirty; reports whether it flushed.
func (f *vrFeature) flushConfigOnce() bool {
	f.mu.Lock()
	if !f.cfgDirty {
		f.mu.Unlock()
		return false
	}
	cp := f.snap
	f.cfgDirty = false
	f.mu.Unlock()
	f.rt.Emit(vrEvConfig, cp)
	return true
}

func (f *vrFeature) camList() []vroverlay.CamPathItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]vroverlay.CamPathItem, len(f.cam))
	for i, c := range f.cam {
		out[i] = vroverlay.CamPathItem{Label: c.Label, File: c.File}
	}
	return out
}

// camLoad forwards a camera-path load to the daemon (vrctools owns OSC + backup there).
func (f *vrFeature) camLoad(file string) error {
	f.rt.Emit(vrEvCamLoad, vrCamLoadEvent{File: file})
	return nil
}

func (f *vrFeature) camGeom(file string) vroverlay.CamPathGeom {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cam {
		if c.File == file {
			return vroverlay.CamPathGeom{Pts: c.Pts, Spd: c.Spd, Dur: c.Dur}
		}
	}
	return vroverlay.CamPathGeom{}
}

func (f *vrFeature) worldSrc() (string, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.world.ID, f.world.Name, f.world.OK
}

func (f *vrFeature) perfStats() []perfmon.Sample {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.perf
}

func (f *vrFeature) netStats() netstats.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.net
}

// Start runs the manager's supervise loop (wait for SteamVR → render → reconnect) plus a 1 Hz state
// mirror to the proxy; ctx cancel unwinds both cleanly (exit 0).
func (f *vrFeature) Start(ctx context.Context) error {
	go f.pushState(ctx)
	go f.flushConfig(ctx) // coalesced in-VR-edit persistence (see mutate)
	return f.mgr.Start(ctx)
}

// pushState mirrors {available, bindingStatus} to the daemon on change (proxy caches it so
// UI polls never round-trip the pipe).
func (f *vrFeature) pushState(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var last *vrStateEvent
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur := vrStateEvent{Available: f.mgr.Available(), Binding: int(f.mgr.BindingStatus())}
		if last == nil || *last != cur {
			last = &cur
			f.rt.Emit(vrEvState, cur)
		}
	}
}

// HandleEvent applies parent→child state pushes (full desired state; idempotent).
func (f *vrFeature) HandleEvent(event string, data json.RawMessage) {
	switch event {
	case vrEvConfig:
		var c config.VROverlayFeature
		if json.Unmarshal(data, &c) == nil {
			f.mu.Lock()
			f.snap = c
			f.mu.Unlock()
		}
	case vrEvWorld:
		var w vrWorldEvent
		if json.Unmarshal(data, &w) == nil {
			f.mu.Lock()
			f.world = w
			f.mu.Unlock()
		}
	case vrEvCamPaths:
		var c vrCamPathsEvent
		if json.Unmarshal(data, &c) == nil {
			f.mu.Lock()
			f.cam = c.Items
			f.mu.Unlock()
		}
	case vrEvStats:
		var s vrStatsEvent
		if json.Unmarshal(data, &s) == nil {
			f.mu.Lock()
			f.perf = s.Perf
			if s.Net != nil {
				f.net = wireToNet(*s.Net)
			}
			f.stats = true
			f.mu.Unlock()
		}
	case vrEvBus:
		var e vrBusEvent
		if json.Unmarshal(data, &e) == nil {
			f.bus.inject(eventbus.Event{Topic: e.Topic, Origin: e.Origin, Local: e.Local, Data: e.Data})
		}
	case vrEvGpuReset:
		var e vrGpuResetEvent
		if json.Unmarshal(data, &e) == nil {
			f.mgr.RequestReinit(e.Detail)
		}
	}
}

// Handle serves proxy control requests (Surface calls + diagnostics).
func (f *vrFeature) Handle(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case vrMethInputDiag:
		return json.Marshal(f.mgr.InputDiag())
	case vrMethBindingStatus:
		return json.Marshal(int(f.mgr.BindingStatus()))
	case vrMethActionBinding:
		var p vrActionParam
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return json.Marshal(f.mgr.ActionBinding(p.Action))
	case vrMethOpenBindingUI:
		return nil, f.mgr.OpenBindingUI()
	case vrMethToggleAll:
		f.mgr.ToggleAllOverlays()
		return nil, nil
	case vrMethEditorToggle:
		f.mgr.RequestEditorToggle()
		return nil, nil
	case vrMethToggleHidden:
		var p vrIDParam
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		f.mgr.ToggleHidden(p.ID)
		return nil, nil
	case vrMethSetHidden:
		var p vrIDParam
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		f.mgr.SetHidden(p.ID, p.Hidden)
		return nil, nil
	case vrMethPerfProbe:
		return json.Marshal(f.mgr.PerfProbe())
	case vrMethSnapshot:
		f.mu.Lock()
		s := vrSnapshot{Overlays: len(f.snap.Overlays), WorldID: f.world.ID, CamPaths: len(f.cam), StatsOK: f.stats}
		f.mu.Unlock()
		s.Available = f.mgr.Available()
		return json.Marshal(s)
	default:
		return nil, errUnknownMethod(method)
	}
}

// vrChildBus is the child-side vroverlay.Bus: local Publish forwards to the daemon (which
// republishes on the real mesh bus); inject fans a daemon-forwarded event out to subscribers
// with Origin/Local preserved (eventbus.Inbound would force Local=false and break the
// "(this PC)" tagging on the OBS overlay).
type vrChildBus struct {
	emit func(topic string, data json.RawMessage)

	mu   sync.Mutex
	subs map[string]map[int]func(eventbus.Event)
	next int
}

// Subscribe mirrors eventbus.Bus.Subscribe.
func (b *vrChildBus) Subscribe(topic string, fn func(eventbus.Event)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs == nil {
		b.subs = map[string]map[int]func(eventbus.Event){}
	}
	if b.subs[topic] == nil {
		b.subs[topic] = map[int]func(eventbus.Event){}
	}
	id := b.next
	b.next++
	b.subs[topic][id] = fn
	return func() {
		b.mu.Lock()
		if m := b.subs[topic]; m != nil {
			delete(m, id)
		}
		b.mu.Unlock()
	}
}

// Publish forwards a child-produced event (vr perf telemetry, OBS commands) to the daemon.
func (b *vrChildBus) Publish(topic string, data json.RawMessage) {
	if b.emit != nil {
		b.emit(topic, data)
	}
}

// inject delivers a daemon-forwarded event to local subscribers.
func (b *vrChildBus) inject(ev eventbus.Event) {
	b.mu.Lock()
	hs := make([]func(eventbus.Event), 0, len(b.subs[ev.Topic]))
	for _, fn := range b.subs[ev.Topic] {
		hs = append(hs, fn)
	}
	b.mu.Unlock()
	for _, fn := range hs {
		fn(ev)
	}
}
