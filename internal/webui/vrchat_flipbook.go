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

	// animated preview + filmstrip (both derive from ONE low-res preview sheet)
	prevURL   string   // preview sheet URL (mpMediaURL - PNG, alpha preserved) ("" = none yet)
	prevGrid  int      // the preview sheet's grid (2|4|8)
	prevN     int      // frames baked into the preview sheet (4|16|64)
	prevFPS   float64  // fps the preview sheet was built at
	prevBusy  bool     // a preview build is in flight (debounced)
	prevGen   int      // debounce/stale generation
	prevCache []fbPrev // ≤ fbPreviewCacheCap sheets, oldest first (evict + delete dir)
}

// fbPrev is one cached preview sheet: crc32-of-options key → sheet path (its dir is deleted on evict).
type fbPrev struct{ key, path string }

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
	// A committed trim edit on the "flipbook" mp host refreshes the animated preview.
	mpTrimDone = func(u *UI, host string) {
		if host == "flipbook" {
			u.fbKickPreview()
		}
	}
	// Silent emoji source: no peaks/loudness workers, no wave chips/captions, muted element.
	mpSkipAudio = func(u *UI, host string) bool { return host == "flipbook" }
	// First-frame poster (no black-on-load) + eager metadata (real duration, no autoplay).
	mpVidPoster = func(u *UI, host string) (poster, preload string) {
		if host != "flipbook" {
			return "", ""
		}
		fb := u.fbSnap()
		if fb.framePath != "" {
			return u.imgURL(fb.framePath, 960), "metadata"
		}
		if fb.source != "" && edvIsVideo(fb.source) {
			u.fbFrame(u.fbInPoint()) // extract the poster frame; it re-patches #mp-flipbook-vid on arrival
		}
		return "", "metadata"
	}
	// Minimal transport for the flipbook: Play/Stop + clock, no seek slider/volume/open-externally.
	mpTpCompose = func(u *UI, t mpSt) (string, bool) {
		if t.host != "flipbook" {
			return "", false
		}
		return u.fbTransportHTML(t), true
	}
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
		u.fbKickPreview()
	})
	onExact("fb-set:fps", func(u *UI, m actMsg) {
		f, err := strconv.ParseFloat(strings.TrimSpace(m.Val), 64)
		if err != nil || f <= 0 || f > 120 {
			return
		}
		u.fbMut(func(v *fbSt) { v.fps = f })
		u.fbPatchKept()
		u.fbKickPreview()
	})
	onExact("fb-set:pingpong", func(u *UI, m actMsg) {
		u.fbMut(func(v *fbSt) { v.pingpong = m.Val == "true" })
		u.fbPatchKept()
		u.fbKickPreview()
	})
	onExact("fb-set:crop", func(u *UI, m actMsg) {
		fb := u.fbMut(func(v *fbSt) { v.cropOn = m.Val == "true" })
		u.fbPatchBody()
		if fb.cropOn && fb.framePath == "" { // first crop-on: extract the frame to drag over
			u.fbFrame(u.fbInPoint())
		}
		u.fbKickPreview() // crop on/off changes the tiled frames
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
		v.prevURL, v.prevGrid, v.prevN, v.prevBusy, v.prevGen = "", 0, 0, false, v.prevGen+1
	})
	if edvIsVideo(path) {
		u.mpMut("flipbook", func(t *mpSt) {
			t.reset()
			t.name = filepath.Base(path)
			// dur seeds the axis so the in/out lanes map to real seconds; fbProbe updates it when the
			// probe lands (0 here on a fresh pick). No peaksLoading: mpSkipAudio runs no peaks worker.
			t.media = []mpMedia{{path: path, kind: "video", size: fileSize(path),
				dur: fb.dur, presetID: "remux"}}
			t.pinned, t.edit = true, true
		})
		u.mpKickAnalyses("flipbook") // src probe only (mpSkipAudio drops peaks + loudness)
	}
	u.fbProbe(path, fb.srcGen)
	u.fbKickPreview()
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
		if changed && si.DurationSec > 0 {
			// seed the player axis with the real duration (peaks are skipped for silent clips, so
			// this probe is the ONLY dur source) - the in/out lanes now map to real seconds.
			u.mpMut("flipbook", func(t *mpSt) {
				for i := range t.media {
					t.media[i].dur = si.DurationSec
				}
			})
		}
		if changed {
			if u.fbSnap().cropOn { // dims known: extract the crop frame (only if the tool is showing)
				u.fbFrame(u.fbInPoint())
			}
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

// fbPlayerHTML is the flipbook's slim player composition: it reuses mp's SUB-fragments (video,
// the shared wavebox with draggable in/out lanes, a minimal transport, the trim readout) with the
// SAME ids mp's self-patching targets, but drops the editor chrome (typed IN/OUT + Set/Auto-trim,
// the ENCODE/EXPORT block, zoom row, volume, loudness/encode chips) - none of which fits an emoji
// sheet. Rides through the card as the RAW Player field. host is always "flipbook".
func (u *UI) fbPlayerHTML(t mpSt) string {
	host := t.host
	inner := mpInnerSt{
		Host:    host,
		Edit:    true, // the in/out lanes + handle triangles need edit mode
		Wave:    u.mpWaveState(t),
		LaneIn:  i18n.T("player.label.dragSetIn"),
		LaneMid: i18n.T("player.label.clickSeekPan"),
		LaneOut: i18n.T("player.label.dragSetOut"),
	}
	var b strings.Builder
	b.WriteString(`<div id=mp-` + host + `-root class="mplayer fb-player">`)
	b.WriteString(`<div id=mp-` + host + `-vid>` + mpVidHTMLOf(u.mpVidState(t)) + `</div>`)
	b.WriteString(mpWaveboxHTML(host, inner)) // shared with mpInnerHTMLOf
	b.WriteString(`<div id=mp-` + host + `-tp>` + u.fbTransportHTML(t) + `</div>`)
	b.WriteString(`<div id=mp-` + host + `-ro>` + mpROHTMLOf(mpROState(t)) + `</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// fbTransportHTML is the flipbook's minimal transport (patched into #mp-flipbook-tp by the mp
// transport machinery via mpTpCompose): Play + Stop + the current/total clock. The timeline lane
// handles seeking, so there is no seek slider; a silent clip needs no volume or open-externally.
func (u *UI) fbTransportHTML(t mpSt) string {
	tr := u.mpEng(&t)
	playLbl, playVar := "▶ "+i18n.T("player.play"), "go"
	switch {
	case tr.loaded && tr.playing:
		playLbl, playVar = "⏸ "+i18n.T("player.pause"), "outline"
	case tr.loaded && tr.paused:
		playLbl = "▶ " + i18n.T("player.resume")
	}
	play := uiBtn{Label: playLbl, Variant: playVar, Act: "mp-play:" + t.host}
	stop := uiBtn{Label: "⏹", Variant: "outline", Act: "mp-stop:" + t.host}
	tx := u.mpTimeText(t)
	return `<div class="mp-tp fb-tp">` + play.html() + stop.html() +
		`<span class="mp-time" id=mp-` + t.host + `-time data-label=` + attrQ("player time") +
		` data-value=` + attrQ(tx) + `>` + htmlEscape(tx) + `</span></div>`
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
		st.Player = u.fbPlayerHTML(u.mpSnap("flipbook"))
		st.KeptLine = u.fbKeptLine(fb)
		st.Frame = u.fbFrameState(fb)
		if fb.prevURL != "" {
			st.AnimURL, st.AnimGrid, st.AnimN = fb.prevURL, fb.prevGrid, fb.prevN
			if fb.prevFPS > 0 {
				st.AnimDur = trimNum(float64(fb.prevN)/fb.prevFPS) + "s"
			}
			cells, more := fbStripCells(fb.prevN)
			g := fb.prevGrid
			for _, cell := range cells {
				px, py := "0", "0"
				if g > 1 {
					px = trimPct(float64(cell%g) / float64(g-1) * 100)
					py = trimPct(float64(cell/g) / float64(g-1) * 100)
				}
				st.StripCells = append(st.StripCells, vrcStripCellSt{PosX: px, PosY: py})
			}
			st.StripMore = more
		}
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
			// the frame is now the video poster: re-render #mp-flipbook-vid unless the element is
			// actively playing (a mid-play swap would restart it; muted/paused/idle is safe).
			if vs := u.mpSnap("flipbook").vid; vs.err == "" && (!vs.started || vs.paused) {
				u.mpPatchVideo(u.mpSnap("flipbook"))
			}
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

// ── animated preview + filmstrip (one low-res sheet drives both) ──

const (
	fbPreviewSheet    = 512 // preview sheet edge px (fast to build; alpha kept)
	fbPreviewCacheCap = 8   // ≤ 8 cached preview sheets (option tuple), evict oldest
	fbStripMax        = 16  // filmstrip thumbnails cap; more ⇒ "+N"
)

// fbKickPreview rebuilds the animated preview sheet after a ~400 ms debounce (source / in-out /
// crop / tier / fps / ping-pong all funnel here). A generation counter drops superseded builds.
func (u *UI) fbKickPreview() {
	if u.fbSnap().source == "" {
		return
	}
	var gen int
	u.fbMut(func(v *fbSt) { v.prevGen++; gen = v.prevGen })
	u.bg(func() {
		time.Sleep(400 * time.Millisecond)
		if u.fbSnap().prevGen != gen {
			return // a newer change superseded this one
		}
		u.fbBuildPreview(gen)
	})
}

// fbBuildPreview generates (or serves from cache) the 512² preview sheet for the current options
// and applies it to #fb-anim + #fb-strip. Runs on a bg goroutine (post-debounce).
func (u *UI) fbBuildPreview(gen int) {
	fb := u.fbSnap()
	if fb.source == "" || !edvIsVideo(fb.source) {
		return
	}
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return // no ffmpeg ⇒ no preview; the static text spec (kept line) stands
	}
	t := u.mpSnap("flipbook")
	trimStart := t.inSec
	if trimStart < 0 {
		trimStart = 0
	}
	o := flipbook.Options{
		Input: fb.source, OutName: "preview", Frames: fb.frames, FPS: fb.fps,
		TrimStart: trimStart, TrimEnd: t.outSec, PingPong: fb.pingpong,
		SheetSize: fbPreviewSheet,
	}
	if fb.cropOn {
		o.Crop = fbCropRect(fb)
	}
	key := fbOptKey(o)
	if path, ok := u.fbPrevLookup(key); ok {
		u.fbApplyPreview(gen, key, path, o)
		return
	}
	dir := fbDataDir()
	if dir == "" {
		return
	}
	o.OutDir = filepath.Join(dir, "preview", key) // keyed subdir so distinct tuples never collide
	u.fbMut(func(v *fbSt) {
		if v.prevGen == gen {
			v.prevBusy = true
		}
	})
	u.fbPatchAnim()
	out, err := flipbook.Generate(ffmpeg, o)
	if err != nil {
		u.logErr("flipbook preview", err)
		u.fbMut(func(v *fbSt) {
			if v.prevGen == gen {
				v.prevBusy = false
			}
		})
		return
	}
	if u.fbSnap().prevGen != gen {
		_ = os.RemoveAll(o.OutDir) // superseded during ffmpeg: drop the orphan
		return
	}
	u.fbApplyPreview(gen, key, out, o)
}

// fbApplyPreview stores the sheet, bounds the cache, and patches #fb-anim + #fb-strip.
func (u *UI) fbApplyPreview(gen int, key, path string, o flipbook.Options) {
	grid := 2
	if tr, err := flipbook.TierFor(o.Frames); err == nil {
		grid = tr.Grid
	}
	url := u.mpMediaURL(path) // PNG served raw → alpha preserved (imgURL flattens to JPEG)
	var evictDir string
	u.fbMut(func(v *fbSt) {
		if v.prevGen != gen {
			return
		}
		v.prevBusy = false
		v.prevURL, v.prevGrid, v.prevN, v.prevFPS = url, grid, o.Frames, o.FPS
		hit := false
		for i := range v.prevCache {
			if v.prevCache[i].key == key {
				v.prevCache[i].path, hit = path, true
			}
		}
		if !hit {
			v.prevCache = append(v.prevCache, fbPrev{key: key, path: path})
		}
		if len(v.prevCache) > fbPreviewCacheCap {
			evictDir = filepath.Dir(v.prevCache[0].path)
			v.prevCache = v.prevCache[1:]
		}
	})
	if evictDir != "" {
		_ = os.RemoveAll(evictDir)
	}
	if u.fbSnap().prevGen == gen {
		u.fbPatchAnim()
		u.fbPatchStrip()
	}
}

// fbPrevLookup returns a cached sheet path whose file still exists.
func (u *UI) fbPrevLookup(key string) (string, bool) {
	for _, e := range u.fbSnap().prevCache {
		if e.key == key {
			if _, err := os.Stat(e.path); err == nil {
				return e.path, true
			}
		}
	}
	return "", false
}

// fbOptKey is the deterministic cache key over every input that changes the sheet.
func fbOptKey(o flipbook.Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%d|%g|%g|%g|%t|%d", o.Input, o.Frames, o.FPS, o.TrimStart, o.TrimEnd, o.PingPong, o.SheetSize)
	if o.Crop != nil {
		fmt.Fprintf(&b, "|%d,%d,%d,%d", o.Crop.X, o.Crop.Y, o.Crop.W, o.Crop.H)
	}
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(b.String())))
}

// fbStripCells picks ≤ fbStripMax evenly-spaced cell indices to show (+ overflow count).
func fbStripCells(total int) (cells []int, more int) {
	if total <= 0 {
		return nil, 0
	}
	if total <= fbStripMax {
		cells = make([]int, total)
		for i := range cells {
			cells[i] = i
		}
		return cells, 0
	}
	cells = make([]int, fbStripMax)
	for i := 0; i < fbStripMax; i++ {
		cells[i] = i * total / fbStripMax
	}
	return cells, total - fbStripMax
}

func (u *UI) fbPatchAnim() {
	st := u.vrcEmotesState()
	u.eval("window.__patch('fb-anim'," + jsQuote(fbAnimHTML(st)) + ")")
}

func (u *UI) fbPatchStrip() {
	st := u.vrcEmotesState()
	u.eval("window.__patch('fb-strip'," + jsQuote(fbStripHTML(st)) + ")")
}

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
		u.eval("window.__patch('vrc-emote-result'," + jsQuote(u.fbResultHTML(out, o)) + ")")
		u.toast(i18n.T("vrchat.toast.spriteGenerated"))
	})
}

// fbResultHTML renders the post-Generate result block (patched into #vrc-emote-result). The saved
// sheet animates through the same .fb-anim recipe as the pre-Generate preview (real 1024² sheet).
func (u *UI) fbResultHTML(out string, o flipbook.Options) string {
	grid := 2
	if tr, err := flipbook.TierFor(o.Frames); err == nil {
		grid = tr.Grid
	}
	dur := "1s"
	if o.FPS > 0 {
		dur = trimNum(float64(o.Frames)/o.FPS) + "s"
	}
	anim := `<div class=fb-anim style="background-image:url(` + htmlEscape(u.mpMediaURL(out)) + `);--fb-g:` +
		strconv.Itoa(grid) + `;--fb-kf:fb-play-` + strconv.Itoa(o.Frames) + `;--fb-d:` + dur + `"></div>`
	return `<div class=vrc-result>` + anim +
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
