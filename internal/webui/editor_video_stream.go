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
	"fmt"
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
		}
		u.edvPrevStart(at, mp.vid.started && !mp.vid.paused)
	})
}

// edvPrevStart (re)spawns the realtime pipeline at source-second t. autoplay
// starts the fresh element as soon as the feed opens.
func (u *UI) edvPrevStart(t float64, autoplay bool) {
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

	// Make-before-break: the running pipeline + /ms/ stream stay ALIVE until the
	// fresh feed is ready and the element re-sourced (or this start dies). Killing
	// them up front drains the playing element mid-edit → stall → respawn storm.
	editor.mu.Lock()
	v.prevGen++
	gen := v.prevGen
	oldCancel, oldRelease, oldPath := v.prevCancel, v.prevRelease, v.prevPath
	v.prevCancel, v.prevRelease = nil, nil
	path := filepath.Join(dir, fmt.Sprintf("prev-%d.mp4", gen))
	v.prevPath = path
	v.prevStarting = true
	v.prevPaused = time.Time{}
	if !autoplay {
		v.prevPaused = time.Now()
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.prevCancel = cancel
	editor.mu.Unlock()

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
		if editor.video.prevGen == gen {
			editor.video.prevStarting = false
		}
		editor.mu.Unlock()
	}

	done := make(chan struct{})
	params := map[string]any{
		"input": proj.Source, "output": path, "t": t, "vf": vf,
		"fit": fit, "decodePost": post,
		"chain": vfx.Chain{W: pw, H: ph, FPS: 30, Fx: fx},
	}
	u.bg(func() { // the pipeline itself
		_, err := u.svc.Workers.RunStream(ctx, "vfx", "vfx.stream", params, nil)
		close(done)
		if err != nil && ctx.Err() == nil {
			u.logErr("vfx preview stream", err)
		}
	})
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
		mp := u.mpMut("editor", func(v *mpSt) {
			v.vid.strURL, v.vid.strMime, v.vid.strT0, v.vid.strAuto = url, transcode.StreamMime, t, autoplay
			v.vid.cur, v.vid.paused, v.vid.started = t, !autoplay, true
		})
		u.mpPatchVideo(mp)
		u.mpPatchTransport(mp)
		u.mpPushRealtime(mp)
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
		v.vid = mpVid{cur: cur, paused: true, started: v.vid.started}
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
	idle := v.prevCancel != nil && !v.prevPaused.IsZero() && time.Since(v.prevPaused) > 30*time.Second
	if idle {
		v.prevCancel()
		v.prevCancel = nil
	}
	editor.mu.Unlock()
}

// edvFitDimsH scales w×h down to maxH high, keeping ratio, forced even.
func edvFitDimsH(w, h, maxH int) (int, int) {
	if h > maxH {
		w = w * maxH / h
		h = maxH
	}
	return w &^ 1, h &^ 1
}
