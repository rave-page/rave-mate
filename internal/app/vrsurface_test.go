package app

import (
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/vroverlay"
)

// vrSurface falls back to the in-proc manager when the proxy is unavailable or subprocess mode is
// off, and routes to the proxy otherwise - the crash-containment default with a safe escape hatch.
func TestVRSurfaceFallback(t *testing.T) {
	mgr := vroverlay.New(logbus.New(8), nil, vroverlay.NewRuntime(),
		func() config.VROverlayFeature { return config.VROverlayFeature{} }, nil)
	proxy, err := featurehost.NewVrOverlayProxy(logbus.New(8), featurehost.VROverlayDeps{
		Cfg: func() config.VROverlayFeature { return config.VROverlayFeature{} },
	})
	if err != nil {
		t.Fatal(err)
	}

	use := false
	s := &vrSurface{mgr: mgr, proxy: proxy, useProxy: func() bool { return use }}
	if s.sel() != vroverlay.Surface(mgr) {
		t.Fatal("want in-proc manager while subprocess mode off")
	}
	use = true
	if s.sel() != vroverlay.Surface(proxy) {
		t.Fatal("want proxy in subprocess mode")
	}
	s.proxy = nil // proxy construction failed → fallback even with the flag on
	if s.sel() != vroverlay.Surface(mgr) {
		t.Fatal("want in-proc fallback when proxy is nil")
	}
}
