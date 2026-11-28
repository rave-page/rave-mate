package mediapipe

import (
	"context"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediatools"
)

// Live respawn over REAL ffmpeg (libx264; skips without it): a material bitrate hint respawns the
// child once after the debounce, the route never EOFs, the first AU after the gap is a keyframe, and
// the wall gap is reported (the receiver-visible hole).
func TestRetargetRespawnGapLive(t *testing.T) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("ffmpeg not available")
	}
	if caps, _ := Probe(context.Background(), nil); !contains(caps.Encoders, "libx264") {
		t.Skip("no libx264 in this ffmpeg build")
	}
	const w, h, fps = 320, 240, 30
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ch := make(chan *medialink.Frame, 1)
	go func() { // live 30 fps source, newest-wins like the webcam tap
		tk := time.NewTicker(time.Second / fps)
		defer tk.Stop()
		for i := 0; ctx.Err() == nil; i++ {
			<-tk.C
			buf := make([]byte, w*h*4)
			for p := 0; p < len(buf); p += 4 {
				buf[p], buf[p+1], buf[p+2], buf[p+3] = byte(i*7+p), byte(p>>8), byte(i), 0xFF
			}
			f := &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA, PTS: time.Now().UnixNano(), Payload: buf}
			select {
			case ch <- f:
			default:
			}
		}
	}()
	enc, err := newEncoder(ctx, logbus.New(64), ffmpeg, medialink.EncodeSpec{Encoder: "libx264", Codec: medialink.CodecH264,
		Width: w, Height: h, FPS: fps, BitrateKbps: 2000}, medialink.NewChanSource(ch))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = enc.Close() }()
	hinted := false
	var last, hintAt time.Time
	var maxGap time.Duration
	keyAfter := false
	n := 0
	for time.Since(hintAt) < 7*time.Second || !hinted {
		f, err := enc.Next(ctx)
		if err != nil {
			t.Fatalf("route ended across the respawn: %v (n=%d)", err, n)
		}
		now := time.Now()
		n++
		if !hinted && n == 30 {
			enc.SetRateHint(medialink.RateHint{MaxBitrateKbps: 700})
			hinted, hintAt = true, now
		}
		if hinted && n > 30 && !last.IsZero() {
			if g := now.Sub(last); g > maxGap {
				maxGap = g
				keyAfter = f.Keyframe()
			}
		}
		last = now
	}
	enc.mu.Lock()
	running := enc.rt.running.kbps
	enc.mu.Unlock()
	if running != 700 {
		t.Fatalf("child not respawned at the hinted ceiling: running %d kbps", running)
	}
	if !keyAfter {
		t.Fatalf("first AU after the respawn gap (%v) is not a keyframe", maxGap)
	}
	t.Logf("respawn gap (receiver-visible hole) = %v over %d AUs", maxGap, n)
	if maxGap > 1500*time.Millisecond {
		t.Fatalf("respawn gap %v too long", maxGap)
	}
}
