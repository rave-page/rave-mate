package webui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/setalign"
	"rave.page/mate/internal/setedit"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/transcode"
)

// Unified media player/editor - state + interaction engine (render side: player.go).
// ONE component drives every playback/edit surface: the Library inspector player, the
// Publish tab set playback and the trim editor (the old trim modal + per-capture
// transports are superseded). One instance per host surface ("publish"/"library"),
// keyed acts (`mp-<verb>:<host>`), package-level under mpMu.
//
// Media kinds share one transport: audio plays on the featurehost PlayerProxy, video
// in an embedded <video> element (loopback Range stream, mediahttp.go) mirrored into Go
// via mp-vtick transport events. Edit mode is OFF by default (pure player);
// the Trim/edit button (or opening a capture for editing) reveals IN/OUT trim, auto-
// trim and export. With an audio + video pair the editor time-aligns both waveforms
// (setalign envelope cross-correlation) so one trim range cuts both recordings.

// mpMedia is one playable/editable file in the component.
type mpMedia struct {
	capID     string // set-capture id ("" = plain library file)
	path      string
	kind      string // "audio" | "video"
	startedAt time.Time
	cues      []musiclib.CuePoint // library track cue markers

	size         int64
	peaks        []byte
	dur          float64
	peaksLoading bool
	peaksErr     string

	loud        *mpLoud
	loudLoading bool

	src        *transcode.SourceInfo
	srcLoading bool

	presetID string // export preset (default: lossless remux)
	outPath  string // export destination ("" = auto "<base>-cut.<ext>")
}

// mpMark is one track-start marker on the timeline (axis seconds).
type mpMark struct {
	off   float64
	label string
}

// mpAlignSt is the dual-media time-alignment state.
type mpAlignSt struct {
	state   string  // "" prior-only | "run" | "ok" | "err"
	msg     string  // stage / error text
	pct     float64 // compute progress
	off     float64 // video t=0 on the audio timeline (s; negative = video starts first)
	conf    float64
	label   string // high | medium | low
	manual  bool   // user nudged/typed the offset - auto result won't overwrite it
	priorOK bool   // off was seeded from capture wall-clock timestamps
}

// mpSt is one host surface's component instance.
type mpSt struct {
	host   string // "publish" | "library"
	gen    int    // load generation; async results check it before applying
	name   string
	recID  string
	media  []mpMedia // 0..2; audio first when both kinds present
	active int

	markers                                      []mpMark
	firstTrackSec, lastTrackEndSec, lastFaderSec float64 // auto-trim hints (axis s; <0 unknown)

	// view window as fractions of the axis (span 1 = fit)
	viewStart, viewSpan  float64
	drag                 string // "" | "in" | "out" | "pan"
	dragAnchor, dragView float64
	dragMoved            bool
	cursorSec            float64 // last click-seek (axis s; <0 none)
	hovT                 float64 // hover position (axis s; <0 none)

	edit      bool
	pinned    bool    // bound explicitly (loose capture) - mpEnsureSet won't rebind until unpinned
	inSec     float64 // axis s
	outSec    float64 // axis s; <0 = to end
	detecting bool

	align mpAlignSt

	exporting   bool
	exportPct   float64
	exportMsg   string
	exportScope string // dual export target: "both" | media index

	vid          mpVid // embedded <video> transport mirror
	lastTrackIdx int   // marker index at playhead (transport current-track display)
}

// mpVid mirrors the embedded <video> element (updated by its mp-vtick events).
type mpVid struct {
	cur, dur float64
	paused   bool
	started  bool   // element has reported at least one event
	err      string // decode/load failure (degrade honestly, no external window)
}

// mpLoud mirrors the transcode.loudtl worker JSON (was libLoud).
type mpLoud struct {
	I    float64   `json:"i"`
	TP   float64   `json:"tp"`
	LRA  float64   `json:"lra"`
	Step float64   `json:"step"`
	Mom  []float64 `json:"mom"`
}

// momAt returns the momentary LUFS at t seconds (ok=false outside data).
func (l *mpLoud) momAt(t float64) (float64, bool) {
	if l == nil || l.Step <= 0 || len(l.Mom) == 0 || t < 0 {
		return 0, false
	}
	i := int(t / l.Step)
	if i >= len(l.Mom) {
		return 0, false
	}
	return l.Mom[i], true
}

// mpNone marks "no position" - axis times can be legitimately negative (video
// starting before the audio recording), so -1 is not a safe sentinel.
const mpNone = -1e9

// mpIsSet reports v carries a real position.
func mpIsSet(v float64) bool { return v > mpNone/2 }

var (
	mpMu        sync.Mutex
	mpInstances = map[string]*mpSt{}
)

// mp returns the host's instance (created empty on first use).
func (u *UI) mp(host string) *mpSt {
	mpMu.Lock()
	defer mpMu.Unlock()
	t := mpInstances[host]
	if t == nil {
		t = &mpSt{host: host, outSec: -1, viewSpan: 1, cursorSec: mpNone, hovT: mpNone,
			firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1}
		mpInstances[host] = t
	}
	return t
}

// mpMut mutates the instance under mpMu and returns a copy for rendering.
func (u *UI) mpMut(host string, fn func(*mpSt)) mpSt {
	t := u.mp(host)
	mpMu.Lock()
	fn(t)
	c := *t
	c.media = append([]mpMedia(nil), t.media...)
	mpMu.Unlock()
	return c
}

// mpSnap returns a render copy.
func (u *UI) mpSnap(host string) mpSt { return u.mpMut(host, func(*mpSt) {}) }

// ── axis (shared wall-clock timeline) ───────────────────────────────────────────

func (t *mpSt) dual() bool { return len(t.media) == 2 }

// mediaStart is media i's t=0 on the axis: 0 for the primary; the alignment offset
// for the video half of a dual pair.
func (t *mpSt) mediaStart(i int) float64 {
	if t.dual() && i == 1 {
		return t.align.off
	}
	return 0
}

// axis returns the shared timeline [lo, lo+length] covering every media extent.
func (t *mpSt) axis() (lo, length float64) {
	hi := 0.0
	for i := range t.media {
		s := t.mediaStart(i)
		if s < lo {
			lo = s
		}
		if d := t.media[i].dur; s+d > hi {
			hi = s + d
		}
	}
	if hi <= lo {
		return lo, 0
	}
	return lo, hi - lo
}

// axisOutEff resolves outSec (-1 = to end) to an absolute axis position.
func (t *mpSt) axisOutEff() float64 {
	if t.outSec >= 0 {
		return t.outSec
	}
	lo, ln := t.axis()
	return lo + ln
}

// ── engines (audio: featurehost player · video: embedded <video> mirror) ────────

// mpTr is a media-local transport snapshot.
type mpTr struct {
	loaded          bool // the engine currently has this file
	playing, paused bool
	cur, total      float64
}

func (u *UI) mpEngineState(t *mpSt, m *mpMedia) mpTr {
	if m == nil {
		return mpTr{}
	}
	if m.kind == "video" {
		v := t.vid
		if !v.started || v.err != "" {
			return mpTr{}
		}
		total := v.dur
		if total <= 0 {
			total = m.dur
		}
		return mpTr{loaded: true, playing: !v.paused, paused: v.paused, cur: v.cur, total: total}
	}
	if u.svc.Player == nil {
		return mpTr{}
	}
	st := u.svc.Player.State()
	if st.Path != m.path || !st.Playing {
		return mpTr{}
	}
	return mpTr{loaded: true, playing: !st.Paused, paused: st.Paused, cur: st.Cur, total: st.Total}
}

// mpVidEval runs js with `v` bound to the host's <video> element (no-op when absent).
func (u *UI) mpVidEval(host, js string) {
	u.eval("var v=document.getElementById(" + jsQuote("mp-vid-"+host) + ");if(v){" + js + "}")
}

// mpActive returns the active media of a snapshot (nil when empty).
func (t *mpSt) activeMedia() *mpMedia {
	if len(t.media) == 0 {
		return nil
	}
	if t.active < 0 || t.active >= len(t.media) {
		return &t.media[0]
	}
	return &t.media[t.active]
}

func (u *UI) mpPlayToggle(host string) {
	t := u.mpSnap(host)
	m := t.activeMedia()
	if m == nil {
		return
	}
	tr := u.mpEngineState(&t, m)
	if tr.loaded {
		if m.kind == "video" {
			u.mpVidEval(host, "if(v.paused){v.play().catch(function(){})}else{v.pause()}")
			u.mpMut(host, func(v *mpSt) { v.vid.paused = !v.vid.paused }) // optimistic; vtick reconciles
		} else {
			u.svc.Player.TogglePause()
		}
		u.mpPatchTransport(u.mpSnap(host))
		return
	}
	u.mpStartPlayback(host, *m, -1)
}

// mpStartPlayback starts the right engine for m; seekTo ≥ 0 = media-local start position.
// Video: the embedded element plays in place (autoplay may demand muted - fall back, the
// user can unmute via the element's native controls).
func (u *UI) mpStartPlayback(host string, m mpMedia, seekTo float64) {
	if m.kind == "video" {
		js := ""
		if seekTo > 0 {
			js = fmt.Sprintf("v.currentTime=%.3f;", seekTo)
		}
		js += "v.play().catch(function(){v.muted=true;v.play().catch(function(){})});"
		u.mpVidEval(host, js)
		u.mpMut(host, func(v *mpSt) { v.vid.started, v.vid.paused = true, false })
		u.mpPatchTransport(u.mpSnap(host))
		return
	}
	if u.svc.Player == nil {
		u.toast(i18n.T("player.toast.playerUnavailable"))
		return
	}
	path := m.path
	u.bg(func() {
		if err := u.svc.Player.Play(path); err != nil {
			u.logErr("player play", err)
			u.toast(i18n.T("player.toast.playFailed") + err.Error())
			return
		}
		if seekTo > 0 {
			u.svc.Player.Seek(seekTo)
		}
		u.mpPatchTransport(u.mpSnap(host))
	})
}

func (u *UI) mpStop(host string) {
	t := u.mpSnap(host)
	m := t.activeMedia()
	if m == nil {
		return
	}
	if m.kind == "video" {
		u.mpVidEval(host, "v.pause();v.currentTime=0;")
		u.mpMut(host, func(v *mpSt) { v.vid.paused, v.vid.cur = true, 0 })
	} else if u.svc.Player != nil {
		u.svc.Player.Stop()
	}
	t = u.mpSnap(host)
	u.mpPatchTransport(t)
	u.mpPatchWave(t)
}

// mpSeekAxis seeks the active engine to an axis position.
func (u *UI) mpSeekAxis(host string, axisSec float64) {
	t := u.mpSnap(host)
	m := t.activeMedia()
	if m == nil {
		return
	}
	local := clampF(axisSec-t.mediaStart(t.active), 0, math.Max(m.dur, 0))
	if m.kind == "video" {
		u.mpVidEval(host, fmt.Sprintf("v.currentTime=%.3f;", local))
		u.mpMut(host, func(v *mpSt) { v.vid.cur = local })
		u.mpPatchWave(u.mpSnap(host))
		return
	}
	if tr := u.mpEngineState(&t, m); tr.loaded {
		u.svc.Player.Seek(local)
	}
}

// ── playhead (axis seconds; -1 = none) ──────────────────────────────────────────

func (u *UI) mpPlayheadAxis(t *mpSt) float64 {
	m := t.activeMedia()
	if m == nil {
		return mpNone
	}
	tr := u.mpEngineState(t, m)
	if !tr.loaded {
		return mpNone
	}
	return tr.cur + t.mediaStart(t.active)
}

// ── loading (per surface) ───────────────────────────────────────────────────────

// mpEnsureFile binds the library instance to a single audio file (no-op when already bound).
func (u *UI) mpEnsureFile(host, path string, tr musiclib.Track) {
	cur := u.mpSnap(host)
	if len(cur.media) == 1 && cur.media[0].path == path && cur.media[0].capID == "" {
		return
	}
	dur := tr.DurationSec
	u.mpMut(host, func(t *mpSt) {
		t.reset()
		t.name = filepath.Base(path)
		t.media = []mpMedia{{path: path, kind: "audio", cues: tr.Cues, dur: dur,
			size: fileSize(path), presetID: "remux", peaksLoading: true}}
	})
	u.mpKickAnalyses(host)
}

// mpEnsureSet binds the publish instance to a recording's captures (first audio +
// first video with a file on disk). No-op when the same paths are already bound.
func (u *UI) mpEnsureSet(r recorder.Recording, caps []libdb.SetRecording) {
	var aud, vid *libdb.SetRecording
	for i := range caps {
		s := &caps[i]
		if !pubFileExists(s.Path) {
			continue
		}
		if s.Kind != libdb.SetKindOBS && pubIsAudio(s.Path) {
			if aud == nil {
				aud = s
			}
		} else if vid == nil {
			vid = s
		}
	}
	var want []string
	for _, s := range []*libdb.SetRecording{aud, vid} {
		if s != nil {
			want = append(want, s.Path)
		}
	}
	cur := u.mpSnap("publish")
	if cur.pinned { // a loose capture is loaded explicitly - keep it until the user re-selects
		return
	}
	if cur.recID == r.ID && len(cur.media) == len(want) {
		same := true
		for i := range want {
			if cur.media[i].path != want[i] {
				same = false
			}
		}
		if same {
			return
		}
	}
	if len(want) == 0 {
		u.mpMut("publish", func(t *mpSt) { t.reset() })
		return
	}
	u.mpLoadCaptures("publish", r, aud, vid)
}

// mpLoadCap binds the publish instance to one loose capture (opened for playback/editing).
func (u *UI) mpLoadCap(s libdb.SetRecording, edit bool) {
	var r recorder.Recording
	if u.svc.Recorder != nil && s.RecordingID != "" {
		if rr, ok := u.svc.Recorder.Get(s.RecordingID); ok {
			r = rr
		}
	}
	if s.Kind != libdb.SetKindOBS && pubIsAudio(s.Path) {
		u.mpLoadCaptures("publish", r, &s, nil)
	} else {
		u.mpLoadCaptures("publish", r, nil, &s)
	}
	u.mpMut("publish", func(t *mpSt) { t.pinned, t.edit = true, edit })
}

// mpLoadCaptures resets + binds captures; markers/auto-trim hints come from the recording,
// anchored to the PRIMARY capture's start (axis 0).
func (u *UI) mpLoadCaptures(host string, r recorder.Recording, aud, vid *libdb.SetRecording) {
	primary := aud
	if primary == nil {
		primary = vid
	}
	if primary == nil {
		return
	}
	name := orSetName(r.Name)
	if r.ID == "" {
		name = filepath.Base(primary.Path)
	}

	first, lastEnd, lastFader := -1.0, -1.0, -1.0
	var marks []mpMark
	if len(r.Tracks) > 0 && !primary.StartedAt.IsZero() {
		first = math.Max(r.Tracks[0].StartedAt.Sub(primary.StartedAt).Seconds(), 0)
		var le time.Time
		for _, tr := range r.Tracks {
			if tr.EndedAt.After(le) {
				le = tr.EndedAt
			}
			off := math.Max(tr.StartedAt.Sub(primary.StartedAt).Seconds(), 0)
			marks = append(marks, mpMark{off, orTrackLine(pubTrackLine(tr))})
		}
		if !le.IsZero() {
			lastEnd = math.Max(le.Sub(primary.StartedAt).Seconds(), 0)
		}
		// last fader-down = the true set end; only when it lands inside this capture
		if !r.LastFaderAt.IsZero() && r.LastFaderAt.After(primary.StartedAt) {
			lastFader = r.LastFaderAt.Sub(primary.StartedAt).Seconds()
		}
	}

	var media []mpMedia
	if aud != nil {
		media = append(media, mpMedia{capID: aud.ID, path: aud.Path, kind: "audio", size: fileSize(aud.Path),
			startedAt: aud.StartedAt, presetID: "remux", peaksLoading: true})
	}
	if vid != nil {
		media = append(media, mpMedia{capID: vid.ID, path: vid.Path, kind: "video", size: fileSize(vid.Path),
			startedAt: vid.StartedAt, presetID: "remux", peaksLoading: true})
	}

	// dual pair: seed the alignment from the captures' wall-clock start timestamps
	prior, hasPrior := 0.0, false
	if aud != nil && vid != nil && !aud.StartedAt.IsZero() && !vid.StartedAt.IsZero() {
		prior = vid.StartedAt.Sub(aud.StartedAt).Seconds()
		hasPrior = true
	}

	u.mpMut(host, func(t *mpSt) {
		t.reset()
		t.name, t.recID = name, r.ID
		t.media = media
		t.markers = marks
		t.firstTrackSec, t.lastTrackEndSec, t.lastFaderSec = first, lastEnd, lastFader
		if hasPrior {
			t.align = mpAlignSt{off: prior, priorOK: true}
		}
	})
	u.mpKickAnalyses(host)
}

// reset clears everything except the host key (gen advances so stale async results drop).
func (t *mpSt) reset() {
	g := t.gen + 1
	*t = mpSt{host: t.host, gen: g, outSec: -1, viewSpan: 1, cursorSec: mpNone, hovT: mpNone,
		firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1}
}

// ── analyses (peaks + loudness + stream info; store-cached, probe/transcode workers) ──

func (u *UI) mpKickAnalyses(host string) {
	t := u.mpSnap(host)
	for i := range t.media {
		u.mpLoadPeaks(host, t.gen, i, t.media[i].path)
		u.mpLoadLoud(host, t.gen, i, t.media[i].path)
		u.mpLoadSrc(host, t.gen, i, t.media[i].path)
	}
}

// mpApply mutates media[idx] iff the instance is still generation gen, then patches.
func (u *UI) mpApply(host string, gen, idx int, fn func(*mpMedia)) bool {
	ok := false
	t := u.mpMut(host, func(t *mpSt) {
		if t.gen != gen || idx >= len(t.media) {
			return
		}
		fn(&t.media[idx])
		ok = true
	})
	if ok {
		u.mpPatchWave(t)
	}
	return ok
}

func (u *UI) mpLoadPeaks(host string, gen, idx int, path string) {
	u.bg(func() {
		dur, data, err := u.mpResolvePeaks(path)
		applied := u.mpApply(host, gen, idx, func(m *mpMedia) {
			m.peaksLoading = false
			if err != nil {
				m.peaksErr = err.Error()
				return
			}
			m.peaks = data
			if dur > 0 {
				m.dur = dur
			}
		})
		if applied {
			t := u.mpSnap(host)
			u.mpPatchTransport(t)
			u.mpPatchEdit(t)
		}
	})
}

// mpPeakBlob is the persisted peaks payload - same {d,p} shape the Fyne cache used.
type mpPeakBlob struct {
	D float64 `json:"d"`
	P []byte  `json:"p"`
}

func (u *UI) mpResolvePeaks(path string) (durSec float64, peaks []byte, err error) {
	var mtime int64
	if fi, serr := os.Stat(path); serr == nil {
		mtime = fi.ModTime().Unix()
	}
	if u.svc.Store != nil {
		if data, ok := u.svc.Store.GetAnalysis(store.KindPeaks, path, mtime); ok {
			var tp mpPeakBlob
			if json.Unmarshal(data, &tp) == nil && len(tp.P) > 0 {
				return tp.D, tp.P, nil
			}
		}
	}
	if u.svc.Workers == nil {
		return 0, nil, errors.New("no worker runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	raw, rerr := u.svc.Workers.RunBackground(ctx, "probe", "probe.peaks", map[string]any{"path": path, "buckets": 8192})
	if rerr != nil {
		return 0, nil, rerr
	}
	var r struct {
		Peaks  string  `json:"peaks"`
		DurSec float64 `json:"durationSeconds"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Peaks == "" || r.DurSec <= 0 {
		return 0, nil, errors.New("empty analysis")
	}
	data, derr := base64.StdEncoding.DecodeString(r.Peaks)
	if derr != nil || len(data) == 0 {
		return 0, nil, errors.New("bad peaks payload")
	}
	if u.svc.Store != nil {
		if b, merr := json.Marshal(mpPeakBlob{r.DurSec, data}); merr == nil {
			u.svc.Store.PutAnalysis(store.KindPeaks, path, mtime, b)
		}
	}
	return r.DurSec, data, nil
}

// mpLoadLoud fetches the EBU R128 timeline (LUFS chip + momentary readout). Works for
// video files too (measures the first audio stream).
func (u *UI) mpLoadLoud(host string, gen, idx int, path string) {
	if u.svc.Workers == nil {
		return
	}
	u.mpApply(host, gen, idx, func(m *mpMedia) { m.loudLoading = true })
	u.bg(func() {
		var mtime int64
		if fi, err := os.Stat(path); err == nil {
			mtime = fi.ModTime().Unix()
		}
		raw, cached := []byte(nil), false
		if u.svc.Store != nil {
			raw, cached = u.svc.Store.GetAnalysis(store.KindLoudnessTL, path, mtime)
		}
		if !cached {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			var err error
			raw, err = u.svc.Workers.RunBackground(ctx, "transcode", "transcode.loudtl", map[string]any{"input": path})
			if err != nil {
				u.mpApply(host, gen, idx, func(m *mpMedia) { m.loudLoading = false })
				return
			}
			if u.svc.Store != nil {
				u.svc.Store.PutAnalysis(store.KindLoudnessTL, path, mtime, raw)
			}
		}
		var l mpLoud
		lerr := json.Unmarshal(raw, &l)
		u.mpApply(host, gen, idx, func(m *mpMedia) {
			m.loudLoading = false
			if lerr == nil {
				m.loud = &l
			}
		})
	})
}

// mpLoadSrc probes stream info for the encoding chip.
func (u *UI) mpLoadSrc(host string, gen, idx int, path string) {
	if u.svc.Workers == nil {
		return
	}
	u.mpApply(host, gen, idx, func(m *mpMedia) { m.srcLoading = true })
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		raw, err := u.svc.Workers.Run(ctx, "probe", "probe.streams", map[string]any{"path": path})
		var si transcode.SourceInfo
		if err == nil {
			si, _ = transcode.ParseProbe(raw)
		}
		u.mpApply(host, gen, idx, func(m *mpMedia) {
			m.srcLoading = false
			if err == nil {
				v := si
				m.src = &v
			}
		})
	})
}

// ── patch helpers (fragment repaints; interaction lanes live OUTSIDE these regions) ──

func (u *UI) mpPatch(host, part, html string) {
	u.eval("window.__patch(" + jsQuote("mp-"+host+"-"+part) + "," + jsQuote(html) + ")")
}

func (u *UI) mpPatchAll(t mpSt) {
	u.mpPatch(t.host, "root", u.mpInnerHTML(t))
	if t.vid.started && t.vid.err == "" { // the <video> element was recreated - restore it
		js := fmt.Sprintf("v.currentTime=%.3f;", t.vid.cur)
		if !t.vid.paused {
			js += "v.play().catch(function(){v.muted=true;v.play().catch(function(){})});"
		}
		u.mpVidEval(t.host, js)
	}
}
func (u *UI) mpPatchWave(t mpSt) { u.mpPatch(t.host, "wave", u.mpWaveInner(t)) }
func (u *UI) mpPatchTransport(t mpSt) {
	u.mpPatch(t.host, "tp", u.mpTransportHTML(t))
}
func (u *UI) mpPatchEdit(t mpSt)   { u.mpPatch(t.host, "edit", u.mpEditHTML(t)) }
func (u *UI) mpPatchExport(t mpSt) { u.mpPatch(t.host, "export", u.mpExportHTML(t)) }
func (u *UI) mpPatchRO(t mpSt)     { u.mpPatch(t.host, "ro", mpReadout(t)) }
func (u *UI) mpPatchHov(t mpSt)    { u.mpPatch(t.host, "hov", u.mpReadoutLine(t)) }

// mpPatchTime updates the transport clock in place (text + ctl data-value) without
// repainting the buttons/slider mid-interaction.
func (u *UI) mpPatchTime(t mpSt) {
	tx := jsQuote(u.mpTimeText(t))
	u.eval("var e=document.getElementById(" + jsQuote("mp-"+t.host+"-time") + ");if(e){e.textContent=" + tx + ";e.setAttribute('data-value'," + tx + ");}")
}

// mpApplyTrim mutates + repaints wave/edit (the common non-drag update path).
func (u *UI) mpApplyTrim(host string, fn func(*mpSt)) {
	t := u.mpMut(host, fn)
	if len(t.media) == 0 {
		return
	}
	u.mpPatchWave(t)
	u.mpPatchEdit(t)
}

// mpTick keeps clock/playhead/transport fresh while this host's tab shows (1 Hz).
func mpTick(u *UI, host string) {
	t := u.mpSnap(host)
	if len(t.media) == 0 || t.drag != "" {
		return
	}
	m := t.activeMedia()
	tr := u.mpEngineState(&t, m)
	if !tr.loaded {
		// audio source stopped - halt a slaved video preview too
		if t.dual() && t.active == 0 && t.vid.started && !t.vid.paused {
			u.mpVidEval(host, "if(!v.paused){v.pause()}")
		}
		return
	}
	// late/extend-bind duration from the engine: peaks may have failed (dur 0) or cover
	// less than the playable total (truncated/corrupt decode, VBR estimates) - the axis
	// must always span the full seekable range.
	if tr.total > m.dur+1 {
		t = u.mpMut(host, func(v *mpSt) {
			if v.active < len(v.media) {
				v.media[v.active].dur = tr.total
			}
		})
		u.mpPatchEdit(t)
	}
	u.mpPatchTime(t)
	if tr.playing {
		u.mpPatchWave(t)
		if !mpIsSet(t.hovT) {
			u.mpPatchHov(t)
		}
		// dual + audio as source: slave the muted video preview to the aligned playhead
		if t.dual() && t.active == 0 {
			want := tr.cur - t.align.off // video-local time for this axis position
			u.mpVidEval(host, fmt.Sprintf(
				"var w=%.3f;if(w<0||(v.duration&&w>v.duration)){if(!v.paused)v.pause();}else{if(Math.abs(v.currentTime-w)>0.75){v.currentTime=w}v.muted=true;if(v.paused){v.play().catch(function(){})}}", want))
		}
		// transport shows the current track - refresh it when the playhead crosses a marker
		if len(t.markers) > 0 {
			idx := mpMarkIdxAt(t.markers, tr.cur+t.mediaStart(t.active))
			if idx != t.lastTrackIdx {
				u.mpMut(host, func(v *mpSt) { v.lastTrackIdx = idx })
				u.mpPatchTransport(u.mpSnap(host))
			}
		}
	}
}

// mpMarkIdxAt returns the marker index whose track covers axis time cur (-1 before track 1).
func mpMarkIdxAt(marks []mpMark, cur float64) int {
	idx := -1
	for i, m := range marks {
		if m.off <= cur+0.001 {
			idx = i
		} else {
			break
		}
	}
	return idx
}

// ── action registry ─────────────────────────────────────────────────────────────

// mpArgs splits a compound act arg on \x1f: host [+ rest].
func mpArgs(arg string) (host, rest string) {
	host, rest, _ = strings.Cut(arg, "\x1f")
	return host, rest
}

func init() {
	onPrefix("mp-media:", func(u *UI, m actMsg) {
		host, rest := mpArgs(m.arg("mp-media:"))
		idx := atoi(rest)
		prev := u.mpSnap(host).active
		t := u.mpMut(host, func(t *mpSt) {
			if idx >= 0 && idx < len(t.media) {
				t.active = idx
			}
		})
		if t.active != prev { // silence the side that lost focus - never double-play
			if t.media[t.active].kind == "video" {
				if u.svc.Player != nil {
					u.svc.Player.Stop()
				}
				u.mpVidEval(host, "v.muted=false;")
			} else {
				u.mpVidEval(host, "if(!v.paused){v.pause()}")
			}
		}
		u.mpPatchAll(t)
	})
	onPrefix("mp-play:", func(u *UI, m actMsg) { u.mpPlayToggle(m.arg("mp-play:")) })
	onPrefix("mp-stop:", func(u *UI, m actMsg) { u.mpStop(m.arg("mp-stop:")) })
	onPrefix("mp-preview:", func(u *UI, m actMsg) { u.mpPreview(m.arg("mp-preview:")) })
	onPrefix("mp-seek:", func(u *UI, m actMsg) {
		host := m.arg("mp-seek:")
		f, _ := strconv.ParseFloat(m.Val, 64)
		t := u.mpSnap(host)
		lo, ln := t.axis()
		if ln > 0 {
			u.mpSeekAxis(host, lo+clampF(f/1000, 0, 1)*ln)
		}
	})
	onPrefix("mp-openext:", func(u *UI, m actMsg) {
		t := u.mpSnap(m.arg("mp-openext:"))
		if mm := t.activeMedia(); mm != nil {
			_ = openURL(mm.path)
		}
	})
	onPrefix("mp-reveal:", func(u *UI, m actMsg) {
		t := u.mpSnap(m.arg("mp-reveal:"))
		if mm := t.activeMedia(); mm != nil {
			_ = openURL(filepath.Dir(mm.path))
		}
	})

	// waveform interaction (actpos/actwheel/acthover transport)
	onPrefix("mp-surf:", func(u *UI, m actMsg) { u.mpSurf(m.arg("mp-surf:"), m.Val) })
	onPrefix("mp-hin:", func(u *UI, m actMsg) { u.mpHandle(m.arg("mp-hin:"), "in", m.Val) })
	onPrefix("mp-hout:", func(u *UI, m actMsg) { u.mpHandle(m.arg("mp-hout:"), "out", m.Val) })
	onPrefix("mp-zoomw:", func(u *UI, m actMsg) {
		host := m.arg("mp-zoomw:")
		dir, fx, ok := mpPos(m.Val)
		if ok {
			u.mpZoomAt(host, dir == "in", fx)
		}
	})
	onPrefix("mp-zin:", func(u *UI, m actMsg) { u.mpZoomAt(m.arg("mp-zin:"), true, 0.5) })
	onPrefix("mp-zout:", func(u *UI, m actMsg) { u.mpZoomAt(m.arg("mp-zout:"), false, 0.5) })
	onPrefix("mp-fit:", func(u *UI, m actMsg) {
		t := u.mpMut(m.arg("mp-fit:"), func(v *mpSt) { v.viewStart, v.viewSpan = 0, 1 })
		u.mpPatchWave(t)
	})
	onPrefix("mp-hov:", func(u *UI, m actMsg) { u.mpHover(m.arg("mp-hov:"), m.Val) })

	// edit mode
	onPrefix("mp-edit:", func(u *UI, m actMsg) {
		t := u.mpMut(m.arg("mp-edit:"), func(v *mpSt) { v.edit = !v.edit })
		u.mpPatchAll(t)
		if t.edit {
			u.mpMaybeAlign(t.host, false)
		}
	})

	// trim fields + set/snap/clear
	onPrefix("mp-in:", func(u *UI, m actMsg) { u.mpSetField(m.arg("mp-in:"), "in", m.Val) })
	onPrefix("mp-out:", func(u *UI, m actMsg) { u.mpSetField(m.arg("mp-out:"), "out", m.Val) })
	onPrefix("mp-setin:", func(u *UI, m actMsg) { u.mpSetAtPos(m.arg("mp-setin:"), "in") })
	onPrefix("mp-setout:", func(u *UI, m actMsg) { u.mpSetAtPos(m.arg("mp-setout:"), "out") })
	onPrefix("mp-clear:", func(u *UI, m actMsg) {
		u.mpApplyTrim(m.arg("mp-clear:"), func(v *mpSt) { v.inSec, v.outSec = 0, -1 })
	})
	onPrefix("mp-snapin:", func(u *UI, m actMsg) { u.mpSnapTrack(m.arg("mp-snapin:"), "in") })
	onPrefix("mp-snapout:", func(u *UI, m actMsg) { u.mpSnapTrack(m.arg("mp-snapout:"), "out") })

	// auto-trim
	onPrefix("mp-auto-tracks:", func(u *UI, m actMsg) {
		u.mpApplyTrim(m.arg("mp-auto-tracks:"), func(v *mpSt) {
			if v.firstTrackSec >= 0 {
				v.inSec = v.firstTrackSec
			}
			if v.lastTrackEndSec > 0 {
				v.outSec = v.lastTrackEndSec
			}
		})
	})
	onPrefix("mp-auto-fader:", func(u *UI, m actMsg) {
		u.mpApplyTrim(m.arg("mp-auto-fader:"), func(v *mpSt) {
			if v.firstTrackSec >= 0 {
				v.inSec = v.firstTrackSec
			}
			if v.lastFaderSec > 0 {
				v.outSec = v.lastFaderSec
			}
		})
	})
	onPrefix("mp-auto-silence:", func(u *UI, m actMsg) { u.mpSilence(m.arg("mp-auto-silence:")) })

	// alignment (dual pair)
	onPrefix("mp-align:", func(u *UI, m actMsg) { u.mpMaybeAlign(m.arg("mp-align:"), true) })
	onPrefix("mp-nudge:", func(u *UI, m actMsg) {
		host, rest := mpArgs(m.arg("mp-nudge:"))
		ms, _ := strconv.ParseFloat(rest, 64)
		u.mpNudge(host, ms/1000)
	})
	onPrefix("mp-aoff:", func(u *UI, m actMsg) { u.mpOffField(m.arg("mp-aoff:"), m.Val) })

	// export
	onPrefix("mp-preset:", func(u *UI, m actMsg) {
		host, idxS := mpArgs(m.arg("mp-preset:"))
		idx := atoi(idxS)
		id := m.Val // smart-select forwards the picked value
		t := u.mpMut(host, func(v *mpSt) {
			if idx >= 0 && idx < len(v.media) {
				v.media[idx].presetID = id
			}
		})
		u.mpPatchExport(t)
	})
	onPrefix("mp-outpath:", func(u *UI, m actMsg) {
		host, idxS := mpArgs(m.arg("mp-outpath:"))
		idx := atoi(idxS)
		val := strings.TrimSpace(m.Val)
		t := u.mpMut(host, func(v *mpSt) {
			if idx >= 0 && idx < len(v.media) {
				v.media[idx].outPath = val
			}
		})
		u.mpPatchExport(t)
	})
	onPrefix("mp-export:", func(u *UI, m actMsg) {
		host, which := mpArgs(m.arg("mp-export:"))
		if which == "" { // condensed UI: scope comes from the export-target select
			t := u.mpSnap(host)
			which = t.exportScope
			if which == "" {
				if t.dual() {
					which = "both"
				} else {
					which = fmt.Sprint(t.active)
				}
			}
		}
		u.mpRunExport(host, which)
	})

	onPrefix("mp-jump:", func(u *UI, m actMsg) {
		host, rest := mpArgs(m.arg("mp-jump:"))
		if rest == "" { // smart-select variant: offset arrives as the value
			rest = m.Val
		}
		off, _ := strconv.ParseFloat(rest, 64)
		u.mpJump(host, off)
	})
	onPrefix("mp-prevtrack:", func(u *UI, m actMsg) { u.mpTrackStep(m.arg("mp-prevtrack:"), -1) })
	onPrefix("mp-nexttrack:", func(u *UI, m actMsg) { u.mpTrackStep(m.arg("mp-nexttrack:"), 1) })

	// embedded <video> transport mirror (element events → Go)
	onPrefix("mp-vtick:", func(u *UI, m actMsg) { u.mpVidTick(m.arg("mp-vtick:"), m.Val) })
	onPrefix("mp-verr:", func(u *UI, m actMsg) {
		t := u.mpMut(m.arg("mp-verr:"), func(v *mpSt) {
			v.vid.err = i18n.T("player.label.cantDecode")
		})
		u.mpPatchAll(t)
	})

	// condensed auto-trim menu (smart-select; val = mode)
	onPrefix("mp-auto:", func(u *UI, m actMsg) { u.mpAutoMode(m.arg("mp-auto:"), m.Val) })
	// dual export target (smart-select; val = "both"|index)
	onPrefix("mp-scope:", func(u *UI, m actMsg) {
		host := m.arg("mp-scope:")
		val := m.Val
		t := u.mpMut(host, func(v *mpSt) { v.exportScope = val })
		u.mpPatchExport(t)
	})

	// loose captures (Publish): load one capture into the player, optionally straight to edit
	onPrefix("mp-loadcap:", func(u *UI, m actMsg) {
		capID, mode := mpArgs(m.arg("mp-loadcap:"))
		s, ok := u.pubCapByID(capID)
		if !ok {
			u.toast(i18n.T("player.toast.captureNotFound"))
			return
		}
		u.mpLoadCap(s, mode == "edit")
		u.patchMain()
	})
}

// ── waveform interaction (ported from the trim modal; axis-space) ───────────────

// mpPos parses an actpos/actwheel value ("down:0.43,0.50" / "in:fx,fy") → phase + x fraction.
func mpPos(val string) (phase string, fx float64, ok bool) {
	phase, rest, found := strings.Cut(val, ":")
	if !found {
		return "", 0, false
	}
	xs, _, _ := strings.Cut(rest, ",")
	f, err := strconv.ParseFloat(xs, 64)
	if err != nil {
		return "", 0, false
	}
	return phase, clampF(f, 0, 1), true
}

// mpAxisAt converts an in-view x fraction to axis seconds.
func (t *mpSt) mpAxisAt(fx float64) float64 {
	lo, ln := t.axis()
	return lo + (t.viewStart+fx*t.viewSpan)*ln
}

// mpHandle drags the IN (top lane) / OUT (bottom lane) trim bound to the pointer.
func (u *UI) mpHandle(host, which, val string) {
	phase, fx, ok := mpPos(val)
	if !ok {
		return
	}
	t := u.mpMut(host, func(v *mpSt) {
		lo, ln := v.axis()
		if len(v.media) == 0 || ln <= 0 {
			return
		}
		sec := clampF(v.mpAxisAt(fx), lo, lo+ln)
		if which == "in" {
			v.drag = "in"
			v.inSec = sec
			if v.outSec >= 0 && v.inSec > v.outSec-0.1 {
				v.inSec = math.Max(v.outSec-0.1, lo)
			}
		} else {
			v.drag = "out"
			if sec >= lo+ln-0.05 {
				v.outSec = -1 // dragged to the far right = "to end"
			} else {
				v.outSec = math.Max(sec, v.inSec+0.1)
			}
		}
		if phase == "up" {
			v.drag = ""
		}
	})
	if len(t.media) == 0 {
		return
	}
	u.mpPatchWave(t)
	u.mpPatchRO(t)
	if phase == "up" {
		u.mpPatchEdit(t)
	}
}

// mpSurf handles the middle lane: click = seek, drag = pan when zoomed.
func (u *UI) mpSurf(host, val string) {
	phase, fx, ok := mpPos(val)
	if !ok {
		return
	}
	seekSec := math.Inf(-1)
	t := u.mpMut(host, func(v *mpSt) {
		if len(v.media) == 0 {
			return
		}
		switch phase {
		case "down":
			v.drag, v.dragAnchor, v.dragView, v.dragMoved = "pan", fx, v.viewStart, false
		case "move":
			if v.drag != "pan" {
				return
			}
			if math.Abs(fx-v.dragAnchor) > 0.01 {
				v.dragMoved = true
			}
			if v.viewSpan < 1 {
				v.viewStart = clampF(v.dragView-(fx-v.dragAnchor)*v.viewSpan, 0, 1-v.viewSpan)
			}
		case "up":
			if v.drag != "pan" {
				return
			}
			v.drag = ""
			if _, ln := v.axis(); !v.dragMoved && ln > 0 {
				seekSec = v.mpAxisAt(fx)
				v.cursorSec = seekSec
			}
		}
	})
	if len(t.media) == 0 {
		return
	}
	switch phase {
	case "move":
		if t.viewSpan < 1 {
			u.mpPatchWave(t)
		}
	case "up":
		if !math.IsInf(seekSec, -1) {
			u.mpSeekAxis(host, seekSec)
		}
		u.mpPatchWave(t)
	}
}

// mpZoomAt zooms the view window keeping the time under fx stationary (min span = 50×).
func (u *UI) mpZoomAt(host string, zoomIn bool, fx float64) {
	t := u.mpMut(host, func(v *mpSt) {
		f := 1.25
		if zoomIn {
			f = 0.8
		}
		ns := clampF(v.viewSpan*f, 0.02, 1)
		cursor := v.viewStart + fx*v.viewSpan
		v.viewStart = clampF(cursor-fx*ns, 0, 1-ns)
		v.viewSpan = ns
	})
	if len(t.media) > 0 {
		u.mpPatchWave(t)
	}
}

// mpHover updates the time + momentary-LUFS readout ("at:fx,fy" / "off").
func (u *UI) mpHover(host, val string) {
	var t mpSt
	if val == "off" {
		t = u.mpMut(host, func(v *mpSt) { v.hovT = mpNone })
	} else {
		phase, fx, ok := mpPos(val)
		if !ok || phase != "at" {
			return
		}
		t = u.mpMut(host, func(v *mpSt) {
			if _, ln := v.axis(); ln > 0 {
				v.hovT = v.mpAxisAt(fx)
			}
		})
	}
	if len(t.media) > 0 {
		u.mpPatchHov(t)
	}
}

// ── trim fields / set / snap (axis-space; ported from the trim modal) ───────────

func (u *UI) mpSetField(host, which, val string) {
	sec, ok := pubParseClock(val)
	if !ok {
		u.toast(i18n.T("player.toast.timeFormat"))
		return
	}
	u.mpApplyTrim(host, func(v *mpSt) {
		lo, ln := v.axis()
		if which == "in" {
			if sec < 0 {
				sec = 0
			}
			if ln > 0 {
				sec = clampF(sec, lo, lo+ln)
			}
			v.inSec = sec
			if v.outSec >= 0 && v.outSec <= v.inSec {
				v.outSec = -1 // Fyne setIn semantics
			}
			return
		}
		if sec >= 0 && ln > 0 {
			sec = clampF(sec, lo, lo+ln)
		}
		if sec >= 0 && sec <= v.inSec {
			sec = -1 // at/before IN = to end
		}
		v.outSec = sec
	})
}

// mpRefPos is the reference position for Set IN/OUT: live playhead, else the click cursor.
func (u *UI) mpRefPos(t *mpSt) float64 {
	if p := u.mpPlayheadAxis(t); mpIsSet(p) {
		return p
	}
	if mpIsSet(t.cursorSec) {
		return t.cursorSec
	}
	return 0
}

func (u *UI) mpSetAtPos(host, which string) {
	t := u.mpSnap(host)
	pos := u.mpRefPos(&t)
	u.mpApplyTrim(host, func(v *mpSt) {
		if which == "in" {
			v.inSec = pos
			if v.outSec >= 0 && v.outSec <= v.inSec {
				v.outSec = -1
			}
			return
		}
		if pos <= v.inSec {
			v.outSec = -1
		} else {
			v.outSec = pos
		}
	})
}

func (u *UI) mpSnapTrack(host, which string) {
	t := u.mpSnap(host)
	pos := u.mpRefPos(&t)
	u.mpApplyTrim(host, func(v *mpSt) {
		if which == "in" {
			v.inSec = mpMarkStartAt(v.markers, pos)
			if v.outSec >= 0 && v.outSec <= v.inSec {
				v.outSec = -1
			}
			return
		}
		next := mpNextMark(v.markers, pos)
		if next <= v.inSec {
			next = -1
		}
		v.outSec = next
	})
}

// mpMarkStartAt returns the start offset of the track playing at cur (0 before track 1).
func mpMarkStartAt(marks []mpMark, cur float64) float64 {
	s := 0.0
	for _, m := range marks {
		if m.off <= cur+0.001 {
			s = m.off
		} else {
			break
		}
	}
	return s
}

// mpNextMark returns the next track start after cur (-1 if none).
func mpNextMark(marks []mpMark, cur float64) float64 {
	for _, m := range marks {
		if m.off > cur+0.1 {
			return m.off
		}
	}
	return -1
}

// mpPreview plays from the trim IN point (start playback first if idle).
func (u *UI) mpPreview(host string) {
	t := u.mpSnap(host)
	m := t.activeMedia()
	if m == nil {
		return
	}
	local := clampF(t.inSec-t.mediaStart(t.active), 0, math.Max(m.dur, 0))
	if tr := u.mpEngineState(&t, m); tr.loaded {
		u.mpSeekAxis(host, t.inSec)
		if m.kind == "video" && tr.paused {
			u.mpVidEval(host, "v.play().catch(function(){})")
			u.mpMut(host, func(v *mpSt) { v.vid.paused = false })
		}
		return
	}
	u.mpStartPlayback(host, *m, local)
}

// mpJump seeks the active engine to an axis offset, starting playback first if idle.
func (u *UI) mpJump(host string, axisSec float64) {
	t := u.mpSnap(host)
	m := t.activeMedia()
	if m == nil {
		return
	}
	if tr := u.mpEngineState(&t, m); tr.loaded {
		u.mpSeekAxis(host, axisSec)
		if m.kind == "video" && tr.paused {
			u.mpVidEval(host, "v.play().catch(function(){})")
			u.mpMut(host, func(v *mpSt) { v.vid.paused = false })
		}
		return
	}
	local := clampF(axisSec-t.mediaStart(t.active), 0, math.Max(m.dur, 0))
	u.mpStartPlayback(host, *m, local)
}

// mpVidTick applies a <video> element event ("cur|dur|paused01") to the mirror.
func (u *UI) mpVidTick(host, val string) {
	parts := strings.Split(val, "|")
	if len(parts) != 3 {
		return
	}
	cur, _ := strconv.ParseFloat(parts[0], 64)
	dur, _ := strconv.ParseFloat(parts[1], 64)
	paused := parts[2] == "1"
	t := u.mpMut(host, func(v *mpSt) {
		v.vid = mpVid{cur: cur, dur: dur, paused: paused, started: true}
		// late-bind the video duration from the element when peaks couldn't provide one
		for i := range v.media {
			if v.media[i].kind == "video" && dur > v.media[i].dur+1 {
				v.media[i].dur = dur
			}
		}
	})
	if m := t.activeMedia(); m != nil && m.kind == "video" {
		u.mpPatchTime(t)
	}
}

// mpTrackStep jumps to the previous/next tracklist marker relative to the playhead.
func (u *UI) mpTrackStep(host string, dir int) {
	t := u.mpSnap(host)
	if len(t.markers) == 0 {
		return
	}
	pos := u.mpRefPos(&t)
	idx := mpMarkIdxAt(t.markers, pos)
	idx += dir
	if idx < 0 {
		idx = 0
	}
	if idx >= len(t.markers) {
		idx = len(t.markers) - 1
	}
	u.mpJump(host, t.markers[idx].off)
}

// mpAutoMode runs one auto-trim menu entry (condensed smart-select).
func (u *UI) mpAutoMode(host, mode string) {
	switch mode {
	case "tracks":
		u.mpApplyTrim(host, func(v *mpSt) {
			if v.firstTrackSec >= 0 {
				v.inSec = v.firstTrackSec
			}
			if v.lastTrackEndSec > 0 {
				v.outSec = v.lastTrackEndSec
			}
		})
	case "fader":
		u.mpApplyTrim(host, func(v *mpSt) {
			if v.firstTrackSec >= 0 {
				v.inSec = v.firstTrackSec
			}
			if v.lastFaderSec > 0 {
				v.outSec = v.lastFaderSec
			}
		})
	case "silence":
		u.mpSilence(host)
	case "snapin":
		u.mpSnapTrack(host, "in")
	case "snapout":
		u.mpSnapTrack(host, "out")
	case "clear":
		u.mpApplyTrim(host, func(v *mpSt) { v.inSec, v.outSec = 0, -1 })
	}
}

// ── auto-trim: silence detection ───────────────────────────────────────────────

func (u *UI) mpSilence(host string) {
	t := u.mpSnap(host)
	m := t.activeMedia()
	if m == nil || t.detecting {
		return
	}
	gen := t.gen
	t = u.mpMut(host, func(v *mpSt) { v.detecting = true })
	u.mpPatchEdit(t)
	path, dur, start := m.path, m.dur, t.mediaStart(t.active)
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		s, err := setedit.DetectSilence(ctx, path, dur)
		t2 := u.mpMut(host, func(v *mpSt) {
			v.detecting = false
			if err != nil || v.gen != gen {
				return
			}
			v.inSec = s.LeadEndSec + start
			if s.TailStartSec > 0 && (dur <= 0 || s.TailStartSec < dur) {
				v.outSec = s.TailStartSec + start
			}
		})
		if err != nil {
			u.logErr("player silence", err)
			u.toast(i18n.T("player.toast.silenceFailed") + err.Error())
		}
		if len(t2.media) == 0 {
			return
		}
		u.mpPatchEdit(t2)
		u.mpPatchWave(t2)
	})
}

// ── dual-media alignment (setalign; envelopes from the probe worker) ────────────

// mpMaybeAlign computes the audio↔video offset for a dual pair. force re-runs and
// overrides a manual nudge; otherwise cached/manual/finished states are kept.
func (u *UI) mpMaybeAlign(host string, force bool) {
	t := u.mpSnap(host)
	if !t.dual() || t.align.state == "run" {
		return
	}
	if !force && (t.align.state == "ok" || t.align.manual) {
		return
	}
	if u.svc.Workers == nil {
		return
	}
	gen := t.gen
	aPath, vPath := t.media[0].path, t.media[1].path
	prior, hasPrior := t.align.off, t.align.priorOK || t.align.state == "ok" || t.align.manual

	// cached result?
	cacheKey := aPath + "\x1f" + vPath
	mtime := fileMtime(aPath) + fileMtime(vPath)
	if !force && u.svc.Store != nil {
		if raw, ok := u.svc.Store.GetAnalysis(store.KindAlign, cacheKey, mtime); ok {
			var r setalign.Result
			if json.Unmarshal(raw, &r) == nil {
				t2 := u.mpMut(host, func(v *mpSt) {
					if v.gen != gen {
						return
					}
					v.align = mpAlignSt{state: "ok", off: r.OffsetSec, conf: r.Confidence, label: r.Label(), priorOK: v.align.priorOK}
				})
				u.mpPatchAll(t2)
				return
			}
		}
	}

	stage := func(pct float64, msg string) {
		t2 := u.mpMut(host, func(v *mpSt) {
			if v.gen != gen {
				return
			}
			v.align.state, v.align.pct, v.align.msg = "run", pct, msg
		})
		if t2.gen == gen {
			u.mpPatchEdit(t2)
		}
	}
	fail := func(err error) {
		u.logErr("player align", err)
		t2 := u.mpMut(host, func(v *mpSt) {
			if v.gen != gen {
				return
			}
			v.align.state, v.align.msg = "err", err.Error()
		})
		if t2.gen == gen {
			u.mpPatchEdit(t2)
		}
	}

	stage(3, i18n.T("player.label.stageAudioEnv"))
	u.bg(func() {
		envA, rate, err := u.mpEnvelope(aPath)
		if err != nil {
			fail(err)
			return
		}
		stage(40, i18n.T("player.label.stageVideoEnv"))
		envB, _, err := u.mpEnvelope(vPath)
		if err != nil {
			fail(err)
			return
		}
		stage(80, i18n.T("player.label.stageCrossCorr"))
		window := 240.0
		if !hasPrior {
			window = 0
		}
		res, err := setalign.Align(envA, envB, rate, prior, hasPrior, window)
		if err != nil && hasPrior {
			// prior may be wrong (clock skew) - retry over the full range
			res, err = setalign.Align(envA, envB, rate, 0, false, 0)
		}
		if err != nil {
			fail(err)
			return
		}
		if u.svc.Store != nil {
			if raw, merr := json.Marshal(res); merr == nil {
				u.svc.Store.PutAnalysis(store.KindAlign, cacheKey, mtime, raw)
			}
		}
		t2 := u.mpMut(host, func(v *mpSt) {
			if v.gen != gen {
				return
			}
			v.align = mpAlignSt{state: "ok", off: res.OffsetSec, conf: res.Confidence, label: res.Label(), priorOK: v.align.priorOK}
		})
		if t2.gen == gen {
			u.mpPatchAll(t2)
			u.toast(i18n.T("player.toast.aligned", i18n.A{"offset": mpSignedClock(res.OffsetSec), "conf": res.Label()}))
		}
	})
}

// mpEnvelope fetches an RMS envelope via the probe worker.
func (u *UI) mpEnvelope(path string) ([]float64, float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	raw, err := u.svc.Workers.RunBackground(ctx, "probe", "probe.envelope", map[string]any{"path": path, "rateHz": 50})
	if err != nil {
		return nil, 0, err
	}
	var r struct {
		Env    string  `json:"env"`
		RateHz float64 `json:"rateHz"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Env == "" || r.RateHz <= 0 {
		return nil, 0, errors.New("empty envelope")
	}
	data, derr := base64.StdEncoding.DecodeString(r.Env)
	if derr != nil || len(data) < 4 {
		return nil, 0, errors.New("bad envelope payload")
	}
	env := make([]float64, len(data)/4)
	for i := range env {
		bits := uint32(data[4*i]) | uint32(data[4*i+1])<<8 | uint32(data[4*i+2])<<16 | uint32(data[4*i+3])<<24
		env[i] = float64(math.Float32frombits(bits))
	}
	return env, r.RateHz, nil
}

// mpNudge shifts the video offset manually (delta seconds).
func (u *UI) mpNudge(host string, delta float64) {
	t := u.mpMut(host, func(v *mpSt) {
		if !v.dual() {
			return
		}
		v.align.off += delta
		v.align.manual = true
	})
	if t.dual() {
		u.mpPatchWave(t)
		u.mpPatchEdit(t)
	}
}

// mpOffField applies a typed video offset ("-1:23.5" / "37.2").
func (u *UI) mpOffField(host, val string) {
	s := strings.TrimSpace(val)
	neg := strings.HasPrefix(s, "-")
	sec, ok := pubParseClock(strings.TrimPrefix(s, "-"))
	if !ok || sec < 0 {
		u.toast(i18n.T("player.toast.offsetFormat"))
		return
	}
	if neg {
		sec = -sec
	}
	t := u.mpMut(host, func(v *mpSt) {
		if !v.dual() {
			return
		}
		v.align.off = sec
		v.align.manual = true
	})
	if t.dual() {
		u.mpPatchWave(t)
		u.mpPatchEdit(t)
	}
}

// ── export (same backend job as the old trim modal: transcode.run TrimStart/TrimEnd) ──

// mpExportPlan is one transcode job of an export run.
type mpExportPlan struct {
	idx    int
	path   string
	preset transcode.Preset
	out    string
	trimS  float64
	trimE  float64 // 0 = to end
}

// mpPlanExport resolves the per-media cut ranges. which: "both" or a media index.
func (u *UI) mpPlanExport(t *mpSt, which string) ([]mpExportPlan, error) {
	var idxs []int
	if which == "both" {
		for i := range t.media {
			idxs = append(idxs, i)
		}
	} else {
		i := atoi(which)
		if i < 0 || i >= len(t.media) {
			return nil, errors.New("no media")
		}
		idxs = []int{i}
	}
	outEff := t.axisOutEff()
	var plans []mpExportPlan
	for _, i := range idxs {
		m := t.media[i]
		start := t.mediaStart(i)
		dur := m.dur
		if dur <= 0 {
			return nil, fmt.Errorf("%s duration unknown - wait for the waveform analysis", m.kind)
		}
		s := clampF(t.inSec-start, 0, dur)
		e := clampF(outEff-start, 0, dur)
		if e-s < 0.1 {
			return nil, fmt.Errorf("the trim range doesn't overlap the %s recording", m.kind)
		}
		preset := mpPreset(u, m.presetID)
		out := m.outPath
		if out == "" {
			out = mpOutPath(m.path, preset)
		}
		te := e
		if e >= dur-0.05 {
			te = 0 // transcode.run treats trimEnd<=start as "to end"
		}
		plans = append(plans, mpExportPlan{idx: i, path: m.path, preset: preset, out: out, trimS: s, trimE: te})
	}
	return plans, nil
}

func (u *UI) mpRunExport(host, which string) {
	t := u.mpSnap(host)
	if len(t.media) == 0 || t.exporting {
		return
	}
	plans, err := u.mpPlanExport(&t, which)
	if err != nil {
		u.toast(i18n.T("player.toast.export") + err.Error())
		return
	}
	gen := t.gen
	t = u.mpMut(host, func(v *mpSt) { v.exporting, v.exportPct, v.exportMsg = true, 0, "" })
	u.mpPatchExport(t)
	if len(plans) == 1 {
		u.toast(i18n.T("player.toast.exportingCut", i18n.A{"preset": plans[0].preset.Label}))
	} else {
		u.toast(i18n.Tn("player.toast.exportingCuts", len(plans)))
	}
	u.bg(func() { u.mpExportRunAll(host, gen, plans) })
}

// mpExportRunAll runs the planned jobs sequentially, folding progress into one bar.
func (u *UI) mpExportRunAll(host string, gen int, plans []mpExportPlan) {
	n := float64(len(plans))
	var outs []string
	for i, p := range plans {
		base := float64(i) / n * 100
		onPct := func(pct float64) {
			t := u.mpMut(host, func(v *mpSt) {
				if v.gen == gen {
					v.exportPct = base + pct/n
				}
			})
			if t.gen == gen {
				u.mpPatchExport(t)
			}
		}
		params := map[string]any{"input": p.path, "output": p.out, "preset": p.preset,
			"trimStart": p.trimS, "trimEnd": p.trimE}
		if err := u.mpExportOne(params, onPct); err != nil {
			u.mpExportDone(host, gen, false, err.Error(), outs)
			return
		}
		outs = append(outs, p.out)
	}
	u.mpExportDone(host, gen, true, "", outs)
}

// mpExportOne runs one transcode job (shared Hub when available, else the worker pool) and
// blocks until it finishes.
func (u *UI) mpExportOne(params map[string]any, onPct func(float64)) error {
	onProgress := func(event string, data json.RawMessage) {
		if event != "progress" {
			return
		}
		var p struct {
			Percent float64 `json:"percent"`
		}
		if json.Unmarshal(data, &p) == nil {
			onPct(p.Percent)
		}
	}
	if u.svc.Hub != nil { // shared transcode hub → live progress + queue visibility
		done := make(chan error, 1)
		jid := fmt.Sprintf("mpcut-%d", time.Now().UnixNano())
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
		return <-done
	}
	if u.svc.Workers == nil {
		return errors.New("worker pool unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	_, err := u.svc.Workers.RunStream(ctx, "transcode", "transcode.run", params, onProgress)
	return err
}

func (u *UI) mpExportDone(host string, gen int, ok bool, errTx string, outs []string) {
	t := u.mpMut(host, func(v *mpSt) {
		if v.gen != gen {
			return
		}
		v.exporting = false
		if ok {
			v.exportPct, v.exportMsg = 100, i18n.T("player.label.exportDone")+strings.Join(outs, " · ")
		} else {
			v.exportMsg = i18n.T("player.label.exportMsgFailed") + errTx
		}
	})
	if ok {
		names := make([]string, len(outs))
		for i, o := range outs {
			names[i] = filepath.Base(o)
		}
		u.toast(i18n.T("player.toast.cutExported") + strings.Join(names, ", "))
	} else {
		u.logErr("player export", errors.New(errTx))
		u.toast(i18n.T("player.toast.exportFailed") + errTx)
	}
	if t.gen == gen {
		u.mpPatchExport(t)
	}
	if u.activeTab() == "publish" {
		u.patchMain() // refresh the sets/captures list
	}
}

// mpPreset resolves a preset id (default: the instant Lossless Remux, like Fyne).
func mpPreset(u *UI, id string) transcode.Preset {
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	presets := transcode.AllPresets(custom)
	for _, p := range presets {
		if p.ID == id {
			return p
		}
	}
	for _, p := range presets {
		if p.ID == "remux" {
			return p
		}
	}
	if len(presets) > 0 {
		return presets[0]
	}
	return transcode.Preset{}
}

// mpExt is the output extension (no dot) - preset container, else the source's own.
func mpExt(file string, preset transcode.Preset) string {
	if preset.Container != "" {
		return preset.Container
	}
	return strings.TrimPrefix(filepath.Ext(file), ".")
}

// mpOutPath mirrors Fyne trimOutPath: unique "<base>-cut[-n].<ext>" beside the source.
func mpOutPath(file string, preset transcode.Preset) string {
	dir := filepath.Dir(file)
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	ext := mpExt(file, preset)
	out := filepath.Join(dir, base+"-cut."+ext)
	for i := 2; pubFileExists(out); i++ {
		out = filepath.Join(dir, fmt.Sprintf("%s-cut-%d.%s", base, i, ext))
	}
	return out
}

// ── small helpers (moved from the retired trim modal) ───────────────────────────

// pubClockF formats seconds as m:ss.d (h:mm:ss.d past an hour) - the trim fields' format.
func pubClockF(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	t := int(math.Round(sec * 10))
	d, s, m, h := t%10, (t/10)%60, (t/600)%60, t/36000
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d.%d", h, m, s, d)
	}
	return fmt.Sprintf("%d:%02d.%d", m, s, d)
}

// mpSignedClock renders a possibly-negative offset ("-1:23.5").
func mpSignedClock(sec float64) string {
	if sec < 0 {
		return "-" + pubClockF(-sec)
	}
	return pubClockF(sec)
}

// pubParseClock parses "mm:ss.s", "h:mm:ss", or plain seconds; "end"/"" → (-1, true).
func pubParseClock(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "end" {
		return -1, true
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			return 0, false
		}
		total = total*60 + v
	}
	return total, true
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampF(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

func pubFileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func fileSize(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.Size()
	}
	return 0
}

func fileMtime(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime().Unix()
	}
	return 0
}
