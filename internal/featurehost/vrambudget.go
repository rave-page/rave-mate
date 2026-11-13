package featurehost

import (
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/gpubench"
	"rave.page/mate/internal/gpubudget"
	"rave.page/mate/internal/gpumem"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/mfenc"
	"rave.page/mate/internal/sysactivity"
)

// VramBudget bundles the ONE gpubudget.Budget for this process with the sender-gate closure the
// mediaroute manager consumes. Decode + encode admission are installed directly into the mfenc seams.
type VramBudget struct {
	Budget      *gpubudget.Budget
	AdmitSender func(w, h int) mediaroute.Admission
	// AdmitReceive is the pre-offer receive gate: the decoder, plus a new sender and the receiver follow
	// it triggers, judged as ONE need; refused => held without offering (no capture/encode on the source).
	AdmitReceive func(w, h, fps int, newSender bool) mediaroute.Admission
	// GovernorFloorMB is the receive rate-ladder floor for the active policy (gpubudget.GovernorFloorMB).
	GovernorFloorMB uint64
}

// admission converts a budget decision for mediaroute.
func admission(d gpubudget.Decision) mediaroute.Admission {
	return mediaroute.Admission{Allow: d.Admit, Attempt: d.Attempt, Reason: d.Reason, Policy: string(d.Mode),
		FreeMB: d.FreeMB, NeedMB: uint64(d.NeedMB + 0.5), RequiredMB: d.ReserveMB}
}

// PolicyFromConfig resolves the gpubudget policy from MediaLink config (unknown preset => balanced,
// ok=false so the caller logs it once).
func PolicyFromConfig(c config.MediaLinkFeature) (gpubudget.Policy, bool) {
	mode, ok := gpubudget.ParseMode(c.VramPolicyName())
	return gpubudget.Policy{Mode: mode, MarginMinMB: uint64(c.ResolvedVramMarginMB()),
		ReserveCfgMB: uint64(c.ResolvedVramReserveMB())}, ok
}

// WireVramBudget builds the shared VRAM budget for whichever process opens GPU sessions (the media
// child by default, or the daemon in legacy in-proc mode) and installs the mfenc decode/encode
// admission + measurement seams so one policy governs decode, sender and encode. liveCfg supplies the
// live MediaLink config; sampler is the shared gpumem sampler; costPath persists learned costs; log
// (nil-safe) reports a corrupt cost file. The reserve config is captured at wire time (consistent
// with mfenc.DecRebuildFloorMB); the governor on/off gate stays live via headroom.
func WireVramBudget(liveCfg func() config.MediaLinkFeature, sampler gpumem.Sampler, costPath string, log *logbus.Bus) *VramBudget {
	headroom := func() gpumem.Headroom {
		if !liveCfg().VramGovernorEnabled() {
			return gpumem.Headroom{} // governor off => Present=false => admission fails open
		}
		return gpumem.ReadHeadroom(sampler)
	}
	self := gpubudget.NewSelfFootprint(sampler, nil, 30*time.Second)
	var adpOnce sync.Once
	var adp gpubudget.Adapter
	adapter := func() gpubudget.Adapter {
		adpOnce.Do(func() {
			name := gpumem.ReadHeadroom(sampler).Name
			// Key by vendor + model@driver: the model distinguishes GPUs, the driver version
			// distinguishes measurements across driver updates (registry; "" when unavailable).
			adp = gpubudget.Adapter{Vendor: gpubudget.VendorOf(name), Driver: name + "@" + primaryGPUDriverVersion(name)}
		})
		return adp
	}
	pol, polOK := PolicyFromConfig(liveCfg())
	if !polOK && log != nil {
		log.Warn("gpubudget", "unknown mediaLink.vramPolicy - using balanced", map[string]any{"value": liveCfg().VramPolicyName()})
	}
	store := gpubudget.NewCostStore(costPath, func(m string) {
		if log != nil {
			log.Warn("gpubudget", m, nil)
		}
	})
	b := gpubudget.New(gpubudget.Options{Policy: pol, Store: store, Headroom: headroom,
		Self: self.Get, SelfNow: self.Sample, Adapter: adapter})

	mfenc.DecAdmit = func(inW, inH, _, _, fps int, hevc bool) mfenc.DecVerdict {
		d := b.AdmitDecode(inW, inH, fps, decCodec(hevc))
		return mfenc.DecVerdict{Allow: d.Admit, Present: d.Present, FreeMB: d.FreeMB,
			CostMB: d.CostMB, ReserveMB: d.ReserveMB, Reason: d.Reason}
	}
	mfenc.DecObserve = func(inW, inH, fps int, hevc bool, mb float64) {
		b.Observe(gpubudget.OpDecode, gpubudget.Geom{W: inW, H: inH, FPS: fps, Codec: decCodec(hevc)}, mb)
	}
	mfenc.EncAdmit = func(outW, outH, fps int, codec string) (bool, string) {
		d := b.AdmitEncode(outW, outH, fps, codec)
		return d.Admit, d.Reason
	}
	mfenc.EncObserve = func(outW, outH, fps int, codec string, mb float64) {
		b.Observe(gpubudget.OpEncode, gpubudget.Geom{W: outW, H: outH, FPS: fps, Codec: codec}, mb)
	}
	return &VramBudget{
		Budget:      b,
		AdmitSender: func(w, h int) mediaroute.Admission { return admission(b.AdmitSender(w, h)) },
		// Codec unknown pre-offer: HEVC is the costlier learned 4K decode on the rig (114 vs 56 MB).
		AdmitReceive: func(w, h, fps int, newSender bool) mediaroute.Admission {
			return admission(b.AdmitReceive(w, h, fps, "hevc", newSender))
		},
		GovernorFloorMB: gpubudget.GovernorFloorMB(pol.Mode, uint64(liveCfg().ResolvedVramReserveMB())),
	}
}

func decCodec(hevc bool) string {
	if hevc {
		return "hevc"
	}
	return "h264"
}

// encCodec maps an encoder name ("h264_nvenc","hevc_amf","libx264") to the throughput-cache codec.
func encCodec(encoder string) string {
	e := strings.ToLower(encoder)
	if strings.Contains(e, "hevc") || strings.Contains(e, "265") {
		return "hevc"
	}
	return "h264"
}

// encDevTTL bounds how often the plan-driven resolver re-enumerates adapters / re-scans util.
const encDevTTL = 5 * time.Second

// NewEncodeDeviceResolver builds the medialink EncodeDevice callback (pin/avoid unchanged; AUTO
// becomes throughput-aware plan-driven via encoderscan.ResolveDevicePlan). Enriches the negotiated
// encoder's candidate device with the measured throughput from the shared budget; UNMEASURED silicon
// stays at the engine default (no worse than before). TTL-cached (Detect is ~300ms); logs the choice
// on change. cpu (nil-safe) supplies system CPU% for a software encoder candidate.
func NewEncodeDeviceResolver(cfg func() config.MediaLinkFeature, budget *gpubudget.Budget, cpu func() float64, log *logbus.Bus) func(w, h, fps int, encoder string) (string, int) {
	obsEnc := func() (stream, record string, active bool, err error) {
		s, r, ok := encoderscan.OBSConfigEncoder()
		if !ok {
			return "", "", false, nil
		}
		return s, r, false, nil // config-only: no obs-websocket round trip, no startup-order race
	}
	var mu sync.Mutex
	var adapters []encoderscan.AdapterInfo
	var report encoderscan.Report
	var at time.Time
	var lastWhy string
	return func(w, h, fps int, encoder string) (string, int) {
		policy, pin := cfg().DevicePref()
		mu.Lock()
		defer mu.Unlock()
		if at.IsZero() || time.Since(at) > encDevTTL {
			adapters = encoderscan.Adapters()
			report = encoderscan.Detect(obsEnc)
			at = time.Now()
		}
		var cpct float64
		if cpu != nil {
			cpct = cpu()
		}
		devs := encoderscan.Devices([]string{encoder}, report, cpct)
		if budget != nil {
			geom := gpubudget.Geom{W: w, H: h, FPS: fps, Codec: encCodec(encoder)}
			if pps, ok := budget.Throughput(gpubudget.OpEncode, geom); ok {
				for i := range devs {
					devs[i].ThroughputPPS = pps
				}
			}
		}
		d := encoderscan.ResolveDevicePlan(policy, pin, adapters, report, devs, encoderscan.PixelRate(w, h, fps))
		if log != nil && d.Reason != lastWhy {
			lastWhy = d.Reason
			log.Info("medialink", "encode device selected", map[string]any{
				"adapter": d.LUID, "index": d.Index, "name": d.Name, "why": d.Reason,
				"encoder": encoder, "reqMpps": encoderscan.PixelRate(w, h, fps) / 1e6})
		}
		return d.LUID, d.Index
	}
}

// benchIdleThreshold: the machine must be idle at least this long before a GPU micro-benchmark runs.
const benchIdleThreshold = 30 * time.Second

// NewBenchRunner builds the throughput micro-benchmark Runner for the media child: encode timer
// (mfenc), gate (headroom above reserve + idle + no protected OBS/Parsec encode + room for the bench's
// own cost), and RouteLive (skip the lazy sweep while any route is live). routeLive=nil => never live.
func NewBenchRunner(budget *gpubudget.Budget, sampler gpumem.Sampler, routeLive func() bool, log *logbus.Bus) *gpubench.Runner {
	obsEnc := func() (stream, record string, active bool, err error) {
		s, r, ok := encoderscan.OBSConfigEncoder()
		if !ok {
			return "", "", false, nil
		}
		return s, r, false, nil
	}
	act := sysactivity.New()
	benchCost := uint64(gpubudget.EstimateCostMB(gpubudget.OpEncode, gpubudget.Geom{W: 3840, H: 2160, FPS: 60, Codec: "h264"}))
	gate := func() encoderscan.BenchGate {
		h := gpumem.ReadHeadroom(sampler)
		idle := false
		if d, ok := act.IdleDuration(); ok && d >= benchIdleThreshold {
			idle = true
		}
		protected := false
		for _, p := range encoderscan.Detect(obsEnc).ProtectedAdapter {
			if p {
				protected = true
				break
			}
		}
		return encoderscan.BenchGate{HeadroomPresent: h.Present, FreeMB: h.FreeMB,
			ReserveMB: budget.ReserveFloorMB(), BenchCostMB: benchCost,
			MachineIdle: idle, ProtectedBusy: protected}
	}
	return gpubench.New(gpubench.Options{
		Budget: budget, Gate: gate, Encode: encodeBenchTimer,
		RouteLive: func(string) bool { return routeLive != nil && routeLive() },
		Log: func(m string, kv map[string]any) {
			if log != nil {
				log.Info("gpubench", m, kv)
			}
		},
	})
}

// FirstHWEncoder returns the first working hardware encoder in the list (ok=false if none).
func FirstHWEncoder(encoders []string) (string, bool) {
	for _, e := range encoders {
		if encoderscan.FamilyFromOBSID(e).IsHardware() {
			return e, true
		}
	}
	return "", false
}

// EncCodec maps an encoder name to the throughput-cache codec (exported for feat_media's lazy Want).
func EncCodec(encoder string) string { return encCodec(encoder) }

// VramCostPath is the shared learned-cost file path (daemon + media child use the same file).
func VramCostPath() string {
	p, err := config.DataPath("gpucost.json")
	if err != nil {
		return ""
	}
	return p
}
