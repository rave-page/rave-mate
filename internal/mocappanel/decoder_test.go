package mocappanel

// Stateful Decoder tests: the three wrapper behaviours - MAGIC 3-miss hysteresis,
// frameCounter staleness, 2-frame boneMask acceptance.

import (
	"errors"
	"image"
	"image/color"
	"testing"
)

// goldenAt re-encodes the golden frame with a chosen frameCounter.
func goldenAt(counter uint32) *image.NRGBA {
	h, d := GoldenFrame()
	h.FrameCounter = counter
	return Encode(h, d)
}

// goldenMask re-encodes the golden frame with dancer0's boneMask replaced.
func goldenMask(mask uint32, counter uint32) *image.NRGBA {
	h, d := GoldenFrame()
	h.FrameCounter = counter
	d[0].BoneMask = mask // Encode zeroes cells of cleared bits
	return Encode(h, d)
}

func TestDecoderMagicHysteresis(t *testing.T) {
	bad := goldenImage()
	fillRect(bad, ColMagic0*MetaCellPx, 0, MetaCellPx, color.NRGBA{0, 0, 0, 255})

	dec := NewDecoder()
	if _, _, err := dec.Decode(goldenAt(1)); err != nil {
		t.Fatalf("good frame: %v", err)
	}
	if !dec.Live() {
		t.Fatal("not live after lock")
	}
	// Two consecutive misses ride out on a locked stream.
	for i := 1; i <= 2; i++ {
		_, _, err := dec.Decode(bad)
		if !errors.Is(err, ErrNoMagic) {
			t.Fatalf("miss %d: err=%v want ErrNoMagic", i, err)
		}
		if !dec.Live() {
			t.Fatalf("dropped after %d misses (rides out 2)", i)
		}
	}
	// Third consecutive miss drops the stream.
	if _, _, err := dec.Decode(bad); !errors.Is(err, ErrNoMagic) {
		t.Fatalf("miss 3: %v", err)
	}
	if dec.Live() {
		t.Fatal("still live after 3 consecutive misses")
	}
	// Next good frame locks a fresh stream.
	if _, _, err := dec.Decode(goldenAt(2)); err != nil {
		t.Fatalf("relock: %v", err)
	}
	if !dec.Live() {
		t.Fatal("not live after relock")
	}

	// Unlocked decoder never rides out: misses without a lock stay dead.
	dec2 := NewDecoder()
	if _, _, err := dec2.Decode(bad); !errors.Is(err, ErrNoMagic) {
		t.Fatalf("unlocked miss: %v", err)
	}
	if dec2.Live() {
		t.Fatal("live without ever locking")
	}
}

func TestDecoderStaleness(t *testing.T) {
	frame := goldenAt(42)
	dec := NewDecoder()
	if _, _, err := dec.Decode(frame); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 14 repeats of the same frameCounter stay inside the window...
	for i := 1; i <= DefaultStalenessWindow-1; i++ {
		if _, _, err := dec.Decode(frame); err != nil {
			t.Fatalf("repeat %d: %v", i, err)
		}
		if !dec.Live() {
			t.Fatalf("stale after only %d non-advancing frames", i)
		}
	}
	// ...the 15th crosses it.
	if _, _, err := dec.Decode(frame); err != nil {
		t.Fatalf("repeat %d: %v", DefaultStalenessWindow, err)
	}
	if dec.Live() {
		t.Fatalf("live after %d non-advancing frames", DefaultStalenessWindow)
	}
	// A counter advance revives liveness.
	if _, _, err := dec.Decode(goldenAt(43)); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !dec.Live() {
		t.Fatal("not live after counter advance")
	}
}

func TestDecoderMaskHysteresis(t *testing.T) {
	const maskA = 0x003FFFFF
	const maskB = 0x003FFF7F // bit 7 (leftUpperArm) dropped; core bits intact

	frameA1, frameA2 := goldenMask(maskA, 1), goldenMask(maskA, 2)
	frameB1, frameB2 := goldenMask(maskB, 3), goldenMask(maskB, 4)

	dec := NewDecoder()
	_, d, err := dec.Decode(frameA1)
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	if d[0].BoneMask != maskA || !d[0].Present[7] {
		t.Fatalf("first sight not accepted as-is: mask=%#x present7=%v", d[0].BoneMask, d[0].Present[7])
	}

	// One frame of the new mask: not yet accepted; the dropped bone reads absent (zero cells).
	_, d, err = dec.Decode(frameB1)
	if err != nil {
		t.Fatalf("B1: %v", err)
	}
	if d[0].BoneMask != maskA {
		t.Fatalf("mask changed after 1 frame: %#x", d[0].BoneMask)
	}
	if d[0].Present[7] {
		t.Fatal("bit7 present though its wire cells are zero")
	}

	// Second identical consecutive frame: change applies.
	_, d, err = dec.Decode(frameB2)
	if err != nil {
		t.Fatalf("B2: %v", err)
	}
	if d[0].BoneMask != maskB {
		t.Fatalf("mask not applied after 2 identical frames: %#x", d[0].BoneMask)
	}

	// One frame back to A: wire bit7 is valid again but not yet accepted -> forced absent.
	_, d, err = dec.Decode(frameA1)
	if err != nil {
		t.Fatalf("A back: %v", err)
	}
	if d[0].BoneMask != maskB {
		t.Fatalf("mask flipped back after 1 frame: %#x", d[0].BoneMask)
	}
	if d[0].Present[7] || d[0].Quats[7] != ([4]float64{}) {
		t.Fatal("unaccepted wire bit leaked a present bone")
	}
	_, d, _ = dec.Decode(frameA2)
	if d[0].BoneMask != maskA || !d[0].Present[7] {
		t.Fatalf("mask not restored after 2 A frames: %#x present7=%v", d[0].BoneMask, d[0].Present[7])
	}

	// Flapping A/B/A/B never applies B (identical CONSECUTIVE frames required).
	flap := NewDecoder()
	seq := []*image.NRGBA{frameA1, frameB1, frameA2, frameB2}
	for i, f := range seq {
		_, d, err = flap.Decode(f)
		if err != nil {
			t.Fatalf("flap %d: %v", i, err)
		}
		if d[0].BoneMask != maskA {
			t.Fatalf("flap frame %d applied mask %#x", i, d[0].BoneMask)
		}
	}
}
