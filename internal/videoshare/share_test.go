package videoshare

import "testing"

// TestSenderShareRefusesGarbage: SenderShare must never pass torn/garbage shim geometry or a
// zero handle through - both are the "keep the readback path" verdict, not a route with a
// 2 TB frame in it (the 4K OOM class).
func TestSenderShareRefusesGarbage(t *testing.T) {
	orig := shareFn
	defer func() { shareFn = orig }()
	cases := []struct {
		name        string
		h           uint64
		fmt         uint32
		w, hh       int
		got, wantOK bool
	}{
		{"no backend", 0, 0, 0, 0, false, false},
		{"zero handle", 0, 87, 1920, 1080, true, false},
		{"torn width", 0x1234, 87, 139846784, 3840, true, false},
		{"zero dims", 0x1234, 87, 0, 0, true, false},
		{"negative dims", 0x1234, 87, -1920, 1080, true, false},
		{"good 4K", 0x1234, 87, 3840, 2160, true, true},
	}
	for _, c := range cases {
		shareFn = func(string) (uint64, uint32, int, int, bool) { return c.h, c.fmt, c.w, c.hh, c.got }
		h, f, w, hh, ok := SenderShare("x")
		if ok != c.wantOK {
			t.Fatalf("%s: ok=%v want %v", c.name, ok, c.wantOK)
		}
		if !ok && (h != 0 || f != 0 || w != 0 || hh != 0) {
			t.Fatalf("%s: refusal must zero every out value, got %v %v %v %v", c.name, h, f, w, hh)
		}
		if ok && (h != c.h || f != c.fmt || w != c.w || hh != c.hh) {
			t.Fatalf("%s: passthrough mismatch", c.name)
		}
	}
}
