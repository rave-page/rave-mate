//go:build vr

package vroverlay

import (
	"sync"
	"testing"
)

// All OpenVR entry is serialized by ovrMu: daemon-RPC surfaces (BindingStatus/InputDiag/
// ActionBinding, polled off-thread) historically raced the VR goroutine's pump + Shutdown's map
// reassignment. Run under -race; needs no SteamVR session - the runtime stays not-ok, but map
// reads (TextureInfo/OverlayQuad) vs Shutdown's reassignments detect a missing lock.
func TestOpenVRConcurrentEntrySerialized(t *testing.T) {
	r := NewRuntime()
	ed, isEd := r.(Editor)
	if !isEd {
		t.Fatal("openvr runtime must implement Editor")
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_ = r.Available()
				_ = r.PollQuit()
				_, _, _, _ = ed.TextureInfo("k")
				_, _, _, _ = ed.OverlayQuad("k")
				_ = ed.BindingStatus()
				_ = ed.InputReady()
				ed.InputUpdate()
				_ = ed.ActionBinding("/actions/main/in/summon")
				_, _ = r.PerfStats()
				r.Shutdown() // reassigns the state maps - the write side of the race
			}
		}()
	}
	wg.Wait()
}
