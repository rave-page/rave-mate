//go:build windows && cgo && spout

package videoshare

import (
	"fmt"
	"image"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/spoutdll"
)

// The crash this gate exists for: ReleaseSender REMOVES a sender from the process-global list in
// shared memory that rave_spout_scan walks BY INDEX. Unlocked, a scan that had already taken its
// GetSenderCount read a slot release had just removed -> access violation INSIDE SpoutLibrary.dll,
// which surfaces as Go's unrecoverable `fatal error: fault`. In the field (2026-07-29) that killed
// the media child and every live route at once, hit by stopping ONE route while the routine 2 s
// mediaroute scan ran.
//
// A fault is unrecoverable, so the assertion is the test process still being alive at the end:
// pre-fix this binary dies mid-run and `go test` reports a failure with the SpoutLibrary frames in
// the trace; post-fix it completes. Revert the registry_mu guards in spout_shim.cpp to see it fail.
func TestScanDoesNotRaceSenderRelease(t *testing.T) {
	if st := spoutdll.Probe(); !st.Installed {
		t.Skip("SpoutLibrary.dll not reachable - stage it beside the test exe or in <RAVE_MATE_CONFIG_DIR>/bin")
	}
	log := logbus.New(64)
	// 1080p: the field crash released a 4K sender, and geometry decides how long the registry entry
	// is half-built. A 64x36 sender registers too fast to collide.
	img := image.NewNRGBA(image.Rect(0, 0, 1920, 1080))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}

	// A resident sender so the scan actually ITERATES a list instead of finding one entry - the
	// crash was an indexed walk landing on a slot that release had removed.
	resident, err := NewFrameSender(log, "rave-mate racegate resident")
	if err != nil {
		t.Skipf("no video-share backend: %v", err)
	}
	defer resident.Close()
	_ = resident.Send(img)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Scanners: what mediaroute.scan() does every 2 s, tightened and doubled to widen the window.
	for range 2 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = ListSenders()
			}
		})
	}

	// Churn: create a sender, publish enough frames that it really registers, release it. Release is
	// the mutator; the first Send is the other one (it is what creates the sender - there is no
	// CreateSender on this SDK pairing). Several names in parallel, as several routes stopping would.
	for c := range 3 {
		name := fmt.Sprintf("rave-mate racegate %d", c)
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				fs, err := NewFrameSender(log, name)
				if err != nil {
					return
				}
				for range 2 {
					_ = fs.Send(img)
				}
				fs.Close()
			}
		})
	}

	time.Sleep(20 * time.Second)
	close(stop)
	wg.Wait()
	// Reaching here at all is the pass: a faulting DLL call would have taken the process down.
}
