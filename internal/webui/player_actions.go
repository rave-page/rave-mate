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

	"rave.page/mate/internal/audio"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/mp4frag"
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
// transports are superseded). One instance per (UI, host surface "publish"/"library"),
// keyed acts (`mp-<verb>:<host>`), package-level under mpMu - a headless remote session
// must never share (or clobber) the window's player state.
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
	drops     []float64           // drop markers (libdb enrichment; render-only)

	size         int64
	peaks        []byte
	bands        []byte // 3 uint8 (low,mid,high) per peak bucket - spectral waveform colour
	dur          float64
	peaksLoading bool
	peaksErr     string

	loud        *mpLoud
	loudLoading bool

	src        *transcode.SourceInfo
	srcLoading bool

	// seekTabLoading: a FLAC SEEKTABLE retrofit scan is running for this path (chip in the
	// wave overlay - first-load seek prep is visible, never a silent hang).
	seekTabLoading bool

	// fragOK: a fragmented-MP4 stream index is cached for this path, so the embedded <video>
	// renders the MSE variant (data-mse) instead of plain src - Chromium's demuxer would
	// range-scan every moof of a multi-GB fMP4 before playing (30 s+ on a busy disk).
	fragOK bool

	presetID string // export preset (default: lossless remux)
	outPath  string // export destination ("" = auto "<base>-cut.<ext>")
	// inline is an unsaved preset from the export preset editor ("apply without saving");
	// wins over presetID until the preset select changes. draft is the editor's open
	// working copy (nil = editor closed).
	inline *transcode.Preset
	draft  *transcode.Preset

	// measured is the exact pass-1 loudness measurement for measKey (path+mtime+trim window),
	// from the store cache or captured off a finished export's worker event. nil = estimate only.
	measured *transcode.Measurement
	measKey  string

	// loudOv is this export's loudness override of presetID's own block, applied in mpPlanExport
	// via transcode.ApplyLoudnessOverride. Off = don't override (never "force off"); 0 targets
	// resolve to transcode's defaults. Per media: an audio/video pair exports two files.
	loudOv loudnessVals
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

	exporting    bool
	exportPct    float64
	exportMsg    string
	exportStage  string // "queued" (pre-worker) | worker stages "prepare"/"measure"/"encode"
	exportScope  string // dual export target: "both" | media index
	exportLoudTx string // applied-loudness line captured off the worker's "loudness" event
	exportCancel func() // cancels the in-flight job (Hub.Cancel / worker ctx); nil = none

	monitorLoud bool // pre-listen: planned loudness gain applied to the audio engine

	vid          mpVid // embedded <video> transport mirror
	lastTrackIdx int   // marker index at playhead (transport current-track display)

	dragGen int // bumped on drag down/up (+ reset); queued coalesced moves from an older gen are stale

	// audio transport RPC guard: blocking PlayerProxy calls run off the act worker with an
	// optimistic engine-state override until the proxy mirror reconciles (mpAudCall).
	audBusy     bool      // one in-flight transport RPC per host
	audLoading  bool      // first-load PlayFrom in flight (transport shows "Loading audio…")
	audOpt      string    // "" none | "play" | "pause" | "stop" (mpSampleEng override)
	audOptUntil time.Time // override expiry (belt-and-braces if the RPC hangs)
	audPend     func()    // one-slot latest-wins queued intent (runs when the in-flight RPC lands)
	audPendOpt  string

	// --- phaseb-b4player ---
	// eng is this snapshot's ONE engine sample (see mpTr / mpEng). nil = not sampled yet.
	// Lives on the render COPY only: a sample stored on the instance would go stale.
	eng *mpTr
	// pgen counts mutations of the instance (bumped by mpMut, never by mpSnap, preserved by
	// reset). A container build marks it before rendering and mpHeal re-reads it after the
	// container patch is enqueued - see "container-render ordering" below.
	pgen uint64
	// --- end phaseb-b4player ---
}

// mpVid mirrors the embedded <video> element (updated by its mp-vtick events).
type mpVid struct {
	cur, dur float64
	paused   bool
	started  bool   // element has reported at least one event
	err      string // decode/load failure (degrade honestly, no external window)

	// realtime preview stream (editor host): element plays a live /ms/ feed whose
	// clock starts at strT0 source-seconds; cur stays ABSOLUTE (t0 + element time).
	strURL  string
	strMime string
	strT0   float64
	strAuto bool // element autoplays when the fresh feed opens
	strLoop bool // feed is the whole bounded IN→OUT span: element loops it natively
	// strPend: a pipeline respawn is in flight, so the element is STILL playing the old feed and
	// its clock is a lie. Ticks must not overwrite cur while this is set, or the seek the user just
	// asked for gets stomped back to the pre-seek position ~1s later (click did nothing; the NEXT
	// click appeared to apply the previous one).
	strPend bool
}

// mpStreamCtl is the host hook driving a realtime preview pipeline ("seek"/"play"/
// "stop"/"pause", t = absolute source seconds). Returns true when the hook handled
// the verb (transport skips its default element eval). Set by the editor.
var mpStreamCtl func(u *UI, host, verb string, t float64) bool

// mpTrimSnap, when set, is called before every trim edit so a host can checkpoint
// its undo history (the trim lives in mpSt, outside the host's own state).
var mpTrimSnap func(u *UI, host string)

// mpVidGrip lets a host make its video box vertically resizable: returns the drag act
// and the persisted height cap in px ("" = CSS default). Registered by the editor.
var mpVidGrip func(u *UI, host string) (grip, maxH string)

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

// ── loudness gain plan (instant estimate from the cached timeline; exact once measured) ──

// mpPlan is what the configured normalization will do to THIS export: source loudness over
// the trim window, the single gain the encoder will apply, and where the result lands.
// Estimate (exact=false) comes from the store-cached momentary timeline - zero I/O, updates
// live as the user drags trim bounds or changes targets. Exact numbers come from a cached
// (or just-run) ffmpeg loudnorm pass over the same window.
type mpPlan struct {
	on      bool // effective preset normalizes
	applies bool // audio codec re-encodes (copy/none can't normalize)
	haveSrc bool // source loudness known (timeline or measure landed)

	srcI  float64 // integrated loudness over the trim window (LUFS)
	srcTP float64 // true peak (dBTP; whole-file until measured exactly)
	exact bool    // srcI/srcTP from a real loudnorm pass (not the timeline estimate)

	targetI, ceilTP float64
	res             transcode.GainPlan
	outI            float64 // projected integrated loudness after the gain
}

// mpActivePreset resolves media's preset: the unsaved inline editor result wins over presetID.
func (u *UI) mpActivePreset(m *mpMedia) transcode.Preset {
	if m.inline != nil {
		return *m.inline
	}
	return mpPreset(u, m.presetID)
}

// mpEffPreset is the preset the export will really run: active preset + this media's
// loudness override folded in (same fold as mpPlanExport - keep them identical).
func (u *UI) mpEffPreset(m *mpMedia) transcode.Preset {
	return transcode.ApplyLoudnessOverride(u.mpActivePreset(m),
		m.loudOv.On, m.loudOv.I, m.loudOv.TP, m.loudOv.RaiseOnly)
}

// mpTrimWindow returns media i's local cut window [s, e) in media seconds (e=0 → to end).
func (t *mpSt) mpTrimWindow(i int) (s, e float64) {
	if i < 0 || i >= len(t.media) {
		return 0, 0
	}
	m := &t.media[i]
	start, dur := t.mediaStart(i), m.dur
	if dur <= 0 {
		return 0, 0
	}
	s = clampF(t.inSec-start, 0, dur)
	e = clampF(t.axisOutEff()-start, 0, dur)
	if e >= dur-0.05 {
		e = 0
	}
	return s, e
}

// mpMeasKey keys an exact loudness measurement to its input identity: window + file mtime.
func mpMeasKey(path string, winS, winE float64) string {
	return fmt.Sprintf("%.2f|%.2f|%d", winS, winE, fileMtime(path))
}

// mpMeasStoreKey is the store key for an exact windowed measurement (KindLoudness).
func mpMeasStoreKey(path string, winS, winE float64) string {
	return fmt.Sprintf("%s\x1f%.2f\x1f%.2f", path, winS, winE)
}

// mpPlanFor computes the loudness plan for media i of snapshot t (nil when normalization is
// off). Pure CPU over the in-memory timeline - safe to call from render.
func (u *UI) mpPlanFor(t *mpSt, i int) *mpPlan {
	if i < 0 || i >= len(t.media) {
		return nil
	}
	m := &t.media[i]
	eff := u.mpEffPreset(m)
	eff = transcode.MigrateLoudness(eff)
	if !eff.LoudnessOn {
		return nil
	}
	p := &mpPlan{on: true, applies: transcode.LoudnessAppliesTo(eff.AudioCodec),
		targetI: eff.LoudnessI, ceilTP: eff.EffectiveTP()}
	if p.targetI == 0 {
		p.targetI = transcode.DefaultLoudnessI
	}
	winS, winE := t.mpTrimWindow(i)
	if m.measured != nil && m.measKey == mpMeasKey(m.path, winS, winE) {
		p.srcI, p.srcTP, p.exact, p.haveSrc = m.measured.I, m.measured.TP, true, true
	} else if l := m.loud; l != nil {
		if est, ok := transcode.IntegrateMomentary(l.Mom, l.Step, winS, winE); ok {
			p.srcI, p.srcTP, p.haveSrc = est, l.TP, true
		}
	}
	if !p.haveSrc {
		return p
	}
	p.res = transcode.PlanGain(transcode.Measurement{I: p.srcI, TP: p.srcTP},
		p.targetI, p.ceilTP, eff.LoudnessRaiseOnly)
	p.outI = p.srcI + p.res.GainDB
	return p
}

// mpKickMeasure checks the store for an exact windowed measurement of every media whose
// normalization is on (off-thread; render never blocks). Found → the plan flips to exact.
func (u *UI) mpKickMeasure(host string) {
	if u.svc.Store == nil {
		return
	}
	t := u.mpSnap(host)
	gen := t.gen
	for i := range t.media {
		m := t.media[i]
		if eff := u.mpEffPreset(&m); !eff.LoudnessOn {
			continue
		}
		winS, winE := t.mpTrimWindow(i)
		key := mpMeasKey(m.path, winS, winE)
		if m.measKey == key {
			continue // current (found or already checked)
		}
		idx, path := i, m.path
		u.mpMut(host, func(v *mpSt) {
			if v.gen == gen && idx < len(v.media) {
				v.media[idx].measKey = key // mark checked; bg fills measured on a hit
				v.media[idx].measured = nil
			}
		})
		u.bg(func() {
			raw, ok := u.svc.Store.GetAnalysis(store.KindLoudness, mpMeasStoreKey(path, winS, winE), fileMtime(path))
			if !ok {
				return
			}
			var mm transcode.Measurement
			if json.Unmarshal(raw, &mm) != nil {
				return
			}
			applied := u.mpApply(host, gen, idx, func(md *mpMedia) {
				if md.measKey == key {
					md.measured = &mm
				}
			})
			if applied {
				u.mpPatchExport(u.mpSnap(host))
				u.mpSyncMonitor(host)
			}
		})
	}
}

// mpSyncMonitor re-pushes the pre-listen gain when loudness monitoring is on: the active
// media's planned gain (0 when the plan is off/unknown/skipped). Fire-and-forget RPC.
func (u *UI) mpSyncMonitor(host string) {
	t := u.mpSnap(host)
	if !t.monitorLoud {
		return
	}
	pl := u.player()
	if pl == nil {
		return
	}
	g := 0.0
	if p := u.mpPlanFor(&t, t.active); p != nil && p.haveSrc && p.applies && !p.res.Skipped {
		g = p.res.GainDB
	}
	pl.SetPreGainDB(g)
}

// mpMonitorOff clears the pre-listen gain (leaving a bound surface / toggling off).
func (u *UI) mpMonitorOff(host string) {
	u.mpMut(host, func(v *mpSt) { v.monitorLoud = false })
	if pl := u.player(); pl != nil {
		pl.SetPreGainDB(0)
	}
}

// mpNone marks "no position" - axis times can be legitimately negative (video
// starting before the audio recording), so -1 is not a safe sentinel.
const mpNone = -1e9

// mpIsSet reports v carries a real position.
func mpIsSet(v float64) bool { return v > mpNone/2 }

var (
	mpMu        sync.Mutex
	mpInstances = map[*UI]map[string]*mpSt{} // per-UI: releaseUIState drops a retired session's entry
)

// mp returns this UI's instance for host (created empty on first use).
func (u *UI) mp(host string) *mpSt {
	mpMu.Lock()
	defer mpMu.Unlock()
	hosts := mpInstances[u]
	if hosts == nil {
		hosts = map[string]*mpSt{}
		mpInstances[u] = hosts
	}
	t := hosts[host]
	if t == nil {
		t = &mpSt{host: host, outSec: -1, viewSpan: 1, cursorSec: mpNone, hovT: mpNone,
			firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1}
		hosts[host] = t
	}
	return t
}

// mpMut mutates the instance under mpMu and returns a copy for rendering. Bumps pgen (mpHeal's
// ordering counter) - a caller that only reads uses mpSnap.
func (u *UI) mpMut(host string, fn func(*mpSt)) mpSt { return u.mpCopy(host, fn) }

// mpSnap returns a render copy without mutating (pgen unchanged).
func (u *UI) mpSnap(host string) mpSt { return u.mpCopy(host, nil) }

// mpCopy is the ONE snapshot funnel: mutate (optional, bumping pgen), copy, then take the
// snapshot's single engine sample - outside mpMu, since the sample locks the proxy mirror.
func (u *UI) mpCopy(host string, fn func(*mpSt)) mpSt {
	t := u.mp(host)
	mpMu.Lock()
	if fn != nil {
		fn(t)
		t.pgen++
	}
	c := *t
	c.media = append([]mpMedia(nil), t.media...)
	mpMu.Unlock()
	c.eng = nil // never inherit the instance's sample: this snapshot gets its own
	u.mpEng(&c)
	return c
}

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

// player is the ONE choke point for the shared audio engine: nil for a headless (remote-
// mirror) session - remote control must never start/stop/seek audio on THIS machine.
func (u *UI) player() *featurehost.PlayerProxy {
	if u.virtual() {
		return nil
	}
	return u.svc.Player
}

// mpAudMirror is the mirror mpSampleEng reads: the audio engine, or the test override. nil =
// no engine (headless session, stub build, no player service).
func (u *UI) mpAudMirror() mpMirror {
	if u.mpMirrorOv != nil {
		return u.mpMirrorOv
	}
	if pl := u.player(); pl != nil {
		return pl
	}
	return nil
}

// playerGateKey picks the "no audio" toast: headless sessions gate audio by design.
func (u *UI) playerGateKey() string {
	if u.virtual() {
		return "player.toast.remoteAudioOff"
	}
	return "player.toast.playerUnavailable"
}

// mpTr is a media-local transport snapshot: ONE sample of the engine, taken per mpSt snapshot
// (mpSt.eng) and read by every consumer of that snapshot.
//
// It used to be re-sampled per consumer (`mpEngineState(&t, m)`), which meant THREE samples in one
// component render - wave playhead, hover readout, transport row - and up to five in one mpTick.
// Both inputs move between samples: the featurehost mirror is rewritten by the child's ~5 Hz tick
// events (and zeroed outright by fireEnd), and the audOpt override expires on a wall clock. So one
// DOM could carry a moving playhead over an idle transport, or a hover readout naming a different
// position than the playhead beside it. The workaround for the resulting flicker was the ~1 Hz
// re-patch that healed it a tick later; sampling once makes the torn DOM unrepresentable.
type mpTr struct {
	loaded          bool // the engine currently has this file
	playing, paused bool
	cur, total      float64
}

// mpMirror is the audio-engine surface the sampler reads (featurehost.PlayerProxy's mirrored
// playback snapshot).
type mpMirror interface{ State() featurehost.State }

// mpEng returns the snapshot's engine sample, taking it on first read. Snapshots from
// mpMut/mpSnap arrive pre-sampled; a hand-built mpSt (tests, fixtures) samples here, so no path
// can render against a zero transport by accident.
func (u *UI) mpEng(t *mpSt) mpTr {
	if t.eng == nil {
		tr := u.mpSampleEng(t)
		t.eng = &tr
	}
	return *t.eng
}

// mpSampleEng samples the engine for t's ACTIVE media - every consumer asks about that one
// (TestMpEngAsksAboutActiveMedia pins it). Only mpEng calls this.
func (u *UI) mpSampleEng(t *mpSt) mpTr {
	m := t.activeMedia()
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
	pl := u.mpAudMirror()
	if pl == nil {
		return mpTr{}
	}
	st := pl.State()
	if st.Path != m.path || !st.Playing {
		return mpTr{}
	}
	if t.audOpt != "" && time.Now().Before(t.audOptUntil) { // optimistic while an RPC is in flight
		if t.audOpt == "stop" {
			return mpTr{}
		}
		paused := t.audOpt == "pause"
		return mpTr{loaded: true, playing: !paused, paused: paused, cur: st.Cur, total: st.Total}
	}
	return mpTr{loaded: true, playing: !st.Paused, paused: st.Paused, cur: st.Cur, total: st.Total}
}

// mpAudCall runs one blocking PlayerProxy RPC off the act worker (a wedged child stalls them up
// to 3-10s - inline they froze the whole acts lane). opt = optimistic mpSampleEng override
// while in flight ("" = none); the proxy mirror reconciles on completion. Busy = the press
// parks in a one-slot latest-wins pending intent (no stacking; a rapid re-tap during the
// ~250ms halt window used to be silently dropped) executed when the in-flight RPC lands.
func (u *UI) mpAudCall(host, opt string, fn func()) {
	queued := false
	u.mpMut(host, func(v *mpSt) {
		if v.audBusy {
			v.audPend, v.audPendOpt = fn, opt // newest wins
			queued = true
			return
		}
		v.audBusy, v.audOpt, v.audOptUntil = true, opt, time.Now().Add(15*time.Second)
	})
	if queued {
		return
	}
	u.bg(func() {
		for {
			fn()
			var next func()
			t := u.mpMut(host, func(v *mpSt) {
				if v.audPend != nil { // chain the parked intent (same busy slot, no stacking)
					next, v.audPend = v.audPend, nil
					v.audOpt, v.audOptUntil = v.audPendOpt, time.Now().Add(15*time.Second)
					v.audPendOpt = ""
					return
				}
				v.audBusy, v.audOpt = false, ""
			})
			if next == nil {
				if len(t.media) > 0 {
					u.mpPatchTransport(t)
					u.mpPatchWave(t)
				}
				return
			}
			fn = next
		}
	})
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
	tr := u.mpEng(&t)
	if tr.loaded {
		if m.kind == "video" {
			switch {
			case t.vid.strURL != "" && tr.paused && mpStreamCtl != nil && mpStreamCtl(u, host, "play", tr.cur):
				// stream host restarted/owns the resume - element autoplays on the fresh feed
			case t.vid.strURL != "":
				u.mpVidEval(host, "if(v.paused){v.play().catch(function(){v.muted=true;v.play().catch(function(){})})}else{v.pause()}")
				if !tr.paused && mpStreamCtl != nil {
					mpStreamCtl(u, host, "pause", tr.cur) // idle-reap bookkeeping
				}
			default:
				// Resume must survive a rebuilt element (patch replaced it → currentTime lost) and an
				// autoplay-policy rejection (retry muted, never swallow into a dead transport). At/past
				// the OUT marker, play loops from IN.
				u.mpVidEval(host, fmt.Sprintf(
					"if(v.paused){var i=parseFloat(v.dataset.in||'0'),o=parseFloat(v.dataset.out||'-1');"+
						"if(o>0&&v.currentTime>=o-0.05){try{v.currentTime=i}catch(e){}}"+
						"else if(%.3f>0.05&&Math.abs(v.currentTime-%.3f)>1.0){try{v.currentTime=%.3f}catch(e){}}"+
						"v.play().catch(function(){v.muted=true;v.play().catch(function(){})})}"+
						"else{v.pause()}", tr.cur, tr.cur, tr.cur))
			}
			u.mpMut(host, func(v *mpSt) { v.vid.paused = !v.vid.paused }) // optimistic; vtick reconciles
		} else {
			opt := "pause" // optimistic flip; blocking toggle RPC off the act worker
			if tr.paused {
				opt = "play"
			}
			// resume at/past the OUT marker loops from IN (mirrors the video element path)
			loopIn := tr.paused && t.outSec > 0 && tr.cur+t.mediaStart(t.active) >= t.outSec-0.5
			inLocal := clampF(t.inSec-t.mediaStart(t.active), 0, math.Max(m.dur, 0))
			u.mpAudCall(host, opt, func() {
				if pl := u.player(); pl != nil {
					if loopIn {
						pl.SeekExplicit(inLocal)
					}
					pl.TogglePause()
				}
			})
		}
		t = u.mpSnap(host)
		u.mpPatchTransport(t)
		u.mpPushRealtime(t) // freeze/launch the rAF playhead NOW, not at the next 1 Hz tick
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
		t := u.mpMut(host, func(v *mpSt) {
			v.vid.started, v.vid.paused = true, false
			if seekTo > 0 {
				v.vid.cur = seekTo
			}
		})
		u.mpPatchTransport(t)
		u.mpPushRealtime(t)
		return
	}
	pl := u.player()
	if pl == nil {
		u.toast(i18n.T(u.playerGateKey()))
		return
	}
	path := m.path
	u.mpMut(host, func(t *mpSt) { t.audLoading = true })
	u.mpPatchTransport(u.mpSnap(host))
	u.mpAudCall(host, "", func() { // guarded: double-press can't stack Play RPCs
		// start offset rides the play RPC: the engine decodes at seekTo directly (no
		// position-0 blip, no seek respawn)
		if err := pl.PlayFrom(path, math.Max(seekTo, 0)); err != nil {
			u.logErr("player play", err)
			u.toast(i18n.T("player.toast.playFailed") + err.Error())
		}
		u.mpMut(host, func(t *mpSt) { t.audLoading = false })
	})
}

// mpStop halts playback and parks the playhead at the trim IN marker (0 when unset).
func (u *UI) mpStop(host string) {
	t := u.mpSnap(host)
	m := t.activeMedia()
	if m == nil {
		return
	}
	inLocal := clampF(t.inSec-t.mediaStart(t.active), 0, math.Max(m.dur, 0))
	if m.kind == "video" {
		if t.vid.strURL != "" && mpStreamCtl != nil && mpStreamCtl(u, host, "stop", inLocal) {
			// stream host respawned paused at IN
		} else {
			u.mpVidEval(host, fmt.Sprintf("v.pause();try{v.currentTime=%.3f}catch(e){}", inLocal))
			u.mpMut(host, func(v *mpSt) { v.vid.paused, v.vid.cur = true, inLocal })
		}
	} else if pl := u.player(); pl != nil {
		u.mpAudCall(host, "stop", func() { pl.Stop() }) // optimistic idle; async halt
		u.mpMut(host, func(v *mpSt) { v.cursorSec = v.inSec })
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
		if t.vid.strURL != "" && mpStreamCtl != nil && mpStreamCtl(u, host, "seek", local) {
			// stream host respawns the pipeline at the target
		} else {
			u.mpVidEval(host, fmt.Sprintf("v.currentTime=%.3f;", local))
		}
		u.mpMut(host, func(v *mpSt) { v.vid.cur = local })
		u.mpPatchWave(u.mpSnap(host))
		return
	}
	if tr := u.mpEng(&t); tr.loaded {
		if pl := u.player(); pl != nil {
			pl.SeekExplicit(local) // user click = real intent; bypass the noop guard
		}
	}
}

// ── playhead (axis seconds; -1 = none) ──────────────────────────────────────────

func (u *UI) mpPlayheadAxis(t *mpSt) float64 {
	m := t.activeMedia()
	if m == nil {
		return mpNone
	}
	tr := u.mpEng(t)
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
	if cur.monitorLoud {
		u.mpMonitorOff(host) // never carry a pre-listen gain onto different media
	}
	u.mpMut(host, func(t *mpSt) {
		t.reset()
		t.name = filepath.Base(path)
		t.media = []mpMedia{{path: path, kind: "audio", cues: tr.Cues, dur: dur,
			size: fileSize(path), presetID: "copy-audio", peaksLoading: true}}
	})
	u.mpKickAnalyses(host)
}

// mpSetDrops mirrors libdb drop markers onto the bound media (render-only).
func (u *UI) mpSetDrops(host, path string, drops []float64) {
	u.mpMut(host, func(v *mpSt) {
		for i := range v.media {
			if v.media[i].path == path {
				v.media[i].drops = append([]float64(nil), drops...)
			}
		}
	})
}

// capOnDiskFresh is how long the capture on-disk existence cache serves before a bg re-sweep
// (picks up removable/spun-down media that appeared or vanished).
const capOnDiskFresh = 10 * time.Second

// capExistCache caches capture-path on-disk existence off the render goroutine. Per-*UI (GC'd
// with the UI); mpEnsureSet reads capOnDiskOr so no os.Stat runs in render.
type capExistCache struct {
	mu   sync.Mutex
	ck   map[string]bool // path -> exists (last sweep)
	busy bool            // a sweep is in flight
	at   time.Time       // last sweep completion (TTL anchor)
}

// capEnsureOnDisk warms the capture existence cache for paths OFF-THREAD (per-capture os.Stat in
// mpEnsureSet froze the publish render on a spun-down/removable drive). Sweeps when a path is
// unknown or the cache is stale; re-renders the publish tab only when a flag actually changed.
func (u *UI) capEnsureOnDisk(paths []string) {
	c := &u.capFS
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return
	}
	stale := time.Since(c.at) > capOnDiskFresh
	unknown := false
	for _, p := range paths {
		if _, ok := c.ck[p]; !ok {
			unknown = true
			break
		}
	}
	if !stale && !unknown {
		c.mu.Unlock()
		return
	}
	c.busy = true
	c.mu.Unlock()
	want := append([]string(nil), paths...)
	u.bg(func() {
		res := make(map[string]bool, len(want))
		for _, p := range want {
			res[p] = pubFileExists(p)
		}
		c.mu.Lock()
		c.busy = false
		c.at = time.Now()
		if c.ck == nil {
			c.ck = make(map[string]bool, len(res))
		}
		changed := false
		for p, ex := range res {
			if old, ok := c.ck[p]; !ok || old != ex {
				changed = true
			}
			c.ck[p] = ex
		}
		c.mu.Unlock()
		if changed && !u.stopped() && u.activeTab() == "publish" {
			u.patchMain()
		}
	})
}

// capOnDiskOr reports a capture path's existence from the swept cache; an un-swept path reads as
// present (neutral) until capEnsureOnDisk fills it in.
func (u *UI) capOnDiskOr(path string) bool {
	c := &u.capFS
	c.mu.Lock()
	defer c.mu.Unlock()
	if ex, ok := c.ck[path]; ok {
		return ex
	}
	return true
}

// mpEnsureSet binds the publish instance to a recording's captures (first audio +
// first video with a file on disk). No-op when the same paths are already bound.
// aud/vid are classified from the libdb row fields (kind + extension) - NO os.Stat in render;
// on-disk existence comes from capOnDiskOr (unknown = present until the bg sweep lands).
func (u *UI) mpEnsureSet(r recorder.Recording, caps []libdb.SetRecording) {
	paths := make([]string, 0, len(caps))
	for i := range caps {
		paths = append(paths, caps[i].Path)
	}
	u.capEnsureOnDisk(paths) // off-thread stat sweep; re-renders publish on change
	var aud, vid *libdb.SetRecording
	for i := range caps {
		s := &caps[i]
		if !u.capOnDiskOr(s.Path) {
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
		u.mpCancelLoads("publish") // unbind: don't let dead analyses hog the pools
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

// trackInCapture reports whether a played track's span intersects the capture window
// [capStart, capEnd]. A zero track EndedAt (still playing / open last track) or zero capEnd
// (open capture) counts as unbounded. The lower bound is the one that matters when the DJ
// armed the recorder late: a track that already ended before capStart isn't in the media.
func trackInCapture(tr recorder.Track, capStart, capEnd time.Time) bool {
	if !tr.EndedAt.IsZero() && !tr.EndedAt.After(capStart) {
		return false // ended at/before the capture began
	}
	if !capEnd.IsZero() && !tr.StartedAt.Before(capEnd) {
		return false // started at/after the capture ended
	}
	return true
}

// capTrackMeta derives track markers + auto-trim hints for one capture window from the
// recording's confirmed plays, anchored to the capture's start (axis 0). Only tracks whose
// play-span intersects the window belong in the media - the recording can start well after
// the set (DJ armed OBS late). A track that ended before the capture started isn't in the
// media; the one still playing when it started maps to 0. lastFader = last fader-down (the
// true set end), only when it lands inside this capture. Absent values are -1.
func capTrackMeta(r recorder.Recording, capStart, capEnd time.Time) (marks []mpMark, first, lastEnd, lastFader float64) {
	first, lastEnd, lastFader = -1, -1, -1
	if len(r.Tracks) == 0 || capStart.IsZero() {
		return
	}
	var le time.Time
	for _, tr := range r.Tracks {
		if !trackInCapture(tr, capStart, capEnd) {
			continue
		}
		if tr.EndedAt.After(le) {
			le = tr.EndedAt
		}
		off := math.Max(tr.StartedAt.Sub(capStart).Seconds(), 0)
		if first < 0 || off < first {
			first = off
		}
		marks = append(marks, mpMark{off, orTrackLine(pubTrackLine(tr))})
	}
	if !le.IsZero() {
		lastEnd = math.Max(le.Sub(capStart).Seconds(), 0)
	}
	if !r.LastFaderAt.IsZero() && r.LastFaderAt.After(capStart) {
		lastFader = r.LastFaderAt.Sub(capStart).Seconds()
	}
	return
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

	marks, first, lastEnd, lastFader := capTrackMeta(r, primary.StartedAt, primary.EndedAt)

	// size from the libdb row (s.Bytes) - no os.Stat here (mpLoadCaptures runs on the render
	// goroutine via mpEnsureSet). An in-progress capture has Bytes 0 until it ends; the size row
	// just stays hidden, matching pubCaptureBlock's own s.Bytes>0 gate.
	var media []mpMedia
	if aud != nil {
		media = append(media, mpMedia{capID: aud.ID, path: aud.Path, kind: "audio", size: aud.Bytes,
			startedAt: aud.StartedAt, presetID: "copy-audio", peaksLoading: true})
	}
	if vid != nil {
		media = append(media, mpMedia{capID: vid.ID, path: vid.Path, kind: "video", size: vid.Bytes,
			startedAt: vid.StartedAt, presetID: "remux", peaksLoading: true})
	}

	// dual pair: seed the alignment from the captures' wall-clock start timestamps
	prior, hasPrior := 0.0, false
	if aud != nil && vid != nil && !aud.StartedAt.IsZero() && !vid.StartedAt.IsZero() {
		prior = vid.StartedAt.Sub(aud.StartedAt).Seconds()
		hasPrior = true
	}

	if u.mpSnap(host).monitorLoud {
		u.mpMonitorOff(host) // never carry a pre-listen gain onto different media
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

// reset clears everything except the host key (gen advances so stale async results drop;
// dragGen advances so queued coalesced moves drop too).
func (t *mpSt) reset() {
	g, dg := t.gen+1, t.dragGen+1
	*t = mpSt{host: t.host, gen: g, dragGen: dg, outSec: -1, viewSpan: 1, cursorSec: mpNone, hovT: mpNone,
		firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1,
		pgen: t.pgen} // pgen must NEVER go backwards: a reset that lands on a marked value would hide a race
}

// ── analyses (peaks + loudness + stream info; store-cached, probe/transcode workers) ──

// mpCancelLoads cancels the host's in-flight analysis jobs. Superseded jobs used to run
// to completion and hog the capped worker pools - rapid track switching queued minutes
// of dead decodes ahead of the current track's peaks.
func (u *UI) mpCancelLoads(host string) {
	u.mpLoadMu.Lock()
	if c := u.mpLoadCancel[host]; c != nil {
		c()
		delete(u.mpLoadCancel, host)
	}
	u.mpLoadMu.Unlock()
}

func (u *UI) mpKickAnalyses(host string) {
	t := u.mpSnap(host)
	// One cancelable parent per kick: a rebind cancels the previous kick's jobs
	// (queued OR mid-decode; the pool kills a canceled worker and respawns).
	// actx's 30s cap is too short for peaks/loudness - jobs bound themselves.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-u.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	u.mpLoadMu.Lock()
	if c := u.mpLoadCancel[host]; c != nil {
		c()
	}
	if u.mpLoadCancel == nil {
		u.mpLoadCancel = map[string]context.CancelFunc{}
	}
	u.mpLoadCancel[host] = cancel
	u.mpLoadMu.Unlock()
	for i := range t.media {
		u.mpLoadPeaks(ctx, host, t.gen, i, t.media[i].path)
		u.mpLoadLoud(ctx, host, t.gen, i, t.media[i].path)
		u.mpLoadSrc(ctx, host, t.gen, i, t.media[i].path)
		if t.media[i].kind == "video" {
			u.mpLoadFrag(host, t.gen, i, t.media[i].path)
		}
		if t.media[i].kind == "audio" && strings.EqualFold(filepath.Ext(t.media[i].path), ".flac") {
			u.mpLoadSeekTab(host, t.gen, i, t.media[i].path)
		}
	}
}

// mpLoadSeekTab retrofits a real SEEKTABLE into a seektable-less FLAC capture (in-place
// padding rewrite, mtime preserved - audio.FLACEnsureSeekTable) so every player seeks it
// instantly. One-shot per file; the wave overlay shows a chip while the scan runs.
func (u *UI) mpLoadSeekTab(host string, gen, idx int, path string) {
	u.mpApply(host, gen, idx, func(m *mpMedia) { m.seekTabLoading = true })
	u.bg(func() {
		wrote, err := audio.FLACEnsureSeekTable(path)
		if err != nil {
			u.log.Debug("player", "flac seektable retrofit failed", map[string]any{
				"file": filepath.Base(path), "err": err.Error(),
			})
		} else if wrote {
			u.log.Info("player", "flac seektable written", map[string]any{"file": filepath.Base(path)})
		}
		u.mpApply(host, gen, idx, func(m *mpMedia) { m.seekTabLoading = false })
	})
}

// mpLoadFrag resolves the fragmented-MP4 stream index (store-cached, path+mtime keyed) and
// flips the video element to MSE streaming. Classic MP4s / unsupported codecs cache a
// negative sentinel and keep the plain-src path. Never patches a video that already started
// playing - swapping the element mid-play would restart it.
func (u *UI) mpLoadFrag(host string, gen, idx int, path string) {
	u.bg(func() {
		data, ok := u.mpResolveFrag(path)
		if !ok {
			return
		}
		var fi mp4frag.Index
		if json.Unmarshal(data, &fi) != nil || len(fi.Frags) == 0 {
			return // negative sentinel or unreadable - plain src stays
		}
		applied := u.mpApply(host, gen, idx, func(m *mpMedia) { m.fragOK = true })
		if applied {
			t := u.mpSnap(host)
			if !t.vid.started {
				u.mpPatchVideo(t)
			}
		}
	})
}

// mpResolveFrag returns the cached index JSON for path (parsing + caching on miss).
// ok=false only when no verdict could be stored (no store / stat failure).
func (u *UI) mpResolveFrag(path string) ([]byte, bool) {
	if u.svc.Store == nil {
		return nil, false
	}
	var mtime int64
	if fi, err := os.Stat(path); err == nil {
		mtime = fi.ModTime().Unix()
	} else {
		return nil, false
	}
	if data, ok := u.svc.Store.GetAnalysis(store.KindMp4Frag, path, mtime); ok {
		var cached mp4frag.Index
		na := json.Unmarshal(data, &cached) == nil && len(cached.Frags) == 0
		// a negative sentinel or a current-contract index serves from cache; an old-contract
		// blob (pre-InitB64) falls through and re-parses
		if na || cached.Ver >= mp4frag.ContractVer {
			return data, true
		}
	}
	idx, err := mp4frag.Parse(path)
	if err != nil {
		// negative cache: classic MP4 / unsupported codec / parse failure - don't re-parse
		// every selection. Any file change (mtime) invalidates the verdict.
		neg := []byte(`{"na":1}`)
		u.svc.Store.PutAnalysis(store.KindMp4Frag, path, mtime, neg)
		if !errors.Is(err, mp4frag.ErrNotFragmented) {
			u.log.Debug("player", "mp4frag parse failed", map[string]any{"path": filepath.Base(path), "err": err.Error()})
		}
		return neg, true
	}
	data, merr := json.Marshal(idx)
	if merr != nil {
		return nil, false
	}
	u.svc.Store.PutAnalysis(store.KindMp4Frag, path, mtime, data)
	u.log.Debug("player", "mp4frag indexed", map[string]any{
		"path": filepath.Base(path), "frags": len(idx.Frags), "dur": idx.Duration, "mime": idx.Mime,
	})
	return data, true
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

func (u *UI) mpLoadPeaks(ctx context.Context, host string, gen, idx int, path string) {
	u.bg(func() {
		dur, data, bands, err := u.mpResolvePeaks(ctx, path)
		if ctx.Err() != nil {
			return // superseded by a newer kick - the new job owns the flags
		}
		applied := u.mpApply(host, gen, idx, func(m *mpMedia) {
			m.peaksLoading = false
			if err != nil {
				m.peaksErr = err.Error()
				return
			}
			m.peaks, m.bands = data, bands
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

// mpPeakBlob is the persisted peaks payload + decode contract. b = 3-byte-per-bucket band colour.
// rate/samp/lead/ver capture the decode origin+scale (dur = samp/rate; lead = gapless encoder
// priming in ms) so waveform↔audio drift is detectable/reconcilable, not silent. A blob missing
// bands, or below the current contract ver, is treated as a cache-miss and re-analysed. Tags d/p
// stay compatible with the Fyne trackPeaks blob (view_studio_player.go) sharing the same cache key.
type mpPeakBlob struct {
	D    float64 `json:"d"`
	P    []byte  `json:"p"`
	B    []byte  `json:"b,omitempty"`
	Rate int     `json:"rate,omitempty"`
	Samp int64   `json:"samp,omitempty"`
	Lead float64 `json:"lead,omitempty"` // gapless lead-skip (ms)
	Ver  int     `json:"ver,omitempty"`
}

// mpPeakContractVer bumps when the peaks decode contract changes (rate/format/added fields) so
// blobs written by an older build are a cache-miss and re-decoded. 1 = rate/samp/lead added;
// 2 = 10 ms duration-proportional binning (binRateHz) - old ~8192-bucket blobs re-decode fine.
const mpPeakContractVer = 2

func (u *UI) mpResolvePeaks(parent context.Context, path string) (durSec float64, peaks, bands []byte, err error) {
	var mtime int64
	if fi, serr := os.Stat(path); serr == nil {
		mtime = fi.ModTime().Unix()
	}
	if u.svc.Store != nil {
		if data, ok := u.svc.Store.GetAnalysis(store.KindPeaks, path, mtime); ok {
			var tp mpPeakBlob
			// require band data AND the current contract ver: a pre-band / pre-contract cache
			// re-analyses so the spectral waveform + decode-contract fields fill in.
			if json.Unmarshal(data, &tp) == nil && len(tp.P) > 0 && len(tp.B) >= 3*len(tp.P) &&
				tp.Ver >= mpPeakContractVer && tp.Rate > 0 && tp.Samp > 0 {
				u.mpPeaksSanity(path, "cache", tp.D, len(tp.P), tp.Rate, tp.Samp, tp.Lead)
				return tp.D, tp.P, tp.B, nil
			}
		}
	}
	if u.svc.Workers == nil {
		return 0, nil, nil, errors.New("no worker runtime")
	}
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	// binRateHz=100 → ~10 ms min/max bins (worker sizes the count to the decoded duration, capped
	// at peaksMaxBuckets so a long set stays memory-safe). Fine detail for the zoomable strip.
	raw, rerr := u.svc.Workers.RunBackground(ctx, "probe", "probe.peaks", map[string]any{"path": path, "binRateHz": 100})
	if rerr != nil {
		return 0, nil, nil, rerr
	}
	var r struct {
		Peaks  string  `json:"peaks"`
		Bands  string  `json:"bands"`
		DurSec float64 `json:"durationSeconds"`
		Rate   int     `json:"rate"`
		Samp   int64   `json:"samples"`
		Lead   float64 `json:"leadSkipMs"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Peaks == "" || r.DurSec <= 0 {
		return 0, nil, nil, errors.New("empty analysis")
	}
	data, derr := base64.StdEncoding.DecodeString(r.Peaks)
	if derr != nil || len(data) == 0 {
		return 0, nil, nil, errors.New("bad peaks payload")
	}
	bandData, _ := base64.StdEncoding.DecodeString(r.Bands) // best-effort; nil → mono fallback render
	if u.svc.Store != nil {
		if b, merr := json.Marshal(mpPeakBlob{D: r.DurSec, P: data, B: bandData,
			Rate: r.Rate, Samp: r.Samp, Lead: r.Lead, Ver: mpPeakContractVer}); merr == nil {
			u.svc.Store.PutAnalysis(store.KindPeaks, path, mtime, b)
		}
	}
	u.mpPeaksSanity(path, "decode", r.DurSec, len(data), r.Rate, r.Samp, r.Lead)
	return r.DurSec, data, bandData, nil
}

// mpPeaksSanity logs a one-line origin/scale assertion for a loaded peaks blob: the decoded
// samples/rate must ≈ the stored duration (peaks*bucketDur ≈ dur). A >1% (or >0.5s) mismatch
// (drift=true) means the waveform's time-scale is diverging from the audio - visible in the logs.
func (u *UI) mpPeaksSanity(path, src string, dur float64, nPeaks, rate int, samples int64, leadMs float64) {
	if u.log == nil || nPeaks == 0 || rate == 0 {
		return
	}
	recon := float64(samples) / float64(rate) // duration implied by the decode contract
	drift := math.Abs(recon-dur) > math.Max(0.5, dur*0.01)
	u.log.Debug("player", "peaks sanity", map[string]any{
		"file": filepath.Base(path), "src": src,
		"dur": fmt.Sprintf("%.2f", dur), "recon": fmt.Sprintf("%.2f", recon),
		"rate": rate, "samples": samples, "leadMs": leadMs, "drift": drift,
	})
}

// mpLoadLoud fetches the EBU R128 timeline (LUFS chip + momentary readout). Works for
// video files too (measures the first audio stream).
func (u *UI) mpLoadLoud(parent context.Context, host string, gen, idx int, path string) {
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
			ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
			defer cancel()
			var err error
			raw, err = u.svc.Workers.RunBackground(ctx, "transcode", "transcode.loudtl", map[string]any{"input": path})
			if err != nil {
				if parent.Err() != nil {
					return // superseded - the new kick owns the flags
				}
				u.mpApply(host, gen, idx, func(m *mpMedia) { m.loudLoading = false })
				return
			}
			if u.svc.Store != nil {
				u.svc.Store.PutAnalysis(store.KindLoudnessTL, path, mtime, raw)
			}
		}
		var l mpLoud
		lerr := json.Unmarshal(raw, &l)
		applied := u.mpApply(host, gen, idx, func(m *mpMedia) {
			m.loudLoading = false
			if lerr == nil {
				m.loud = &l
			}
		})
		if applied && lerr == nil {
			// the loudness plan just became computable - refresh its surfaces
			u.mpPatchExport(u.mpSnap(host))
			u.mpKickMeasure(host)
			u.mpSyncMonitor(host)
		}
	})
}

// probeStreams runs (or serves from the KindStreams cache) the ffprobe stream/format JSON for
// path. Persisted by path+mtime so the encoding chip's codec/container/bitrate isn't re-spawned
// on every media load / library selection and survives restart. ctx bounds a cache-miss probe.
func (u *UI) probeStreams(ctx context.Context, path string) ([]byte, error) {
	if u.svc.Workers == nil {
		return nil, errors.New("no worker runtime")
	}
	mtime := fileMtime(path)
	if u.svc.Store != nil {
		if raw, ok := u.svc.Store.GetAnalysis(store.KindStreams, path, mtime); ok {
			return raw, nil
		}
	}
	raw, err := u.svc.Workers.Run(ctx, "probe", "probe.streams", map[string]any{"path": path})
	if err != nil {
		return nil, err
	}
	if u.svc.Store != nil {
		u.svc.Store.PutAnalysis(store.KindStreams, path, mtime, raw)
	}
	return raw, nil
}

// mpLoadSrc probes stream info for the encoding chip (store-cached; see probeStreams).
func (u *UI) mpLoadSrc(parent context.Context, host string, gen, idx int, path string) {
	if u.svc.Workers == nil {
		return
	}
	u.mpApply(host, gen, idx, func(m *mpMedia) { m.srcLoading = true })
	u.bg(func() {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		raw, err := u.probeStreams(ctx, path)
		if parent.Err() != nil {
			return // superseded - the new kick owns the flags
		}
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

// ── container-render ordering (phase B4a; replaces mpResync) ────────────────────
//
// A container render (main / #lib-body / #lib-detail) builds its HTML from a player SNAPSHOT and
// enqueues it when the build finishes. A mutation landing in between - an analysis apply, a
// transport RPC completing - patches the player fragment with FRESH markup that the container
// patch then overwrites: the player showed "Analyzing waveform…" forever while the state was
// healthy. mpResync papered over it by re-emitting the whole component after EVERY container
// patch: two full component renders (waveform SVG included) per tab switch, unconditionally -
// and it did not even close the race, because its patch carried the `mp-<host>-root` coalescing
// key and folded into the position of an earlier queued root patch, i.e. BEFORE the container
// patch it was meant to beat (two container patches in one flush window is enough).
//
// The generation counter decides it instead. mpSt.pgen counts mutations; the mark is taken BEFORE
// the build and re-read AFTER the container patch is enqueued. Every mutation that can still be
// overwritten bumped pgen before that read (bump and read both under mpMu, so the read
// happens-after), and one landing afterwards has its own patch enqueued behind the container
// patch. Nothing is re-rendered when nothing moved. Same shape as the B3 fragment scheduler's
// u.fragGen / commitFrags (tick_sched.go).

// mpHosts are the surfaces a container render can embed.
var mpHosts = [2]string{"library", "publish"}

// mpGens is the per-host mutation generation a container build started from.
type mpGens [len(mpHosts)]uint64

// mpOrdered builds a container fragment, emits it, then heals any player whose state moved during
// the build. EVERY container patch that can embed the player goes through here.
func (u *UI) mpOrdered(build func() string, emit func(html string)) {
	mk := u.mpMarkGens()
	emit(build())
	u.mpHeal(mk)
}

// mpMarkGens snapshots each host's mutation generation.
func (u *UI) mpMarkGens() (mk mpGens) {
	for i, host := range mpHosts {
		mk[i] = u.mpGen(host)
	}
	return mk
}

// mpGen reads a host's mutation generation.
func (u *UI) mpGen(host string) uint64 {
	t := u.mp(host)
	mpMu.Lock()
	defer mpMu.Unlock()
	return t.pgen
}

// mpHeal re-emits a host's component when its state moved since mk. A host with no media is not
// in the DOM, so there is nothing to heal (a fragment id the page lacks is a __patch no-op).
func (u *UI) mpHeal(mk mpGens) {
	for i, host := range mpHosts {
		if u.mpGen(host) == mk[i] {
			continue
		}
		if t := u.mpSnap(host); len(t.media) > 0 {
			u.mpPatchAll(t)
		}
	}
}

func (u *UI) mpPatchAll(t mpSt) {
	// UNCOALESCED (key ""): a keyed entry folds into an already-queued root patch and keeps THAT
	// entry's position, which can be ahead of the container patch this has to land after.
	u.enqueueEval("", "window.__patch("+jsQuote("mp-"+t.host+"-root")+","+jsQuote(u.mpInnerHTML(t))+")")
	if t.vid.started && t.vid.err == "" { // the <video> element was recreated - restore it
		js := fmt.Sprintf("v.currentTime=%.3f;", t.vid.cur)
		if !t.vid.paused {
			js += "v.play().catch(function(){v.muted=true;v.play().catch(function(){})});"
		}
		u.mpVidEval(t.host, js)
	}
}
func (u *UI) mpPatchWave(t mpSt) {
	u.mpPatch(t.host, "wave", u.mpWaveInner(t))
	u.mpPushRealtime(t) // re-sync the client rAF playhead/clock interpolator to the fresh render
}
func (u *UI) mpPatchTransport(t mpSt) {
	u.mpPatch(t.host, "tp", u.mpTransportHTML(t))
}
func (u *UI) mpPatchEdit(t mpSt)   { u.mpPatch(t.host, "edit", u.mpEditHTML(t)) }
func (u *UI) mpPatchExport(t mpSt) { u.mpPatch(t.host, "export", u.mpExportHTML(t)) }
func (u *UI) mpPatchVideo(t mpSt)  { u.mpPatch(t.host, "vid", u.mpVideoHTML(t)) }
func (u *UI) mpPatchRO(t mpSt)     { u.mpPatch(t.host, "ro", mpReadout(t)) }
func (u *UI) mpPatchHov(t mpSt)    { u.mpPatch(t.host, "hov", u.mpReadoutLine(t)) }

// mpPatchTime updates the transport clock in place (text + ctl data-value) without
// repainting the buttons/slider mid-interaction.
func (u *UI) mpPatchTime(t mpSt) {
	tx := jsQuote(u.mpTimeText(t))
	u.eval("var e=document.getElementById(" + jsQuote("mp-"+t.host+"-time") + ");if(e){e.textContent=" + tx + ";e.setAttribute('data-value'," + tx + ");}")
}

// mpPushRealtime hands the client rAF interpolator (shell.go __rt) the playhead's sparse
// motion so the mint playhead line + transport clock animate smoothly between the coarse
// ~1 Hz Go wave re-renders: start x + velocity (viewBox units/sec in the fixed 1000-wide
// viewBox) + clock seconds + playback rate (the native engine runs at 1.0x). rate 0 =
// paused/stopped/idle → the client snaps once and stops its loop (idle = zero frames; the
// "must not clog the system" contract). Coalesced per host so a fast scrub can't pile up
// pushes. Called wherever the wave (hence the playhead line) is re-rendered.
func (u *UI) mpPushRealtime(t mpSt) {
	if u.shell == nil {
		return
	}
	key := "mprt-" + t.host
	m := t.activeMedia()
	if m == nil {
		u.enqueueEval(key, "window.__rt&&window.__rt('ph',"+jsQuote("mp-"+t.host)+",null)")
		return
	}
	tr := u.mpEng(&t)
	total := m.dur
	if tr.loaded && tr.total > 0 {
		total = tr.total
	}
	pos, x0, vx, rate := 0.0, 0.0, 0.0, 0.0
	lo, ln := t.axis()
	if tr.loaded {
		pos = tr.cur
		if ln > 0 && t.viewSpan > 0 {
			pAxis := tr.cur + t.mediaStart(t.active)
			x0 = ((pAxis-lo)/ln - t.viewStart) / t.viewSpan * 1000.0
			if tr.playing {
				if vss := t.viewSpan * ln; vss > 0 {
					vx = 1000.0 / vss // 1.0x engine → 1000 units per view-span-second
				}
				rate = 1.0
			}
		}
	}
	js := fmt.Sprintf("window.__rt&&window.__rt('ph',%s,{ph:%s,clk:%s,x0:%.2f,vx:%.4f,pos:%.3f,rate:%.3f,total:%.3f,w:1000})",
		jsQuote("mp-"+t.host), jsQuote("mp-"+t.host+"-ph"), jsQuote("mp-"+t.host+"-time"), x0, vx, pos, rate, total)
	u.enqueueEval(key, js)
}

// mpApplyTrim mutates + repaints wave/edit (the common non-drag update path). The trim
// window feeds the loudness plan, so the exact-measure lookup + monitor gain re-sync ride here.
func (u *UI) mpApplyTrim(host string, fn func(*mpSt)) {
	if mpTrimSnap != nil {
		mpTrimSnap(u, host) // undo checkpoint BEFORE the edit (editor tab)
	}
	t := u.mpMut(host, fn)
	if len(t.media) == 0 {
		return
	}
	u.mpPatchWave(t)
	u.mpPatchEdit(t)
	u.mpKickMeasure(host)
	u.mpSyncMonitor(host)
	u.mpSyncVidTrim(t)
}

// mpSyncVidTrim mirrors the trim window onto the video element's data-in/data-out
// (element-side OUT stop + loop-from-IN) without replacing the element mid-play.
func (u *UI) mpSyncVidTrim(t mpSt) {
	vi := -1
	for i := range t.media {
		if t.media[i].kind == "video" {
			vi = i
			break
		}
	}
	if vi < 0 || (t.dual() && t.active == 0) { // dual slave preview: audio engine owns the trim
		return
	}
	in := clampF(t.inSec-t.mediaStart(vi), 0, math.Max(t.media[vi].dur, 0))
	js := fmt.Sprintf("v.dataset.in='%.3f';", in)
	if t.outSec > 0 {
		js += fmt.Sprintf("v.dataset.out='%.3f';", clampF(t.outSec-t.mediaStart(vi), 0, math.Max(t.media[vi].dur, 0)))
	} else {
		js += "delete v.dataset.out;"
	}
	u.mpVidEval(t.host, js)
}

// mpTick keeps clock/playhead/transport fresh while this host's tab shows (1 Hz).
func mpTick(u *UI, host string) {
	t := u.mpSnap(host)
	if len(t.media) == 0 || t.drag != "" {
		return
	}
	m := t.activeMedia()
	tr := u.mpEng(&t)
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
		if m.kind == "video" {
			// self-heal: force an unthrottled element report while the mirror says playing - a
			// rebuilt element or a rejected play() otherwise leaves a phantom moving playhead
			u.mpVidEval(host, "window.rave(JSON.stringify({act:'mp-vtick:"+host+
				"',val:v.currentTime+'|'+(v.duration||0)+'|'+(v.paused?'1':'0')}))")
			// live-feed clock ≠ source clock, so the element can't enforce the OUT marker.
			// A loop feed carries the whole IN→OUT span and wraps itself - hands off.
			if t.vid.strURL != "" && !t.vid.strLoop && t.outSec > 0 && tr.cur+t.mediaStart(t.active) >= t.outSec {
				u.mpVidEval(host, "if(!v.paused){v.pause()}")
			}
		} else if t.outSec > 0 && tr.cur+t.mediaStart(t.active) >= t.outSec {
			// audio OUT-marker stop (video stops element-side via data-out)
			u.mpAudCall(host, "pause", func() {
				if pl := u.player(); pl != nil {
					pl.TogglePause()
				}
			})
			u.mpPatchTransport(u.mpSnap(host))
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
				if pl := u.player(); pl != nil {
					u.bg(func() { pl.Stop() }) // blocking halt off the act worker
				}
				u.mpVidEval(host, "v.muted=false;")
			} else {
				u.mpVidEval(host, "if(!v.paused){v.pause()}")
			}
		}
		u.mpPatchAll(t)
	})
	// page-JS diagnostics (MSE runtime stage/failure reports; ctl logs surface them)
	onExact("__jsdbg", func(u *UI, m actMsg) {
		u.log.Info("webui-js", m.Val, nil)
	})
	// global playback volume: ONE persisted value for every media surface (audio engine +
	// embedded videos), applied live and re-applied at startup/child respawn.
	onPrefix("mp-vol:", func(u *UI, m actMsg) {
		host := m.arg("mp-vol:")
		raw, err := strconv.ParseFloat(m.Val, 64)
		if err != nil {
			return
		}
		v := clampF(raw/100, 0, 1)
		if u.svc.Cfg != nil {
			vol := v
			u.svc.Cfg.Features.Player.Volume = &vol
			u.saveCfg()
		}
		if pl := u.player(); pl != nil {
			u.bg(func() { pl.SetVolume(v) }) // proxy RPC off the act lane
		}
		u.eval(fmt.Sprintf("document.querySelectorAll('video').forEach(function(v){v.volume=%.3f})", v))
		u.mpPatchTransport(u.mpSnap(host))
	})
	onPrefix("mp-play:", func(u *UI, m actMsg) { u.mpPlayToggle(m.arg("mp-play:")) })
	// client-asserted video transport (shell.go __vplay/__vpause): the element already
	// flipped and the playhead already froze/launched - Go mirrors the RESULT and runs
	// the side effects. Never toggles: a toggle here would race the click.
	onPrefix("mp-vplay:", func(u *UI, m actMsg) { u.mpVidAssert(m.arg("mp-vplay:"), m.Val, false) })
	onPrefix("mp-vpause:", func(u *UI, m actMsg) { u.mpVidAssert(m.arg("mp-vpause:"), m.Val, true) })
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
		dir, _, ok := mpPos(m.Val)
		if ok {
			u.mpZoomAtPlayhead(host, dir == "in") // zoom on the playhead, not the mouse
		}
	})
	onPrefix("mp-zin:", func(u *UI, m actMsg) { u.mpZoomAtPlayhead(m.arg("mp-zin:"), true) })
	onPrefix("mp-zout:", func(u *UI, m actMsg) { u.mpZoomAtPlayhead(m.arg("mp-zout:"), false) })
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
	// transport ⋯ menu (collection context; smart-select action menu)
	onPrefix("mp-more:", func(u *UI, m actMsg) {
		if m.Val != "edit" {
			return
		}
		t := u.mpMut(m.arg("mp-more:"), func(v *mpSt) { v.edit = true })
		u.mpPatchAll(t)
		u.mpMaybeAlign(t.host, false)
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
				md := &v.media[idx]
				md.presetID, md.inline = id, nil // picking a preset discards an unsaved edit
				// the output file follows the preset's container - swap the extension in
				// place (user-typed base names survive, only the ext tracks the format)
				if md.outPath != "" {
					md.outPath = swapExt(md.outPath, mpExt(md.path, mpPreset(u, id)))
				}
			}
		})
		u.mpPatchExport(t)
		u.mpPatchWave(t) // preset may change the loudness viz (target line / projection)
		u.mpKickMeasure(host)
		u.mpSyncMonitor(host)
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
	// per-media loudness override: "mp-loud:<host>\x1f<idx>\x1f<field>" (loudnessFields' contract)
	onPrefix("mp-loud:", func(u *UI, m actMsg) {
		host, rest := mpArgs(m.arg("mp-loud:"))
		idxS, f, _ := strings.Cut(rest, "\x1f")
		idx := atoi(idxS)
		t := u.mpMut(host, func(v *mpSt) {
			if idx < 0 || idx >= len(v.media) {
				return
			}
			ov := &v.media[idx].loudOv
			switch f {
			case "loudon":
				ov.On = m.Val == "true"
			case "loudi":
				ov.I = atof(m.Val)
			case "loudtp":
				ov.TP = atof(m.Val)
			case "loudraise":
				ov.RaiseOnly = m.Val == "true"
			case "loudtarget": // quick-pick chip: "<I>|<TP>" pair
				iS, tpS, _ := strings.Cut(m.Val, "|")
				ov.I, ov.TP = atof(iS), atof(tpS)
				ov.On = true
			}
		})
		u.mpPatchExport(t) // loudon shows/hides the targets
		u.mpPatchWave(t)   // target line + projected curve track the settings
		u.mpKickMeasure(host)
		u.mpSyncMonitor(host)
	})
	// one-tap fix: normalization needs a re-encode - switch a copy preset to FLAC (lossless)
	onPrefix("mp-loudfix:", func(u *UI, m actMsg) {
		host, idxS := mpArgs(m.arg("mp-loudfix:"))
		idx := atoi(idxS)
		t := u.mpMut(host, func(v *mpSt) {
			if idx >= 0 && idx < len(v.media) {
				md := &v.media[idx]
				md.presetID, md.inline = "flac", nil
				if md.outPath != "" {
					md.outPath = swapExt(md.outPath, "flac")
				}
			}
		})
		u.mpPatchExport(t)
		u.mpPatchWave(t)
		u.mpKickMeasure(host)
		u.mpSyncMonitor(host)
	})
	// pre-listen: audition the planned gain on the live audio engine
	onPrefix("mp-monloud:", func(u *UI, m actMsg) {
		host := m.arg("mp-monloud:")
		if u.player() == nil {
			u.toast(i18n.T(u.playerGateKey()))
			return
		}
		t := u.mpMut(host, func(v *mpSt) { v.monitorLoud = !v.monitorLoud })
		if t.monitorLoud {
			u.mpSyncMonitor(host)
		} else {
			if pl := u.player(); pl != nil {
				pl.SetPreGainDB(0)
			}
		}
		u.mpPatchExport(t)
	})
	// cancel the in-flight export (kills the ffmpeg tree; partial output is removed)
	onPrefix("mp-excancel:", func(u *UI, m actMsg) {
		t := u.mpSnap(m.arg("mp-excancel:"))
		if t.exporting && t.exportCancel != nil {
			t.exportCancel()
		}
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
	onPrefix("mp-vstall:", func(u *UI, m actMsg) {
		// live-feed stall: producer ended and playback drained - respawn there
		host := m.arg("mp-vstall:")
		t := u.mpSnap(host)
		if t.vid.strURL == "" || mpStreamCtl == nil {
			return
		}
		cur, _ := strconv.ParseFloat(strings.TrimSpace(m.Val), 64)
		mpStreamCtl(u, host, "stall", t.vid.strT0+cur)
	})
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

// ── drag-move coalescing ────────────────────────────────────────────────────────
//
// actpos 'move' events arrive far faster than an SVG rebuild+eval renders; handling each on the
// act worker queued a repaint per event and lagged the whole acts lane. Moves collapse into a
// latest-wins mailbox (cap 1/host, newest wins) with ONE render goroutine in flight; down/up
// (and reset) bump dragGen, so a pending move from a finished drag is stale + skipped - it can
// never re-latch v.drag after a dropped 'up' (acts channel is cap-64 drop-newest).

// mpMoveEv is one pending coalesced move.
type mpMoveEv struct {
	lane string // "pan" | "in" | "out"
	fx   float64
	gen  int // mpSt.dragGen at enqueue
}

// mpMoveKey scopes the coalescer per (UI, host) - a remote session's drags must not steal
// or stall the window's mailbox.
type mpMoveKey struct {
	u    *UI
	host string
}

var (
	mpMoveMu   sync.Mutex
	mpMovePend = map[mpMoveKey]mpMoveEv{} // newest pending move
	mpMoveBusy = map[mpMoveKey]bool{}     // render in flight
)

// mpMoveCoalesce enqueues a move (newest wins) and starts the per-key render worker if idle.
func (u *UI) mpMoveCoalesce(host, lane string, fx float64) {
	t := u.mp(host)
	mpMu.Lock()
	gen := t.dragGen
	// click-vs-pan classification must not lag the coalescer: mark moved at enqueue, not render
	if lane == "pan" && t.drag == "pan" && math.Abs(fx-t.dragAnchor) > 0.01 {
		t.dragMoved = true
	}
	mpMu.Unlock()
	k := mpMoveKey{u, host}
	mpMoveMu.Lock()
	mpMovePend[k] = mpMoveEv{lane, fx, gen}
	if mpMoveBusy[k] {
		mpMoveMu.Unlock()
		return
	}
	mpMoveBusy[k] = true
	mpMoveMu.Unlock()
	u.bg(func() {
		for {
			mpMoveMu.Lock()
			ev, ok := mpMovePend[k]
			delete(mpMovePend, k)
			if !ok {
				delete(mpMoveBusy, k) // delete, not =false: headless sessions come and go
				mpMoveMu.Unlock()
				return
			}
			mpMoveMu.Unlock()
			if ev.lane == "pan" {
				u.mpSurfMove(host, ev.fx, ev.gen)
			} else {
				u.mpHandleMove(host, ev.lane, ev.fx, ev.gen)
			}
		}
	})
}

// mpMoveCancel drops this UI's pending coalesced move (a down/up landed - it is stale).
func (u *UI) mpMoveCancel(host string) {
	mpMoveMu.Lock()
	delete(mpMovePend, mpMoveKey{u, host})
	mpMoveMu.Unlock()
}

// mpHandle drags the IN (top lane) / OUT (bottom lane) trim bound to the pointer.
func (u *UI) mpHandle(host, which, val string) {
	phase, fx, ok := mpPos(val)
	if !ok {
		return
	}
	if phase == "move" { // coalesced: latest-wins, one render in flight
		u.mpMoveCoalesce(host, which, fx)
		return
	}
	u.mpMoveCancel(host)
	t := u.mpMut(host, func(v *mpSt) {
		v.dragGen++ // invalidate queued moves
		if phase == "down" {
			v.drag = "" // defensive: a dropped 'up' must never leave drag latched (mpTick freeze)
		}
		v.mpHandleTo(which, fx, phase == "up")
	})
	if len(t.media) == 0 {
		return
	}
	u.mpPatchWave(t)
	u.mpPatchRO(t)
	if phase == "up" {
		u.mpPatchEdit(t)
		u.mpKickMeasure(host) // drag settled - refresh the plan's exact-measure lookup
		u.mpSyncMonitor(host)
		u.mpSyncVidTrim(t)
	}
}

// mpHandleTo moves the in/out bound to fx (shared by direct down/up and coalesced moves).
func (v *mpSt) mpHandleTo(which string, fx float64, up bool) {
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
	if up {
		v.drag = ""
	}
}

// mpHandleMove applies one coalesced handle move (skipped when stale: down/up bumped dragGen).
func (u *UI) mpHandleMove(host, which string, fx float64, gen int) {
	stale := false
	t := u.mpMut(host, func(v *mpSt) {
		if v.dragGen != gen {
			stale = true
			return
		}
		v.mpHandleTo(which, fx, false)
	})
	if stale || len(t.media) == 0 {
		return
	}
	u.mpPatchWave(t)
	u.mpPatchRO(t)
}

// mpSurf handles the middle lane: click = seek, drag = pan when zoomed.
func (u *UI) mpSurf(host, val string) {
	if u.ceActiveFor(host) {
		u.ceSurf(host, val)
		return
	}
	phase, fx, ok := mpPos(val)
	if !ok {
		return
	}
	if phase == "move" { // coalesced: latest-wins, one render in flight
		u.mpMoveCoalesce(host, "pan", fx)
		return
	}
	u.mpMoveCancel(host)
	seekSec := math.Inf(-1)
	t := u.mpMut(host, func(v *mpSt) {
		v.dragGen++ // invalidate queued moves
		if phase == "down" {
			v.drag = "" // defensive: a dropped 'up' must never leave drag latched (mpTick freeze)
		}
		if len(v.media) == 0 {
			return
		}
		switch phase {
		case "down":
			v.drag, v.dragAnchor, v.dragView, v.dragMoved = "pan", fx, v.viewStart, false
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
	if phase == "up" {
		if !math.IsInf(seekSec, -1) {
			u.mpSeekAxis(host, seekSec)
			// The seek moved the playhead; `t` is the snapshot from BEFORE it. Repainting from
			// that stale copy re-rendered the wave at the old position AND re-anchored the rAF
			// interpolator there, so a click looked like it did nothing and the NEXT click
			// appeared to apply the previous one. Always repaint from post-seek state.
			t = u.mpSnap(host)
		}
		u.mpPatchWave(t)
	}
}

// mpSurfMove applies one coalesced pan move (skipped when stale or not panning).
func (u *UI) mpSurfMove(host string, fx float64, gen int) {
	render := false
	t := u.mpMut(host, func(v *mpSt) {
		if v.dragGen != gen || v.drag != "pan" || v.viewSpan >= 1 || len(v.media) == 0 {
			return
		}
		v.viewStart = clampF(v.dragView-(fx-v.dragAnchor)*v.viewSpan, 0, 1-v.viewSpan)
		render = true
	})
	if render {
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

// mpZoomAtPlayhead zooms keeping the playhead stationary (in cue-edit the beat cursor, else the
// last click, else the view centre) rather than the mouse - so +/-/wheel zoom in on where you're
// working, not where the pointer happens to be.
func (u *UI) mpZoomAtPlayhead(host string, zoomIn bool) {
	t := u.mpSnap(host)
	fx := 0.5
	if lo, ln := t.axis(); ln > 0 && t.viewSpan > 0 {
		anchorSec := math.Inf(-1)
		switch {
		case mpIsSet(u.mpPlayheadAxis(&t)):
			anchorSec = u.mpPlayheadAxis(&t)
		case u.ceActiveFor(host):
			c := u.ce()
			c.mu.Lock()
			anchorSec = c.cursorMs / 1000
			c.mu.Unlock()
		case mpIsSet(t.cursorSec):
			anchorSec = t.cursorSec
		}
		if !math.IsInf(anchorSec, -1) {
			fx = clampF(((anchorSec-lo)/ln-t.viewStart)/t.viewSpan, 0, 1)
		}
	}
	u.mpZoomAt(host, zoomIn, fx)
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

// mpPreview (⇤ IN) moves the playhead to the trim IN marker. Seek only - play/pause
// state is preserved (playing keeps playing from IN; paused just moves the playhead).
func (u *UI) mpPreview(host string) {
	u.mpJump(host, u.mpSnap(host).inSec)
}

// mpJump seeks the active engine to an axis offset, preserving play state. Idle video
// primes the element paused at the target (frame visible, transport live); idle audio
// starts playback there (a stopped audio engine has no paused-preview state).
func (u *UI) mpJump(host string, axisSec float64) {
	t := u.mpSnap(host)
	m := t.activeMedia()
	if m == nil {
		return
	}
	if u.mpEng(&t).loaded {
		u.mpSeekAxis(host, axisSec)
		return
	}
	local := clampF(axisSec-t.mediaStart(t.active), 0, math.Max(m.dur, 0))
	if m.kind == "video" {
		u.mpVidEval(host, fmt.Sprintf("v.preload='auto';try{v.currentTime=%.3f}catch(e){}", local))
		t = u.mpMut(host, func(v *mpSt) { v.vid.started, v.vid.paused, v.vid.cur = true, true, local })
		u.mpPatchTransport(t)
		u.mpPatchWave(t)
		return
	}
	u.mpStartPlayback(host, *m, local)
}

// mpVidTick applies a <video> element event ("cur|dur|paused01") to the mirror. A
// play-state flip repaints transport + wave (button glyph, rAF rate); position-only
// ticks re-sync the rAF interpolator so pause/seek desyncs can't survive a second.
func (u *UI) mpVidTick(host, val string) {
	parts := strings.Split(val, "|")
	if len(parts) != 3 {
		return
	}
	cur, _ := strconv.ParseFloat(parts[0], 64)
	dur, _ := strconv.ParseFloat(parts[1], 64)
	paused := parts[2] == "1"
	flip := false
	t := u.mpMut(host, func(v *mpSt) {
		flip = v.vid.paused != paused || !v.vid.started
		str := v.vid // stream binding survives element event churn
		if str.strURL != "" {
			cur += str.strT0 // live feed clock → absolute source seconds
			dur = 0          // a growing feed's duration is meaningless for the axis
		}
		if str.strPend {
			cur = str.cur // respawn in flight: hold the REQUESTED position, not the old feed's clock
		}
		v.vid = mpVid{cur: cur, dur: dur, paused: paused, started: true,
			strURL: str.strURL, strMime: str.strMime, strT0: str.strT0, strAuto: str.strAuto,
			strLoop: str.strLoop, strPend: str.strPend}
		// late-bind the video duration from the element when peaks couldn't provide one
		for i := range v.media {
			if v.media[i].kind == "video" && dur > v.media[i].dur+1 {
				v.media[i].dur = dur
			}
		}
	})
	if m := t.activeMedia(); m != nil && m.kind == "video" {
		u.mpPatchTime(t)
		if flip {
			u.mpPatchTransport(t)
			u.mpPatchWave(t)
		} else {
			u.mpPushRealtime(t)
		}
	}
}

// mpVidAssert mirrors a client-side transport flip: elemCur is the element's own clock
// (stream feeds are strT0-relative). Runs the stream-pipeline side effects a bare
// element play/pause can't: respawn a reaped feed, loop from IN past OUT, idle-reap.
func (u *UI) mpVidAssert(host, val string, paused bool) {
	cur, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
	if err != nil {
		return
	}
	t := u.mpSnap(host)
	if t.vid.strURL != "" {
		cur += t.vid.strT0
	}
	t = u.mpMut(host, func(v *mpSt) {
		if !v.vid.strPend { // a respawn owns the position until its feed lands
			v.vid.cur = cur
		}
		v.vid.paused, v.vid.started = paused, true
	})
	if mpStreamCtl != nil && t.vid.strURL != "" {
		verb := "play"
		if paused {
			verb = "pause"
		}
		mpStreamCtl(u, host, verb, cur)
		t = u.mpSnap(host)
	}
	u.mpPatchTransport(t)
	u.mpPushRealtime(t)
	u.mpPatchWave(t)
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

// mpEnvelope fetches an RMS envelope via the probe worker, persisted per-file (KindEnvelope,
// key path+mtime, fixed rateHz=50) so aligning one audio against N videos decodes each file once
// and the envelope survives restart. The align RESULT is cached per-pair (KindAlign); this caches
// its per-file input.
func (u *UI) mpEnvelope(path string) ([]float64, float64, error) {
	mtime := fileMtime(path)
	raw, cached := []byte(nil), false
	if u.svc.Store != nil {
		raw, cached = u.svc.Store.GetAnalysis(store.KindEnvelope, path, mtime)
	}
	if !cached {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var err error
		raw, err = u.svc.Workers.RunBackground(ctx, "probe", "probe.envelope", map[string]any{"path": path, "rateHz": 50})
		if err != nil {
			return nil, 0, err
		}
		if u.svc.Store != nil {
			u.svc.Store.PutAnalysis(store.KindEnvelope, path, mtime, raw)
		}
	}
	return parseEnvelope(raw)
}

// parseEnvelope decodes a probe.envelope payload (base64 little-endian float32 buckets) to the
// RMS envelope + its rate.
func parseEnvelope(raw []byte) ([]float64, float64, error) {
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
		// The override replaces the preset's loudness block wholesale; off leaves the preset's own
		// settings alone. The worker's NormalizePreset clamps the targets and drops loudness for
		// copy/none audio (the export UI warns before it gets here). Inline (unsaved editor)
		// presets win over presetID; a "source format" container resolves against the input.
		preset := transcode.ApplyLoudnessOverride(u.mpActivePreset(&m),
			m.loudOv.On, m.loudOv.I, m.loudOv.TP, m.loudOv.RaiseOnly)
		out := m.outPath
		if out == "" {
			out = mpOutPath(m.path, preset)
		}
		preset = transcode.ResolveSourceContainer(preset, m.path)
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
	// "queued" until the worker's first stage event - a busy transcode pool no longer looks
	// like a hung 0% bar while the job waits for a slot.
	t = u.mpMut(host, func(v *mpSt) {
		v.exporting, v.exportPct, v.exportMsg, v.exportStage, v.exportLoudTx = true, 0, "", "queued", ""
	})
	u.mpPatchExport(t)
	if len(plans) == 1 {
		u.toast(i18n.T("player.toast.exportingCut", i18n.A{"preset": plans[0].preset.Label}))
	} else {
		u.toast(i18n.Tn("player.toast.exportingCuts", len(plans)))
	}
	u.bg(func() { u.mpExportRunAll(host, gen, plans) })
}

// mpExportRunAll runs the planned jobs sequentially, folding progress into one bar. Runs on
// a bg goroutine: the exact-measure cache lookup (skip pass 1) and the partial-output cleanup
// on failure/cancel both live here.
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
		onStage := func(name string) {
			t := u.mpMut(host, func(v *mpSt) {
				if v.gen == gen {
					v.exportStage = name
				}
			})
			if t.gen == gen {
				u.mpPatchExport(t)
			}
		}
		params := map[string]any{"input": p.path, "output": p.out, "preset": p.preset,
			"trimStart": p.trimS, "trimEnd": p.trimE}
		// Exact windowed measurement already cached → the worker skips its measure pass
		// entirely (the "analyze loudness every time" complaint).
		if p.preset.LoudnessOn && transcode.LoudnessAppliesTo(p.preset.AudioCodec) && u.svc.Store != nil {
			if raw, ok := u.svc.Store.GetAnalysis(store.KindLoudness,
				mpMeasStoreKey(p.path, p.trimS, p.trimE), fileMtime(p.path)); ok {
				var mm transcode.Measurement
				if json.Unmarshal(raw, &mm) == nil {
					params["measured"] = mm
				}
			}
		}
		if err := u.mpExportOne(host, gen, p, params, onPct, onStage); err != nil {
			canceled := err.Error() == "canceled"
			if pubFileExists(p.out) {
				_ = os.Remove(p.out) // ffmpeg's partial output is corrupt either way
			}
			u.mpExportDone(host, gen, false, canceled, err.Error(), outs)
			return
		}
		outs = append(outs, p.out)
	}
	u.mpExportDone(host, gen, true, false, "", outs)
}

// mpExportOne runs one transcode job (shared Hub when available, else the worker pool) and
// blocks until it finishes. onStage receives the worker's "stage" events (prepare/measure/
// encode) so the bar caption tracks what the job is actually doing. The job's cancel handle
// is published to the state (Cancel button); the worker's "loudness" event is captured to
// the store (next export of the same window skips the measure) and to the applied-gain line.
func (u *UI) mpExportOne(host string, gen int, plan mpExportPlan, params map[string]any, onPct func(float64), onStage func(string)) error {
	onProgress := func(event string, data json.RawMessage) {
		switch event {
		case "stage":
			var s struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(data, &s) == nil && s.Name != "" && onStage != nil {
				onStage(s.Name)
			}
		case "loudness":
			u.mpExportLoudEvent(host, gen, plan, data)
		case "progress":
			var p struct {
				Percent float64 `json:"percent"`
			}
			if json.Unmarshal(data, &p) == nil {
				onPct(p.Percent)
			}
		}
	}
	setCancel := func(fn func()) {
		u.mpMut(host, func(v *mpSt) {
			if v.gen == gen {
				v.exportCancel = fn
			}
		})
	}
	defer setCancel(nil)
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
		setCancel(func() { u.svc.Hub.Cancel(jid) })
		return <-done
	}
	if u.svc.Workers == nil {
		return errors.New("worker pool unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	setCancel(cancel)
	_, err := u.svc.Workers.RunStream(ctx, "transcode", "transcode.run", params, onProgress)
	if ctx.Err() != nil {
		return errors.New("canceled")
	}
	return err
}

// mpExportLoudEvent handles the worker's pass-1 result: persist the exact measurement for
// this (input, trim window) so the NEXT export skips the measure pass, adopt it into the
// live plan, and record the applied-gain line for the result readout.
func (u *UI) mpExportLoudEvent(host string, gen int, plan mpExportPlan, data json.RawMessage) {
	var ev struct {
		InputI     float64 `json:"inputI"`
		InputTP    float64 `json:"inputTP"`
		InputLRA   float64 `json:"inputLRA"`
		GainDB     float64 `json:"gainDB"`
		PeakCapped bool    `json:"peakCapped"`
		Skipped    bool    `json:"skipped"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	mm := transcode.Measurement{I: ev.InputI, TP: ev.InputTP, LRA: ev.InputLRA}
	if u.svc.Store != nil {
		if raw, err := json.Marshal(mm); err == nil {
			u.svc.Store.PutAnalysis(store.KindLoudness,
				mpMeasStoreKey(plan.path, plan.trimS, plan.trimE), fileMtime(plan.path), raw)
		}
	}
	tx := ""
	switch {
	case ev.Skipped:
		tx = i18n.T("player.label.loudApSkipped")
	case ev.PeakCapped:
		tx = i18n.T("player.label.loudApCapped", i18n.A{
			"gain": fmt.Sprintf("%+.1f", ev.GainDB), "out": fmt.Sprintf("%.1f", ev.InputI+ev.GainDB)})
	default:
		tx = i18n.T("player.label.loudApplied", i18n.A{
			"gain": fmt.Sprintf("%+.1f", ev.GainDB), "out": fmt.Sprintf("%.1f", ev.InputI+ev.GainDB)})
	}
	u.mpMut(host, func(v *mpSt) {
		if v.gen != gen {
			return
		}
		v.exportLoudTx = tx
		if plan.idx < len(v.media) && v.media[plan.idx].path == plan.path {
			md := &v.media[plan.idx]
			md.measKey = mpMeasKey(plan.path, plan.trimS, plan.trimE)
			md.measured = &mm
		}
	})
}

func (u *UI) mpExportDone(host string, gen int, ok, canceled bool, errTx string, outs []string) {
	t := u.mpMut(host, func(v *mpSt) {
		if v.gen != gen {
			return
		}
		v.exporting, v.exportStage, v.exportCancel = false, "", nil
		switch {
		case ok:
			v.exportPct, v.exportMsg = 100, i18n.T("player.label.exportDone")+strings.Join(outs, " · ")
		case canceled:
			v.exportPct, v.exportMsg, v.exportLoudTx = 0, i18n.T("player.label.exportCanceled"), ""
		default:
			v.exportMsg = i18n.T("player.label.exportMsgFailed") + errTx
		}
	})
	switch {
	case ok:
		names := make([]string, len(outs))
		for i, o := range outs {
			names[i] = filepath.Base(o)
		}
		u.toast(i18n.T("player.toast.cutExported") + strings.Join(names, ", "))
	case canceled:
		u.toast(i18n.T("player.toast.exportCanceled"))
	default:
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

// swapExt swaps path's extension for extNoDot (no-op when extNoDot is empty).
func swapExt(path, extNoDot string) string {
	if extNoDot == "" {
		return path
	}
	return strings.TrimSuffix(path, filepath.Ext(path)) + "." + extNoDot
}

// mpExt is the output extension (no dot) - the preset container's canonical extension
// (Preset.Ext, so an "aac" container correctly writes .m4a), else the source's own.
func mpExt(file string, preset transcode.Preset) string {
	if e := preset.Ext(); e != "" {
		return strings.TrimPrefix(e, ".")
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
