package timecode

import (
	"testing"
	"time"

	"rave.page/mate/internal/medialink"
)

// TestMTCQuarterFrameSequence checks the 8 quarter-frame data bytes for a known TC + rate bits.
func TestMTCQuarterFrameSequence(t *testing.T) {
	// 01:02:03:04 @30fps non-drop → rate code 11 (=3).
	tc := Timecode{H: 1, M: 2, S: 3, F: 4, Rate: medialink.FPS30}
	want := []byte{
		0x04, // p0 frames LSN = 4
		0x10, // p1 frames MSN = 0
		0x23, // p2 seconds LSN = 3
		0x30, // p3 seconds MSN = 0
		0x42, // p4 minutes LSN = 2
		0x50, // p5 minutes MSN = 0
		0x61, // p6 hours LSN = 1
		0x76, // p7 hours MSN 0 | rateCode 3<<1
	}
	for p := 0; p < 8; p++ {
		if got := mtcQuarterFrame(tc, p); got != want[p] {
			t.Errorf("piece %d = %#02x, want %#02x", p, got, want[p])
		}
	}
}

// TestMTCQuarterFrameDropFrame checks rate code 10 (29.97 DF) + a two-digit field split.
func TestMTCQuarterFrameDropFrame(t *testing.T) {
	// 10:20:30:25 @29.97DF. MTC nibbles are BINARY (not BCD): frames=25=0x19 → LSN 9, MSN 1.
	// seconds=30=0x1E → LSN 0xE, MSN 1. minutes=20=0x14 → LSN 4, MSN 1. hours=10=0x0A → LSN 0xA,
	// MSN 0. rate code 10 (=2).
	tc := Timecode{H: 10, M: 20, S: 30, F: 25, Rate: medialink.FPS2997}
	want := []byte{
		0x09, // p0 frames LSN
		0x11, // p1 frames MSN
		0x2E, // p2 seconds LSN
		0x31, // p3 seconds MSN
		0x44, // p4 minutes LSN
		0x51, // p5 minutes MSN
		0x6A, // p6 hours LSN
		0x74, // p7 hours MSN 0 | rateCode 2<<1
	}
	for p := 0; p < 8; p++ {
		if got := mtcQuarterFrame(tc, p); got != want[p] {
			t.Errorf("df piece %d = %#02x, want %#02x", p, got, want[p])
		}
	}
}

// TestMTCFullFrame checks the full-frame SysEx layout incl. the rate code in the hours byte.
func TestMTCFullFrame(t *testing.T) {
	tc := Timecode{H: 10, M: 20, S: 30, F: 25, Rate: medialink.FPS2997} // rate code 2
	want := []byte{0xF0, 0x7F, 0x7F, 0x01, 0x01, (2 << 5) | 10, 20, 30, 25, 0xF7}
	got := mtcFullFrame(tc)
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("byte %d = %#02x, want %#02x", i, got[i], want[i])
		}
	}
}

// TestArtTimeCodePacket golden-checks the 19-byte ArtTimeCode packet.
func TestArtTimeCodePacket(t *testing.T) {
	tc := Timecode{H: 1, M: 2, S: 3, F: 4, Rate: medialink.FPS2997} // type 2 (29.97 DF)
	want := [19]byte{
		'A', 'r', 't', '-', 'N', 'e', 't', 0x00,
		0x00, 0x97, // OpCode 0x9700 LE
		0x00, 0x0E, // ProtVer 14 BE
		0x00, 0x00, // filler
		4, 3, 2, 1, // Frames, Seconds, Minutes, Hours
		2, // Type
	}
	if got := artTimeCodePacket(tc); got != want {
		t.Errorf("packet = % x\n want   = % x", got, want)
	}
}

// TestArtNetTypeCodes covers all four supported rate → type mappings.
func TestArtNetTypeCodes(t *testing.T) {
	cases := []struct {
		r    Rate
		want byte
	}{
		{medialink.FPS24, 0}, {medialink.FPS25, 1}, {medialink.FPS2997, 2}, {medialink.FPS30, 3},
	}
	for _, c := range cases {
		if got := artNetType(c.r); got != c.want {
			t.Errorf("artNetType(%v) = %d, want %d", c.r, got, c.want)
		}
	}
}

// TestLTCSlewRoundTrip renders slew-limited LTC frames and confirms medialink.DecodeLTC still
// recovers the timecode - the slew must not destroy the biphase zero-crossings.
func TestLTCSlewRoundTrip(t *testing.T) {
	rates := []Rate{medialink.FPS24, medialink.FPS25, medialink.FPS30, medialink.FPS2997}
	for _, r := range rates {
		start := Timecode{H: 12, M: 34, S: 56, F: 7, Rate: r}
		g := newLTCGen(r, 48000, gainToAmp(-3), start.Frames())
		// pull ~3 frames of continuous slew-limited audio
		buf := make([]int16, 48000/8) // ~125ms → several frames at any rate
		g.fill(buf)
		tc, ok := medialink.DecodeLTC(buf, r)
		if !ok {
			t.Fatalf("rate %v: DecodeLTC found no frame in slew-limited stream", r)
		}
		if tc.H != start.H || tc.M != start.M || tc.S != start.S {
			t.Errorf("rate %v: decoded %s, want ~%s", r, tc.String(), start.String())
		}
		if tc.Rate.Drop != r.Drop {
			t.Errorf("rate %v: decoded drop=%v", r, tc.Rate.Drop)
		}
	}
}

// TestGeneratorFrameMath29976DF advances the master clock over simulated hours at 29.97DF and
// checks the frame count matches wall-clock (exact fractional rate, drop-frame aware).
func TestGeneratorFrameMath2997DF(t *testing.T) {
	var fake time.Time
	base := time.Unix(0, 0)
	fake = base
	g := NewGenerator()
	g.now = func() time.Time { return fake }
	g.Start(medialink.FPS2997, Timecode{Rate: medialink.FPS2997})

	for _, secs := range []int64{1, 60, 600, 3600, 7200} {
		fake = base.Add(time.Duration(secs) * time.Second)
		// 29.97 = 30000/1001 fps → frames = secs * 30000 / 1001
		wantFrames := secs * 30000 / 1001
		if got := g.FrameNow(); got != wantFrames {
			t.Errorf("t=%ds: FrameNow=%d, want %d", secs, got, wantFrames)
		}
		// Round-trip through the drop-frame Timecode should preserve the frame index.
		tc := g.Now()
		if tc.Frames() != wantFrames {
			t.Errorf("t=%ds: Now().Frames()=%d, want %d (tc=%s)", secs, tc.Frames(), wantFrames, tc.String())
		}
	}
}

// TestGeneratorQuarterFrame confirms sub-frame resolution: the QF index steps 0..7 within a
// 2-frame pair at ~8.33ms cadence @30fps (MTC would emit piece = qf&7).
func TestGeneratorQuarterFrame(t *testing.T) {
	var fake time.Time
	base := time.Unix(0, 0)
	fake = base
	g := NewGenerator()
	g.now = func() time.Time { return fake }
	g.Start(medialink.FPS30, Timecode{Rate: medialink.FPS30})

	qfPeriod := time.Second / (30 * 4) // 8.33ms (truncated; epsilon keeps ticks past the boundary)
	for i := int64(0); i < 16; i++ {
		fake = base.Add(time.Duration(i)*qfPeriod + time.Microsecond)
		if got := g.QuarterFrameNow(); got != i {
			t.Fatalf("tick %d: QuarterFrameNow=%d, want %d", i, got, i)
		}
	}
}

// TestGeneratorJam re-locates the clock and confirms Now follows.
func TestGeneratorJam(t *testing.T) {
	var fake time.Time
	base := time.Unix(100, 0)
	fake = base
	g := NewGenerator()
	g.now = func() time.Time { return fake }
	g.Start(medialink.FPS30, Timecode{Rate: medialink.FPS30})

	fake = base.Add(5 * time.Second)
	g.Jam(Timecode{H: 1, M: 0, S: 0, F: 0, Rate: medialink.FPS30})
	if got := g.Now(); got.H != 1 || got.M != 0 || got.S != 0 {
		t.Fatalf("post-jam Now=%s, want ~01:00:00:00", got.String())
	}
	fake = fake.Add(2 * time.Second)
	if got := g.Now(); got.S != 2 {
		t.Errorf("2s after jam: Now=%s, want ~01:00:02:xx", got.String())
	}
}

// TestParseRate + TestParseStartTC quick sanity.
func TestParseRateAndStart(t *testing.T) {
	if r := ParseRate("29.97"); !r.Drop || r.Nominal != 30 {
		t.Errorf("ParseRate(29.97) = %+v", r)
	}
	if r := ParseRate("25"); r.Drop || r.Nominal != 25 {
		t.Errorf("ParseRate(25) = %+v", r)
	}
	tc := ParseStartTC("01:02:03:04", medialink.FPS30)
	if tc.H != 1 || tc.M != 2 || tc.S != 3 || tc.F != 4 {
		t.Errorf("ParseStartTC = %s", tc.String())
	}
	if bad := ParseStartTC("nope", medialink.FPS30); bad.Frames() != 0 {
		t.Errorf("ParseStartTC(nope) should be zero, got %s", bad.String())
	}
}
