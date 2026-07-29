package testcard

import (
	"image"
	"math/rand"
	"testing"
	"time"
)

func renderFrame(w, h int, p Payload) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	Render(img, p, time.Unix(1753000000, 0))
	return img
}

// The contract the whole harness rests on: what Render encodes, Decode returns bit-exactly.
func TestRoundTrip(t *testing.T) {
	p := Payload{Session: 0xABC, Seq: 1234567, T0ms: 0xDEADBEEF, FPS: 30, Flags: FlagBehind}
	for _, size := range [][2]int{{1280, 720}, {1920, 1080}, {3840, 2160}, {640, 360}} {
		img := renderFrame(size[0], size[1], p)
		got, derr := Decode(img)
		if derr != DecodeOK {
			t.Fatalf("%dx%d: decode failed: %v", size[0], size[1], derr)
		}
		if got != p {
			t.Fatalf("%dx%d: got %+v want %+v", size[0], size[1], got, p)
		}
	}
}

// scaleNearest simulates OBS/compositor resampling of the card to a different canvas.
func scaleNearest(src *image.NRGBA, w, h int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	for y := range h {
		sy := y * sh / h
		for x := range w {
			sx := x * sw / w
			so := sy*src.Stride + sx*4
			do := y*dst.Stride + x*4
			copy(dst.Pix[do:do+4], src.Pix[so:so+4])
		}
	}
	return dst
}

// The card must survive being scaled - the OBS leg stretches it to the canvas, and the route may
// carry a different resolution than the generator rendered.
func TestSurvivesScaling(t *testing.T) {
	p := Payload{Session: 0x123, Seq: 42, T0ms: 1000, FPS: 60}
	src := renderFrame(1920, 1080, p)
	for _, size := range [][2]int{{1280, 720}, {3840, 2160}, {960, 540}, {720, 480}} {
		got, derr := Decode(scaleNearest(src, size[0], size[1]))
		if derr != DecodeOK || got != p {
			t.Fatalf("scaled to %dx%d: derr=%v got=%+v", size[0], size[1], derr, got)
		}
	}
}

// blur3x3 approximates codec softening: every pixel becomes the mean of its neighborhood.
func blur3x3(src *image.NRGBA) *image.NRGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewNRGBA(src.Rect)
	for y := range h {
		for x := range w {
			var s [4]int
			n := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					px, py := x+dx, y+dy
					if px < 0 || px >= w || py < 0 || py >= h {
						continue
					}
					o := py*src.Stride + px*4
					for c := range 4 {
						s[c] += int(src.Pix[o+c])
					}
					n++
				}
			}
			o := y*dst.Stride + x*4
			for c := range 4 {
				dst.Pix[o+c] = byte(s[c] / n)
			}
		}
	}
	return dst
}

// squeezeRange simulates video-range YUV round trips: 0..255 compressed toward 16..235.
func squeezeRange(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Rect)
	for i := 0; i+3 < len(src.Pix); i += 4 {
		for c := range 3 {
			dst.Pix[i+c] = byte(16 + int(src.Pix[i+c])*219/255)
		}
		dst.Pix[i+3] = 255
	}
	return dst
}

// H.264 at streaming bitrates softens and range-squeezes; cell centers must still read.
func TestSurvivesBlurAndRangeSqueeze(t *testing.T) {
	p := Payload{Session: 0x777, Seq: 999999, T0ms: 5555, FPS: 30}
	img := squeezeRange(blur3x3(renderFrame(1280, 720, p)))
	got, derr := Decode(img)
	if derr != DecodeOK || got != p {
		t.Fatalf("blur+squeeze: derr=%v got=%+v want=%+v", derr, got, p)
	}
	// And scaled down after the damage, like a downstream preview.
	got, derr = Decode(scaleNearest(img, 854, 480))
	if derr != DecodeOK || got != p {
		t.Fatalf("blur+squeeze+scale: derr=%v got=%+v", derr, got)
	}
}

// A frame that is not a testcard must be rejected on the cheap path, not mis-decoded.
func TestNonCardFramesAreCheaplyRejected(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	img := image.NewNRGBA(image.Rect(0, 0, 640, 360))
	for i := range img.Pix {
		img.Pix[i] = byte(rng.Intn(256))
	}
	if _, derr := Decode(img); derr != ErrNoCard {
		t.Fatalf("random noise decoded as %v, want ErrNoCard", derr)
	}
	if _, derr := Decode(image.NewNRGBA(image.Rect(0, 0, 640, 360))); derr != ErrNoCard {
		t.Fatal("black frame not rejected")
	}
	if _, derr := Decode(nil); derr != ErrNoCard {
		t.Fatal("nil frame not rejected")
	}
}

// Damage INSIDE the grid must fail the CRC, never return a wrong payload as valid - a silently
// wrong seq would corrupt every verdict built on it.
func TestCorruptedGridFailsCRC(t *testing.T) {
	img := renderFrame(1280, 720, Payload{Session: 1, Seq: 7, T0ms: 9, FPS: 30})
	// Invert a horizontal band through the data grid rows.
	w, h := 1280, 720
	r := cellRect(w, h, gridX, gridY+2, gridX+gridCols, gridY+3)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			o := y*img.Stride + x*4
			img.Pix[o], img.Pix[o+1], img.Pix[o+2] = 255-img.Pix[o], 255-img.Pix[o+1], 255-img.Pix[o+2]
		}
	}
	if _, derr := Decode(img); derr != ErrCRC {
		t.Fatalf("corrupted grid decoded as %v, want ErrCRC", derr)
	}
}

func TestPackUnpackExhaustsFields(t *testing.T) {
	for _, p := range []Payload{
		{},
		{Session: 0xFFF, Seq: 0xFFFFFFFF, T0ms: 0xFFFFFFFF, FPS: 255, Flags: 255},
		{Session: 0x800, Seq: 1 << 31, T0ms: 1, FPS: 1, Flags: FlagBehind},
	} {
		got, derr := unpack(pack(p))
		if derr != DecodeOK || got != p {
			t.Fatalf("pack/unpack: %+v -> %+v (%v)", p, got, derr)
		}
	}
}

// DeltaMs must be wrap-correct: T0 lives in a uint32 that rolls over every ~49.7 days.
func TestDeltaMsWrap(t *testing.T) {
	now := time.UnixMilli(1753000000123)
	p := Payload{T0ms: uint32(now.UnixMilli()) - 250}
	if d := p.DeltaMs(now); d != 250 {
		t.Fatalf("delta = %d, want 250", d)
	}
	// T0 just before the uint32 boundary, "now" just after it.
	p = Payload{T0ms: 0xFFFFFF00}
	boundary := now.UnixMilli() &^ 0xFFFFFFFF // now's epoch with the low 32 bits zeroed
	if d := p.DeltaMs(time.UnixMilli(boundary + 0x100)); d != 0x200 {
		t.Fatalf("wrap delta = %d, want 512", d)
	}
}
