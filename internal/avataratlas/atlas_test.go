package avataratlas

import (
	"bytes"
	"image"
	"reflect"
	"strings"
	"testing"
)

// handAtlas builds a structurally rich atlas: negative mins, degenerate axes (size 0),
// q extremes, points across the slot table including a reserved slot (codec is structural;
// §5 semantics live above it).
func handAtlas() *Atlas {
	a := &Atlas{Version: Version, Flags: 0, SlotIndex: 7, BoneCount: 4}
	a.Boxes[0] = Box{Min: [3]int16{-251, -1, -1}, Size: [3]uint16{502, 302, 2}}
	a.Boxes[3] = Box{Min: [3]int16{-32768, 0, 100}, Size: [3]uint16{65535, 0, 1}} // degenerate Y
	a.Boxes[21] = Box{Min: [3]int16{32767, -100, 0}, Size: [3]uint16{0, 0, 0}}    // fully degenerate
	a.Boxes[31] = Box{Min: [3]int16{-1, -1, -1}, Size: [3]uint16{2, 2, 2}}
	a.Points = []AtlasPoint{
		{Q: [3]uint16{0, 32768, 65535}, Slot: 0, Weight: 255, RGB: [3]uint8{0, 128, 255}},
		{Q: [3]uint16{65535, 0, 0}, Slot: 3, Weight: 255, RGB: [3]uint8{1, 2, 3}},
		{Q: [3]uint16{0, 0, 0}, Slot: 21, Weight: 255, RGB: [3]uint8{255, 255, 255}},
		{Q: [3]uint16{1, 2, 3}, Slot: 31, Weight: 255, RGB: [3]uint8{42, 42, 42}},
	}
	return a
}

// TestAtlasRoundTrip: encode -> PNG -> decode is field-exact, degenerate boxes included.
func TestAtlasRoundTrip(t *testing.T) {
	a := handAtlas()
	var buf bytes.Buffer
	if err := a.EncodePNG(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodePNG(&buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(a, got) {
		t.Fatalf("round trip mismatch:\n enc %+v\n dec %+v", a, got)
	}
}

// TestAtlasDegenerateDecode: size==0 axis decodes the quantized coord as min (§11).
func TestAtlasDegenerateDecode(t *testing.T) {
	a := handAtlas()
	pos := a.PointPos(a.Points[1]) // slot 3, Y degenerate, qy=0
	if pos[1] != 0 {
		t.Errorf("degenerate Y: got %v, want box min 0", pos[1])
	}
	pos = a.PointPos(a.Points[2]) // slot 21, fully degenerate
	want := [3]float64{float64(32767) / 1000, float64(-100) / 1000, 0}
	for ax := 0; ax < 3; ax++ {
		if pos[ax] != want[ax] {
			t.Errorf("fully degenerate axis %d: got %v, want %v", ax, pos[ax], want[ax])
		}
	}
	// Non-degenerate extremes hit exact box edges.
	p0 := a.PointPos(a.Points[0]) // slot 0: q = 0, 32768, 65535
	if p0[0] != float64(-251)/1000 {
		t.Errorf("q=0 -> min: got %v, want -0.251", p0[0])
	}
	if p0[2] != float64(-1)/1000+float64(2)/1000 {
		t.Errorf("q=65535 -> min+size: got %v, want 0.001", p0[2])
	}
}

// TestAtlasMaxSize: the max-height atlas (2048 rows) encodes + decodes; one point more rejects.
func TestAtlasMaxSize(t *testing.T) {
	a := &Atlas{Version: Version, SlotIndex: 0, BoneCount: 1}
	a.Boxes[0] = Box{Min: [3]int16{-1, -1, -1}, Size: [3]uint16{2, 2, 2}}
	a.Points = make([]AtlasPoint, MaxPoints)
	for i := range a.Points {
		a.Points[i] = AtlasPoint{Q: [3]uint16{uint16(i), uint16(i >> 3), uint16(i >> 7)}, Slot: 0, Weight: 255, RGB: [3]uint8{uint8(i), uint8(i >> 8), uint8(i >> 16)}}
	}
	img, err := a.Image()
	if err != nil {
		t.Fatalf("max-size encode: %v", err)
	}
	if h := img.Rect.Dy(); h != MaxHeight {
		t.Fatalf("max-size height %d, want %d", h, MaxHeight)
	}
	got, err := DecodeImage(img)
	if err != nil {
		t.Fatalf("max-size decode: %v", err)
	}
	if !reflect.DeepEqual(a, got) {
		t.Fatal("max-size round trip mismatch")
	}

	a.Points = append(a.Points, AtlasPoint{Slot: 0, Weight: 255})
	if _, err := a.Image(); err == nil {
		t.Fatal("MaxPoints+1 encoded, want error")
	}
}

// TestVerifyRejectList exercises every §11 reject: MAGIC/version mismatch, dim mismatch,
// self-test mismatch, slotIndex out of range, pointCount over capacity.
func TestVerifyRejectList(t *testing.T) {
	freshImg := func() *image.NRGBA {
		img, err := handAtlas().Image()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return img
	}
	if err := Verify(freshImg()); err != nil {
		t.Fatalf("pristine atlas rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*image.NRGBA)
		want string
	}{
		{"magic", func(m *image.NRGBA) { m.Pix[0] = 'X' }, "MAGIC"},
		{"version", func(m *image.NRGBA) { m.Pix[4] = 9 }, "version"},
		{"dims", func(m *image.NRGBA) { m.Pix[3*4+3] = 99 }, "px3 dims"}, // height field byte
		{"selftest", func(m *image.NRGBA) { m.Pix[m.PixOffset(10, 1)+2] ^= 0xFF }, "self-test"},
		{"slot", func(m *image.NRGBA) { m.Pix[1*4+2] = 16 }, "slotIndex"},
		{"capacity", func(m *image.NRGBA) { m.Pix[2*4] = 0xFF }, "capacity"}, // pointCount hi byte
	}
	for _, c := range cases {
		img := freshImg()
		c.mut(img)
		err := Verify(img)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want mention of %q", c.name, err, c.want)
		}
	}

	// Physically resized canvas (px3 untouched) must reject too.
	img := freshImg()
	bigger := image.NewNRGBA(image.Rect(0, 0, Width, img.Rect.Dy()+1))
	copy(bigger.Pix, img.Pix)
	for i := 3; i < len(bigger.Pix); i += 4 {
		if bigger.Pix[i] == 0 && i > len(img.Pix) {
			bigger.Pix[i] = 255
		}
	}
	if err := Verify(bigger); err == nil {
		t.Error("resized canvas accepted, want px3 dim mismatch")
	}

	// Non-2048 width rejects outright.
	narrow := image.NewNRGBA(image.Rect(0, 0, 1024, 4))
	if err := Verify(narrow); err == nil {
		t.Error("1024-wide canvas accepted")
	}
}

// TestBuildAtlasBoxes: tight AABB +1mm pad (floor/ceil), quantization against the mm wire box,
// decode returns positions within half a quantization step.
func TestBuildAtlasBoxes(t *testing.T) {
	samples := []SampledPoint{
		{Slot: 5, Local: [3]float64{-0.25, 0.0, 0.0004}, RGB: [3]uint8{10, 20, 30}},
		{Slot: 5, Local: [3]float64{0.25, 0.2999, 0.0004}, RGB: [3]uint8{40, 50, 60}},
	}
	a, err := BuildAtlas(samples, 2)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if a.BoneCount != 1 || a.SlotIndex != 2 {
		t.Fatalf("boneCount %d slotIndex %d, want 1/2", a.BoneCount, a.SlotIndex)
	}
	box := a.Boxes[5]
	wantMin := [3]int16{-251, -1, -1}
	wantSize := [3]uint16{502, 302, 3} // y: ceil(299.9)+1=301 -> 302; z: floor(0.4)-1=-1, ceil(0.4)+1=2 -> 3
	if box.Min != wantMin || box.Size != wantSize {
		t.Fatalf("box = %+v/%+v, want %v/%v", box.Min, box.Size, wantMin, wantSize)
	}
	for i, s := range samples {
		got := a.PointPos(a.Points[i])
		for ax := 0; ax < 3; ax++ {
			step := float64(box.Size[ax]) / 1000 / 65535
			if d := got[ax] - s.Local[ax]; d > step/2+1e-12 || d < -step/2-1e-12 {
				t.Errorf("point %d axis %d: decoded %v vs sampled %v (step %v)", i, ax, got[ax], s.Local[ax], step)
			}
		}
	}
}
