package mediaroute

import (
	"image"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/framedebug"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/testcard"
	"rave.page/mate/internal/videoshare"
)

// tcManager: only live seam is NewFrameSender, so Testcard runs without a GPU or Spout DLL.
func tcManager(t *testing.T, snd videoshare.FrameSender) *Manager {
	t.Helper()
	framedebug.SetDir(t.TempDir())
	return New(Options{
		Log:            logbus.New(16),
		Router:         &fakeRouter{},
		Cfg:            func() config.MediaLinkFeature { return config.MediaLinkFeature{} },
		NewFrameSender: func(string) (videoshare.FrameSender, error) { return snd, nil },
	})
}

type captureSender struct {
	frames chan *image.NRGBA
}

func (c *captureSender) Send(img *image.NRGBA) error {
	// Copy: the generator reuses its buffer the moment Send returns (the FrameSender contract).
	cp := image.NewNRGBA(img.Rect)
	copy(cp.Pix, img.Pix)
	select {
	case c.frames <- cp:
	default:
	}
	return nil
}
func (c *captureSender) Close() {}

// The manager lifecycle: start publishes decodable frames under the fixed name, stats carry the
// generator's ground truth, stop tears down, restart survives.
func TestManagerTestcardLifecycle(t *testing.T) {
	snd := &captureSender{frames: make(chan *image.NRGBA, 4)}
	m := tcManager(t, snd)

	rep, err := m.Testcard("start", 0, 0, 0) // defaults
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gen == nil || rep.Gen.W != testcard.DefaultW || rep.Gen.FPS != testcard.DefaultFPS {
		t.Fatalf("start report gen=%+v", rep.Gen)
	}
	var img *image.NRGBA
	select {
	case img = <-snd.frames:
	case <-time.After(3 * time.Second):
		t.Fatal("no frame from the generator within 3s")
	}
	p, derr := testcard.Decode(img)
	if derr != testcard.DecodeOK {
		t.Fatalf("published frame not decodable: %v", derr)
	}
	if p.Session != rep.Gen.Session {
		t.Fatalf("frame session %d != reported %d", p.Session, rep.Gen.Session)
	}

	rep, err = m.Testcard("stop", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gen != nil {
		t.Fatal("gen still reported after stop")
	}

	if _, err := m.Testcard("bogus", 0, 0, 0); err == nil {
		t.Fatal("unknown op accepted")
	}
}

// The receive sink is a verifier stage: card frames arriving through the REAL Write path register
// under "out:<sender>" with exact gap/dup accounting - this is the end-of-chain truth the whole
// harness exists to produce.
func TestSpoutSinkVerifiesTestcardFrames(t *testing.T) {
	testcard.VerifyReset()
	framedebug.SetDir(t.TempDir())
	s := &spoutSink{log: logbus.New(16), fs: &countingSender{}, name: "verify me", w: 640, h: 360}

	now := time.Now()
	send := func(seq uint32) {
		img := image.NewNRGBA(image.Rect(0, 0, 640, 360))
		testcard.Render(img, testcard.Payload{Session: 0x321, Seq: seq,
			T0ms: uint32(now.UnixMilli()), FPS: 30}, now)
		if err := s.Write(&medialink.Frame{Kind: medialink.KindVideo, Payload: img.Pix}); err != nil {
			t.Fatal(err)
		}
	}
	for _, seq := range []uint32{5, 6, 6, 6, 9} { // 2 dups, then a jump of 3 (2 missed)
		send(seq)
	}
	v, ok := testcard.VerifySnapshot()["out:verify me"]
	if !ok {
		t.Fatal("sink stage not registered by the real Write path")
	}
	if v.Decoded != 5 || v.Dups != 2 || v.MaxDupRun != 2 || v.Gaps != 2 {
		t.Fatalf("decoded=%d dups=%d maxRun=%d gaps=%d, want 5/2/2/2", v.Decoded, v.Dups, v.MaxDupRun, v.Gaps)
	}
}

// FrameShot on a testcard sender must report per-grab seqs - the origin-side "how does this sender
// advance" answer.
func TestFrameShotDecodesTestcard(t *testing.T) {
	now := time.Now()
	seq := uint32(100)
	m := shotManager(t, func(string, int, int) (*image.NRGBA, error) {
		img := image.NewNRGBA(image.Rect(0, 0, 640, 360))
		testcard.Render(img, testcard.Payload{Session: 0x42, Seq: seq,
			T0ms: uint32(now.UnixMilli()), FPS: 30}, now)
		seq += 3
		return img, nil
	})
	m.senderSize = func(string) (int, int, bool) { return 640, 360, true }
	shot, err := m.FrameShot("card", 4, 1, 0, [4]int{})
	if err != nil {
		t.Fatal(err)
	}
	if len(shot.CardSeqs) != 4 {
		t.Fatalf("cardSeqs=%v, want 4 entries", shot.CardSeqs)
	}
	for i, want := range []int64{100, 103, 106, 109} {
		if shot.CardSeqs[i] != want {
			t.Fatalf("cardSeqs=%v, want [100 103 106 109]", shot.CardSeqs)
		}
	}
	if shot.CardSession != 0x42 || shot.CardFPS != 30 {
		t.Fatalf("session=%d fps=%d", shot.CardSession, shot.CardFPS)
	}
	if shot.CardSummary() == "" {
		t.Fatal("no card summary line")
	}
	// And a non-card sender must NOT grow card fields.
	m2 := shotManager(t, func(string, int, int) (*image.NRGBA, error) {
		return shotFrame(32, 18, 40), nil
	})
	shot2, err := m2.FrameShot("src-a", 3, 1, 0, [4]int{})
	if err != nil {
		t.Fatal(err)
	}
	if shot2.CardSeqs != nil || shot2.CardSummary() != "" {
		t.Fatalf("non-card sender reported card data: %v", shot2.CardSeqs)
	}
}
