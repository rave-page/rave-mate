package webui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/flipbook"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/transcode"
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
		u.fbMut(func(v *fbSt) { v.cropOn = m.Val == "true" })
		u.fbPatchBody()
	})
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
			u.fbPatchCard()
		}
	})
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
	}
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
