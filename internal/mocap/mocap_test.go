package mocap

import (
	"context"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mocapnode"
)

func TestBuildSourceMapping(t *testing.T) {
	log := logbus.New(16)
	logf := func(string, ...any) {}

	src, desc, err := buildSource(config.MocapFeature{Monitor: 1, FPS: 25}, log, logf)
	if err != nil {
		t.Fatalf("desktop: %v", err)
	}
	d, ok := src.(*mocapnode.FFmpegDesktopSource)
	if !ok {
		t.Fatalf("desktop: got %T", src)
	}
	if d.Monitor != 1 || d.FPS != 25 || d.W != 1920 || d.H != 1080 {
		t.Errorf("desktop source = %+v", d)
	}
	if desc != "desktop monitor 1" {
		t.Errorf("desktop desc = %q", desc)
	}

	src, desc, err = buildSource(config.MocapFeature{Source: "spout", Device: "VRChat-StreamCamera"}, log, logf)
	if err != nil {
		t.Fatalf("spout: %v", err)
	}
	sp, ok := src.(*mocapnode.SpoutSource)
	if !ok || sp.Sender != "VRChat-StreamCamera" {
		t.Errorf("spout source = %T %+v", src, src)
	}
	if desc != "spout VRChat-StreamCamera" {
		t.Errorf("spout desc = %q", desc)
	}
	if _, _, err := buildSource(config.MocapFeature{Source: "spout"}, log, logf); err == nil {
		t.Error("spout without device: want error")
	}

	src, _, err = buildSource(config.MocapFeature{Source: "dshow", Device: "OBS Virtual Camera", FPS: 15}, log, logf)
	if err != nil {
		t.Fatalf("dshow: %v", err)
	}
	ds, ok := src.(*mocapnode.FFmpegDShowSource)
	if !ok || ds.Device != "OBS Virtual Camera" || ds.FPS != 15 {
		t.Errorf("dshow source = %T %+v", src, src)
	}
	if _, _, err := buildSource(config.MocapFeature{Source: "dshow"}, log, logf); err == nil {
		t.Error("dshow without device: want error")
	}
}

// The spout source fails fast on a non-SPOUT build (and on a missing sender otherwise), so
// the lifecycle test exercises the supervised-restart path without spawning ffmpeg.
func TestOverlayLifecycle(t *testing.T) {
	s := New(logbus.New(16), func() config.MocapFeature {
		return config.MocapFeature{Source: "spout", Device: "x"}
	})
	if s.Overlay() != nil {
		t.Fatal("Overlay before Start: want nil")
	}
	if st := s.Status(); st.Running {
		t.Fatal("Status before Start: want not running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.Overlay() == nil {
		t.Fatal("Overlay while running: want non-nil")
	}
	if st := s.Status(); !st.Running || st.Source != "spout x" {
		t.Fatalf("Status while running = %+v", st)
	}

	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for s.Overlay() != nil {
		if time.Now().After(deadline) {
			t.Fatal("Overlay after cancel: still non-nil")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := s.Status(); st.Running {
		t.Fatalf("Status after cancel = %+v", st)
	}
}

func TestStartRejectsInvalidConfig(t *testing.T) {
	s := New(logbus.New(16), func() config.MocapFeature {
		return config.MocapFeature{Source: "spout"} // no sender name
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err == nil {
		t.Fatal("Start with invalid config: want error")
	}
	if s.Overlay() != nil {
		t.Fatal("Overlay after failed Start: want nil")
	}
}
