package webui

// Realtime editor preview: the mp player's <video> plays the fx'd, reframed
// result live (DaVinci-style). A 3-proc pipeline in the vfx worker child
// (ffmpeg decode -re with the animated crop expr → rave-mate-vfx --pipe →
// ffmpeg x264-ultrafast fragmented MP4) appends to a growing file; mediahttp
// /ms/ tails it into the shell __mst MSE runtime. Seek/param edits respawn the
// pipeline at the playhead (debounced); an empty chain on crop layout plays the
// source directly (CSS reframe - instant, no pipeline).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/mp4frag"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/vfx"
	"rave.page/mate/internal/videoedit"
)

// edvPrevDefaultH is the default preview render-height cap (the quality selector
// trades sharpness for realtime headroom on weaker machines).
const edvPrevDefaultH = 540

// edvLoopMaxSec caps the IN→OUT span the preview renders as ONE bounded segment the
// element then loops natively (seamless, no respawn). Longer cuts keep the open-ended
// feed: the whole span has to stay in the MSE buffer for the loop to be gapless.
const edvLoopMaxSec = 180

func init() {
	mpStreamCtl = func(u *UI, host, verb string, t float64) bool {
		if host != "editor" {
			return false
		}
		switch verb {
		case "seek":
			if !u.edvPrevNeeded() {
				return false // direct element handles it
			}
			u.edvPrevStart(t, !u.mpSnap("editor").vid.paused)
			return true
		case "stall":
			// live feed drained. During a handover that's expected (the old feed was
			// retired) - the fresh feed re-sources the element shortly; respawning here
			// would supersede the in-flight start and storm the pipeline.
			editor.mu.Lock()
			starting := editor.video.prevStarting
			editor.mu.Unlock()
			if starting {
				return true
			}
			if !u.edvPrevNeeded() {
				return false
			}
			u.edvPrevStart(t, !u.mpSnap("editor").vid.paused)
			return true
		case "play":
			editor.mu.Lock()
			dead := editor.video.prevCancel == nil
			editor.mu.Unlock()
			// loop from IN when resuming at/past OUT (live clock can't check element-side)
			mp := u.mpSnap("editor")
			if mp.outSec > 0 && t >= mp.outSec-0.5 {
				u.edvPrevStart(mp.inSec, true)
				return true
			}
			if dead {
				u.edvPrevStart(t, true)
				return true
			}
			u.edvMut(func(v *edvSt) { v.prevPaused = time.Time{} })
			return false // pipeline alive - default v.play() resumes the buffer
		case "pause":
			u.edvMut(func(v *edvSt) { v.prevPaused = time.Now() })
			return false
		case "stop":
			u.edvPrevStart(t, false) // respawn paused at IN
			u.mpMut("editor", func(v *mpSt) { v.vid.paused, v.vid.cur = true, t })
			u.mpPatchTransport(u.mpSnap("editor"))
			return true
		}
		return false
	}
	onExact("edv-prevres", func(u *UI, m actMsg) {
		h, err := strconv.Atoi(strings.TrimSpace(m.Val))
		if err != nil {
			return
		}
		u.edvMut(func(v *edvSt) { v.prevH = h })
		if u.svc.Cfg != nil {
			u.svc.Cfg.Features.Editor.PreviewH = h
			u.saveCfgBG("edv-prevres", nil, nil)
		}
		u.edvPatchInsp()
		u.edvPrevKick()
	})
	// preview box resized by its bottom-edge grip: the drag already applied the height
	// client-side, so Go only persists it (a re-render would rebuild the playing <video>).
	onExact("edv-vsize", func(u *UI, m actMsg) {
		h, err := strconv.Atoi(strings.TrimSpace(m.Val))
		if err != nil || h < 120 || u.svc.Cfg == nil {
			return
		}
		u.svc.Cfg.Features.Editor.PreviewViewH = h
		u.saveCfgBG("edv-vsize", nil, nil)
	})
	mpVidSurface = func(u *UI, host string) (string, string) {
		if host != "editor" || !u.edvSurfaceOn() || !u.edvPrevNeeded() {
			return "", ""
		}
		pw, ph := u.edvPrevDims()
		if pw <= 0 || ph <= 0 {
			return "", ""
		}
		return edvSurfaceID, fmt.Sprintf("%d/%d", pw, ph)
	}
	mpVidGrip = func(u *UI, host string) (string, string) {
		if host != "editor" {
			return "", ""
		}
		h := 0
		if u.svc.Cfg != nil {
			h = u.svc.Cfg.Features.Editor.PreviewViewH
		}
		if h <= 0 {
			return "edv-vsize", ""
		}
		return "edv-vsize", strconv.Itoa(h)
	}
}

// edvPrevH resolves the realtime render-height cap: session override, else the
// persisted preference, else the default.
func (u *UI) edvPrevH() int {
	editor.mu.Lock()
	h := editor.video.prevH
	editor.mu.Unlock()
	if h <= 0 && u.svc.Cfg != nil {
		h = u.svc.Cfg.Features.Editor.PreviewH
	}
	if h <= 0 {
		h = edvPrevDefaultH
	}
	return h
}

// edvPrevNeeded reports whether the preview needs the pipeline: any enabled
// resolvable effect, or the fit layout (its filled background can't be CSS'd).
func (u *UI) edvPrevNeeded() bool {
	srcW, srcH, _ := u.edvSrcDims()
	editor.mu.Lock()
	edvEnsure()
	proj := editor.video.proj
	plugins := editor.video.fxPlugins
	editor.mu.Unlock()
	if proj.Source == "" || !edvIsVideo(proj.Source) || srcW <= 0 {
		return false
	}
	cw, ch, _ := videoedit.CropSizeZoom(srcW, srcH, videoedit.AspectByKey(proj.Aspect), proj.Zoom)
	hasCrop := cw > 0 && (cw < srcW || ch < srcH)
	if proj.Layout == "fit" && hasCrop {
		return true
	}
	return len(edvResolveFx(proj.Effects, plugins)) > 0
}

// edvPrevDims is the preview picture's size: the reframed crop fitted under the render-height cap.
// Only its RATIO matters on the surface path (the hole is CSS-fitted to it and the producer renders
// at whatever rect that resolves to); the pipeline path renders at exactly these pixels.
func (u *UI) edvPrevDims() (int, int) {
	srcW, srcH, _ := u.edvSrcDims()
	if srcW <= 0 {
		return 0, 0
	}
	editor.mu.Lock()
	edvEnsure()
	proj := editor.video.proj
	editor.mu.Unlock()
	cw, ch, _ := videoedit.CropSizeZoom(srcW, srcH, videoedit.AspectByKey(proj.Aspect), proj.Zoom)
	if cw <= 0 || ch <= 0 {
		cw, ch = srcW, srcH
	}
	return edvFitDimsH(cw, ch, u.edvPrevH())
}

// edvPrevKick debounces a pipeline (re)start after chain/reframe edits; when the
// pipeline is no longer needed it falls back to the direct element.
func (u *UI) edvPrevKick() {
	editor.mu.Lock()
	edvEnsure()
	editor.video.prevWant++
	want := editor.video.prevWant
	editor.mu.Unlock()
	u.bg(func() {
		time.Sleep(450 * time.Millisecond)
		if u.stopped() {
			return
		}
		editor.mu.Lock()
		cur := editor.video.prevWant
		editor.mu.Unlock()
		if cur != want {
			return
		}
		mp := u.mpSnap("editor")
		if !u.edvPrevNeeded() {
			u.edvPrevStop()
			return
		}
		at := mp.vid.cur
		if mp.vid.strURL == "" { // entering stream mode: keep the direct element's position
			at = u.edvPlayhead()
		} else if mp.vid.strLoop { // stay a loop: re-render the whole span from IN
			at = mp.inSec
		}
		u.edvPrevStart(at, mp.vid.started && !mp.vid.paused)
	})
}

// edvSurfaceID is the [data-surface] the editor preview declares. One id, one producer: the
// picture is BREAK-before-make because two producers cannot share a surface's kernel objects.
const edvSurfaceID = "editor-preview"

// edvSurfaceOn reports whether this preview start goes to the native render surface
// (SDL_WEBVIEW_SURFACE_DESIGN P4). Every "no" answer here leaves edvPrevStart on the MSE path
// verbatim: opt-in flag off, composition hosting off, not the real window child (a headless mirror
// has no compositor), or a previous start proved no consumer ever attached (§5 fallback).
func (u *UI) edvSurfaceOn() bool {
	if u.svc.Cfg == nil || !u.svc.Cfg.Features.Editor.PreviewSurface || !u.svc.Cfg.Features.UI.VisualShellHosting() {
		return false
	}
	if _, isProc := u.shell.(*procShell); !isProc {
		return false
	}
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return !editor.video.surfFail
}

// edvPrevStart (re)spawns the realtime pipeline at source-second t. autoplay
// starts the fresh element as soon as the feed opens.
func (u *UI) edvPrevStart(t float64, autoplay bool) {
	t0 := time.Now() // click→pixel stopwatch: both paths log the same interval (P4 deliverable)
	srcW, srcH, _ := u.edvSrcDims()
	prevH := u.edvPrevH()
	editor.mu.Lock()
	edvEnsure()
	v := &editor.video
	proj := v.proj
	plugins := v.fxPlugins
	editor.mu.Unlock()
	if proj.Source == "" || !edvIsVideo(proj.Source) || srcW <= 0 || u.svc.Workers == nil {
		return
	}
	if t < 0 {
		t = 0
	}

	fx := edvResolveFx(proj.Effects, plugins)
	cw, ch, _ := videoedit.CropSizeZoom(srcW, srcH, videoedit.AspectByKey(proj.Aspect), proj.Zoom)
	hasCrop := cw > 0 && (cw < srcW || ch < srcH)
	fit := proj.Layout == "fit" && hasCrop
	vf := ""
	if hasCrop {
		vf = proj.CropFilter(srcW, srcH, t) // animated pan expr, keys shifted by the -ss seek
	} else {
		cw, ch = srcW, srcH
	}
	pw, ph := edvFitDimsH(cw, ch, prevH)
	post := ""
	if fit {
		post = edvBlurFilter(proj.BGBlur)
	}
	dir := edDataDir("videoeditor")
	if dir == "" {
		return
	}
	surf := u.edvSurfaceOn()

	// Make-before-break: the running pipeline + /ms/ stream stay ALIVE until the
	// fresh feed is ready and the element re-sourced (or this start dies). Killing
	// them up front drains the playing element mid-edit → stall → respawn storm.
	editor.mu.Lock()
	v.prevGen++
	gen := v.prevGen
	oldCancel, oldRelease, oldPath := v.prevCancel, v.prevRelease, v.prevPath
	v.prevCancel, v.prevRelease = nil, nil
	// The PICTURE producer is retired here, not at the handover: one surface id, one set of kernel
	// objects, so a second producer would collide on the ring generation instead of overlapping.
	// The audio half below keeps the make-before-break handover the element's transport depends on.
	oldSurf := v.prevSurf
	v.prevSurf = nil
	path := filepath.Join(dir, fmt.Sprintf("prev-%d.mp4", gen))
	// A generation counter restarts at 1 every launch, so the first start of a session can find LAST
	// session's prev-1.mp4 still on disk - and mp4frag.Parse then succeeds instantly on a file the
	// pipeline has not written yet, binding the element to stale content (measured: "picture on
	// screen in 9 ms", playing the previous session's cut).
	_ = os.Remove(path)
	v.prevPath = path
	v.prevStarting = true
	v.prevPaused = time.Time{}
	if !autoplay {
		v.prevPaused = time.Now()
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.prevCancel = cancel
	surfCtx, surfCancel := context.WithCancel(context.Background())
	if surf {
		v.prevSurf = surfCancel
	} else {
		surfCancel()
	}
	v.prevSurfOn = surf
	editor.mu.Unlock()
	if oldSurf != nil {
		oldSurf()
	}
	// The old feed keeps playing until the new one is live (make-before-break), so its ticks are
	// stale from here on: hold the requested position until the fresh feed binds.
	u.mpMut("editor", func(m *mpSt) { m.vid.strPend = true })

	var retireOnce sync.Once
	retire := func() { // exactly-once teardown of the superseded feed
		retireOnce.Do(func() {
			if oldCancel != nil {
				oldCancel()
			}
			if oldRelease != nil {
				oldRelease()
			}
		})
	}
	unstart := func() { // this start is settled; a newer gen owns the flag past us
		editor.mu.Lock()
		mine := editor.video.prevGen == gen
		if mine {
			editor.video.prevStarting = false
		}
		editor.mu.Unlock()
		if mine { // settled either way - never leave the playhead frozen on a dead start
			u.mpMut("editor", func(m *mpSt) { m.vid.strPend = false })
		}
	}

	// Loop segment: starting AT the IN marker with a short enough cut renders exactly
	// [IN,OUT] once; the element then loops that fully-buffered segment natively -
	// seamless, no respawn at the loop point, no pipeline running forever.
	mp := u.mpSnap("editor")
	span := 0.0
	if mp.outSec > mp.inSec && math.Abs(t-mp.inSec) < 0.25 {
		if s := mp.outSec - mp.inSec; s <= edvLoopMaxSec {
			span = s
		}
	}
	if surf {
		// No bounded-span native loop on the surface path: ffmpeg's -stream_loop and a bounded input
		// duration are mutually exclusive (verified by execution - `-ss X -t D -stream_loop -1` plays
		// the span ONCE), so the producer cannot wrap at OUT. A looping audio clock over a picture
		// that stopped at OUT is worse than the long-cut behaviour, which is what this falls back to:
		// the element pauses at OUT and Play restarts from IN.
		span = 0
	}

	done := make(chan struct{})
	chain := vfx.Chain{W: pw, H: ph, FPS: 30, Fx: fx}
	if surf {
		// Picture: decode → chain → (fit overlay) → shared texture. No encoder, no MSE.
		u.bg(func() {
			shown := false
			onEv := func(ev string, data json.RawMessage) { // first PRESENTED frame = picture on screen
				if shown || ev != "stats" || !strings.Contains(string(data), `"presentSeq"`) {
					return
				}
				var d struct {
					Pub struct {
						PresentSeq uint64 `json:"presentSeq"`
					} `json:"pub"`
				}
				if json.Unmarshal(data, &d) != nil || d.Pub.PresentSeq == 0 {
					return
				}
				shown = true
				u.log.Info("editor", fmt.Sprintf("preview picture on screen in %d ms (surface)",
					time.Since(t0).Milliseconds()), nil)
			}
			raw, err := u.svc.Workers.RunStream(surfCtx, "vfx", "vfx.surface", map[string]any{
				"input": proj.Source, "surface": edvSurfaceID, "t": t, "vf": vf,
				"fit": fit, "decodePost": post, "chain": chain,
			}, onEv)
			if surfCtx.Err() != nil {
				return
			}
			if err != nil {
				u.edvSurfaceFail(err, t, autoplay)
				return
			}
			// A settled rect change ends the job (the chain is sized at load): respawn where we are.
			if strings.Contains(string(raw), `"resize":true`) {
				u.edvPrevRespawn(gen)
			}
		})
		// Clock: audio-only fragmented MP4 through the SAME /ms/ tail + __mst runtime, so every
		// transport verb (play/pause/seek/stall/reap/strPend) is untouched by this phase.
		u.bg(func() {
			_, err := u.svc.Workers.RunStream(ctx, "vfx", "vfx.audio", map[string]any{
				"input": proj.Source, "output": path, "t": t, "d": span,
			}, nil)
			close(done)
			if err != nil && ctx.Err() == nil {
				u.logErr("vfx preview clock", err)
			}
		})
	} else {
		params := map[string]any{
			"input": proj.Source, "output": path, "t": t, "d": span, "vf": vf,
			"fit": fit, "decodePost": post, "chain": chain,
		}
		u.bg(func() { // the pipeline itself
			_, err := u.svc.Workers.RunStream(ctx, "vfx", "vfx.stream", params, nil)
			close(done)
			if err != nil && ctx.Err() == nil {
				u.logErr("vfx preview stream", err)
			}
		})
	}
	u.bg(func() { // wait for the init segment + first fragment, then re-source the element
		defer unstart()
		for { // no give-up: a heavy chain renders slower than realtime - WAIT, never cancel
			if u.stopped() || ctx.Err() != nil {
				retire() // this start died; the old feed goes with it
				return
			}
			if _, err := mp4frag.Parse(path); err == nil {
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
		editor.mu.Lock()
		stale := editor.video.prevGen != gen
		editor.mu.Unlock()
		if stale {
			retire()
			return
		}
		url, release := u.mpStreamURL(path, done)
		if url == "" {
			retire()
			return
		}
		editor.mu.Lock()
		editor.video.prevRelease = release
		editor.mu.Unlock()
		mime := transcode.StreamMime
		if surf { // audio-only feed: the picture is on the native surface, the element is the clock
			mime = transcode.StreamAudioMime
		}
		st := u.mpMut("editor", func(v *mpSt) {
			v.vid.strURL, v.vid.strMime, v.vid.strT0, v.vid.strAuto = url, mime, t, autoplay
			v.vid.strLoop = span > 0
			v.vid.strPend = false // fresh feed IS the clock now
			v.vid.cur, v.vid.paused, v.vid.started = t, !autoplay, true
		})
		if !surf { // surface path logs the same interval from its first PRESENTED frame instead
			u.log.Info("editor", fmt.Sprintf("preview picture on screen in %d ms (mse)",
				time.Since(t0).Milliseconds()), nil)
		}
		u.mpPatchVideo(st)
		u.mpPatchTransport(st)
		u.mpPushRealtime(st)
		retire()      // fresh feed is live - now drop the superseded pipeline + its token
		u.bg(func() { // sweep retired segment files once their handlers have drained
			time.Sleep(3 * time.Second)
			if oldPath != "" && oldPath != path {
				_ = os.Remove(oldPath)
			}
			if olds, err := filepath.Glob(filepath.Join(dir, "prev-*.mp4")); err == nil {
				for _, p := range olds {
					editor.mu.Lock()
					cur := editor.video.prevPath
					editor.mu.Unlock()
					if p != cur {
						_ = os.Remove(p) // locked (draining) files skip silently; next sweep retries
					}
				}
			}
		})
	})
}

// edvPrevStop kills the pipeline and returns the element to direct playback.
func (u *UI) edvPrevStop() {
	editor.mu.Lock()
	v := &editor.video
	v.prevGen++
	if v.prevSurf != nil {
		v.prevSurf()
		v.prevSurf = nil
	}
	v.prevSurfOn = false
	if v.prevCancel != nil {
		v.prevCancel()
		v.prevCancel = nil
	}
	if v.prevRelease != nil {
		v.prevRelease()
		v.prevRelease = nil
	}
	path := v.prevPath
	v.prevPath = ""
	editor.mu.Unlock()
	wasStream := false
	mp := u.mpMut("editor", func(v *mpSt) {
		wasStream = v.vid.strURL != ""
		cur := v.vid.cur
		v.vid = mpVid{cur: cur, paused: true, started: v.vid.started} // strPend + strSurf clear with it
	})
	if wasStream {
		u.mpPatchVideo(mp)
		u.mpPatchTransport(mp)
		u.mpVidEval("editor", fmt.Sprintf("try{v.currentTime=%.3f}catch(e){}", mp.vid.cur))
		u.mpPushRealtime(mp)
	}
	if path != "" {
		u.bg(func() { time.Sleep(2 * time.Second); _ = os.Remove(path) })
	}
}

// edvPrevReap cancels a pipeline that has been paused for a while (the buffered
// element keeps its picture; resume respawns at the playhead). Editor live tick.
func (u *UI) edvPrevReap() {
	editor.mu.Lock()
	v := &editor.video
	idle := (v.prevCancel != nil || v.prevSurf != nil) && !v.prevPaused.IsZero() && time.Since(v.prevPaused) > 30*time.Second
	if idle {
		if v.prevCancel != nil {
			v.prevCancel()
			v.prevCancel = nil
		}
		if v.prevSurf != nil { // the picture producer dies with the clock; the last frame stays up
			v.prevSurf()
			v.prevSurf = nil
		}
	}
	editor.mu.Unlock()
}

// edvSurfaceFail retires the surface path for this session and re-starts on MSE. §5 makes the
// fallback mandatory and AUTOMATIC: composition hosting can fail on a user's rig (R5), and a
// preview that silently shows nothing is the one outcome that must not ship.
func (u *UI) edvSurfaceFail(err error, t float64, autoplay bool) {
	editor.mu.Lock()
	first := !editor.video.surfFail
	editor.video.surfFail = true
	editor.mu.Unlock()
	if !first {
		return
	}
	u.logErr("editor preview surface unavailable - falling back to the MSE feed", err)
	u.bg(func() { u.edvPrevStart(t, autoplay) })
}

// edvPrevRespawn restarts the preview where it is now, if gen is still the live one. The surface
// producer ends its job on a settled rect change (a chain is sized at load), and this is the seam
// that rebuilds it - the same one a param edit uses.
func (u *UI) edvPrevRespawn(gen int) {
	editor.mu.Lock()
	stale := editor.video.prevGen != gen
	editor.mu.Unlock()
	if stale || u.stopped() {
		return
	}
	mp := u.mpSnap("editor")
	u.edvPrevStart(u.edvPlayhead(), mp.vid.started && !mp.vid.paused)
}

// edvFitDimsH scales w×h down to maxH high, keeping ratio, forced even.
func edvFitDimsH(w, h, maxH int) (int, int) {
	if h > maxH {
		w = w * maxH / h
		h = maxH
	}
	return w &^ 1, h &^ 1
}
