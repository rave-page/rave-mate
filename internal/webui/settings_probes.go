package webui

import (
	"sync"
	"time"

	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/stt"
	"rave.page/mate/internal/timecode"
	"rave.page/mate/internal/unityproj"
	"rave.page/mate/internal/vrdll"
	"rave.page/mate/internal/vroverlay"
)

// Settings probes (phase B4c) - the slow fs/PATH/device lookups the Settings tab reads.
//
// mediatools.Tool.Status() (os.Stat + a PATH scan), vrdll.Probe() (DLL fs probe) and the device
// enumerations (winmm / WASAPI / STT / capture + unityproj.Inspect per project) must never run on
// the render or handler lane: called inline from the card bodies they froze tab-open for seconds.
// That part is unchanged. What B4c removes is the RETAINED-STATE WORKAROUND around them - one
// `busy` flag plus a 10 s TTL:
//
//   - the single flag serialized the whole set, so every probe refreshed at the pace of the slowest
//     member (a long PATH held up the MIDI enumeration);
//   - the TTL existed to bound how often that serial pass ran, i.e. it was a lane/GC budget, not a
//     correctness requirement - and it made every value up to 10 s stale.
//
// Now each probe is INDEPENDENT: its own single-flight guard, its own goroutine and its own slot
// commit - the old pass published ALL slots only after its slowest member returned - and the
// re-render a result triggers is coalesced, so a cold start still patches exactly once. The probe
// rate is the DEMAND rate: the ~1 Hz settings tick (governor-gated, only while Settings is the
// active tab), tab-open renders, and the Refresh button.
//
// THE ONE GATE LEFT is not a TTL and not hand-sized: a probe may not restart until
// probeBudget x ITS OWN last measured duration has passed, so no probe can cost more than
// 1/probeBudget of a core however often demand arrives. Measured on a 5950X
// (TestProbeRealDurations): tools 4.2 ms, dev:midi 59 ms, dev:waveout 7.1 ms, dev:midiout 0.5 ms,
// dev:sttmic 297 ms, the rest ~0. Six of the eight therefore re-run at the full demand rate (1 Hz,
// 10x fresher than the 10 s TTL) while the 297 ms STT mic enumeration - the probe that actually
// motivated the TTL - prices itself out to ~6 s. That is the point: a cheap probe is no longer
// gated by an expensive sibling, which is exactly the coupling one busy flag + one TTL created.
//
// The gridfix env probe keeps its own long TTL (settings_gridfix.go): it spawns Python, so even
// cost-proportional pacing would mean an interpreter every few seconds. Different class, kept.

// probeCache is the retained probe state. Slots are read by the render path (never blocking, never
// touching the OS); live/done/pend/dirty are the concurrency bookkeeping.
type probeCache struct {
	mu    sync.Mutex
	tools map[string]mediatools.Status // key ("ffmpeg"|"fpcalc"|"mpv") → last status
	vr    vrdll.Status
	devs  map[string][]string          // kind ("midi"|"waveout"|"midiout"|"sttmic"|"audiorec") → names
	unity map[string]unityproj.Project // project dir → inspect result
	gpus  []gpuAdapterRow              // encode-GPU picker rows (DXGI adapters + live encode load)

	slots map[string]*probeSlot // probe key → its single-flight + pacing state
	pend  int                   // probes in flight across the current kick(s)
	dirty bool                  // a landed probe changed what the tab renders
	// repatches counts the coalesced re-renders this cache asked for. A cold start lands eight
	// probes and must ask exactly ONCE; the coalescing gate reads this (the eval queue cannot serve
	// as the witness - it coalesces by fragment id, so N patchMains look like one entry there).
	repatches int
	// frozen freezes the cache: kickProbes starts nothing. Test fixtures set it so a golden render
	// can never race a real OS probe overwriting the fixture's slots (pre-B4c the fixtures did this
	// by parking `at` inside the TTL window, which is exactly the mechanism this replaces).
	frozen bool
}

// probeSlot is one probe's scheduling state.
type probeSlot struct {
	live bool          // a run is in flight (per-probe single-flight; the rate limit while it runs)
	done bool          // at least one run landed (gates render off this, never off a placeholder)
	took time.Duration // last measured run time (the pacing input)
	at   time.Time     // when the last run finished
}

// probeBudget caps a probe's share of one core: it may not restart until probeBudget x its own last
// duration has elapsed. 20 = 5%, which keeps off-lane background work in the noise (this repo's
// idle-CPU discipline) with no fixed staleness bound anywhere.
const probeBudget = 20

// probe keys (single-flight identities; also the map keys the tests assert on).
const (
	pkTools    = "tools"
	pkVR       = "vr"
	pkMidi     = "dev:midi"
	pkWaveOut  = "dev:waveout"
	pkMidiOut  = "dev:midiout"
	pkSttMic   = "dev:sttmic"
	pkAudioRec = "dev:audiorec"
	pkUnity    = "unity"
	pkGPUEnc   = "dev:gpuenc"
)

// probeSpec is one independent probe: it does its own blocking work, commits its own slot and
// reports whether the rendered DOM can differ now.
type probeSpec struct {
	key string
	run func(u *UI) bool
}

// settingsProbeTable is the probe set. Order is irrelevant - they run concurrently. Swapped only by
// tests (serially), which is why it is a var and not a function.
var settingsProbeTable = []probeSpec{
	{pkTools, probeTools},
	{pkVR, probeVRDLL},
	{pkMidi, devProbe(pkMidi, "midi", midi.Ports)},
	{pkWaveOut, devProbe(pkWaveOut, "waveout", timecode.WaveOutDevices)},
	{pkMidiOut, devProbe(pkMidiOut, "midiout", timecode.MidiOutDevices)},
	{pkSttMic, devProbe(pkSttMic, "sttmic", stt.InputDevices)},
	{pkAudioRec, probeAudioRec},
	{pkUnity, probeUnity},
	{pkGPUEnc, probeGPUEncoders},
}

// ── slot readers (render path: never blocks, never touches the OS) ──

// toolStatusCached returns the retained status for a media tool (zero/uninstalled until its probe
// first lands).
func (u *UI) toolStatusCached(key string) mediatools.Status {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	return u.probes.tools[key]
}

// vrStatusCached returns the retained vrdll probe.
func (u *UI) vrStatusCached() vrdll.Status {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	return u.probes.vr
}

// devNamesCached returns the retained device enumeration for kind (empty until its probe lands).
func (u *UI) devNamesCached(kind string) []string {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	return u.probes.devs[kind]
}

// unityInfoCached returns the retained inspect for a Unity project dir.
func (u *UI) unityInfoCached(dir string) unityproj.Project {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	return u.probes.unity[dir]
}

// gpuAdaptersCached returns the retained encode-GPU rows (empty until the probe lands, and always
// empty off Windows - the picker then offers automatic/avoid only).
func (u *UI) gpuAdaptersCached() []gpuAdapterRow {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	return u.probes.gpus
}

// probeDone reports that key's probe has landed at least once (a cold slot renders zero values, so
// callers that need a real answer - the timecode device modal - ask first).
func (u *UI) probeDone(key string) bool {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	s := u.probes.slots[key]
	return s != nil && s.done
}

// slotOf returns key's slot, creating it. Caller holds mu.
func (p *probeCache) slotOf(key string) *probeSlot {
	if p.slots == nil {
		p.slots = map[string]*probeSlot{}
	}
	s := p.slots[key]
	if s == nil {
		s = &probeSlot{}
		p.slots[key] = s
	}
	return s
}

// ── scheduling ──

// kickProbes starts every probe that is not already running, each on its own goroutine. Non-blocking
// (a map walk + N goroutine spawns), so it is safe on the render and handler lanes.
func (u *UI) kickProbes() {
	u.probes.mu.Lock()
	if u.probes.frozen {
		u.probes.mu.Unlock()
		return
	}
	var start []probeSpec
	for _, p := range settingsProbeTable {
		s := u.probes.slotOf(p.key)
		if s.live {
			continue // still running: its guard IS the rate limit
		}
		if s.done && time.Since(s.at) < probeBudget*s.took {
			continue // cost-proportional pacing (probeBudget), never a fixed staleness bound
		}
		s.live = true
		u.probes.pend++
		start = append(start, p)
	}
	u.probes.mu.Unlock()
	for _, p := range start {
		spec := p
		u.bg(func() { u.runProbe(spec) })
	}
}

// probeNow runs the named probes on the CALLER's goroutine (which must be off the render/handler
// lane) and returns once their slots are committed - used right after an install, where the
// follow-up patchMain has to see the new state instead of waiting for the next demand kick. A probe
// already in flight is skipped: it will commit the same filesystem truth.
func (u *UI) probeNow(keys ...string) {
	for _, k := range keys {
		for _, p := range settingsProbeTable {
			if p.key != k {
				continue
			}
			u.probes.mu.Lock()
			s := u.probes.slotOf(p.key)
			if u.probes.frozen || s.live {
				u.probes.mu.Unlock()
				continue
			}
			s.live, u.probes.pend = true, u.probes.pend+1
			u.probes.mu.Unlock()
			u.runProbe(p)
		}
	}
}

// runProbe executes one probe off-lane and releases its guard. The LAST probe in flight owns the
// re-render, so eight probes landing together (cold start) patch ONCE - patchMain rebuilds the whole
// document, and eight concurrent rebuilds would also race the smart-select registration.
func (u *UI) runProbe(p probeSpec) {
	t0 := time.Now()
	changed := p.run(u)
	took := time.Since(t0)
	u.probes.mu.Lock()
	s := u.probes.slotOf(p.key)
	s.live, s.took, s.at = false, took, time.Now()
	if !s.done {
		s.done = true
		changed = true // first landing always patches (the pre-B4c !ready arm)
	}
	u.probes.dirty = u.probes.dirty || changed
	u.probes.pend--
	patch := u.probes.pend <= 0 && u.probes.dirty
	if patch {
		u.probes.dirty, u.probes.pend = false, 0
		u.probes.repatches++
	}
	u.probes.mu.Unlock()
	if patch && !u.stopped() && u.activeTab() == "settings" {
		u.patchMain()
	}
}

// ── the probes ──

// probeTools re-probes the managed media tools (os.Stat + PATH scan each). Patch-worthy when an
// install state or resolved path moved: the install-card bodies only re-render on a full patchMain,
// not on the 1 Hz status tick.
func probeTools(u *UI) bool {
	next := map[string]mediatools.Status{
		"ffmpeg": mediatools.FFmpeg.Status(),
		"fpcalc": mediatools.Fpcalc.Status(),
		"mpv":    mediatools.MPV.Status(),
	}
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	changed := toolInstallChanged(u.probes.tools, next)
	u.probes.tools = next
	return changed
}

// probeVRDLL re-probes openvr_api.dll (fs probe; skipped entirely on a non-VR build).
func probeVRDLL(u *UI) bool {
	var next vrdll.Status
	if vroverlay.BuiltWithVR() {
		next = vrdll.Probe()
	}
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	changed := next.Installed != u.probes.vr.Installed
	u.probes.vr = next
	return changed
}

// devProbe builds a probe that enumerates one device kind (OS device APIs + fs).
func devProbe(key, kind string, list func() ([]string, error)) func(*UI) bool {
	return func(u *UI) bool { return u.commitDevs(kind, mustNames(list)) }
}

// probeAudioRec enumerates capture devices through the recorder proxy (absent until wired).
func probeAudioRec(u *UI) bool {
	if u.svc.AudioRec == nil {
		return u.commitDevs("audiorec", nil)
	}
	return u.commitDevs("audiorec", mustNames(u.svc.AudioRec.Devices))
}

// commitDevs stores one device list; patch-worthy when the names changed (the pickers are rendered
// by the card bodies, not the status tick).
func (u *UI) commitDevs(kind string, names []string) bool {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	if u.probes.devs == nil {
		u.probes.devs = map[string][]string{}
	}
	changed := !sameNames(u.probes.devs[kind], names)
	u.probes.devs[kind] = names
	return changed
}

// probeUnity re-inspects every configured Unity project (fs reads per dir).
func probeUnity(u *UI) bool {
	next := map[string]unityproj.Project{}
	if u.svc.Cfg != nil {
		for _, dir := range u.svc.Cfg.Features.Unity.Projects {
			next[dir] = unityproj.Inspect(dir)
		}
	}
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	// pre-B4c the changed-check ignored Unity entirely, so a project that gained/lost its plugin
	// only surfaced on the next unrelated patchMain. Deliberately fixed: same settled DOM, sooner.
	changed := len(next) != len(u.probes.unity)
	for dir, p := range next {
		if u.probes.unity[dir] != p {
			changed = true
		}
	}
	u.probes.unity = next
	return changed
}

// gpuAdapterRow is one encode-GPU option: adapter LUID + a label carrying the GPU name, its live
// video-encode load and who is holding it.
type gpuAdapterRow struct {
	LUID  string
	Label string
}

// probeGPUEncoders enumerates GPU adapters (DXGI, ~1 ms) and, ONLY on a multi-adapter machine, joins
// each with its live video-encode load + detected holders (PDH GPU-Engine counters + a config-only
// OBS read, ~300 ms - the probe budget then paces it to a few seconds). A single-GPU box gets the
// cheap path: with one adapter every device policy resolves to "automatic" anyway, so the load
// column would cost 300 ms to show something un-actionable. Read-only: never touches the GPU, never
// starts an encode.
func probeGPUEncoders(u *UI) bool {
	ads := encoderscan.Adapters()
	var rep encoderscan.Report
	if len(ads) > 1 {
		rep = encoderscan.Detect(func() (stream, record string, active bool, err error) {
			s, r, ok := encoderscan.OBSConfigEncoder()
			if !ok {
				return "", "", false, nil
			}
			return s, r, false, nil
		})
	}
	next := make([]gpuAdapterRow, 0, len(ads))
	for _, a := range ads {
		label := a.Name
		if label == "" {
			label = a.LUID
		}
		if load := rep.AdapterLoad(a.LUID); load != "" {
			label += " · " + load
		}
		if who := rep.AdapterHolders(a.LUID); who != "" {
			label += " · " + who
		}
		next = append(next, gpuAdapterRow{LUID: a.LUID, Label: label})
	}
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	changed := len(next) != len(u.probes.gpus)
	for i := range next {
		if !changed && next[i] != u.probes.gpus[i] {
			changed = true
		}
	}
	u.probes.gpus = next
	return changed
}

// sameNames compares two device enumerations.
func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// toolInstallChanged reports whether any tool's install-state (installed/path) differs between two
// probe snapshots - the trigger for the one-shot Settings re-render.
func toolInstallChanged(a, b map[string]mediatools.Status) bool {
	if len(a) != len(b) {
		return true
	}
	for k, bv := range b {
		av := a[k]
		if av.Installed != bv.Installed || av.Path != bv.Path {
			return true
		}
	}
	return false
}

// mustNames drops the error from a device enumeration (an unavailable API = no devices).
func mustNames(fn func() ([]string, error)) []string {
	n, _ := fn()
	return n
}
