package giokit

import "testing"

func TestWaveColumns(t *testing.T) {
	// Downsample: 8 buckets → 4 columns, max per pair.
	got := WaveColumns([]byte{1, 9, 2, 3, 8, 4, 0, 5}, 4)
	for i, want := range []byte{9, 3, 8, 5} {
		if got[i] != want {
			t.Errorf("col %d = %d, want %d", i, got[i], want)
		}
	}
	// Upsample: fewer buckets than columns - every column still gets a value.
	got = WaveColumns([]byte{10, 20}, 4)
	for i, want := range []byte{10, 10, 20, 20} {
		if got[i] != want {
			t.Errorf("upsample col %d = %d, want %d", i, got[i], want)
		}
	}
	if WaveColumns(nil, 4) != nil || WaveColumns([]byte{1}, 0) != nil {
		t.Error("degenerate inputs must return nil")
	}
	if n := len(WaveColumns(make([]byte, 8192), 1900)); n != 1900 {
		t.Errorf("cols = %d, want 1900", n)
	}
}

func TestClampFrac(t *testing.T) {
	for _, c := range []struct{ in, want float32 }{{-1, 0}, {0, 0}, {0.5, 0.5}, {1, 1}, {2, 1}} {
		if got := clampFrac(c.in); got != c.want {
			t.Errorf("clampFrac(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
