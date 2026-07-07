package featurehost

import (
	"encoding/json"
	"math"

	"rave.page/mate/internal/netstats"
	"rave.page/mate/internal/perfmon"
)

// ── vr feature wire protocol (daemon proxy ↔ `rave-mate feature vr` child) ──
// Declarative + idempotent: parent→child pushes are FULL desired state (config, world, campath
// list, stats snapshot), so a restarted child rebuilds everything from a re-push - never deltas.
// Textures never cross the pipe; the child talks to OpenVR directly.

// Parent→child events (Host.Send → HandleEvent).
const (
	vrEvConfig   = "config"   // full config.VROverlayFeature (also child→parent after an in-VR edit)
	vrEvWorld    = "world"    // current VRChat world (vrctools timeline)
	vrEvCamPaths = "campaths" // full camera-path list + geometry
	vrEvStats    = "stats"    // perf ring + net snapshot for live-stats overlays (1 Hz while wanted)
	vrEvBus      = "bus"      // eventbus bridge, both directions (Origin/Local preserved down; child→up published as self)
)

// Child→parent events.
const (
	vrEvState   = "state"   // {available, binding} on change
	vrEvAction  = "action"  // VR slot / quick button fired a daemon-side bind action
	vrEvCamLoad = "campath" // load a camera path into VRChat (runs in-daemon via vrctools)
)

// Parent→child request methods (Host.Call → Handle).
const (
	vrMethInputDiag     = "inputDiag"
	vrMethBindingStatus = "bindingStatus"
	vrMethActionBinding = "actionBinding"
	vrMethOpenBindingUI = "openBindingUI"
	vrMethToggleAll     = "toggleAll"
	vrMethToggleHidden  = "toggleHidden"
	vrMethSetHidden     = "setHidden"
	vrMethEditorToggle  = "editorToggle"
	vrMethPerfProbe     = "perfProbe"
	vrMethSnapshot      = "snapshot" // child-state diag (tests + ctl debugging)
)

// vrBusEvent bridges one eventbus event across the pipe. Downstream (daemon→child) Origin/Local
// carry the daemon bus's view; upstream the child leaves them zero (daemon publishes as itself).
type vrBusEvent struct {
	Topic  string          `json:"topic"`
	Origin string          `json:"origin,omitempty"`
	Local  bool            `json:"local,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// vrStateEvent mirrors the child's live VR state to the proxy (Available/BindingStatus caches).
type vrStateEvent struct {
	Available bool `json:"available"`
	Binding   int  `json:"binding"` // vroverlay.BindingStatus
}

// vrActionEvent fires a daemon-side keybind action (OBS control, STT, app groups, …).
type vrActionEvent struct {
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
}

// vrCamLoadEvent asks the daemon to load a camera path into VRChat over OSC.
type vrCamLoadEvent struct {
	File string `json:"file"`
}

// vrWorldEvent is the current VRChat world for per-world overlay layouts.
type vrWorldEvent struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	OK   bool   `json:"ok"`
}

// vrCamPathItem is one selectable camera path + its preview geometry (parallel slices).
type vrCamPathItem struct {
	Label string       `json:"label"`
	File  string       `json:"file"`
	Pts   [][3]float32 `json:"pts,omitempty"`
	Spd   []float32    `json:"spd,omitempty"`
	Dur   []float32    `json:"dur,omitempty"`
}

// vrCamPathsEvent replaces the child's full camera-path list.
type vrCamPathsEvent struct {
	Items []vrCamPathItem `json:"items"`
}

// vrStatsEvent feeds the live-stats overlays (perf/network/timing kinds).
type vrStatsEvent struct {
	Perf []perfmon.Sample `json:"perf,omitempty"`
	Net  *vrNetWire       `json:"net,omitempty"`
}

// vrNetWire is netstats.Snapshot with NaN-safe series (JSON can't carry NaN; nil = gap).
type vrNetWire struct {
	PeerIn      []*float64  `json:"peerIn,omitempty"`
	PeerOut     []*float64  `json:"peerOut,omitempty"`
	APIIn       []*float64  `json:"apiIn,omitempty"`
	APIOut      []*float64  `json:"apiOut,omitempty"`
	SessPeerIn  uint64      `json:"sessPeerIn,omitempty"`
	SessPeerOut uint64      `json:"sessPeerOut,omitempty"`
	SessAPIIn   uint64      `json:"sessApiIn,omitempty"`
	SessAPIOut  uint64      `json:"sessApiOut,omitempty"`
	RTT         []vrRTTWire `json:"rtt,omitempty"`
	Span        int         `json:"span,omitempty"`
}

// vrRTTWire is one peer's RTT series (NaN-safe).
type vrRTTWire struct {
	NodeID   string     `json:"nodeId"`
	Label    string     `json:"label,omitempty"`
	Ms       []*float64 `json:"ms,omitempty"`
	LatestMs float64    `json:"latestMs,omitempty"`
	Has      bool       `json:"has,omitempty"`
}

// f64sToPtr encodes a series NaN-safely (NaN → nil).
func f64sToPtr(in []float64) []*float64 {
	if in == nil {
		return nil
	}
	out := make([]*float64, len(in))
	for i, v := range in {
		if !math.IsNaN(v) {
			v := v
			out[i] = &v
		}
	}
	return out
}

// ptrToF64s decodes a NaN-safe series (nil → NaN).
func ptrToF64s(in []*float64) []float64 {
	if in == nil {
		return nil
	}
	out := make([]float64, len(in))
	for i, p := range in {
		if p == nil {
			out[i] = math.NaN()
		} else {
			out[i] = *p
		}
	}
	return out
}

// netToWire converts a netstats snapshot for the pipe.
func netToWire(s netstats.Snapshot) vrNetWire {
	w := vrNetWire{
		PeerIn: f64sToPtr(s.PeerIn), PeerOut: f64sToPtr(s.PeerOut),
		APIIn: f64sToPtr(s.APIIn), APIOut: f64sToPtr(s.APIOut),
		SessPeerIn: s.SessPeerIn, SessPeerOut: s.SessPeerOut,
		SessAPIIn: s.SessAPIIn, SessAPIOut: s.SessAPIOut,
		Span: s.Span,
	}
	for _, r := range s.RTT {
		w.RTT = append(w.RTT, vrRTTWire{NodeID: r.NodeID, Label: r.Label, Ms: f64sToPtr(r.Ms), LatestMs: r.LatestMs, Has: r.Has})
	}
	return w
}

// wireToNet converts back on the child side.
func wireToNet(w vrNetWire) netstats.Snapshot {
	s := netstats.Snapshot{
		PeerIn: ptrToF64s(w.PeerIn), PeerOut: ptrToF64s(w.PeerOut),
		APIIn: ptrToF64s(w.APIIn), APIOut: ptrToF64s(w.APIOut),
		SessPeerIn: w.SessPeerIn, SessPeerOut: w.SessPeerOut,
		SessAPIIn: w.SessAPIIn, SessAPIOut: w.SessAPIOut,
		Span: w.Span,
	}
	for _, r := range w.RTT {
		s.RTT = append(s.RTT, netstats.RTTSeries{NodeID: r.NodeID, Label: r.Label, Ms: ptrToF64s(r.Ms), LatestMs: r.LatestMs, Has: r.Has})
	}
	return s
}

// vrIDParam addresses one overlay (toggleHidden / setHidden).
type vrIDParam struct {
	ID     string `json:"id"`
	Hidden bool   `json:"hidden,omitempty"`
}

// vrActionParam names a SteamVR action path (actionBinding).
type vrActionParam struct {
	Action string `json:"action"`
}

// vrSnapshot is the child-state diag payload (snapshot method).
type vrSnapshot struct {
	Overlays  int    `json:"overlays"`
	WorldID   string `json:"worldId,omitempty"`
	CamPaths  int    `json:"camPaths"`
	StatsOK   bool   `json:"statsOk"`
	Available bool   `json:"available"`
}
