package featurehost

import (
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gpubudget"
	"rave.page/mate/internal/gpumem"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mfenc"
)

// VramBudget bundles the ONE gpubudget.Budget for this process with the sender-gate closure the
// mediaroute manager consumes. Decode + encode admission are installed directly into the mfenc seams.
type VramBudget struct {
	Budget      *gpubudget.Budget
	AdmitSender func(w, h int) (allow bool, reason string, freeMB, reserveMB uint64)
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
		adpOnce.Do(func() { adp = gpubudget.Adapter{Vendor: gpubudget.VendorOf(gpumem.ReadHeadroom(sampler).Name)} })
		return adp
	}
	pol := gpubudget.Policy{ReserveCfgMB: uint64(liveCfg().ResolvedVramReserveMB())}
	store := gpubudget.NewCostStore(costPath, func(m string) {
		if log != nil {
			log.Warn("gpubudget", m, nil)
		}
	})
	b := gpubudget.New(gpubudget.Options{Policy: pol, Store: store, Headroom: headroom, Self: self.Get, Adapter: adapter})

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
		Budget: b,
		AdmitSender: func(w, h int) (bool, string, uint64, uint64) {
			d := b.AdmitSender(w, h)
			return d.Admit, d.Reason, d.FreeMB, d.ReserveMB
		},
	}
}

func decCodec(hevc bool) string {
	if hevc {
		return "hevc"
	}
	return "h264"
}

// VramCostPath is the shared learned-cost file path (daemon + media child use the same file).
func VramCostPath() string {
	p, err := config.DataPath("gpucost.json")
	if err != nil {
		return ""
	}
	return p
}
