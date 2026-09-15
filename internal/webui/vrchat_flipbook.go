package webui

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/flipbook"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/videoedit"
)

// Animated-emoji flipbook creator (VRChat Profile tab, #vrc-emotes). Once a source clip is
// picked the card is a visual creator, not a form: the mp video player + draggable in/out trim
// (host "flipbook"), a square crop tool, a filmstrip of the tiled frames and a looping animated
// preview - all before Generate. State-driven: controls post fb-set:* and the card re-renders
// from u.fb, so a re-render never drops typed input. Rendering: render_vrchat.go.

// fbSt is the per-UI flipbook creator state (guarded by u.fbMu).
type fbSt struct {
	source   string  // picked source clip ("" = empty state)
	name     string  // emoji name (drives the output filename)
	frames   int     // tier: 4|16|64 (0 = default 16)
	fps      float64 // playback fps (0 = default 20)
	pingpong bool
	cropOn   bool

	srcW, srcH int     // probed source pixels (0 until the probe lands)
	dur        float64 // probed duration s
	srcGen     int     // bumped per source load: drops a stale async probe result

	// square crop (own videoedit.Project, Aspect locked 1x1 - NOT the editor's global proj)
	proj      videoedit.Project
	frameT    float64 // source seconds of the extracted crop frame
	framePath string  // extracted frame PNG ("" = none yet)
	frameBusy bool
	frameGen  int     // drops a stale async frame extract
	panDrag   bool    // crop pan drag in flight
	panLive   float64 // live free-axis window position while dragging
	panLive2  float64 // live cross-axis position (0..1; 0.5 = centered)
}

const (
	fbDefaultFrames = 16
	fbDefaultFPS    = 20
)

// fbEnsure folds defaults into a zero-value state (called under fbMu).
func (v *fbSt) ensure() {
	if v.frames == 0 {
		v.frames = fbDefaultFrames
	}
	if v.fps == 0 {
		v.fps = fbDefaultFPS
	}
}

// fbSnap returns a copy of the creator state (defaults folded).
func (u *UI) fbSnap() fbSt {
	u.fbMu.Lock()
	defer u.fbMu.Unlock()
	u.fb.ensure()
	return u.fb
}

// fbMut mutates the creator state under the lock and returns the new snapshot.
func (u *UI) fbMut(fn func(*fbSt)) fbSt {
	u.fbMu.Lock()
	defer u.fbMu.Unlock()
	u.fb.ensure()
	fn(&u.fb)
	return u.fb
}

func init() {
	// Source Browse target (pickSelfPatch: no patchMain - we self-patch #vrc-emotes).
	onExact("vrc-emote-source", func(u *UI, m actMsg) { u.fbSetSource(m.Val) })
	// Output-folder Browse target: persist FlipbookDir, then re-render the card footer.
	onExact("vrc-emote-outdir", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VRChat
		f.FlipbookDir = strings.TrimSpace(m.Val)
		u.saveCfg()
		u.fbPatchCard()
	})
	onExact("fb-set:name", func(u *UI, m actMsg) { u.fbMut(func(v *fbSt) { v.name = m.Val }) })
	onExact("fb-set:frames", func(u *UI, m actMsg) {
		n, err := strconv.Atoi(strings.TrimSpace(m.Val))
		if err != nil {
			return
		}
		if _, terr := flipbook.TierFor(n); terr != nil {
			return
		}
		u.fbMut(func(v *fbSt) { v.frames = n })
		u.fbPatchBody()
		u.fbPatchKept()
	})
	onExact("fb-set:fps", func(u *UI, m actMsg) {
		f, err := strconv.ParseFloat(strings.TrimSpace(m.Val), 64)
		if err != nil || f <= 0 || f > 120 {
			return
		}
		u.fbMut(func(v *fbSt) { v.fps = f })
		u.fbPatchKept()
	})
	onExact("fb-set:pingpong", func(u *UI, m actMsg) {
		u.fbMut(func(v *fbSt) { v.pingpong = m.Val == "true" })
		u.fbPatchKept()
	})
	onExact("fb-set:crop", func(u *UI, m actMsg) {
		fb := u.fbMut(func(v *fbSt) { v.cropOn = m.Val == "true" })
		u.fbPatchBody()
		if fb.cropOn && fb.framePath == "" { // first crop-on: extract the frame to drag over
			u.fbFrame(u.fbInPoint())
		}
	})
	onExact("fb-pan", func(u *UI, m actMsg) { u.fbPan(m.Val) })
	onExact("fb-zoom", func(u *UI, m actMsg) { u.fbZoom(m.Val) })
	onExact("fb-generate", func(u *UI, _ actMsg) { u.vrcEmoteGen() })
}

// fbSetSource binds a picked clip: store it, load the mp "flipbook" host into edit mode (video +
// draggable trim), probe the source dimensions, then re-render the whole card fragment.
func (u *UI) fbSetSource(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		u.toast(i18n.T("vrchat.toast.sourceMissing"))
		return
	}
	fb := u.fbMut(func(v *fbSt) {
		v.source = path
		v.srcW, v.srcH, v.dur = 0, 0, 0
		v.srcGen++
		v.frameT, v.framePath, v.frameGen = 0, "", v.frameGen+1
		v.proj = videoedit.Project{Aspect: "1x1"} // crop locked to a square; own proj, NOT editor's
		v.proj.Normalize()
	})
	if edvIsVideo(path) {
		u.mpMut("flipbook", func(t *mpSt) {
			t.reset()
			t.name = filepath.Base(path)
			t.media = []mpMedia{{path: path, kind: "video", size: fileSize(path),
				presetID: "remux", peaksLoading: true}}
			t.pinned, t.edit = true, true
		})
		u.mpKickAnalyses("flipbook")
	}
	u.fbProbe(path, fb.srcGen)
	u.fbPatchCard()
}

// fbProbe resolves source width/height/duration off-thread (store-cached; same probe the video
// editor uses), then re-renders the card so downstream tools have real pixels.
func (u *UI) fbProbe(path string, gen int) {
	if u.svc.Workers == nil {
		return
	}
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		raw, err := u.probeStreams(ctx, path)
		if err != nil {
			return
		}
		si, ok := transcode.ParseProbe(raw)
		if !ok {
			return
		}
		changed := false
		u.fbMut(func(v *fbSt) {
			if v.srcGen != gen || v.source != path {
				return // superseded by a newer source load
			}
			v.srcW, v.srcH = si.Width, si.Height
			if si.DurationSec > 0 {
				v.dur = si.DurationSec
			}
			changed = true
		})
		if changed {
			u.fbFrame(u.fbInPoint()) // dims known: extract the crop frame at the in-point
			u.fbPatchCard()
		}
	})
}

// fbInPoint returns the current trim in-point in source seconds (0 = clip start).
func (u *UI) fbInPoint() float64 {
	if s := u.mpSnap("flipbook").inSec; s > 0 {
		return s
	}
	return 0
}

// fbKeptLine renders the exact spec of the sprite the current controls produce (P8: text carries
// the values the visual surfaces show): frame count, tier grid, per-frame px, fps, loop length.
func (u *UI) fbKeptLine(fb fbSt) string {
	loop := 0.0
	if fb.fps > 0 {
		loop = float64(fb.frames) / fb.fps
	}
	grid, res := 0, 0
	if t, err := flipbook.TierFor(fb.frames); err == nil {
		grid, res = t.Grid, t.FrameRes
	}
	return i18n.T("vrchat.emotes.keptLine", i18n.A{
		"frames": strconv.Itoa(fb.frames),
		"grid":   fmt.Sprintf("%d×%d", grid, grid),
		"res":    strconv.Itoa(res),
		"fps":    trimNum(fb.fps),
		"loop":   fmt.Sprintf("%.1f", loop),
	})
}

// vrcEmotesState resolves the creator card from u.fb + config (+ the mp player markup).
func (u *UI) vrcEmotesState() vrcEmotesSt {
	f := &u.svc.Cfg.Features.VRChat
	fb := u.fbSnap()
	opts := make([]vrcFrameOptSt, 0, 3)
	for _, t := range flipbook.Tiers() {
		opts = append(opts, vrcFrameOptSt{Frames: t.Frames, Grid: t.Grid, Res: t.FrameRes, Sel: t.Frames == fb.frames})
	}
	st := vrcEmotesSt{
		Hint:         i18n.T("vrchat.emotes.hint"),
		HasSource:    fb.source != "",
		SourceLabel:  i18n.T("vrchat.emotes.field.source"),
		Source:       fb.source,
		Browse:       i18n.T("common.browse"),
		EmptyHint:    i18n.T("vrchat.emotes.empty"),
		NameLabel:    i18n.T("vrchat.emotes.field.name"),
		Name:         fb.name,
		FramesLabel:  i18n.T("vrchat.emotes.field.frames"),
		FrameOpts:    opts,
		FPSLabel:     i18n.T("vrchat.emotes.field.fps"),
		FPS:          trimNum(fb.fps),
		PingPong:     i18n.T("vrchat.emotes.pingpong"),
		PingPongOn:   fb.pingpong,
		Crop:         i18n.T("vrchat.emotes.crop"),
		CropOn:       fb.cropOn,
		Generate:     i18n.T("vrchat.emotes.generate"),
		OutDir:       f.ResolvedFlipbookDir(),
		OpenFolder:   i18n.T("vrchat.action.openOutputFolder"),
		PreviewLabel: i18n.T("vrchat.emotes.preview"),
	}
	if st.HasSource {
		st.Player = u.mpHTML("flipbook")
		st.KeptLine = u.fbKeptLine(fb)
		st.Frame = u.fbFrameState(fb)
	}
	return st
}

// fbFrameState resolves the square crop tool (#fb-frame; also the drag fragment). Mirrors
// edvFrameState but flipbook-scoped (own 1x1 proj, no keyframes).
func (u *UI) fbFrameState(fb fbSt) edvFrameSt {
	st := edvFrameSt{}
	if !fb.cropOn || fb.source == "" || !edvIsVideo(fb.source) || fb.srcW <= 0 || fb.srcH <= 0 {
		return st
	}
	st.Show = true
	st.AW, st.AH = strconv.Itoa(fb.srcW), strconv.Itoa(fb.srcH)
	if fb.framePath != "" {
		st.ImgURL = u.imgURL(fb.framePath, 960)
	} else if fb.frameBusy {
		st.Busy = i18n.T("vrchat.emotes.extracting")
	} else {
		st.Busy = i18n.T("vrchat.emotes.noFrame")
	}
	cw, ch, axis := videoedit.CropSizeZoom(fb.srcW, fb.srcH, videoedit.AspectByKey("1x1"), fb.proj.Zoom)
	if cw == 0 || (fb.srcW-cw < 2 && fb.srcH-ch < 2) {
		return st // square source at zoom 1: whole frame IS the crop, no slack
	}
	pos, pos2 := clamp01(fb.proj.Pan), clamp01(0.5+fb.proj.Pan2)
	if fb.panDrag {
		pos, pos2 = fb.panLive, fb.panLive2
	}
	posX, posY := pos, pos2
	if axis == "y" {
		posX, posY = pos2, pos
	}
	st.HasCrop = true
	st.CropW, st.CropH = trimPct(float64(cw)/float64(fb.srcW)*100), trimPct(float64(ch)/float64(fb.srcH)*100)
	st.CropL = trimPct(float64(fb.srcW-cw) / float64(fb.srcW) * 100 * posX)
	st.CropT = trimPct(float64(fb.srcH-ch) / float64(fb.srcH) * 100 * posY)
	return st
}

// ── fragment patches (never rebuild the player <video> on a control change) ──

// fbPatchCard re-renders the whole #vrc-emotes fragment (source change: player is (re)bound).
func (u *UI) fbPatchCard() {
	u.eval("window.__patch('vrc-emotes'," + jsQuote(u.vrcEmotesFragHTML()) + ")")
}

// fbPatchBody re-renders #fb-body only (controls + primary + footer) - the player wrap is a sibling.
func (u *UI) fbPatchBody() {
	st := u.vrcEmotesState()
	u.eval("window.__patch('fb-body'," + jsQuote(fbBodyHTML(st)) + ")")
}

// fbPatchKept re-renders the #fb-keptline spec text.
func (u *UI) fbPatchKept() {
	u.eval("window.__patch('fb-keptline'," + jsQuote(htmlEscape(u.fbKeptLine(u.fbSnap()))) + ")")
}

// fbPatchFrame re-renders the whole crop tool (#fb-frame) - used on frame-image arrival / zoom.
func (u *UI) fbPatchFrame() {
	u.eval("window.__patch('fb-frame'," + jsQuote(fbFrameHTML(u.fbFrameState(u.fbSnap()))) + ")")
}

// fbPatchOvl re-renders only the crop overlay (#fb-fovl) - the 60 Hz pan-drag path. Patching
// #fb-frame mid-drag would replace the actpos box and drop the pointer capture.
func (u *UI) fbPatchOvl() {
	u.eval("window.__patch('fb-fovl'," + jsQuote(fbFrameOvlHTML(u.fbFrameState(u.fbSnap()))) + ")")
}

// ── square crop (own 1x1 videoedit.Project; thin clone of the editor's pan/zoom, no keyframes) ──

// fbPan interprets the crop box's actpos stream: dragging slides the 1x1 window over the frame;
// release persists the static pan. Mirrors edvPan, flipbook-scoped.
func (u *UI) fbPan(val string) {
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
	fb := u.fbSnap()
	cw, ch, axis := videoedit.CropSizeZoom(fb.srcW, fb.srcH, videoedit.AspectByKey("1x1"), fb.proj.Zoom)
	if cw == 0 || (fb.srcW-cw < 2 && fb.srcH-ch < 2) {
		return
	}
	// pointer position → window CENTER → normalized per-axis position (an axis without slack pins to center)
	posOf := func(f float64, c, s int) float64 {
		if s-c <= 0 {
			return 0.5
		}
		half := float64(c) / float64(s) / 2
		return clamp01((f - half) / (1 - 2*half))
	}
	posX, posY := posOf(fx, cw, fb.srcW), posOf(fy, ch, fb.srcH)
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
		u.fbMut(func(v *fbSt) { v.panDrag, v.panLive, v.panLive2 = true, pos, pos2 })
		u.fbPatchOvl()
	case "up":
		committed := false
		u.fbMut(func(v *fbSt) {
			if !v.panDrag {
				return
			}
			v.panDrag = false
			v.proj.Pan, v.proj.Pan2 = pos, pos2-0.5
			committed = true
		})
		if committed {
			u.fbPatchOvl()
			u.fbKickPreview() // crop moved → refresh the animated preview (phase 3)
		}
	}
}

// fbZoom handles a wheel step over the crop box (punch-in ≥1).
func (u *UI) fbZoom(val string) {
	dir, _, ok := strings.Cut(val, ":")
	if !ok {
		return
	}
	z := u.fbSnap().proj.Zoom
	if z <= 0 {
		z = 1
	}
	if dir == "in" {
		z *= 1.15
	} else {
		z /= 1.15
	}
	if z < videoedit.ZoomMin {
		z = videoedit.ZoomMin
	}
	if z > videoedit.ZoomMax {
		z = videoedit.ZoomMax
	}
	u.fbMut(func(v *fbSt) { v.proj.Zoom = z })
	u.fbPatchOvl()
	u.fbKickPreview()
}

// fbDataDir is the on-disk cache dir for extracted crop frames + preview sheets.
func fbDataDir() string {
	p, err := config.DataPath("flipbook")
	if err != nil {
		return ""
	}
	return p
}

// fbFrame extracts the source frame at t into the flipbook cache dir (deterministic name per
// src|t so the /img/ cache serves repeats). Bounded: the /img/ token cache evicts (mediahttp.go);
// the PNGs are content-stable per position and reused via os.Stat, not re-extracted. Mirrors edvFrame.
func (u *UI) fbFrame(t float64) {
	fb := u.fbSnap()
	if fb.source == "" || !edvIsVideo(fb.source) || fb.frameBusy {
		return
	}
	src := fb.source
	var gen int
	u.fbMut(func(v *fbSt) { v.frameBusy = true; v.frameGen++; gen = v.frameGen })
	dir := fbDataDir()
	if dir == "" {
		u.fbMut(func(v *fbSt) { v.frameBusy = false })
		return
	}
	key := crc32.ChecksumIEEE([]byte(fmt.Sprintf("%s|%.2f", src, t)))
	out := filepath.Join(dir, fmt.Sprintf("frame-%08x.png", key))
	u.bg(func() {
		var err error
		if _, statErr := os.Stat(out); statErr != nil { // cached extract wins
			err = u.fbFrameWorker(src, t, out)
		}
		done := false
		u.fbMut(func(v *fbSt) {
			if v.frameGen != gen {
				return // superseded by a newer extract/source
			}
			v.frameBusy = false
			if err == nil {
				v.frameT, v.framePath, done = t, out, true
			}
		})
		if err != nil {
			u.logErr("flipbook frame", err)
			return
		}
		if done {
			u.fbPatchFrame()
			u.fbKickPreview() // preview follows the new frame (phase 3)
		}
	})
}

func (u *UI) fbFrameWorker(src string, t float64, out string) error {
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

// fbCropRect converts the 1x1 crop window + pan into a source-pixel flipbook.Rect (nil = no crop).
func fbCropRect(fb fbSt) *flipbook.Rect {
	cw, ch, axis := videoedit.CropSizeZoom(fb.srcW, fb.srcH, videoedit.AspectByKey("1x1"), fb.proj.Zoom)
	if cw == 0 || (fb.srcW-cw < 2 && fb.srcH-ch < 2) {
		return nil // square source at zoom 1: nothing to crop
	}
	pos, pos2 := clamp01(fb.proj.Pan), clamp01(0.5+fb.proj.Pan2)
	posX, posY := pos, pos2
	if axis == "y" {
		posX, posY = pos2, pos
	}
	return &flipbook.Rect{
		X: int(float64(fb.srcW-cw) * posX),
		Y: int(float64(fb.srcH-ch) * posY),
		W: cw, H: ch,
	}
}

// fbKickPreview refreshes the animated preview sheet (debounced). Filled in phase 3; a no-op here.
func (u *UI) fbKickPreview() {}

// ── generation ──

func (u *UI) vrcEmoteGen() {
	f := &u.svc.Cfg.Features.VRChat
	fb := u.fbSnap()
	if fb.source == "" {
		u.toast(i18n.T("vrchat.toast.pickSource"))
		return
	}
	t := u.mpSnap("flipbook")
	trimStart := t.inSec
	if trimStart < 0 {
		trimStart = 0
	}
	o := flipbook.Options{
		Input: fb.source, OutName: fb.name, Frames: fb.frames, FPS: fb.fps,
		TrimStart: trimStart, TrimEnd: t.outSec, // outSec < 0 = to end (bounded by frame count)
		PingPong: fb.pingpong, OutDir: f.ResolvedFlipbookDir(),
	}
	if fb.cropOn {
		o.Crop = fbCropRect(fb)
	}
	if err := o.Validate(); err != nil {
		u.toast(err.Error())
		return
	}
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		u.toast(i18n.T("vrchat.toast.ffmpegNotFound"))
		u.eval("window.__patch('vrc-emote-result'," + jsQuote(`<div class="vrc-note over">`+i18n.T("vrchat.emotes.result.ffmpegMissing")+`</div>`) + ")")
		return
	}
	u.eval("window.__patch('vrc-emote-result'," + jsQuote(`<div class="vrc-note vrc-busy">`+i18n.T("vrchat.emotes.result.generating")+`</div>`) + ")")
	u.bg(func() {
		out, genErr := flipbook.Generate(ffmpeg, o)
		if genErr != nil {
			u.eval("window.__patch('vrc-emote-result'," + jsQuote(`<div class="vrc-note over">`+i18n.T("vrchat.emotes.result.failed")+`: `+htmlEscape(genErr.Error())+`</div>`) + ")")
			return
		}
		u.eval("window.__patch('vrc-emote-result'," + jsQuote(u.fbResultHTML(out)) + ")")
		u.toast(i18n.T("vrchat.toast.spriteGenerated"))
	})
}

// fbResultHTML renders the post-Generate result block (patched into #vrc-emote-result).
func (u *UI) fbResultHTML(out string) string {
	return `<div class=vrc-result>` +
		`<img class=vrc-sheet loading=lazy src="` + u.imgURL(out, 512) + `" alt="">` +
		`<div class=vrc-result-body>` +
		`<div class=vrc-note><b>` + htmlEscape(i18n.T("vrchat.emotes.result.savedTitle")) + `</b></div>` +
		`<div class=vrc-path>` + htmlEscape(out) + `</div>` +
		`<div class=btn-row>` +
		btn(i18n.T("vrchat.emotes.result.copyPath"), "ghost", "copy", out) +
		btn(i18n.T("vrchat.emotes.result.openFolder"), "ghost", "open-url", filepath.Dir(out)) +
		btn(i18n.T("vrchat.emotes.result.upload"), "ghost", "open-url", flipbook.EmojiUploadURL) +
		`</div>` +
		`<div class=vrc-note>` + htmlEscape(i18n.T("vrchat.emotes.result.savedHint")) + `</div>` +
		`</div></div>`
}
