// Package encoderscan detects which video encoders are already in use on this machine so medialink
// can steer its cross-PC encode/decode onto a DIFFERENT device and never starve the workloads the
// user actually depends on during a live set - OBS→stream, Parsec remote, VRChat. Detection is
// READ-ONLY (config/API reads + OS perf counters): it never touches the GPU or starts an encode.
//
// Three signals, combined for confidence:
//   - OBS: the obs-websocket-configured stream/record encoder (jim_nvenc, h264_texture_amf, …).
//   - Parsec: the parsecd process + its log's active-encoder line.
//   - Utilization: per-process video-encode engine load per GPU adapter (Windows PDH GPU Engine
//     counters - the same source Task Manager's "Video Encode" graph uses; vendor-neutral).
//
// The synthesis is pure and platform-agnostic (Scan); the GPU sampler is injected so it's testable
// with fakes and stubbed to nil off Windows.
package encoderscan

import (
	"fmt"
	"sort"
	"strings"
)

// EncoderFamily is the hardware block an encoder runs on - the unit of contention. Two encoders in
// the same family on the same adapter fight for the same silicon; different families don't.
type EncoderFamily string

const (
	FamilyNVENC        EncoderFamily = "nvenc"        // NVIDIA NVENC
	FamilyAMF          EncoderFamily = "amf"          // AMD AMF (discrete GPU + Ryzen iGPU/VCN)
	FamilyQSV          EncoderFamily = "qsv"          // Intel Quick Sync
	FamilyMF           EncoderFamily = "mf"           // Windows Media Foundation - wraps ANY registered HW MFT (incl. custom cards)
	FamilyVAAPI        EncoderFamily = "vaapi"        // Linux VA-API (Intel/AMD/other)
	FamilyV4L2         EncoderFamily = "v4l2"         // Linux V4L2 M2M (embedded/SoC/custom)
	FamilyVideoToolbox EncoderFamily = "videotoolbox" // Apple VideoToolbox
	FamilyMediaCodec   EncoderFamily = "mediacodec"   // Android MediaCodec
	FamilyOMX          EncoderFamily = "omx"          // OpenMAX (SoC/custom)
	FamilyOtherHW      EncoderFamily = "otherhw"      // hardware encoder of an unrecognized vendor (still HW, not CPU)
	FamilyX264         EncoderFamily = "x264"         // CPU (libx264/libx265/software) - no GPU encode block
	FamilyUnknown      EncoderFamily = "unknown"
)

// hwFamilies is every family that runs on dedicated encode silicon (not the CPU). Vendor-neutral:
// the point is "does this contend for a GPU/ASIC encode engine", not "which brand".
var hwFamilies = map[EncoderFamily]bool{
	FamilyNVENC: true, FamilyAMF: true, FamilyQSV: true, FamilyMF: true, FamilyVAAPI: true,
	FamilyV4L2: true, FamilyVideoToolbox: true, FamilyMediaCodec: true, FamilyOMX: true, FamilyOtherHW: true,
}

// IsHardware reports whether the family runs on dedicated encode silicon (vs the CPU).
func (f EncoderFamily) IsHardware() bool { return hwFamilies[f] }

// FamilyFromOBSID maps an OBS encoder id OR an ffmpeg encoder name to its hardware family, vendor-
// neutrally. Substring match so it survives OBS renames + covers every backend ffmpeg exposes
// (jim_nvenc/ffmpeg_hevc_nvenc→NVENC; h264_texture_amf/"amd"→AMF; obs_qsv11→QSV; h264_mf→MF;
// *_vaapi→VAAPI; *_v4l2m2m→V4L2; *_videotoolbox→VideoToolbox; *_mediacodec→MediaCodec; *_omx→OMX;
// x264/x265/lib*→CPU). An unrecognized "<codec>_<suffix>" HW-looking id → OtherHW (still hardware).
func FamilyFromOBSID(id string) EncoderFamily {
	s := strings.ToLower(strings.TrimSpace(id))
	switch {
	case s == "":
		return FamilyUnknown
	case strings.Contains(s, "nvenc"):
		return FamilyNVENC
	case strings.Contains(s, "qsv"):
		return FamilyQSV
	case strings.Contains(s, "amf"), strings.Contains(s, "amd"):
		return FamilyAMF
	case strings.Contains(s, "vaapi"):
		return FamilyVAAPI
	case strings.Contains(s, "v4l2"):
		return FamilyV4L2
	case strings.Contains(s, "videotoolbox"):
		return FamilyVideoToolbox
	case strings.Contains(s, "mediacodec"):
		return FamilyMediaCodec
	case strings.Contains(s, "omx"):
		return FamilyOMX
	case strings.HasSuffix(s, "_mf"), strings.Contains(s, "_mf_"), strings.Contains(s, "mediafoundation"):
		return FamilyMF
	case strings.Contains(s, "x264"), strings.Contains(s, "x265"), strings.Contains(s, "libvpx"),
		strings.Contains(s, "libaom"), strings.Contains(s, "libsvt"), strings.HasPrefix(s, "lib"):
		return FamilyX264
	case hasCodecPrefix(s): // "<codec>_<vendor>" we don't recognize (a custom card) → still HW, not CPU
		return FamilyOtherHW
	default:
		return FamilyUnknown
	}
}

// hasCodecPrefix reports whether an id looks like "<video-codec>_<backend>" - the shape of an
// ffmpeg HW encoder for a vendor we don't explicitly name (so it's classified hardware, not CPU).
func hasCodecPrefix(s string) bool {
	for _, p := range []string{"h264_", "h265_", "hevc_", "av1_", "vp9_", "vp8_", "mpeg2_", "mpeg4_"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// Proc is a running process (name + pid) used to detect Parsec and attribute GPU util.
type Proc struct {
	Name string // image name, e.g. "obs64.exe"
	PID  int
}

// GPUSample is one process's video-engine utilization on one GPU adapter (from PDH on Windows).
type GPUSample struct {
	PID         int
	Adapter     string  // adapter LUID key ("" if unknown)
	AdapterName string  // human name if resolved ("NVIDIA GeForce RTX 4090")
	EncodePct   float64 // VideoEncode engine utilization %
	DecodePct   float64 // VideoDecode engine utilization %
}

// GPUSampler returns current per-process video encode/decode utilization. nil = unavailable
// (non-Windows / no counters) → the scan falls back to family-only protection.
type GPUSampler func() ([]GPUSample, error)

// Deps are the injected, individually-optional detection inputs (nil = that signal is skipped).
type Deps struct {
	// OBSEncoder returns the configured stream/record encoder ids + whether OBS is actively
	// streaming or recording (i.e. actually holding an encoder). nil = OBS not connected.
	OBSEncoder func() (stream, record string, active bool, err error)
	// Processes lists running processes (Parsec/VRChat detection + PID→name for GPU samples).
	Processes func() ([]Proc, error)
	// GPU samples per-process video-engine utilization. nil = no live util (family-only fallback).
	GPU GPUSampler
	// ParsecEncoder parses the active encoder + adapter from Parsec's log ("" if not found).
	ParsecEncoder func() (family EncoderFamily, adapter string, ok bool)
	// AdapterNames resolves adapter LUID key → human GPU name (DXGI on Windows). nil = no names.
	AdapterNames func() map[string]string
	// AdapterVRAM resolves adapter LUID key → free (budgeted) VRAM MB (DXGI QueryVideoMemoryInfo on
	// Windows). nil / missing key = unknown → the planner skips that device's VRAM ceiling.
	AdapterVRAM func() map[string]float64
}

// Consumer is a process observed (or configured) to use a video-encode engine - a workload
// medialink must avoid contending with.
type Consumer struct {
	Role     string        // "obs" | "parsec" | "vrchat" | "other"
	Name     string        // process image name if known
	PID      int           // 0 = from config only, not a live process match
	Family   EncoderFamily // encoder family (from config/log; Unknown if only util-detected)
	Adapter  string        // GPU adapter key if known ("" = unknown / CPU)
	EncPct   float64       // live VideoEncode util % (-1 = unknown)
	Critical bool          // true = must never be starved (OBS actively streaming/recording, Parsec)
}

// Report is the synthesized scan: who's encoding, and which adapters/families medialink must avoid.
type Report struct {
	Consumers        []Consumer             // detected encode workloads
	ProtectedAdapter map[string]bool        // GPU adapter keys busy with a critical consumer → avoid
	ProtectedFamily  map[EncoderFamily]bool // families a critical consumer uses (fallback when adapter unknown)
	AdapterEncPct    map[string]float64     // total VideoEncode util % per adapter (device-load ranking)
	AdapterNames     map[string]string      // adapter LUID key → human GPU name (DXGI; empty if unresolved)
	AdapterVRAMFree  map[string]float64     // adapter LUID key → free (budgeted) VRAM MB (missing = unknown)
	Notes            []string               // human-readable detection notes
}

// roleForProc classifies a process image name.
func roleForProc(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "obs"): // obs64.exe, obs32.exe, obs.exe
		return "obs"
	case strings.Contains(n, "parsec"): // parsecd.exe, parsec.exe
		return "parsec"
	case strings.Contains(n, "vrchat"):
		return "vrchat"
	default:
		return ""
	}
}

// encThreshold is the VideoEncode % above which a process counts as actively holding an encoder.
const encThreshold = 3.0

// Scan runs the detection and synthesizes the Report. Pure given its Deps - every OS/IO edge is an
// injected func, so the whole thing is unit-testable with fakes.
func Scan(d Deps) Report {
	r := Report{ProtectedAdapter: map[string]bool{}, ProtectedFamily: map[EncoderFamily]bool{},
		AdapterEncPct: map[string]float64{}}

	var procs []Proc
	if d.Processes != nil {
		if ps, err := d.Processes(); err == nil {
			procs = ps
		} else {
			r.Notes = append(r.Notes, "process list unavailable: "+err.Error())
		}
	}
	// pid → live GPU samples (encode/decode util per adapter).
	var samples []GPUSample
	if d.GPU != nil {
		if ss, err := d.GPU(); err == nil {
			samples = ss
			if len(ss) == 0 {
				note := "gpu utilization: no video encode/decode samples (util shown as ?)"
				if diag := gpuDiagNote(); diag != "" {
					note += " - " + diag
				}
				r.Notes = append(r.Notes, note)
			}
		} else {
			r.Notes = append(r.Notes, "gpu utilization unavailable: "+err.Error())
		}
	}
	if d.AdapterNames != nil {
		r.AdapterNames = d.AdapterNames()
	}
	if d.AdapterVRAM != nil {
		r.AdapterVRAMFree = d.AdapterVRAM()
	}
	byPID := map[int][]GPUSample{}
	for i, s := range samples {
		if r.AdapterNames != nil {
			samples[i].AdapterName = r.AdapterNames[s.Adapter]
		}
		byPID[s.PID] = append(byPID[s.PID], samples[i])
		if s.Adapter != "" {
			r.AdapterEncPct[s.Adapter] += s.EncodePct
		}
	}
	nameByPID := map[int]string{}
	for _, p := range procs {
		nameByPID[p.PID] = p.Name
	}

	// helper: the adapter a pid is encoding on + its util (busiest encode sample), if any.
	encodeOf := func(pid int) (adapter, adapterName string, pct float64, ok bool) {
		best := -1.0
		for _, s := range byPID[pid] {
			if s.EncodePct > best {
				best, adapter, adapterName = s.EncodePct, s.Adapter, s.AdapterName
			}
		}
		if best >= 0 {
			return adapter, adapterName, best, true
		}
		return "", "", 0, false
	}

	addConsumer := func(c Consumer) {
		r.Consumers = append(r.Consumers, c)
		if c.Critical {
			if c.Adapter != "" {
				r.ProtectedAdapter[c.Adapter] = true
			}
			if c.Family != FamilyUnknown && c.Family != FamilyX264 {
				r.ProtectedFamily[c.Family] = true
			}
		}
	}

	// ── OBS (the one that must never be touched) ──────────────────────────────
	if d.OBSEncoder != nil {
		stream, record, active, err := d.OBSEncoder()
		if err != nil {
			r.Notes = append(r.Notes, "obs encoder query failed: "+err.Error())
		} else {
			fam := FamilyFromOBSID(stream)
			if fam == FamilyUnknown && record != "" {
				fam = FamilyFromOBSID(record)
			}
			c := Consumer{Role: "obs", Family: fam, EncPct: -1, Critical: active}
			// attach the live adapter/util from the obs process, if we can match it.
			for _, p := range procs {
				if roleForProc(p.Name) == "obs" {
					c.Name, c.PID = p.Name, p.PID
					if a, an, pct, ok := encodeOf(p.PID); ok {
						c.Adapter, c.EncPct = a, pct
						_ = an
						if pct >= encThreshold {
							c.Critical = true // actually encoding right now
						}
					}
					break
				}
			}
			addConsumer(c)
			note := fmt.Sprintf("OBS stream encoder=%q (%s)", stream, fam)
			if active {
				note += " - ACTIVE (streaming/recording)"
			}
			r.Notes = append(r.Notes, note)
		}
	}

	// ── Parsec (remote desktop encoder) ───────────────────────────────────────
	var parsecPID int
	var parsecName string
	for _, p := range procs {
		if roleForProc(p.Name) == "parsec" {
			parsecPID, parsecName = p.PID, p.Name
			break
		}
	}
	if parsecPID != 0 {
		c := Consumer{Role: "parsec", Name: parsecName, PID: parsecPID, Family: FamilyUnknown, EncPct: -1, Critical: true}
		if d.ParsecEncoder != nil {
			if fam, adapter, ok := d.ParsecEncoder(); ok {
				c.Family, c.Adapter = fam, adapter
			}
		}
		if a, _, pct, ok := encodeOf(parsecPID); ok {
			if c.Adapter == "" {
				c.Adapter = a
			}
			c.EncPct = pct
		}
		addConsumer(c)
		r.Notes = append(r.Notes, fmt.Sprintf("Parsec running (pid %d, %s)", parsecPID, c.Family))
	}

	// ── Anything else on the encode engine (VRChat, stray encoders) ───────────
	seen := map[int]bool{}
	for _, c := range r.Consumers {
		if c.PID != 0 {
			seen[c.PID] = true
		}
	}
	for pid, ss := range byPID {
		if seen[pid] {
			continue
		}
		best := -1.0
		var adapter string
		for _, s := range ss {
			if s.EncodePct > best {
				best, adapter = s.EncodePct, s.Adapter
			}
		}
		if best < encThreshold {
			continue
		}
		name := nameByPID[pid]
		role := roleForProc(name)
		if role == "" {
			role = "other"
		}
		// VRChat sharing a GPU matters for headroom but isn't a hard "never touch" like OBS/Parsec.
		crit := role == "vrchat"
		addConsumer(Consumer{Role: role, Name: name, PID: pid, Family: FamilyUnknown,
			Adapter: adapter, EncPct: best, Critical: crit})
	}

	sort.SliceStable(r.Consumers, func(i, j int) bool {
		return r.Consumers[i].EncPct > r.Consumers[j].EncPct
	})
	return r
}

// String renders a compact human-readable report (for the ctl dry-run).
// adapterLabel renders "<luid> (GPU name)" when the name is known, else just the luid key.
func (r Report) adapterLabel(luid string) string {
	if n := r.AdapterNames[luid]; n != "" {
		return luid + " (" + n + ")"
	}
	return luid
}

// adapterRows renders EVERY adapter the machine has, not just the ones PDH happened to sample.
// Each row carries its encoder FAMILY (which silicon it would encode on) and free VRAM, because
// "which adapters exist and what can each of them encode" is the question this scan is asked, and
// answering it from utilization counters alone silently omits idle hardware.
func (r Report) adapterRows() []string {
	seen := map[string]bool{}
	for luid := range r.AdapterNames {
		seen[luid] = true
	}
	for luid := range r.AdapterEncPct {
		seen[luid] = true // sampled but not enumerated (shouldn't happen; never hide it if it does)
	}
	rows := make([]string, 0, len(seen))
	for luid := range seen {
		row := r.adapterLabel(luid)
		if fam := familyForAdapter(r.AdapterNames[luid]); fam != "" {
			row += " " + string(fam)
		}
		// enc=? distinguishes "idle / no counters" from a measured 0%: an adapter with no PDH
		// GPU-Engine instances is not the same as one measured at zero.
		if pct, ok := r.AdapterEncPct[luid]; ok {
			row += fmt.Sprintf(" enc=%.0f%%", pct)
		} else {
			row += " enc=? (idle: no GPU-Engine counters)"
		}
		if free, ok := r.AdapterVRAMFree[luid]; ok {
			row += fmt.Sprintf(" vram-free=%.0fMB", free)
		}
		rows = append(rows, row)
	}
	sort.Strings(rows)
	return rows
}

// ambiguousVendors names each encoder family that matches MORE THAN ONE adapter, i.e. where
// adapterForFamily deliberately refuses to guess.
func (r Report) ambiguousVendors() []string {
	count := map[EncoderFamily]int{}
	for _, name := range r.AdapterNames {
		if fam := familyForAdapter(name); fam != "" {
			count[fam]++
		}
	}
	var out []string
	for fam, n := range count {
		if n > 1 {
			out = append(out, fmt.Sprintf("%d %s adapters", n, fam))
		}
	}
	sort.Strings(out)
	return out
}

// familyForAdapter maps a GPU description to the hardware encoder family it would use ("" =
// unrecognised vendor). Same vendor table the advertisement join uses.
func familyForAdapter(name string) EncoderFamily {
	lc := strings.ToLower(name)
	for fam, vendors := range familyVendors {
		for _, v := range vendors {
			if strings.Contains(lc, v) {
				return fam
			}
		}
	}
	return ""
}

func (r Report) String() string {
	var b strings.Builder
	b.WriteString("encoder scan - workloads to avoid:\n")
	if len(r.Consumers) == 0 {
		b.WriteString("  (none detected)\n")
	}
	for _, c := range r.Consumers {
		util := "util=?"
		if c.EncPct >= 0 {
			util = fmt.Sprintf("enc=%.0f%%", c.EncPct)
		}
		adapter := c.Adapter
		if adapter == "" {
			adapter = "adapter=?"
		}
		crit := ""
		if c.Critical {
			crit = " [PROTECT]"
		}
		fmt.Fprintf(&b, "  %-7s %-14s pid=%-6d fam=%-7s %s %s%s\n",
			c.Role, c.Name, c.PID, c.Family, adapter, util, crit)
	}
	if len(r.ProtectedAdapter) > 0 {
		var as []string
		for a := range r.ProtectedAdapter {
			as = append(as, r.adapterLabel(a))
		}
		sort.Strings(as)
		b.WriteString("protected adapters: " + strings.Join(as, ", ") + "\n")
	}
	// Adapters come from the DXGI ENUMERATION (AdapterNames), unioned with whatever PDH sampled.
	// Rendering only AdapterEncPct hid every adapter with no live GPU-Engine counters: an IDLE
	// discrete GPU has no engine instances at all, so a machine with an iGPU driving the display
	// and a dGPU doing nothing listed only the iGPU - the good encoder was invisible, and any
	// device policy reading this list could only ever pick the wrong GPU. Verified in the field:
	// a Radeon RX 7900 XTX never appeared while the Ryzen iGPU always did.
	if adapters := r.adapterRows(); len(adapters) > 0 {
		b.WriteString("adapters: " + strings.Join(adapters, " | ") + "\n")
	}
	// Two GPUs of the SAME vendor cannot be told apart by the encoder-name→vendor join, so those
	// devices render as "device=?" with a conservative load. Say so out loud: silently ambiguous
	// device rows on a machine that HAS a good discrete GPU read as "no device info available".
	if amb := r.ambiguousVendors(); len(amb) > 0 {
		fmt.Fprintf(&b, "note: %s - encoder→adapter binding is ambiguous (same-vendor GPUs); "+
			"pin the encode device explicitly to target one\n", strings.Join(amb, ", "))
	}
	if len(r.ProtectedFamily) > 0 {
		var fs []string
		for f := range r.ProtectedFamily {
			fs = append(fs, string(f))
		}
		sort.Strings(fs)
		b.WriteString("protected families: " + strings.Join(fs, ", ") + "\n")
	}
	for _, n := range r.Notes {
		b.WriteString("note: " + n + "\n")
	}
	return b.String()
}
