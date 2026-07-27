//go:build spout

package videoshare

// soak_spout_test.go - live 4K60 capture soak, the reproduction harness for the media-child
// OOM ("fatal error: runtime: cannot allocate memory" within seconds of a 3840x2160@60
// Spout capture→encode route). Opt-in: needs a GPU + SpoutLibrary.dll, so it is skipped
// unless RAVE_MATE_SPOUT_SOAK=1.
//
//	go test -tags spout ./internal/videoshare -run TestSpout4KCaptureSoak -v -timeout 5m
//	  RAVE_MATE_SPOUT_SOAK=1 SOAK_SECS=60 GOMEMLIMIT=1638MiB
//
// TWO PROCESSES on purpose: a same-process Spout send+receive loop deadlocks in the
// driver's keyed mutex (which is also why mediaroute refuses same-PC routes, §3). The
// parent re-execs itself as the publisher, then runs the real receiver poll loop with a
// consumer that holds each frame ~10 ms (stand-in for the 4K encode submit). Assert: pool
// occupancy plateaus at a couple of frames and the process RSS stays flat (sample it from
// outside with tasklist).

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

const soakSender = "rave-mate 4k soak"

// TestSpoutSoakPublisher is the child role: publishes a synthetic 4K60 stream.
func TestSpoutSoakPublisher(t *testing.T) {
	if os.Getenv("RAVE_PROBE_ROLE") != "send" {
		t.Skip("child role only (set by TestSpout4KCaptureSoak)")
	}
	log := logbus.New(64)
	fs, err := newFrameSender(log, soakSender)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[send] no sender:", err)
		return
	}
	defer fs.Close()
	src := image.NewNRGBA(image.Rect(0, 0, 3840, 2160))
	for i := range src.Pix {
		src.Pix[i] = byte(i)
	}
	tk := time.NewTicker(time.Second / 60)
	defer tk.Stop()
	stop := time.After(5 * time.Minute) // backstop; the parent kills us
	for n := 0; ; n++ {
		select {
		case <-stop:
			return
		case <-tk.C:
			src.Pix[0] = byte(n) // make every frame distinct
			_ = fs.Send(src)
		}
	}
}

func TestSpout4KCaptureSoak(t *testing.T) {
	if os.Getenv("RAVE_MATE_SPOUT_SOAK") == "" {
		t.Skip("set RAVE_MATE_SPOUT_SOAK=1 (needs a GPU + SpoutLibrary.dll)")
	}
	secs := 20
	if v := os.Getenv("SOAK_SECS"); v != "" {
		if k, err := strconv.Atoi(v); err == nil && k > 0 {
			secs = k
		}
	}
	if os.Getenv("RAVE_PROBE_EXTERNAL") == "" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=TestSpoutSoakPublisher", "-test.v")
		cmd.Env = append(os.Environ(), "RAVE_PROBE_ROLE=send")
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("spawn publisher: %v", err)
		}
		defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	}
	deadline := time.Now().Add(20 * time.Second)
	for !spoutFindSender(soakSender) {
		if time.Now().After(deadline) {
			t.Skip("publisher never registered (no GPU / no Spout runtime)")
		}
		time.Sleep(100 * time.Millisecond)
	}

	rx, err := newFrameReceiver(logbus.New(256), soakSender, RecvOptions{MaxFPS: 60})
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	defer rx.Close()

	var got, peakLive, peakIdle, blank int
	done := time.After(time.Duration(secs) * time.Second)
	report := time.NewTicker(5 * time.Second)
	defer report.Stop()
	for loop := true; loop; {
		select {
		case f, ok := <-rx.Frames():
			if !ok {
				loop = false
				break
			}
			if n := len(f.Pix); n != 3840*2160*4 {
				t.Fatalf("frame %d is %d bytes, want %d", got, n, 3840*2160*4)
			}
			// CONTENT check, not just framing: this soak counted frames happily for the whole time
			// the receive path was delivering all-zero buffers (the black-frame P0). The publisher
			// writes a gradient, so a mid-frame sample is non-zero on every real frame.
			mid := (2160/2)*(3840*4) + (3840/2)*4
			if f.Pix[mid] == 0 && f.Pix[mid+1] == 0 && f.Pix[mid+2] == 0 {
				blank++
			}
			got++
			time.Sleep(10 * time.Millisecond) // stand-in for the 4K encode submit
			PutPix(f.Pix)
		case <-report.C:
			live, idle, bufs := PoolStats()
			if live > peakLive {
				peakLive = live
			}
			if idle > peakIdle {
				peakIdle = idle
			}
			// Written to stderr: the consume loop starves t.Log while frames flow.
			fmt.Fprintf(os.Stderr, "[soak] frames=%d poolLiveMB=%d poolIdleMB=%d poolBufs=%d\n",
				got, live>>20, idle>>20, bufs)
		case <-done:
			loop = false
		}
	}
	if got == 0 {
		t.Fatal("no frames received")
	}
	// A soak that counts blank frames is measuring plumbing, not video.
	if blank*2 > got {
		t.Fatalf("%d of %d frames were BLANK - the readback delivered no pixels", blank, got)
	}
	// The pool must PLATEAU: a few frames in flight, never a climb. Before the geometry
	// guard this test's process reached 54 GB RSS and died.
	if peakLive > poolMaxLiveBytes {
		t.Errorf("peak live pool = %d B, over the ceiling %d", peakLive, poolMaxLiveBytes)
	}
	if peakIdle > poolMaxIdleBytes {
		t.Errorf("peak idle pool = %d B, over the cap %d", peakIdle, poolMaxIdleBytes)
	}
	t.Logf("soak: %d frames in %ds (%.1f fps, %d blank) peakLive=%dMB peakIdle=%dMB",
		got, secs, float64(got)/float64(secs), blank, peakLive>>20, peakIdle>>20)
}
