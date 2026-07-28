package mediaroute

import (
	"errors"
	"image"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/framedebug"
	"rave.page/mate/internal/logbus"
)

// shotManager builds a Manager whose only live seam is GrabFrame, so the verdict logic is testable
// without a GPU or a Spout DLL.
func shotManager(t *testing.T, grab func(string, int, int) (*image.NRGBA, error)) *Manager {
	t.Helper()
	framedebug.SetDir(t.TempDir())
	return New(Options{
		Log:         logbus.New(16),
		Router:      &fakeRouter{},
		Cfg:         func() config.MediaLinkFeature { return config.MediaLinkFeature{} },
		ListSenders: func() []string { return []string{"src-a", "src-b"} },
		SenderSize: func(n string) (int, int, bool) {
			if n == "src-a" {
				return 32, 18, true
			}
			return 0, 0, false
		},
		GrabFrame: grab,
	})
}

func shotFrame(w, h int, v byte) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = v
	}
	return img
}

// The verdict that matters: a sender whose own texture never changes is FROZEN AT THE SOURCE, and
// saying so requires reading the sender directly - every downstream counter reads healthy meanwhile.
func TestFrameShotCallsAFrozenSenderFrozen(t *testing.T) {
	m := shotManager(t, func(string, int, int) (*image.NRGBA, error) {
		return shotFrame(32, 18, 42), nil // same content every grab
	})
	shot, err := m.FrameShot("src-a", 5, 1, 1, [4]int{})
	if err != nil {
		t.Fatal(err)
	}
	if shot.Grabs != 5 {
		t.Fatalf("grabs=%d, want 5", shot.Grabs)
	}
	if shot.Changes != 0 {
		t.Fatalf("changes=%d on identical content, want 0", shot.Changes)
	}
	if !shot.Frozen() {
		t.Fatalf("Frozen()=false for 5 identical grabs; verdict was %q", shot.Verdict())
	}
	if len(shot.PNG) == 0 {
		t.Fatal("no PNG: a frozen verdict must still show WHAT is frozen")
	}
	if len(shot.Hashes) != 5 {
		t.Fatalf("hashes=%d, want one per grab so WHICH grabs differed survives", len(shot.Hashes))
	}
}

func TestFrameShotCallsAChangingSenderLive(t *testing.T) {
	i := 0
	m := shotManager(t, func(string, int, int) (*image.NRGBA, error) {
		i++
		return shotFrame(32, 18, byte(i*10)), nil
	})
	shot, err := m.FrameShot("src-a", 4, 1, 1, [4]int{})
	if err != nil {
		t.Fatal(err)
	}
	if shot.Changes != 3 {
		t.Fatalf("changes=%d over 4 differing grabs, want 3", shot.Changes)
	}
	if shot.Frozen() {
		t.Fatalf("Frozen()=true on changing content; verdict was %q", shot.Verdict())
	}
}

// Two grabs cannot tell a freeze from bad luck, so the verdict must NOT claim frozen.
func TestFrameShotWontCallTwoGrabsFrozen(t *testing.T) {
	m := shotManager(t, func(string, int, int) (*image.NRGBA, error) {
		return shotFrame(32, 18, 7), nil
	})
	shot, _ := m.FrameShot("src-a", 2, 1, 1, [4]int{})
	if shot.Frozen() {
		t.Fatalf("2 identical grabs reported FROZEN: %q", shot.Verdict())
	}
}

// An unknown or unnamed sender must list the candidates rather than fail blankly - the name is the
// thing a caller most often gets wrong, and Spout names carry spaces.
func TestFrameShotListsCandidatesForUnknownSender(t *testing.T) {
	m := shotManager(t, func(string, int, int) (*image.NRGBA, error) {
		return shotFrame(32, 18, 1), nil
	})
	for _, name := range []string{"", "src-b", "nope"} {
		shot, err := m.FrameShot(name, 3, 1, 1, [4]int{})
		if err != nil {
			t.Fatal(err)
		}
		if shot.Grabs != 0 || shot.Err == "" {
			t.Fatalf("sender %q: grabs=%d err=%q, want 0 grabs + an error", name, shot.Grabs, shot.Err)
		}
		if len(shot.Senders) == 0 {
			t.Fatalf("sender %q: no candidate list returned", name)
		}
	}
}

// A transient grab failure must not void the whole sample: the surviving grabs still carry a verdict,
// and the error is reported alongside rather than swallowed.
func TestFrameShotSurvivesTransientGrabFailures(t *testing.T) {
	i := 0
	m := shotManager(t, func(string, int, int) (*image.NRGBA, error) {
		i++
		if i == 2 {
			return nil, errors.New("transient miss")
		}
		return shotFrame(32, 18, byte(i)), nil
	})
	shot, err := m.FrameShot("src-a", 4, 1, 1, [4]int{})
	if err != nil {
		t.Fatal(err)
	}
	if shot.Grabs != 3 {
		t.Fatalf("grabs=%d, want 3 (one grab failed)", shot.Grabs)
	}
	if shot.Err == "" {
		t.Fatal("the failed grab was swallowed; a partial sample must say so")
	}
}
