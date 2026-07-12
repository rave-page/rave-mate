package webui

// RMPC point-cloud VIEWER (task #83, rave-mate side). Opens an exported .rmpc and plays it
// back in a raw-WebGL canvas (assets/pcviewer/pc_viewer.js) - no three.js/framework. Data path:
// Go validates the header (pointcloud.NewDecoder), registers the file on the loopback media
// endpoint (mpMediaURL, Range-capable, token-guarded, 127.0.0.1 only), renders the modal shell
// (canvas + transport), injects the WebGL module once, and calls __pcv.open(url,meta). The JS
// module fetches the raw bytes, ports the RMPC decoder, keeps the quantized u16 frame stream in
// one ArrayBuffer, and dequantizes on the GPU via bounds uniforms. This keeps 100k+-point clouds
// off the eval bridge (which coalesces whole batches - unbounded for a big blob). GL is disposed
// on close / next open (see pc_viewer.js dispose).

import (
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/pointcloud"
)

// moPCViewMaxBytes caps the file the viewer loads (the whole quantized stream lands in a browser
// ArrayBuffer). A cloud past this is impractical to orbit interactively anyway.
const moPCViewMaxBytes = 768 << 20 // 768 MiB

var (
	pcvJSOnce sync.Once
	pcvJS     string
)

// pcViewerJS returns the embedded WebGL module source (cached). Injected on open; idempotent
// (guards `if(window.__pcv)return`), so re-injection on subsequent opens is a no-op.
func pcViewerJS() string {
	pcvJSOnce.Do(func() {
		b, _ := assetsFS.ReadFile("assets/pcviewer/pc_viewer.js")
		pcvJS = string(b)
	})
	return pcvJS
}

func init() {
	// "Open .rmpc…" → native open picker → redispatched here with the chosen path.
	onExact("mo-pc-view", func(u *UI, m actMsg) { u.moPCView(m.Val) })
	// close disposes the GL context/buffers (JS) before clearing the modal.
	onExact("pcv-close", func(u *UI, _ actMsg) {
		u.eval("window.__pcv&&window.__pcv.close()")
		u.closeModal()
	})
	// enable the webview GPU escape hatch so WebGL is available after a restart.
	onExact("pcv-enablegpu", func(u *UI, _ actMsg) { u.pcvEnableGPU() })
}

// moPCView validates the picked .rmpc, registers a loopback URL, and opens the WebGL viewer.
// Off-thread: file stat + header decode must not block the act worker.
func (u *UI) moPCView(path string) {
	if path == "" {
		return
	}
	// The viewer renders on the GPU (WebGL). rave-mate keeps GPU compositing off by default to stay
	// out of a live stream's way, so intercept the common case with a native prompt (nicer than the
	// JS "edit config + restart" fallback, which only shows when GPU is on but still unavailable).
	if u.svc.Cfg != nil && !u.svc.Cfg.Features.UI.AllowWebviewGPU() {
		u.openModal(u.moPCGpuModal(false))
		return
	}
	u.bg(func() {
		f, err := os.Open(path)
		if err != nil {
			u.toast(i18n.T("motion.toast.pcViewOpen") + err.Error())
			return
		}
		if st, err := f.Stat(); err == nil && st.Size() > moPCViewMaxBytes {
			_ = f.Close()
			u.toast(i18n.T("motion.toast.pcViewTooBig", i18n.A{"size": humanBytes(uint64(st.Size()))}))
			return
		}
		dec, err := pointcloud.NewDecoder(f)
		_ = f.Close()
		if err != nil {
			u.toast(i18n.T("motion.toast.pcViewBad") + err.Error())
			return
		}
		h := dec.Header()
		if h.PointCount <= 0 || h.FrameCount <= 0 {
			u.toast(i18n.T("motion.toast.pcViewBad") + "empty")
			return
		}
		url := u.mpMediaURL(path)
		if url == "" {
			u.toast(i18n.T("motion.toast.pcViewNoUrl"))
			return
		}
		u.openModal(u.moPCViewerModal(filepath.Base(path), h))
		u.eval(pcViewerJS())
		meta, _ := json.Marshal(map[string]any{
			"url": url, "name": filepath.Base(path),
			"fps": h.FPS, "frameCount": h.FrameCount, "pointCount": h.PointCount, "hasColor": h.HasColor,
		})
		u.eval("window.__pcv&&window.__pcv.open(" + jsQuote(string(meta)) + ")")
	})
}

// moPCViewerModal builds the viewer modal shell (canvas + transport). Close controls dispatch
// pcv-close (dispose GL) instead of the generic modal-close. Transport buttons/slider carry NO
// data-act - the runtime leaves them alone and pc_viewer.js wires them (no Go round-trip per frame).
func (u *UI) moPCViewerModal(name string, h pointcloud.Header) string {
	maxFrame := strconv.Itoa(max(h.FrameCount-1, 0))
	body := `<div class=pcv-wrap>` +
		`<div id=pcv-stage class=pcv-stage><canvas id=pcv-canvas class=pcv-canvas></canvas></div>` +
		`<div class=pcv-transport>` +
		`<button id=pcv-play class="rp-btn rp-btn--go pcv-play">▶ ` + html.EscapeString(i18n.T("player.play")) + `</button>` +
		`<input id=pcv-scrub class=slider-input type=range min=0 max=` + maxFrame + ` step=1 value=0>` +
		`<span id=pcv-time class=pcv-time data-label="pcv-time"></span>` +
		`</div>` +
		`<div id=pcv-info class=pcv-info data-label="pcv-info"></div>` +
		`<div class=pcv-hint>` + html.EscapeString(i18n.T("motion.pcViewHint")) + `</div>` +
		`</div>`
	foot := btn(i18n.T("common.close"), "outline", "pcv-close", "")
	title := i18n.T("motion.pcViewTitle", i18n.A{"name": name})
	return `<div class=modal-scrim data-act=pcv-close></div>` +
		`<div class="modal pcv-modal" role=dialog><div class=modal-head><h3 class=modal-title>` +
		html.EscapeString(title) + `</h3>` +
		`<button class=modal-x data-act=pcv-close aria-label=Close>✕</button></div>` +
		`<div class=modal-body>` + body + `</div><div class=modal-foot>` + foot + `</div></div>`
}

// moPCGpuModal is the "viewer needs GPU" prompt. enabled=false: explain the streaming-safe default
// + offer one-click enable; enabled=true: confirm + tell the user to restart. We never auto-restart
// - GPU-off is what keeps rave-mate off a live encoder, so the user restarts when they're not on air.
func (u *UI) moPCGpuModal(enabled bool) string {
	var body, foot string
	if enabled {
		body = `<p class=pcv-gpu-msg>` + html.EscapeString(i18n.T("motion.pcGpuEnabled")) + `</p>`
		foot = btn(i18n.T("common.close"), "outline", "pcv-close", "")
	} else {
		body = `<p class=pcv-gpu-msg>` + html.EscapeString(i18n.T("motion.pcGpuWhy")) + `</p>`
		foot = btn(i18n.T("motion.pcGpuEnable"), "go", "pcv-enablegpu", "") +
			btn(i18n.T("common.close"), "outline", "pcv-close", "")
	}
	title := i18n.T("motion.pcGpuTitle")
	return `<div class=modal-scrim data-act=pcv-close></div>` +
		`<div class="modal pcv-modal" role=dialog><div class=modal-head><h3 class=modal-title>` +
		html.EscapeString(title) + `</h3>` +
		`<button class=modal-x data-act=pcv-close aria-label=Close>✕</button></div>` +
		`<div class=modal-body>` + body + `</div><div class=modal-foot>` + foot + `</div></div>`
}

// pcvEnableGPU flips the webview-GPU escape hatch on + persists it, then swaps the prompt to the
// "restart to apply" confirmation. Takes effect on next launch (the flag is read once at shell
// creation); reverts on save failure.
func (u *UI) pcvEnableGPU() {
	if u.svc.Cfg == nil {
		return
	}
	on := true
	u.svc.Cfg.Features.UI.WebviewGPU = &on
	u.openModal(u.moPCGpuModal(true)) // optimistic - confirm before the disk write returns
	u.saveCfgBG("ui-webview-gpu", nil, func() {
		u.svc.Cfg.Features.UI.WebviewGPU = nil // save failed - revert the flip
		u.toast(i18n.T("motion.toast.pcGpuSaveFail"))
	})
}
