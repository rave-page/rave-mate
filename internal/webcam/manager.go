// Package webcam is the medialink P5 webcam/UVC source (MEDIALINK_DESIGN.md §5): a chosen video
// capture device on this instance is captured by a supervised ffmpeg dshow child (rawvideo RGBA
// pipe) and published as a local Spout sender ("rave-mate cam <device>"), with UVC PTZ/exposure
// control over a native DirectShow COM shim. Everything is driveable from a paired instance over
// the media.cam.* bus surface - the camera runs on the instance that owns it; a peer's UI drives
// it remotely. Disabled = zero footprint (no ffmpeg child, no COM). The network video route
// (webcam → paired instance's Spout) lands with the P4 encode path; the capture already speaks
// medialink.Source so it plugs into the route manager unchanged.
package webcam

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediatools"
)

const (
	source      = "webcam"
	statusEvery = 2 * time.Second
	staleAge    = 7 * time.Second // remote cameras that stop publishing drop from snapshots
	defaultW    = 1280
	defaultH    = 720
	defaultFPS  = 30
	enumTimeout = 30 * time.Second
)

type remoteEntry struct {
	Instance
	seen time.Time
}

// Router is the medialink registration surface for the network video route (P4). The webcam
// advertises itself as a video source while running; SourceOpen taps the live capture.
type Router interface {
	RegisterSource(medialink.SourceDesc, medialink.SourceOpen)
	UnregisterSource(id string)
}

// camSourceID is the webcam's medialink source id (one camera per instance).
const camSourceID = "webcam"

// Manager owns the local camera lifecycle + the media.cam.* bus surface. Create with New; the
// module manager calls Start/Stop (feature toggle). Safe for concurrent use.
type Manager struct {
	log    *logbus.Bus
	bus    *eventbus.Bus // may be nil (local-only)
	self   string        // local node id (= Status.ID / Cmd.Target)
	label  string        // human label (hostname)
	cfg    func() config.WebcamFeature
	router Router // may be nil (no media plane)

	// seams (defaults wired in New; injectable for tests)
	enumerate func(ctx context.Context) ([]DeviceInfo, error)
	getProps  func(device string) ([]PropState, error)
	setProp   func(device, prop string, value int32, auto bool) error
	openRoute func(ctx context.Context, desc capDesc) (stop func(), stats func() capStats, sender string, err error)

	startMu sync.Mutex // serializes StartCamera/StopCamera (concurrent bus cmds must not leak a capture)

	mu          sync.Mutex
	ctx         context.Context
	running     bool
	cur         capDesc
	stopCap     func()
	capStat     func() capStats
	localSender string // active local Spout sender name ("" = Spout unavailable; capture still runs)
	devices     []DeviceInfo
	props       []PropState
	lastErr     string
	remotes     map[string]remoteEntry
	unsub       []func()
	taps        map[uint64]chan *medialink.Frame // network-route fan-out (P4)
	tapSeq      uint64
}

// New builds the manager (does nothing until Start).
func New(log *logbus.Bus, bus *eventbus.Bus, self, label string, cfg func() config.WebcamFeature) *Manager {
	m := &Manager{log: log, bus: bus, self: self, label: label, cfg: cfg,
		remotes: map[string]remoteEntry{}, taps: map[uint64]chan *medialink.Frame{}}
	m.enumerate = enumerateDevices
	m.getProps = uvcProps
	m.setProp = uvcSet
	m.openRoute = m.openLocalRoute
	return m
}

// SetRouter wires the media plane: a running camera is advertised as source "webcam" (P4
// network route - §13 "RegisterSource + fan-out, no capture change").
func (m *Manager) SetRouter(r Router) { m.router = r }

// Start is the module entry: subscribe the bus surface, advertise the capability, enumerate, and
// begin the status broadcast (all ctx-bound). Non-blocking.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	if m.bus != nil {
		m.unsub = []func(){
			m.bus.Subscribe(TopicStatus, m.onStatus),
			m.bus.Subscribe(TopicCmd, m.onCmd),
		}
	}
	m.mu.Unlock()
	if m.bus != nil {
		m.bus.AddCap(CapCam)
	}
	debuglog.Go(m.log, source, func() { m.runLoop(ctx) })
	return nil
}

// Stop is the module teardown: stop any capture, unsubscribe, drop the capability.
func (m *Manager) Stop() {
	m.mu.Lock()
	unsub := m.unsub
	m.unsub = nil
	m.mu.Unlock()
	for _, u := range unsub {
		u()
	}
	if m.bus != nil {
		m.bus.RemoveCap(CapCam)
	}
	m.StopCamera()
}

// runLoop refreshes device/prop state once, optionally auto-starts, then broadcasts status.
func (m *Manager) runLoop(ctx context.Context) {
	m.refresh(ctx)
	if c := m.cfg(); c.AutoStart && strings.TrimSpace(c.Device) != "" {
		if err := m.StartCamera("", 0, 0, 0); err != nil {
			m.log.Warn(source, "auto-start failed", map[string]any{"error": err.Error()})
		}
	}
	m.publishStatus()
	t := time.NewTicker(statusEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.StopCamera()
			return
		case <-t.C:
			m.publishStatus()
		}
	}
}

// ── local camera lifecycle ────────────────────────────────────────────────────

// StartCamera starts (or restarts) the capture. Empty/zero args fall back to config, then to the
// device's first advertised mode, then to 1280x720@30.
func (m *Manager) StartCamera(device string, w, h, fps int) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	c := m.cfg()
	if !c.Enabled {
		return fmt.Errorf("webcam: feature is off (enable it in Settings)")
	}
	if device == "" {
		device = strings.TrimSpace(c.Device)
	}
	if device == "" {
		return fmt.Errorf("webcam: no device selected")
	}
	if w <= 0 || h <= 0 {
		w, h = c.Width, c.Height
	}
	if fps <= 0 {
		fps = c.FPS
	}
	reqW, reqH := w, h
	m.mu.Lock()
	w, h, fps, clamped := resolveCaptureMode(m.devices, device, w, h, fps)
	ctx := m.ctx
	m.mu.Unlock()
	if clamped {
		m.log.Warn(source, "capture size too large - clamped to a live-video mode", map[string]any{
			"device": device, "requested": fmt.Sprintf("%dx%d", reqW, reqH),
			"using": fmt.Sprintf("%dx%d", w, h), "capMax": fmt.Sprintf("%dx%d", maxCapW, maxCapH)})
	}
	if ctx == nil {
		return fmt.Errorf("webcam: not started")
	}
	m.stopCamera()

	desc := capDesc{Device: device, W: w, H: h, FPS: fps}
	stop, stats, sender, err := m.openRoute(ctx, desc)
	m.mu.Lock()
	if err != nil {
		m.lastErr = err.Error()
		m.mu.Unlock()
		return err
	}
	m.running, m.cur, m.stopCap, m.capStat, m.localSender, m.lastErr = true, desc, stop, stats, sender, ""
	m.mu.Unlock()
	if m.router != nil { // P4: the running camera is a routable video source
		m.router.RegisterSource(medialink.SourceDesc{ID: camSourceID, Name: "Webcam " + device,
			Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA,
			Width: w, Height: h, FPS: float64(fps)}, m.openTap)
	}
	m.log.Info(source, "camera started", map[string]any{
		"device": device, "size": fmt.Sprintf("%dx%d", w, h), "fps": fps, "sender": SenderName(device)})
	m.refreshProps(device)
	m.publishStatus()
	return nil
}

// StopCamera stops the active capture (no-op when idle).
func (m *Manager) StopCamera() {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.stopCamera()
}

// stopCamera is StopCamera's body (caller holds startMu).
func (m *Manager) stopCamera() {
	m.mu.Lock()
	stop := m.stopCap
	was := m.running
	m.running, m.stopCap, m.capStat, m.localSender = false, nil, nil, ""
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	if was {
		if m.router != nil {
			m.router.UnregisterSource(camSourceID)
		}
		m.closeTaps()
		m.log.Info(source, "camera stopped", nil)
		m.publishStatus()
	}
}

// ── network-route fan-out (P4) ────────────────────────────────────────────────

// openTap is the medialink SourceOpen for the running camera: a newest-wins tap on the live
// capture (per route). Fails while the camera is off.
func (m *Manager) openTap(context.Context, medialink.Offer) (medialink.Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return nil, fmt.Errorf("webcam: camera not running")
	}
	ch := make(chan *medialink.Frame, 1)
	id := m.tapSeq
	m.tapSeq++
	m.taps[id] = ch
	return &tapSource{m: m, id: id, ch: ch}, nil
}

// fanout hands a captured frame to every network tap, newest-wins. Each tap gets a shallow COPY
// (payload shared read-only) - routes stamp Stream/Seq/PTS on their own copy.
func (m *Manager) fanout(f *medialink.Frame) {
	m.mu.Lock()
	for _, ch := range m.taps {
		cp := *f
		select {
		case ch <- &cp:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- &cp:
			default:
			}
		}
	}
	m.mu.Unlock()
}

// closeTaps ends every network tap (capture stopped) - routes see EOF and close cleanly.
func (m *Manager) closeTaps() {
	m.mu.Lock()
	for id, ch := range m.taps {
		close(ch)
		delete(m.taps, id)
	}
	m.mu.Unlock()
}

// removeTap detaches one tap (route closed first).
func (m *Manager) removeTap(id uint64) {
	m.mu.Lock()
	if ch, ok := m.taps[id]; ok {
		close(ch)
		delete(m.taps, id)
	}
	m.mu.Unlock()
}

// tapSource adapts a fan-out channel to a medialink Source.
type tapSource struct {
	m    *Manager
	id   uint64
	ch   chan *medialink.Frame
	once sync.Once
}

func (t *tapSource) Next(ctx context.Context) (*medialink.Frame, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case f, ok := <-t.ch:
		if !ok {
			return nil, io.EOF // camera stopped
		}
		return f, nil
	}
}

func (t *tapSource) Close() error {
	t.once.Do(func() { t.m.removeTap(t.id) })
	return nil
}

// openLocalRoute is the default route: supervised ffmpeg capture → local Spout sender (optional) +
// network-route taps, pumped through the medialink Source/Sink seams (§5 "the webcam is just
// another Source"). The local Spout sender is best-effort: a missing SpoutLibrary.dll / non-spout
// build only drops the LOCAL preview - the capture still runs so PTZ + the cross-PC network route
// (P4) work. Only a missing ffmpeg (no capture at all) is fatal.
func (m *Manager) openLocalRoute(ctx context.Context, desc capDesc) (func(), func() capStats, string, error) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, nil, "", fmt.Errorf("webcam: ffmpeg not found (install it in Settings → Library & media, or add to PATH)")
	}
	sink, err := newSpoutSink(m.log, SenderName(desc.Device), desc.W, desc.H)
	if err != nil {
		m.log.Warn(source, "local Spout share unavailable - capture runs without a local sender",
			map[string]any{"device": desc.Device, "error": err.Error()})
		sink = nil // best-effort: keep capturing for PTZ + the cross-PC route
	}
	cap, err := newCapture(ctx, m.log, ffmpeg, desc)
	if err != nil {
		if sink != nil {
			_ = sink.Close()
		}
		return nil, nil, "", err
	}
	sender := ""
	if sink != nil {
		sender = SenderName(desc.Device)
	}
	pctx, cancel := context.WithCancel(ctx)
	// Distributor (P4 fan-out): every captured frame feeds the local Spout sink (when present) AND
	// every network tap (newest-wins) - one capture, N consumers, no capture change (§13).
	debuglog.Go(m.log, source, func() {
		defer m.closeTaps()
		for {
			f, err := cap.Next(pctx)
			if err != nil {
				if pctx.Err() == nil && err != io.EOF {
					m.log.Warn(source, "camera pump ended", map[string]any{"error": err.Error()})
				}
				return
			}
			if sink != nil {
				if err := sink.Write(f); err != nil && pctx.Err() == nil {
					m.log.Warn(source, "camera sink write failed", map[string]any{"error": err.Error()})
				}
			}
			m.fanout(f)
		}
	})
	stop := func() {
		cancel()
		_ = cap.Close()
		if sink != nil {
			_ = sink.Close()
		}
	}
	return stop, cap.stats, sender, nil
}

// maxCapW/maxCapH bound an AUTO-picked capture size. Devices advertise modes largest-first, and the
// largest is often a low-fps stills mode (e.g. C920 2304x1536@2) whose ~14MB RGBA frames starve the
// media plane - never auto-pick above 1080p.
const (
	maxCapW = 1920
	maxCapH = 1080
)

// resolveCaptureMode decides the capture size/fps. An explicit/config w×h within the cap is honored;
// an oversized one (stills-resolution) is dropped in favour of an auto-picked live mode. Empty w/h
// auto-picks. Returns the resolved size + whether an oversized request was clamped (for a warn log).
func resolveCaptureMode(devices []DeviceInfo, device string, w, h, fps int) (rw, rh, rfps int, clamped bool) {
	if w > 0 && h > 0 {
		if w <= maxCapW && h <= maxCapH {
			if fps <= 0 {
				fps = defaultFPS
			}
			return w, h, fps, false
		}
		rw, rh, rfps = pickMode(devices, device, fps)
		return rw, rh, rfps, true
	}
	rw, rh, rfps = pickMode(devices, device, fps)
	return rw, rh, rfps, false
}

// pickMode auto-selects a sane live-video capture mode for device: exact 720p if offered, else the
// largest mode within the cap at >=24fps, else the highest-fps mode within the cap, else 1280x720.
// An explicit fps overrides the mode's rate; else the mode's rounded rate, else 30.
func pickMode(devices []DeviceInfo, device string, fps int) (int, int, int) {
	rate := func(m Mode) int {
		if fps > 0 {
			return fps
		}
		if r := int(m.FPS + 0.5); r > 0 {
			return r
		}
		return defaultFPS
	}
	for _, d := range devices {
		if d.Name != device || len(d.Modes) == 0 {
			continue
		}
		for _, m := range d.Modes { // 1) exact 720p - the live-cam sweet spot
			if m.W == defaultW && m.H == defaultH {
				return m.W, m.H, rate(m)
			}
		}
		for _, m := range d.Modes { // 2) biggest mode within the cap at >=24fps (modes are largest-first)
			if m.W <= maxCapW && m.H <= maxCapH && m.FPS >= 24 {
				return m.W, m.H, rate(m)
			}
		}
		var best Mode // 3) highest-fps mode within the cap
		for _, m := range d.Modes {
			if m.W <= maxCapW && m.H <= maxCapH && m.FPS > best.FPS {
				best = m
			}
		}
		if best.W > 0 {
			return best.W, best.H, rate(best)
		}
		break // nothing within the cap - fall through to the default (ffmpeg scales)
	}
	if fps <= 0 {
		fps = defaultFPS
	}
	return defaultW, defaultH, fps
}

// ── device/prop refresh ───────────────────────────────────────────────────────

// refresh re-enumerates devices and re-reads properties for the relevant device.
func (m *Manager) refresh(ctx context.Context) {
	ectx, cancel := context.WithTimeout(ctx, enumTimeout)
	defer cancel()
	devs, err := m.enumerate(ectx)
	if err != nil {
		m.log.Warn(source, "device enumeration failed", map[string]any{"error": err.Error()})
	} else {
		// Log the names, not just the count - a camera missing here (busy / held exclusively
		// by another app, or a driver ffmpeg's dshow can't list) never reaches the picker.
		names := make([]string, 0, len(devs))
		for _, d := range devs {
			names = append(names, d.Name)
		}
		m.log.Info(source, "devices enumerated", map[string]any{
			"count": len(devs), "devices": strings.Join(names, " | ")})
	}
	m.mu.Lock()
	if err != nil {
		if len(m.devices) == 0 { // keep a stale list over an error blank
			m.lastErr = err.Error()
		}
	} else {
		m.devices = devs
	}
	dev := m.cur.Device
	m.mu.Unlock()
	if dev == "" {
		dev = strings.TrimSpace(m.cfg().Device)
	}
	if dev != "" {
		m.refreshProps(dev)
	}
}

// refreshProps re-reads the UVC property table for device (logged, never fatal - a device
// without controls just shows none).
func (m *Manager) refreshProps(device string) {
	props, err := m.getProps(device)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.props = nil
		m.log.Info(source, "uvc properties unavailable", map[string]any{"device": device, "error": err.Error()})
		return
	}
	m.props = props
}

// SetProp sets a UVC property (or its auto mode) on this instance's selected device.
func (m *Manager) SetProp(prop string, value int32, auto bool) error {
	m.mu.Lock()
	dev := m.cur.Device
	m.mu.Unlock()
	if dev == "" {
		dev = strings.TrimSpace(m.cfg().Device)
	}
	if dev == "" {
		return fmt.Errorf("webcam: no device selected")
	}
	if err := m.setProp(dev, prop, value, auto); err != nil {
		return err
	}
	m.refreshProps(dev)
	m.publishStatus()
	return nil
}

// ── bus surface ───────────────────────────────────────────────────────────────

// Command issues a camera command: over the bus when wired (the targeted instance executes -
// including this one via local fanout), else directly.
func (m *Manager) Command(cmd Cmd) error {
	if m.bus == nil {
		m.execCmd(cmd)
		return nil
	}
	raw, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	m.bus.Publish(TopicCmd, raw)
	return nil
}

// onCmd executes a command targeting this instance (Target == self, or "" = local).
func (m *Manager) onCmd(e eventbus.Event) {
	var cmd Cmd
	if json.Unmarshal(e.Data, &cmd) != nil {
		return
	}
	if cmd.Target != "" && cmd.Target != m.self {
		return
	}
	// off the bus fanout goroutine - enumeration/COM/process spawn may block
	debuglog.Go(m.log, source, func() { m.execCmd(cmd) })
}

func (m *Manager) execCmd(cmd Cmd) {
	var err error
	switch cmd.Action {
	case ActStart:
		err = m.StartCamera(cmd.Device, cmd.W, cmd.H, cmd.FPS)
	case ActStop:
		m.StopCamera()
	case ActSet:
		err = m.SetProp(cmd.Prop, cmd.Value, cmd.Auto)
	case ActRefresh:
		m.mu.Lock()
		ctx := m.ctx
		m.mu.Unlock()
		if ctx != nil {
			m.refresh(ctx)
			m.publishStatus()
		}
	default:
		return
	}
	if err != nil {
		m.mu.Lock()
		m.lastErr = err.Error()
		m.mu.Unlock()
		m.log.Warn(source, "command failed", map[string]any{"action": cmd.Action, "error": err.Error()})
		m.publishStatus()
	}
}

// onStatus records a paired instance's camera status.
func (m *Manager) onStatus(e eventbus.Event) {
	if e.Local {
		return
	}
	var st Status
	if json.Unmarshal(e.Data, &st) != nil || st.ID == "" {
		return
	}
	m.mu.Lock()
	m.remotes[st.ID] = remoteEntry{Instance: Instance{Node: e.Origin, Local: false, Status: st}, seen: time.Now()}
	m.mu.Unlock()
}

// status snapshots the local camera state.
func (m *Manager) status() Status {
	c := m.cfg()
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{ID: m.self, Label: m.label, Enabled: c.Enabled, Running: m.running,
		Device: m.cur.Device, W: m.cur.W, H: m.cur.H, FPS: m.cur.FPS,
		Err: m.lastErr, Devices: m.devices, Props: m.props}
	if !m.running {
		st.Device = strings.TrimSpace(c.Device)
		st.W, st.H, st.FPS = 0, 0, 0
	} else {
		st.Sender = m.localSender // "" when Spout is unavailable (capture still runs for the route)
		if m.capStat != nil {
			if cs := m.capStat(); cs.LastErr != "" && st.Err == "" {
				st.Err = cs.LastErr
			}
		}
	}
	return st
}

// publishStatus broadcasts the local status (peers render it; the local UI reads Instances).
func (m *Manager) publishStatus() {
	if m.bus == nil {
		return
	}
	if raw, err := json.Marshal(m.status()); err == nil {
		m.bus.Publish(TopicStatus, raw)
	}
}

// Instances returns the local camera plus every paired instance's (stale-pruned), local first.
func (m *Manager) Instances() []Instance {
	local := Instance{Node: m.self, Local: true, Status: m.status()}
	m.mu.Lock()
	now := time.Now()
	out := []Instance{local}
	for id, e := range m.remotes {
		if now.Sub(e.seen) > staleAge {
			delete(m.remotes, id)
			continue
		}
		out = append(out, e.Instance)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Local != out[j].Local {
			return out[i].Local
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// CaptureStats snapshots the running capture's counters (zero when idle).
func (m *Manager) CaptureStats() capStats {
	m.mu.Lock()
	fn := m.capStat
	m.mu.Unlock()
	if fn == nil {
		return capStats{}
	}
	return fn()
}
