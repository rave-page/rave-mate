package webui

import (
	"strconv"
	"strings"
	"sync"

	"rave.page/mate/internal/webcam"
)

// peers_actions.go: Peers-tab action handlers (webcam PTZ, per-connection Control toggle, MIDI
// forwarding, file-xfer settings) + the ~1 Hz peers-body live tick. The core peer/media/xfer
// actions (peer-connect/-forget/-sas, media-recv/-stop, xfer-accept/-cancel) stay wired in ui.go.

// camPend holds the pending device/mode selection per camera instance so a user's pick survives the
// 1 Hz body re-render (the Fyne widgets held this state in-memory). Cleared on start.
var camPend = struct {
	sync.Mutex
	dev  map[string]string
	mode map[string]string
}{dev: map[string]string{}, mode: map[string]string{}}

func camPendingDevice(node, fallback string) string {
	camPend.Lock()
	defer camPend.Unlock()
	if d, ok := camPend.dev[node]; ok {
		return d
	}
	return fallback
}

func camPendingMode(node string) string {
	camPend.Lock()
	defer camPend.Unlock()
	return camPend.mode[node]
}

func init() {
	// Per-connection Control toggle + control-banner Stop (MIDI forwarding to a paired instance).
	onPrefix("peers-control:", func(u *UI, m actMsg) {
		u.setPeerControl(m.arg("peers-control:"), m.Val == "1")
	})

	// ── webcam ──
	onPrefix("peers-cam-refresh:", func(u *UI, m actMsg) {
		u.camCommand(webcam.Cmd{Target: m.arg("peers-cam-refresh:"), Action: webcam.ActRefresh})
	})
	onPrefix("peers-cam-device:", func(u *UI, m actMsg) {
		node := m.arg("peers-cam-device:")
		camPend.Lock()
		camPend.dev[node] = m.Val
		delete(camPend.mode, node) // modes depend on the device
		camPend.Unlock()
		if u.svc.Identity != nil && node == u.svc.Identity.NodeID && u.svc.Cfg != nil {
			u.svc.Cfg.Features.Webcam.Device = m.Val
			u.saveCfg()
		}
		u.patchMain()
	})
	onPrefix("peers-cam-mode:", func(u *UI, m actMsg) {
		node := m.arg("peers-cam-mode:")
		camPend.Lock()
		camPend.mode[node] = m.Val
		camPend.Unlock()
	})
	onPrefix("peers-cam-start:", func(u *UI, m actMsg) {
		u.camStartStop(m.arg("peers-cam-start:"), m.Val == "start")
	})
	onPrefix("peers-cam-prop:", func(u *UI, m actMsg) {
		node, prop, ok := strings.Cut(m.arg("peers-cam-prop:"), "\x1f")
		if !ok {
			return
		}
		v, err := strconv.Atoi(strings.TrimSpace(m.Val))
		if err != nil {
			return
		}
		u.camCommand(webcam.Cmd{Target: node, Action: webcam.ActSet, Prop: prop, Value: int32(v)})
	})
	onPrefix("peers-cam-auto:", func(u *UI, m actMsg) {
		node, prop, ok := strings.Cut(m.arg("peers-cam-auto:"), "\x1f")
		if !ok {
			return
		}
		cmd := webcam.Cmd{Target: node, Action: webcam.ActSet, Prop: prop, Auto: m.Val == "true"}
		if m.Val != "true" { // manual takeover - pin at the property's current value
			cmd.Value = u.camPropValue(node, prop)
		}
		u.camCommand(cmd)
	})

	// ── file transfer settings ──
	onExact("peers-xfer-enabled", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		on := m.Val == "true"
		u.svc.Cfg.Features.FileXfer.Enabled = on
		u.saveCfg()
		if u.svc.Modules != nil {
			u.svc.Modules.SetEnabled("filexfer", on)
		}
		u.patchMain()
		u.toast("Receive files" + onOff(on))
	})
	onPrefix("peers-xfer-mode:", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		mode := m.Val
		if mode != "auto" {
			mode = "ask"
		}
		u.svc.Cfg.Features.FileXfer.AcceptMode = mode
		u.saveCfg()
		u.patchMain()
	})
	onExact("peers-xfer-dir", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		u.svc.Cfg.Features.FileXfer.DownloadDir = strings.TrimSpace(m.Val)
		u.saveCfg()
	})

	// ~1 Hz body refresh - skip while a peers-body select/input is focused (don't clobber an open
	// dropdown or in-progress typing in the download-dir field).
	onLiveTick("peers", func(u *UI) {
		u.eval("(function(){var el=document.activeElement,b=document.getElementById('peers-body');" +
			"if(b&&el&&b.contains(el)&&/^(SELECT|INPUT)$/.test(el.tagName))return;" +
			"window.__patch('peers-body'," + jsQuote(u.peersBody()) + ");})()")
	})
}

// setPeerControl routes this machine's MIDI/control to a peer (on) or clears it (off).
func (u *UI) setPeerControl(node string, on bool) {
	if u.svc.PeerBridge == nil {
		return
	}
	if on {
		u.svc.PeerBridge.SetControlTarget(node)
		u.svc.PeerBridge.SetMIDIForwarding(true)
	} else {
		u.svc.PeerBridge.SetMIDIForwarding(false)
		u.svc.PeerBridge.SetControlTarget("")
	}
	u.patchMain()
}

// camCommand publishes a camera command on the media.cam bus (executes on the owning instance).
func (u *UI) camCommand(cmd webcam.Cmd) {
	if u.svc.Webcam == nil {
		return
	}
	u.bg(func() { u.logErr("webcam command", u.svc.Webcam.Command(cmd)) })
}

// camStartStop starts (with the pending/instance device+mode) or stops the node's camera.
func (u *UI) camStartStop(node string, start bool) {
	if u.svc.Webcam == nil {
		return
	}
	if !start {
		u.camCommand(webcam.Cmd{Target: node, Action: webcam.ActStop})
		return
	}
	dev, mode := u.camSelection(node)
	cmd := webcam.Cmd{Target: node, Action: webcam.ActStart, Device: dev}
	cmd.W, cmd.H, cmd.FPS = parseCamMode(mode)
	camPend.Lock()
	delete(camPend.dev, node)
	delete(camPend.mode, node)
	camPend.Unlock()
	u.camCommand(cmd)
}

// camSelection resolves the effective device + mode for a node (pending pick, else instance state).
func (u *UI) camSelection(node string) (device, mode string) {
	var inDev string
	for _, in := range u.svc.Webcam.Instances() {
		if in.ID == node {
			inDev = in.Device
			if mode == "" && in.Running {
				mode = fmtCamMode(in.W, in.H, in.FPS)
			}
			break
		}
	}
	device = camPendingDevice(node, inDev)
	if pm := camPendingMode(node); pm != "" {
		mode = pm
	}
	return device, mode
}

// camPropValue reads a property's current value from the node's live status (0 if unknown).
func (u *UI) camPropValue(node, prop string) int32 {
	if u.svc.Webcam == nil {
		return 0
	}
	for _, in := range u.svc.Webcam.Instances() {
		if in.ID != node {
			continue
		}
		for _, p := range in.Props {
			if p.ID == prop {
				return p.Value
			}
		}
	}
	return 0
}

// parseCamMode parses "1280x720 @ 30" back into w/h/fps (zeros → device default).
func parseCamMode(s string) (w, h, fps int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, 0
	}
	size, rate, _ := strings.Cut(s, "@")
	xw, xh, ok := strings.Cut(strings.TrimSpace(size), "x")
	if !ok {
		return 0, 0, 0
	}
	w, _ = strconv.Atoi(strings.TrimSpace(xw))
	h, _ = strconv.Atoi(strings.TrimSpace(xh))
	fps, _ = strconv.Atoi(strings.TrimSpace(rate))
	if w <= 0 || h <= 0 {
		return 0, 0, 0
	}
	return w, h, fps
}
