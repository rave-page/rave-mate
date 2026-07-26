package featurehost

import (
	"math"
	"os"
	"runtime/debug"
	"testing"

	"rave.page/mate/internal/config"
)

// The media child is the one subsystem that OOM'd a host: it must carry BOTH liveness backstops -
// a heartbeat timeout (a wedged cgo capture stops beating but keeps holding the camera) and the
// job-object RAM cap propagated into the spawn snapshot so the child can set its own soft limit.
func TestNewMediaHostWiresHeartbeatAndMemLimit(t *testing.T) {
	h, err := NewMediaHost(nil, nil, nil, MediaHostDeps{
		Self: "node", Label: "box",
		Cfg: func() (config.MediaLinkFeature, config.WebcamFeature) {
			return config.MediaLinkFeature{}, config.WebcamFeature{}
		},
		Secrets:    func() map[string][]byte { return map[string][]byte{"peer": {1, 2, 3}} },
		Codecs:     func() (enc, dec []string) { return []string{"h264_mf"}, []string{"h264"} },
		SyncPeer:   func() string { return "" },
		SameHost:   func(string) bool { return false },
		MemLimitMB: 2048,
	})
	if err != nil {
		t.Fatalf("NewMediaHost: %v", err)
	}
	if h.host.opt.HeartbeatTimeout <= 0 {
		t.Error("media child must have a heartbeat timeout (it beats from its 1 Hz telemetry loop)")
	}
	if h.host.opt.MemLimitMB != 2048 {
		t.Errorf("job-object RAM cap not applied: %d", h.host.opt.MemLimitMB)
	}
	in, ok := h.host.opt.Init().(mediaInit)
	if !ok {
		t.Fatalf("Init snapshot is not a mediaInit: %T", h.host.opt.Init())
	}
	if in.MemLimitMB != 2048 {
		t.Errorf("spawn snapshot must carry the cap so the child can set GOMEMLIMIT below it, got %d", in.MemLimitMB)
	}
	if len(in.Secrets) != 1 || in.SyncPeer != "" || len(in.Encoders) != 1 {
		t.Errorf("spawn snapshot regressed: %+v", in)
	}
}

// The child's Go soft limit must sit BELOW the hard job cap (which kills the process), so the GC
// fights a frame runaway first.
func TestSetChildMemoryLimit(t *testing.T) {
	if os.Getenv("GOMEMLIMIT") != "" {
		t.Skip("operator GOMEMLIMIT wins by design")
	}
	prev := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(prev)

	setChildMemoryLimit(0, nil) // uncapped child: leave the runtime default alone
	if got := debug.SetMemoryLimit(-1); got != prev {
		t.Errorf("capMB<=0 must not touch the limit: %d != %d", got, prev)
	}
	setChildMemoryLimit(2048, nil)
	want := int64(2048) * childMemSoftPct / 100 * 1024 * 1024
	if got := debug.SetMemoryLimit(-1); got != want {
		t.Errorf("soft limit = %d, want %d (%d%% of the 2048MB hard cap)", got, want, childMemSoftPct)
	}
	if want >= int64(2048)*1024*1024 || want >= math.MaxInt64 {
		t.Error("soft limit must stay strictly under the hard cap")
	}
}
