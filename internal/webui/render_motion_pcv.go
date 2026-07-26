package webui

import "rave.page/mate/internal/zigui"

// Point-cloud viewer DIALOG surfaces (wave-4 dialog sweep B): the viewer shell and the
// "viewer needs GPU" prompt. Only the STRUCTURAL chrome is Go/Zig-rendered - everything inside
// #pcv-canvas is pc_viewer.js (THREE.js), and the transport controls carry NO data-act on purpose
// (the JS wires them, so a frame never costs a Go round-trip). The renderers below are pure and
// mirrored in native/zigui/src/dialogs_b.zig (gate: zigui_golden_dialogs_b_test.go).
//
// Both dialogs hand-roll their chrome instead of using components.go modal(): the dialog carries an
// extra `pcv-modal` class and every close control dispatches pcv-close (dispose GL) rather than the
// generic modal-close. That bracket is therefore local to this pair in BOTH renderers.

// moPCViewSt is the viewer shell. MaxFrame is a pre-formatted integer spliced into an UNQUOTED
// attribute, exactly as the Go original did.
type moPCViewSt struct {
	Title     string `json:"title"`
	PlayLabel string `json:"playLabel"`
	MaxFrame  string `json:"maxFrame"`
	Hint      string `json:"hint"`
	Close     string `json:"close"`
}

// moPCGpuSt is the GPU prompt. Enabled = the flag was just flipped, so the card confirms and asks
// for a restart instead of offering the one-click enable.
type moPCGpuSt struct {
	Title       string `json:"title"`
	Msg         string `json:"msg"`
	Enabled     bool   `json:"enabled,omitempty"`
	EnableLabel string `json:"enableLabel,omitempty"`
	Close       string `json:"close"`
}

// pcvModalOpen emits the pcv dialog head + opens .modal-body (local twin of components.go modal,
// with the pcv-modal class and pcv-close as the scrim/✕ act).
func pcvModalOpen(title string) string {
	return `<div class=modal-scrim data-act=pcv-close></div>` +
		`<div class="modal pcv-modal" role=dialog><div class=modal-head><h3 class=modal-title>` +
		htmlEscape(title) + `</h3>` +
		`<button class=modal-x data-act=pcv-close aria-label=Close>✕</button></div>` +
		`<div class=modal-body>`
}

// pcvModalClose closes .modal-body, emits the footer and closes the dialog.
func pcvModalClose(footHTML string) string {
	return `</div><div class=modal-foot>` + footHTML + `</div></div>`
}

// moPCViewerHTMLOf is the pure viewer-shell renderer.
func moPCViewerHTMLOf(st moPCViewSt) string {
	body := `<div class=pcv-wrap>` +
		`<div id=pcv-stage class=pcv-stage><canvas id=pcv-canvas class=pcv-canvas></canvas></div>` +
		`<div class=pcv-transport>` +
		`<button id=pcv-play class="rp-btn rp-btn--go pcv-play">▶ ` + htmlEscape(st.PlayLabel) + `</button>` +
		`<input id=pcv-scrub class=slider-input type=range min=0 max=` + st.MaxFrame + ` step=1 value=0>` +
		`<span id=pcv-time class=pcv-time data-label="pcv-time"></span>` +
		`</div>` +
		`<div id=pcv-info class=pcv-info data-label="pcv-info"></div>` +
		`<div class=pcv-hint>` + htmlEscape(st.Hint) + `</div>` +
		`</div>`
	return pcvModalOpen(st.Title) + body + pcvModalClose(btn(st.Close, "outline", "pcv-close", ""))
}

// moPCGpuHTMLOf is the pure GPU-prompt renderer.
func moPCGpuHTMLOf(st moPCGpuSt) string {
	body := `<p class=pcv-gpu-msg>` + htmlEscape(st.Msg) + `</p>`
	foot := ""
	if !st.Enabled {
		foot = btn(st.EnableLabel, "go", "pcv-enablegpu", "")
	}
	foot += btn(st.Close, "outline", "pcv-close", "")
	return pcvModalOpen(st.Title) + body + pcvModalClose(foot)
}

// renderPCViewerModal bridges the viewer shell to Zig.
func renderPCViewerModal(st moPCViewSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPCViewerV2", wirePCView(st), zigui.RenderPCViewerV2,
			zigui.RenderPCViewer, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return moPCViewerHTMLOf(st)
}

// renderPCGpuModal bridges the GPU prompt to Zig.
func renderPCGpuModal(st moPCGpuSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPCGpuV2", wirePCGpu(st), zigui.RenderPCGpuV2,
			zigui.RenderPCGpu, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return moPCGpuHTMLOf(st)
}
