package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/jobs"
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

	panDrag bool
	panLive float64 // live window position while dragging

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
	onExact("edv-src", func(u *UI, m actMsg) { u.edvLoad(m.Val) }) // pick-file target
	onPrefix("edv-cap:", func(u *UI, m actMsg) {
		if s, ok := u.pubCapByID(m.arg("edv-cap:")); ok {
			u.edvLoad(s.Path)
		}
	})
	onExact("edv-aspect", func(u *UI, m actMsg) {
		u.edvMut(func(v *edvSt) { v.proj.Aspect = m.Val; v.proj.Normalize() })
		u.patchMain()
	})
	onExact("edv-pan", func(u *UI, m actMsg) { u.edvPan(m.Val) })
	onExact("edv-frame", func(u *UI, m actMsg) { u.edvFrameAtPlayhead() })
	onExact("edv-kf-add", func(u *UI, m actMsg) { u.edvKfAdd() })
	onPrefix("edv-kf-del:", func(u *UI, m actMsg) { u.edvKfDel(m.arg("edv-kf-del:")) })
	onPrefix("edv-kf-go:", func(u *UI, m actMsg) { u.edvKfGo(m.arg("edv-kf-go:")) })
	onExact("edv-kf-clear", func(u *UI, m actMsg) {
		u.edvMut(func(v *edvSt) { v.proj.PanKF = nil })
		u.patchMain()
	})
	onExact("edv-preset", func(u *UI, m actMsg) {
		u.edvMut(func(v *edvSt) { v.proj.PresetKey = m.Val; v.proj.Normalize() })
		u.patchMain()
	})
	onExact("edv-out", func(u *UI, m actMsg) {
		u.edvMut(func(v *edvSt) { v.proj.OutPath = strings.TrimSpace(m.Val) })
	})
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
	if kind == "video" {
		u.edvFrame(0)
	}
	u.patchMain()
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
		u.patchMain()
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
	cw, ch, axis := videoedit.CropSize(srcW, srcH, a)
	if axis == "" {
		editor.mu.Unlock()
		return
	}
	// pointer position → window CENTER → normalized free-axis position
	var pos float64
	if axis == "x" {
		half := float64(cw) / float64(srcW) / 2
		pos = (fx - half) / (1 - 2*half)
	} else {
		half := float64(ch) / float64(srcH) / 2
		pos = (fy - half) / (1 - 2*half)
	}
	pos = clamp01(pos)
	switch phase {
	case "down", "move":
		v.panDrag, v.panLive = true, pos
		editor.mu.Unlock()
		u.edvPatchFrame()
	case "up":
		if !v.panDrag {
			editor.mu.Unlock()
			return
		}
		v.panDrag = false
		if len(v.proj.PanKF) == 0 {
			v.proj.Pan = pos
		} else {
			edvKfUpsert(&v.proj, v.frameT, pos)
		}
		edvSave()
		editor.mu.Unlock()
		u.patchMain()
	default:
		editor.mu.Unlock()
	}
}

// edvKfUpsert replaces a keyframe within 0.25s of t, else inserts one.
func edvKfUpsert(p *videoedit.Project, t, x float64) {
	for i := range p.PanKF {
		if absF(p.PanKF[i].T-t) < 0.25 {
			p.PanKF[i].X = x
			return
		}
	}
	p.PanKF = append(p.PanKF, videoedit.PanKey{T: t, X: x})
	p.Normalize()
}

// edvPatchFrame re-renders only the reframe box (60 Hz drag path).
func (u *UI) edvPatchFrame() {
	st := u.edvFrameState()
	u.eval("window.__patch('edv-frame'," + jsQuote(edvFrameHTML(st)) + ")")
}

// ── keyframes ──

func (u *UI) edvKfAdd() {
	t := u.edvPlayhead()
	u.edvFrame(t)
	u.edvMut(func(v *edvSt) {
		pos := v.proj.Pan
		if len(v.proj.PanKF) > 0 {
			pos = v.panLive
		}
		edvKfUpsert(&v.proj, t, pos)
	})
	u.patchMain()
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
	u.patchMain()
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
	v.export = edvExport{running: true, stage: "prepare"}
	editor.mu.Unlock()

	params := map[string]any{
		"input": src, "output": out, "preset": preset,
		"trimStart": trimS, "trimEnd": trimE, "vf": crop,
	}
	u.patchMain()
	u.bg(func() { u.edvRunExport(out, params) })
}

func (u *UI) edvRunExport(out string, params map[string]any) {
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
	err := u.edvDispatch(params, onEvent)
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
	u.patchMain()
}

// edvDispatch runs the transcode via the shared hub (queue visibility) or the
// raw worker pool - mirror of mpExportOne without the mp coupling.
func (u *UI) edvDispatch(params map[string]any, onProgress func(string, json.RawMessage)) error {
	if u.svc.Hub != nil {
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
	_, err := u.svc.Workers.RunStream(ctx, "transcode", "transcode.run", params, onProgress)
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
