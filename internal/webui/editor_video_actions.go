package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/vfx"
	"rave.page/mate/internal/videoedit"
)

// Editor tab video mode: cut (mp component, host "editor" - the same trim/
// waveform/silence spine Publish uses), vertical reframe (aspect + pan
// keyframes → ffmpeg crop expr) and platform export. Project persists in
// <dataDir>/visualeditor/videoproject.json. All actions are namespaced edv-.

// edvSt is the video-mode state (rides edState, guarded by editor.mu).
type edvSt struct {
	proj   videoedit.Project
	loaded bool // project JSON read (or defaulted) once

	frameT    float64 // source seconds of the preview frame
	framePath string  // extracted frame PNG ("" = none yet)
	frameBusy bool
	frameGen  int // drops stale async extract results

	panDrag  bool
	panLive  float64 // live free-axis window position while dragging
	panLive2 float64 // live cross-axis position (0..1; 0.5 = centered)

	rebound bool // one-shot: mp re-bind of the persisted source after restart

	fxPlugins  []vfx.Plugin // discovered effect plugins (scan once per session)
	fxScanned  bool
	fxScanning bool
	packBusy   bool   // Vidvox ISF pack download in flight
	fxPrev     string // fx preview PNG ("" = never rendered)
	fxPrevBusy bool
	fxPrevGen  int
	fxPrevWant int // live-preview debounce: bumped per change, render fires when it settles

	export edvExport
}

type edvExport struct {
	running bool
	pct     float64
	stage   string
	cancel  func()
	result  string
	err     string
}

// edvVideoExts marks sources the reframe/preview pipeline treats as video.
var edvVideoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".mov": true, ".webm": true, ".avi": true,
	".ts": true, ".m4v": true, ".flv": true,
}

func edvIsVideo(path string) bool {
	return edvVideoExts[strings.ToLower(filepath.Ext(path))]
}

func init() {
	onExact("edv-src", func(u *UI, m actMsg) { // pick-file target
		u.closeModal()
		u.edvLoad(m.Val)
	})
	onExact("edv-src-open", func(u *UI, m actMsg) { u.edvOpenSources() })
	onPrefix("edv-cap:", func(u *UI, m actMsg) {
		if s, ok := u.pubCapByID(m.arg("edv-cap:")); ok {
			u.closeModal()
			u.edvLoad(s.Path)
		}
	})
	onExact("edv-aspect", func(u *UI, m actMsg) {
		u.edvMut(func(v *edvSt) { v.proj.Aspect = m.Val; v.proj.Normalize() })
		u.edvPatchInsp()
		u.edvSyncPlayerVars()
		u.edvFxPrevKick()
	})
	onExact("edv-layout", func(u *UI, m actMsg) {
		u.edvMut(func(v *edvSt) { v.proj.Layout = m.Val; v.proj.Normalize() })
		u.edvPatchInsp()
		u.edvSyncPlayerVars()
		u.edvFxPrevKick()
	})
	onExact("edv-reframe-open", func(u *UI, m actMsg) { u.edvOpenReframe() })
	onExact("edv-bgblur", func(u *UI, m actMsg) {
		f, err := strconv.ParseFloat(strings.TrimSpace(m.Val), 64)
		if err != nil {
			return
		}
		u.edvMut(func(v *edvSt) { v.proj.BGBlur = clamp01(f) })
		u.edvFxPrevKick()
	})
	onExact("edv-zoomset", func(u *UI, m actMsg) { // inspector slider (self-labeled)
		f, err := strconv.ParseFloat(strings.TrimSpace(m.Val), 64)
		if err != nil {
			return
		}
		u.edvSetZoom(f, false)
	})
	onExact("edv-zoom", func(u *UI, m actMsg) { // wheel over the preview frame
		dir, _, ok := strings.Cut(m.Val, ":")
		if !ok {
			return
		}
		editor.mu.Lock()
		edvEnsure()
		z := editor.video.proj.Zoom
		editor.mu.Unlock()
		if z <= 0 {
			z = 1
		}
		if dir == "in" {
			z *= 1.15
		} else {
			z /= 1.15
		}
		u.edvSetZoom(z, true)
	})
	onExact("edv-pan", func(u *UI, m actMsg) { u.edvPan(m.Val) })
	onExact("edv-frame", func(u *UI, m actMsg) { u.edvFrameAtPlayhead() })
	onExact("edv-kf-add", func(u *UI, m actMsg) { u.edvKfAdd() })
	onPrefix("edv-kf-del:", func(u *UI, m actMsg) { u.edvKfDel(m.arg("edv-kf-del:")) })
	onPrefix("edv-kf-go:", func(u *UI, m actMsg) { u.edvKfGo(m.arg("edv-kf-go:")) })
	onExact("edv-kf-clear", func(u *UI, m actMsg) {
		u.edvMut(func(v *edvSt) { v.proj.PanKF = nil })
		u.edvPatchKfBox()
		u.edvPatchFrame()
	})
	onExact("edv-preset", func(u *UI, m actMsg) {
		u.edvMut(func(v *edvSt) { v.proj.PresetKey = m.Val; v.proj.Normalize() })
		u.edvPatchInsp()
	})
	onExact("edv-out", func(u *UI, m actMsg) {
		u.edvMut(func(v *edvSt) { v.proj.OutPath = strings.TrimSpace(m.Val) })
	})
	onExact("edv-fx-add", func(u *UI, m actMsg) { u.edvFxAdd(m.Val) })
	onPrefix("edv-fx-del:", func(u *UI, m actMsg) { u.edvFxEdit(m.arg("edv-fx-del:"), "del") })
	onPrefix("edv-fx-up:", func(u *UI, m actMsg) { u.edvFxEdit(m.arg("edv-fx-up:"), "up") })
	onPrefix("edv-fx-dn:", func(u *UI, m actMsg) { u.edvFxEdit(m.arg("edv-fx-dn:"), "dn") })
	onPrefix("edv-fx-tog:", func(u *UI, m actMsg) { u.edvFxEdit(m.arg("edv-fx-tog:"), "tog") })
	onPrefix("edv-fx-p:", func(u *UI, m actMsg) { u.edvFxParamSet(m.arg("edv-fx-p:"), m.Val) })
	onPrefix("edv-fx-c:", func(u *UI, m actMsg) { u.edvFxColorSet(m.arg("edv-fx-c:"), m.Val) })
	onExact("edv-fx-prev", func(u *UI, m actMsg) { u.edvFxPrevRender() })
	onPrefix("edv-fx-www:", func(u *UI, m actMsg) {
		switch m.arg("edv-fx-www:") {
		case "isf":
			_ = openURL("https://editor.isf.video/shaders")
		case "frei0r":
			_ = openURL("https://frei0r.dyne.org")
		}
	})
	onPrefix("edv-fx-dir:", func(u *UI, m actMsg) {
		d, err := config.Dir()
		if err != nil {
			return
		}
		switch m.arg("edv-fx-dir:") {
		case "isf":
			_ = openURL(vfx.ISFDir(d))
		case "frei0r":
			p := filepath.Join(d, "vfx", "frei0r")
			_ = os.MkdirAll(p, 0o755)
			_ = openURL(p)
		}
	})
	onExact("edv-fx-getpack", func(u *UI, m actMsg) { u.edvFetchPack() })
	onExact("edv-export", func(u *UI, m actMsg) { u.edvExport() })
	onExact("edv-excancel", func(u *UI, m actMsg) {
		editor.mu.Lock()
		c := editor.video.export.cancel
		editor.mu.Unlock()
		if c != nil {
			c()
		}
	})
}

// ── project persistence ──

// edvProjPath is the project file under the visualeditor data dir.
func edvProjPath() string { return edDataDir("videoproject.json") }

// edvEnsure loads the project once (caller holds editor.mu).
func edvEnsure() {
	if editor.video.loaded {
		return
	}
	editor.video.loaded = true
	editor.video.proj.Normalize()
	if data, err := os.ReadFile(edvProjPath()); err == nil {
		if p, err := videoedit.Unmarshal(data); err == nil {
			editor.video.proj = p
		}
	}
}

// edvSave persists the project (caller holds editor.mu).
func edvSave() {
	path := edvProjPath()
	if path == "" {
		return
	}
	if data, err := editor.video.proj.Marshal(); err == nil {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, data, 0o644)
	}
}

// edvMut mutates video state under the lock + persists the project.
func (u *UI) edvMut(fn func(*edvSt)) {
	edEnsure()
	editor.mu.Lock()
	edvEnsure()
	fn(&editor.video)
	edvSave()
	editor.mu.Unlock()
}

// ── source binding ──

// edvLoad binds a media file as the video-mode source: project + the mp
// component (host "editor", straight into edit mode).
func (u *UI) edvLoad(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		u.toast(i18n.T("editor.video.toast.sourceMissing"))
		return
	}
	kind, presetID := "audio", "copy-audio"
	if edvIsVideo(path) {
		kind, presetID = "video", "remux"
	}
	u.edvMut(func(v *edvSt) {
		v.proj.Source = path
		v.proj.PanKF = nil
		v.proj.Normalize()
		v.frameT, v.framePath, v.frameGen = 0, "", v.frameGen+1
		v.export = edvExport{}
	})
	if u.mpSnap("editor").monitorLoud {
		u.mpMonitorOff("editor")
	}
	u.mpMut("editor", func(t *mpSt) {
		t.reset()
		t.name = filepath.Base(path)
		t.media = []mpMedia{{path: path, kind: kind, size: fileSize(path),
			presetID: presetID, peaksLoading: true}}
		t.pinned, t.edit = true, true
	})
	u.mpKickAnalyses("editor")
	u.bg(func() { u.edvBindMarks(path) })
	if kind == "video" {
		u.edvFrame(0)
	}
	u.patchMain()
}

// edvBindMarks surfaces a source's set tracklist in the editor player when the path
// is a known capture: track markers (jump-to-track + wave ticks) and the snap-IN/OUT
// auto-trim hints - cuts land on track boundaries. Off-thread: the capture cache may
// still be cold (Publish tab never opened this session); poll it briefly, then bail.
func (u *UI) edvBindMarks(path string) {
	var s libdb.SetRecording
	var r recorder.Recording
	for tries := 0; ; tries++ {
		var ok, loaded bool
		s, r, ok, loaded = u.capByPath(path)
		if ok {
			break
		}
		if loaded || tries >= 40 || u.stopped() {
			return // not a capture (or cache never warmed) - plain source, no markers
		}
		time.Sleep(50 * time.Millisecond)
	}
	marks, first, lastEnd, lastFader := capTrackMeta(r, s.StartedAt, s.EndedAt)
	if len(marks) == 0 {
		return
	}
	stale := false
	u.mpMut("editor", func(t *mpSt) {
		if len(t.media) == 0 || t.media[0].path != path {
			stale = true // source changed while we resolved
			return
		}
		t.recID = s.RecordingID
		t.markers = marks
		t.firstTrackSec, t.lastTrackEndSec, t.lastFaderSec = first, lastEnd, lastFader
		t.media[0].startedAt = s.StartedAt
	})
	if !stale {
		// markers live in the mp component - patch its fragments; a full patch
		// would rebuild the <video> seconds after load (MSE re-init churn)
		t := u.mpSnap("editor")
		u.mpPatchWave(t)
		u.mpPatchTransport(t)
	}
}

// edvRebind re-binds the persisted project source into the mp component once
// per session (after a restart the project remembers the source but the player
// starts empty).
func (u *UI) edvRebind() {
	editor.mu.Lock()
	edvEnsure()
	src := editor.video.proj.Source
	done := editor.video.rebound
	editor.video.rebound = true
	editor.mu.Unlock()
	if done || src == "" || len(u.mpSnap("editor").media) > 0 {
		return
	}
	if _, err := os.Stat(src); err != nil {
		return
	}
	u.bg(func() { u.edvLoad(src) })
}

// edvOpenSources shows the source-picker modal: browse + recent captures.
func (u *UI) edvOpenSources() {
	editor.mu.Lock()
	edvEnsure()
	cur := editor.video.proj.Source
	editor.mu.Unlock()

	var b strings.Builder
	b.WriteString(btnRow(uiBtn{Label: i18n.T("editor.browse"), Variant: "outline", Act: "pick-file:edv-src"}.html()))
	b.WriteString(`<div class=edv-srclist>`)
	if caps, loaded := u.pubCapList(); !loaded {
		b.WriteString(hint("info", i18n.T("editor.video.noMedia")))
	} else if len(caps) > 0 {
		n := len(caps)
		if n > 40 {
			n = 40
		}
		for _, s := range caps[:n] {
			meta := i18n.T("editor.video.srcAudio")
			if edvIsVideo(s.Path) {
				meta = i18n.T("editor.video.srcVideo")
			}
			cls := "edv-srcrow"
			if s.Path == cur {
				cls = "edv-srcrow edv-srcrow-sel"
			}
			b.WriteString(`<button class="` + cls + `" data-act=` + attrQ("edv-cap:"+s.ID) + `>` +
				`<span class=edv-srcrow-name>` + htmlEscape(filepath.Base(s.Path)) + `</span>` +
				`<span class=edv-srcrow-meta>` + htmlEscape(meta) + `</span></button>`)
		}
	} else {
		b.WriteString(hint("info", i18n.T("editor.video.noSource")))
	}
	b.WriteString(`</div>`)
	u.openModal(modal(i18n.T("editor.video.sectionSource"), b.String(), `<span></span>`))
}

// edvSrcDims returns the probed source dimensions from the mp instance (0,0
// until the stream probe lands).
func (u *UI) edvSrcDims() (w, h int, dur float64) {
	t := u.mpSnap("editor")
	if len(t.media) == 0 {
		return 0, 0, 0
	}
	m := t.media[0]
	if m.src != nil {
		return m.src.Width, m.src.Height, m.src.DurationSec
	}
	return 0, 0, m.dur
}

// edvPlayhead returns the current edit playhead in source seconds.
func (u *UI) edvPlayhead() float64 {
	t := u.mpSnap("editor")
	if len(t.media) == 0 {
		return 0
	}
	if t.media[0].kind == "video" {
		return t.vid.cur
	}
	if tr := u.mpEng(&t); tr.loaded {
		return tr.cur
	}
	return 0
}

// ── preview frame ──

// edvFrame extracts the source frame at t into the visualeditor data dir
// (stable name per position - the /img/ cache can serve repeats).
func (u *UI) edvFrame(t float64) {
	edEnsure()
	editor.mu.Lock()
	edvEnsure()
	src := editor.video.proj.Source
	if src == "" || !edvIsVideo(src) || editor.video.frameBusy {
		editor.mu.Unlock()
		return
	}
	editor.video.frameBusy = true
	editor.video.frameGen++
	gen := editor.video.frameGen
	editor.mu.Unlock()

	dir := edDataDir("videoeditor")
	if dir == "" {
		return
	}
	key := crc32.ChecksumIEEE([]byte(fmt.Sprintf("%s|%.2f", src, t)))
	out := filepath.Join(dir, fmt.Sprintf("frame-%08x.png", key))
	u.bg(func() {
		var err error
		if _, statErr := os.Stat(out); statErr != nil { // cached extract wins
			err = u.edvFrameWorker(src, t, out)
		}
		editor.mu.Lock()
		if editor.video.frameGen == gen {
			editor.video.frameBusy = false
			if err == nil {
				editor.video.frameT, editor.video.framePath = t, out
			}
		}
		editor.mu.Unlock()
		if err != nil {
			u.logErr("videoedit frame", err)
			return
		}
		u.edvPatchModalFrame()
		u.edvFxPrevKick() // preview follows the new frame
	})
}

func (u *UI) edvFrameWorker(src string, t float64, out string) error {
	if u.svc.Workers == nil {
		return errors.New("worker pool unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_, err := u.svc.Workers.RunStream(ctx, "transcode", "transcode.frame", map[string]any{
		"input": src, "t": t, "output": out, "maxW": 960,
	}, nil)
	return err
}

func (u *UI) edvFrameAtPlayhead() { u.edvFrame(u.edvPlayhead()) }

// ── pan drag (reframe window) ──

// edvPan interprets the frame box's actpos stream: dragging slides the crop
// window along the free axis; release persists (static pan, or upserts the
// keyframe at the current frame time when keyframes exist).
func (u *UI) edvPan(val string) {
	phase, rest, ok := strings.Cut(val, ":")
	if !ok {
		return
	}
	parts := strings.Split(rest, ",")
	if len(parts) < 2 {
		return
	}
	fx, e1 := strconv.ParseFloat(parts[0], 64)
	fy, e2 := strconv.ParseFloat(parts[1], 64)
	if e1 != nil || e2 != nil {
		return
	}
	srcW, srcH, _ := u.edvSrcDims()
	editor.mu.Lock()
	edvEnsure()
	v := &editor.video
	a := videoedit.AspectByKey(v.proj.Aspect)
	cw, ch, axis := videoedit.CropSizeZoom(srcW, srcH, a, v.proj.Zoom)
	if cw == 0 || (srcW-cw < 2 && srcH-ch < 2) {
		editor.mu.Unlock()
		return
	}
	// pointer position → window CENTER → normalized per-axis position (an axis
	// without slack pins to center)
	posOf := func(f float64, c, s int) float64 {
		if s-c <= 0 {
			return 0.5
		}
		half := float64(c) / float64(s) / 2
		return clamp01((f - half) / (1 - 2*half))
	}
	posX, posY := posOf(fx, cw, srcW), posOf(fy, ch, srcH)
	prim := axis
	if prim == "" {
		prim = "x"
	}
	pos, pos2 := posX, posY
	if prim == "y" {
		pos, pos2 = posY, posX
	}
	switch phase {
	case "down", "move":
		v.panDrag, v.panLive, v.panLive2 = true, pos, pos2
		editor.mu.Unlock()
		u.edvPatchFrame()
	case "up":
		if !v.panDrag {
			editor.mu.Unlock()
			return
		}
		v.panDrag = false
		if len(v.proj.PanKF) == 0 {
			v.proj.Pan, v.proj.Pan2 = pos, pos2-0.5
		} else {
			edvKfUpsert(&v.proj, v.frameT, pos, pos2-0.5)
		}
		edvSave()
		editor.mu.Unlock()
		u.edvPatchKfBox()
		u.edvPatchFrame()
		u.edvFxPrevKick()
	default:
		editor.mu.Unlock()
	}
}

// edvKfUpsert replaces a keyframe within 0.25s of t, else inserts one.
// y is the cross-axis center offset (-0.5..0.5).
func edvKfUpsert(p *videoedit.Project, t, x, y float64) {
	for i := range p.PanKF {
		if absF(p.PanKF[i].T-t) < 0.25 {
			p.PanKF[i].X, p.PanKF[i].Y = x, y
			return
		}
	}
	p.PanKF = append(p.PanKF, videoedit.PanKey{T: t, X: x, Y: y})
	p.Normalize()
}

// edvPatchFrame re-renders only the crop overlay (60 Hz drag path) - patching
// #edv-frame itself would replace the actpos element and drop the capture.
func (u *UI) edvPatchFrame() {
	st := u.edvFrameState()
	u.eval("window.__patch('edv-fovl'," + jsQuote(edvFrameOvlHTML(st)) + ")")
	u.edvSyncPlayerVars()
}

// edvPatchInsp re-renders the inspector fragment only - the player <video> in the
// viewer pane is never touched (a rebuilt element kills playback + MSE state).
func (u *UI) edvPatchInsp() {
	u.eval("window.__patch('edv-insp'," + jsQuote(u.renderEditorVideoInsp()) + ")")
}

// edvPatchModalFrame re-renders the reframe modal's frame block (busy → image).
func (u *UI) edvPatchModalFrame() {
	u.eval("window.__patch('edv-frame'," + jsQuote(u.edvFrameFragHTML(u.edvFrameState())) + ")")
	u.edvSyncPlayerVars()
}

// edvPatchKfBox re-renders the reframe modal's keyframe row + chips.
func (u *UI) edvPatchKfBox() {
	u.eval("window.__patch('edv-kfbox'," + jsQuote(u.edvKfBoxFragHTML(u.edvReframeState())) + ")")
}

// edvOpenReframe opens the reframe/area-select modal, grabbing a playhead frame.
func (u *UI) edvOpenReframe() {
	u.edvFrameAtPlayhead()
	st := u.edvReframeState()
	u.openModal(modal(st.Title, u.edvReframeBodyHTML(st), ""))
}

// edvSyncPlayerVars pushes the live reframe-preview class + vars at the
// timeline player (drag / zoom / playback follow) without a full patch.
func (u *UI) edvSyncPlayerVars() {
	srcW, srcH, _ := u.edvSrcDims()
	editor.mu.Lock()
	edvEnsure()
	v := &editor.video
	proj := v.proj
	if proj.Source == "" || !edvIsVideo(proj.Source) {
		editor.mu.Unlock()
		return
	}
	if v.panDrag { // live drag position wins over keyframes
		proj.Pan, proj.Pan2 = v.panLive, v.panLive2-0.5
		proj.PanKF = nil
	}
	editor.mu.Unlock()
	if srcW <= 0 || srcH <= 0 {
		return
	}
	cls, vars := u.edvPlayerReframe(proj, srcW, srcH)
	u.eval("(function(){var p=document.querySelector('.edv-pane-view .edv-player');if(!p)return;" +
		"p.className='edv-player" + cls + "';p.style.cssText=" + jsQuote(vars) + ";})()")
}

// edvSetZoom clamps + persists the crop zoom. fromWheel patches the overlay +
// the inspector slider row; the slider's own events patch only the overlay
// (replacing the input mid-drag would break the slider).
func (u *UI) edvSetZoom(z float64, fromWheel bool) {
	u.edvMut(func(v *edvSt) {
		v.proj.Zoom = math.Round(z*100) / 100 // 2 decimals: clean label, stable steps
		v.proj.Normalize()
	})
	u.edvPatchFrame()
	if fromWheel {
		u.edvPatchZoomRow()
	}
	u.edvFxPrevKick()
}

// edvPatchZoomRow refreshes the inspector zoom slider (wheel path).
func (u *UI) edvPatchZoomRow() {
	editor.mu.Lock()
	edvEnsure()
	z := editor.video.proj.Zoom
	editor.mu.Unlock()
	sl := newSlider(i18n.T("editor.video.zoomLabel"), "edv-zoomset", 1, 4, 0.05, z, "")
	u.eval("window.__patch('edv-zoomrow'," + jsQuote(sl.html()) + ")")
}

// ── keyframes ──

func (u *UI) edvKfAdd() {
	t := u.edvPlayhead()
	u.edvFrame(t)
	u.edvMut(func(v *edvSt) {
		pos, pos2 := v.proj.PanAt(t), v.proj.Pan2At(t)
		if v.panDrag {
			pos, pos2 = v.panLive, v.panLive2
		}
		edvKfUpsert(&v.proj, t, pos, pos2-0.5)
	})
	u.edvPatchKfBox()
}

func (u *UI) edvKfDel(idxStr string) {
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return
	}
	u.edvMut(func(v *edvSt) {
		if idx >= 0 && idx < len(v.proj.PanKF) {
			v.proj.PanKF = append(v.proj.PanKF[:idx], v.proj.PanKF[idx+1:]...)
		}
	})
	u.edvPatchKfBox()
	u.edvPatchFrame()
}

func (u *UI) edvKfGo(idxStr string) {
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return
	}
	editor.mu.Lock()
	edvEnsure()
	var t float64 = -1
	if idx >= 0 && idx < len(editor.video.proj.PanKF) {
		t = editor.video.proj.PanKF[idx].T
	}
	editor.mu.Unlock()
	if t < 0 {
		return
	}
	u.mpSeekAxis("editor", t)
	u.edvFrame(t)
}

// ── effect chain (frei0r/ISF via the vfx worker) ──

// edvFxScan discovers plugins once per session (kicked from the state builder).
func (u *UI) edvFxScan() {
	if u.svc.Workers == nil {
		return
	}
	edEnsure()
	editor.mu.Lock()
	edvEnsure()
	if editor.video.fxScanned || editor.video.fxScanning {
		editor.mu.Unlock()
		return
	}
	editor.video.fxScanning = true
	editor.mu.Unlock()
	u.bg(func() {
		var plugins []vfx.Plugin
		if d, err := config.Dir(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			raw, err := u.svc.Workers.RunStream(ctx, "vfx", "vfx.list",
				map[string]any{"dirs": vfx.PluginDirs(d)}, nil)
			if err != nil {
				u.logErr("vfx list", err)
			} else {
				var out struct {
					Plugins []vfx.Plugin `json:"plugins"`
				}
				if json.Unmarshal(raw, &out) == nil {
					plugins = out.Plugins
				}
			}
		}
		editor.mu.Lock()
		editor.video.fxScanning, editor.video.fxScanned = false, true
		editor.video.fxPlugins = plugins
		editor.mu.Unlock()
		if len(plugins) > 0 {
			u.edvPatchInsp()
		}
	})
}

// edvPackBusy reports whether the Vidvox pack download is running.
func edvPackBusy() bool {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	edvEnsure()
	return editor.video.packBusy
}

// edvFetchPack downloads the MIT-licensed Vidvox ISF pack (200+ shaders) into
// <config>/vfx/isf/vidvox and rescans the plugin list. One run at a time.
func (u *UI) edvFetchPack() {
	editor.mu.Lock()
	edvEnsure()
	busy := editor.video.packBusy
	editor.video.packBusy = true
	editor.mu.Unlock()
	if busy {
		return
	}
	u.toast(i18n.T("editor.video.toast.packStart"))
	u.edvPatchInsp()
	u.bg(func() {
		d, err := config.Dir()
		var n int
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			n, err = vfx.FetchVidvoxPack(ctx, vfx.ISFDir(d))
		}
		editor.mu.Lock()
		editor.video.packBusy = false
		if err == nil {
			editor.video.fxScanned = false // picks up the new pack dir on re-render
		}
		editor.mu.Unlock()
		if u.stopped() {
			return
		}
		if err != nil {
			u.log.Error("editor", "isf pack download failed", map[string]any{"err": err.Error()})
			u.toast(i18n.T("editor.video.toast.packFail"))
		} else {
			u.toast(i18n.T("editor.video.toast.packDone", i18n.A{"n": strconv.Itoa(n)}))
		}
		u.edvPatchInsp()
	})
}

// edvFxAdd appends a discovered plugin (by index in fxPlugins) to the chain.
func (u *UI) edvFxAdd(idxStr string) {
	idx, err := strconv.Atoi(strings.TrimSpace(idxStr))
	if err != nil {
		return
	}
	u.edvMut(func(v *edvSt) {
		if idx < 0 || idx >= len(v.fxPlugins) {
			return
		}
		p := v.fxPlugins[idx]
		v.proj.Effects = append(v.proj.Effects, videoedit.EffectInst{
			Kind: p.Kind, Ref: filepath.Base(p.Ref),
		})
	})
	u.edvPatchInsp()
	u.edvFxPrevKick()
}

// edvFxEdit handles del/up/dn/tog on a chain row.
func (u *UI) edvFxEdit(idxStr, op string) {
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return
	}
	u.edvMut(func(v *edvSt) {
		fx := v.proj.Effects
		if idx < 0 || idx >= len(fx) {
			return
		}
		switch op {
		case "del":
			v.proj.Effects = append(fx[:idx], fx[idx+1:]...)
		case "up":
			if idx > 0 {
				fx[idx-1], fx[idx] = fx[idx], fx[idx-1]
			}
		case "dn":
			if idx+1 < len(fx) {
				fx[idx+1], fx[idx] = fx[idx], fx[idx+1]
			}
		case "tog":
			fx[idx].Off = !fx[idx].Off
		}
	})
	u.edvPatchInsp()
	u.edvFxPrevKick()
}

// edvFxParamSet stores a param value; arg is "<idx>:<name>", val a number or
// a switch's "true"/"false".
func (u *UI) edvFxParamSet(arg, val string) {
	idxStr, name, ok := strings.Cut(arg, ":")
	if !ok || name == "" {
		return
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return
	}
	var f float64
	switch strings.TrimSpace(val) {
	case "true":
		f = 1
	case "false":
		f = 0
	default:
		f, err = strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			return
		}
	}
	u.edvMut(func(v *edvSt) {
		if idx < 0 || idx >= len(v.proj.Effects) {
			return
		}
		e := &v.proj.Effects[idx]
		if e.Params == nil {
			e.Params = map[string]float64{}
		}
		e.Params[name] = clamp01(f)
	})
	u.edvFxPrevKick()
}

// edvFxCh resolves a dotted channel value (override else listing default).
func edvFxCh(params map[string]float64, name, comp string, def float64) float64 {
	if v, ok := params[name+"."+comp]; ok {
		return v
	}
	return def
}

// edvFxColorSet applies a hex color to a color param's r/g/b channels.
func (u *UI) edvFxColorSet(arg, val string) {
	idxStr, name, ok := strings.Cut(arg, ":")
	if !ok || name == "" {
		return
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return
	}
	c, ok := edParseHex(val)
	if !ok {
		return
	}
	u.edvMut(func(v *edvSt) {
		if idx < 0 || idx >= len(v.proj.Effects) {
			return
		}
		e := &v.proj.Effects[idx]
		if e.Params == nil {
			e.Params = map[string]float64{}
		}
		e.Params[name+".r"] = float64(c.R) / 255
		e.Params[name+".g"] = float64(c.G) / 255
		e.Params[name+".b"] = float64(c.B) / 255
	})
	u.edvPatchInsp() // refresh the swatch
	u.edvFxPrevKick()
}

// edvResolveFx maps enabled chain entries to loaded plugins (base-name match;
// missing/disabled entries drop out).
func edvResolveFx(effects []videoedit.EffectInst, plugins []vfx.Plugin) []vfx.Fx {
	out := []vfx.Fx{} // never nil: a nil slice marshals "fx":null, which the vfx child rejects (BadSpec)
	for _, e := range effects {
		if e.Off {
			continue
		}
		for i := range plugins {
			if filepath.Base(plugins[i].Ref) == e.Ref {
				out = append(out, vfx.Fx{Kind: e.Kind, Ref: plugins[i].Ref, Params: e.Params})
				break
			}
		}
	}
	return out
}

// edvBlurFilter maps the 0..1 background-blur knob to a gblur filter ("" = off).
func edvBlurFilter(v float64) string {
	sigma := clamp01(v) * 40
	if sigma < 0.5 {
		return ""
	}
	return fmt.Sprintf("gblur=sigma=%.1f", sigma)
}

// edvFitDims scales w×h down to maxW, keeping ratio, forced even.
func edvFitDims(w, h, maxW int) (int, int) {
	if w > maxW {
		h = h * maxW / w
		w = maxW
	}
	w, h = w&^1, h&^1
	if w < 2 {
		w = 2
	}
	if h < 2 {
		h = 2
	}
	return w, h
}

// edvFxPrevKick live-updates the fx preview: debounced (a slider drag fires many
// acts - render once it settles), dropped when a newer change or an in-flight
// render exists (completion re-kicks if the state moved meanwhile).
func (u *UI) edvFxPrevKick() {
	editor.mu.Lock()
	edvEnsure()
	editor.video.fxPrevWant++
	want := editor.video.fxPrevWant
	editor.mu.Unlock()
	u.bg(func() {
		time.Sleep(450 * time.Millisecond)
		if u.stopped() {
			return
		}
		editor.mu.Lock()
		cur, busy := editor.video.fxPrevWant, editor.video.fxPrevBusy
		editor.mu.Unlock()
		if cur != want || busy {
			return
		}
		u.edvFxPrevRender()
	})
}

// edvFxPrevRender runs the current preview frame through the chain (cropped at
// the frame's time, so the result matches the export).
func (u *UI) edvFxPrevRender() {
	srcW, srcH, _ := u.edvSrcDims()
	t := u.mpSnap("editor")

	editor.mu.Lock()
	edvEnsure()
	v := &editor.video
	src, frameT, proj, plugins := v.proj.Source, v.frameT, v.proj, v.fxPlugins
	busy := v.fxPrevBusy
	editor.mu.Unlock()
	if src == "" || !edvIsVideo(src) || srcW <= 0 || busy {
		return
	}
	fx := edvResolveFx(proj.Effects, plugins)
	cw, ch, axis := videoedit.CropSizeZoom(srcW, srcH, videoedit.AspectByKey(proj.Aspect), proj.Zoom)
	hasCrop := cw > 0 && (cw < srcW || ch < srcH)
	fit := proj.Layout == "fit" && hasCrop
	if len(fx) == 0 && !fit {
		return
	}
	vf := ""
	if hasCrop {
		pos, pos2 := proj.PanAt(frameT), proj.Pan2At(frameT)
		posX, posY := pos, pos2
		if axis == "y" {
			posX, posY = pos2, pos
		}
		x := int(float64(srcW-cw)*posX + 0.5)
		y := int(float64(srcH-ch)*posY + 0.5)
		vf = fmt.Sprintf("crop=%d:%d:%d:%d", cw, ch, x, y)
	} else {
		cw, ch = srcW, srcH
	}
	pw, ph := edvFitDims(cw, ch, 480)
	fxT := frameT - t.inSec
	if fxT < 0 {
		fxT = 0
	}
	chain := vfx.Chain{W: pw, H: ph, FPS: 30, Fx: fx}
	post := ""
	if fit {
		post = edvBlurFilter(proj.BGBlur)
	}
	dir := edDataDir("videoeditor")
	if dir == "" {
		return
	}
	chainRaw, _ := json.Marshal(chain)
	key := crc32.ChecksumIEEE([]byte(fmt.Sprintf("%s|%.2f|%s|%v|%s", src, frameT, chainRaw, fit, post)))
	out := filepath.Join(dir, fmt.Sprintf("fxprev-%08x.png", key))

	editor.mu.Lock()
	v.fxPrevBusy = true
	v.fxPrevGen++
	gen := v.fxPrevGen
	wantStart := v.fxPrevWant
	editor.mu.Unlock()
	u.edvPatchFxPrev()

	u.bg(func() {
		var err error
		if _, statErr := os.Stat(out); statErr != nil { // cached render wins
			if u.svc.Workers == nil {
				err = errors.New("worker pool unavailable")
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()
				_, err = u.svc.Workers.RunStream(ctx, "vfx", "vfx.preview", map[string]any{
					"input": src, "t": frameT, "fxT": fxT, "vf": vf,
					"chain": chain, "output": out, "fit": fit, "decodePost": post,
				}, nil)
			}
		}
		editor.mu.Lock()
		stale := false
		if editor.video.fxPrevGen == gen {
			editor.video.fxPrevBusy = false
			if err == nil {
				editor.video.fxPrev = out
			}
			stale = editor.video.fxPrevWant != wantStart // changed mid-render
		}
		editor.mu.Unlock()
		if err != nil {
			u.logErr("vfx preview", err)
			u.edvPatchFxPrev()
			return
		}
		// targeted patch only: patchMain would rebuild the player <video> (MSE re-init churn)
		u.edvPatchFxPrev()
		if stale && !u.stopped() {
			u.edvFxPrevKick()
		}
	})
}

// edvPatchFxPrev re-renders only the fx preview box.
func (u *UI) edvPatchFxPrev() {
	st := u.edvFxPrevState()
	u.eval("window.__patch('edv-fxprev'," + jsQuote(edvFxPrevHTML(st)) + ")")
}

// ── export ──

// edvOutDefault derives the default export path beside the source. The suffix
// keeps the name outside caprecover's capture pattern (never re-swept).
func edvOutDefault(src, presetKey string) string {
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	dir := filepath.Dir(src)
	out := filepath.Join(dir, base+"-"+presetKey+".mp4")
	for n := 2; ; n++ {
		if _, err := os.Stat(out); err != nil {
			return out
		}
		out = filepath.Join(dir, fmt.Sprintf("%s-%s-%d.mp4", base, presetKey, n))
	}
}

func (u *UI) edvExport() {
	srcW, srcH, _ := u.edvSrcDims()
	t := u.mpSnap("editor")

	editor.mu.Lock()
	edvEnsure()
	v := &editor.video
	src := v.proj.Source
	if src == "" || v.export.running {
		editor.mu.Unlock()
		return
	}
	if edvIsVideo(src) && (srcW == 0 || srcH == 0) {
		editor.mu.Unlock()
		u.toast(i18n.T("editor.video.toast.probing"))
		return
	}
	trimS := 0.0
	if t.inSec > 0 {
		trimS = t.inSec
	}
	trimE := 0.0
	if t.outSec > 0 {
		trimE = t.outSec
	}
	ep := videoedit.ExportPresetByKey(v.proj.PresetKey)
	preset := ep.Preset()
	crop := v.proj.CropFilter(srcW, srcH, trimS)
	out := strings.TrimSpace(v.proj.OutPath)
	if out == "" {
		out = edvOutDefault(src, ep.Key)
	}
	fx := edvResolveFx(v.proj.Effects, v.fxPlugins)
	aspect, layout, bgBlur, zoom := v.proj.Aspect, v.proj.Layout, v.proj.BGBlur, v.proj.Zoom
	v.export = edvExport{running: true, stage: "prepare"}
	editor.mu.Unlock()

	params := map[string]any{
		"input": src, "output": out, "preset": preset,
		"trimStart": trimS, "trimEnd": trimE, "vf": crop,
	}
	typ, method := "transcode", "transcode.run"
	cwz, chz, _ := videoedit.CropSizeZoom(srcW, srcH, videoedit.AspectByKey(aspect), zoom)
	hasCrop := cwz > 0 && (cwz < srcW || chz < srcH)
	fit := layout == "fit" && hasCrop // fit needs a real reframe to fill
	if (len(fx) > 0 || fit) && edvIsVideo(src) {
		// route through the effects pipeline: chain runs at the export target size
		cw, ch := cwz, chz
		if cw == 0 {
			cw, ch = srcW&^1, srcH&^1
		}
		tw, th := preset.Width, preset.Height
		if tw <= 0 || th <= 0 {
			tw, th = cw, ch
		}
		fps := 30.0
		if len(t.media) > 0 && t.media[0].src != nil && t.media[0].src.FPS > 0 {
			fps = t.media[0].src.FPS
		}
		params["chain"] = vfx.Chain{W: tw, H: th, FPS: fps, Fx: fx}
		if fit {
			params["fit"] = true
			if post := edvBlurFilter(bgBlur); post != "" {
				params["decodePost"] = post
			}
		}
		typ, method = "vfx", "vfx.run"
	}
	u.edvPatchInsp()
	u.bg(func() { u.edvRunExport(out, typ, method, params) })
}

func (u *UI) edvRunExport(out, typ, method string, params map[string]any) {
	upd := func(fn func(*edvExport)) {
		editor.mu.Lock()
		fn(&editor.video.export)
		editor.mu.Unlock()
		u.edvPatchExport()
	}
	onEvent := func(event string, data json.RawMessage) {
		switch event {
		case "stage":
			var s struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(data, &s) == nil && s.Name != "" {
				upd(func(e *edvExport) { e.stage = s.Name })
			}
		case "progress":
			var p struct {
				Percent float64 `json:"percent"`
			}
			if json.Unmarshal(data, &p) == nil {
				upd(func(e *edvExport) { e.pct = p.Percent })
			}
		}
	}
	err := u.edvDispatch(typ, method, params, onEvent)
	editor.mu.Lock()
	e := &editor.video.export
	e.running, e.cancel = false, nil
	if err != nil {
		e.err = err.Error()
		_ = os.Remove(out) // never leave a partial file behind
	} else {
		e.err, e.result = "", out
	}
	editor.mu.Unlock()
	if err != nil {
		u.toast(i18n.T("editor.video.toast.exportFailed") + err.Error())
	} else {
		u.toast(i18n.T("editor.video.toast.exported", i18n.A{"path": out}))
	}
	u.edvPatchInsp()
}

// edvDispatch runs the export via the shared hub (queue visibility; plain
// transcodes only - the hub is bound to transcode.run) or the raw worker pool.
func (u *UI) edvDispatch(typ, method string, params map[string]any, onProgress func(string, json.RawMessage)) error {
	if u.svc.Hub != nil && typ == "transcode" && method == "transcode.run" {
		done := make(chan error, 1)
		jid := fmt.Sprintf("edvcut-%d", time.Now().UnixNano())
		u.svc.Hub.Start(jid, params, onProgress, func(r jobs.EndResult) {
			switch {
			case r.Canceled:
				done <- errors.New("canceled")
			case !r.OK:
				done <- errors.New(r.Error)
			default:
				done <- nil
			}
		})
		editor.mu.Lock()
		editor.video.export.cancel = func() { u.svc.Hub.Cancel(jid) }
		editor.mu.Unlock()
		return <-done
	}
	if u.svc.Workers == nil {
		return errors.New("worker pool unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	editor.mu.Lock()
	editor.video.export.cancel = cancel
	editor.mu.Unlock()
	_, err := u.svc.Workers.RunStream(ctx, typ, method, params, onProgress)
	if ctx.Err() != nil {
		return errors.New("canceled")
	}
	return err
}

// edvPatchExport re-renders only the export block.
func (u *UI) edvPatchExport() {
	st := u.edvExportState()
	u.eval("window.__patch('edv-export'," + jsQuote(edvExportHTML(st)) + ")")
}
