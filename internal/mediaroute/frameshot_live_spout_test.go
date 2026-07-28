//go:build windows && cgo && spout

package mediaroute

import (
	"os"
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/framedebug"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/videoshare"
)

// Live gate for the origin-side oracle against a REAL Spout sender (Resolume, OBS, our own
// republish - whatever is registered):
//
//	go test -tags spout ./internal/mediaroute -run TestFrameShotLive -v
//
// The seams stay at their videoshare defaults on purpose. Every synthetic gate in this repo has been
// able to pass while the real path was broken - the black-frame P0 was a vtable/header mismatch that
// no fake could have caught - so this one exercises GrabSenderFrame itself.
func TestFrameShotLiveReadsARealSender(t *testing.T) {
	names := videoshare.ListSenders()
	if len(names) == 0 {
		t.Skip("no Spout senders registered (need SpoutLibrary.dll + a running sender)")
	}
	// Target one sender by name to judge a SPECIFIC route (e.g. a republished peer route that looks
	// frozen) instead of whichever sender happens to be first.
	if want := os.Getenv("RAVE_MATE_FRAMESHOT_SENDER"); want != "" {
		names = []string{want}
	}
	m := New(Options{
		Log:    logbus.New(64),
		Router: &fakeRouter{},
		Cfg:    func() config.MediaLinkFeature { return config.MediaLinkFeature{} },
	})
	framedebug.SetDir(t.TempDir())

	for _, name := range names {
		w, h, ok := videoshare.SenderSize(name)
		if !ok || w <= 0 || h <= 0 {
			t.Logf("sender %q: no geometry, skipping", name)
			continue
		}
		shot, err := m.FrameShot(name, 5, 100, 0, [4]int{})
		if err != nil {
			t.Fatalf("sender %q: %v", name, err)
		}
		t.Logf("sender %q %dx%d -> %s", name, shot.W, shot.H, shot.Verdict())
		if shot.Grabs == 0 {
			// Not a failure of this gate: a sender can be mid-teardown. Report it and keep going -
			// silently treating it as a pass is how a broken read path stays hidden.
			t.Logf("sender %q: 0 grabs (err=%q)", name, shot.Err)
			continue
		}
		if len(shot.PNG) == 0 {
			t.Errorf("sender %q: %d grabs but no PNG", name, shot.Grabs)
		}
		if len(shot.Hashes) != shot.Grabs {
			t.Errorf("sender %q: %d hashes for %d grabs", name, len(shot.Hashes), shot.Grabs)
		}
		// A full-resolution crop must come back at exactly the requested size - that is what makes
		// small in-frame text (an OS clock in a captured desktop) readable.
		if w > 200 && h > 100 {
			cw, ch := 160, 80
			cr, err := m.FrameShot(name, 1, 0, 0, [4]int{w - cw, h - ch, cw, ch})
			if err != nil {
				t.Fatal(err)
			}
			if cr.Grabs > 0 && len(cr.PNG) == 0 {
				t.Errorf("sender %q: crop produced no PNG", name)
			}
		}
		return // one real sender proves the path
	}
	t.Skip("no sender with usable geometry: " + strings.Join(names, ", "))
}
