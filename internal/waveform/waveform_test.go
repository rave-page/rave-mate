package waveform

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestBucketPeaks(t *testing.T) {
	// 8 s16 samples: values 0, ±100, ±20000, ±32767, 0. Two buckets → max-abs of each half.
	vals := []int16{0, 100, -20000, 200, 32767, -32767, 50, 0}
	pcm := make([]byte, len(vals)*2)
	for i, v := range vals {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	got := bucketPeaks(pcm, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 buckets, got %d", len(got))
	}
	// first half max-abs = 20000 → 20000>>7 = 156; second half = 32767 → 255.
	if got[0] != byte(20000>>7) {
		t.Errorf("bucket0 = %d, want %d", got[0], 20000>>7)
	}
	if got[1] != byte(32767>>7) {
		t.Errorf("bucket1 = %d, want %d", got[1], 32767>>7)
	}
}

func TestBucketPeaksFewerSamplesThanBuckets(t *testing.T) {
	pcm := make([]byte, 4) // 2 samples
	a, b := int16(1000), int16(-2000)
	binary.LittleEndian.PutUint16(pcm[0:], uint16(a))
	binary.LittleEndian.PutUint16(pcm[2:], uint16(b))
	got := bucketPeaks(pcm, 10) // clamps to sample count
	if len(got) != 2 {
		t.Fatalf("want 2 buckets (clamped), got %d", len(got))
	}
}

func TestDiskRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, nil)
	key := "abc123"
	want := &Peaks{Data: []byte{1, 2, 3, 250, 0, 128}, DurationMs: 123456}
	if err := r.writeDisk(key, want); err != nil {
		t.Fatalf("writeDisk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, key+cacheExt)); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
	got, ok := r.loadDisk(key)
	if !ok {
		t.Fatal("loadDisk miss after write")
	}
	if got.DurationMs != want.DurationMs {
		t.Errorf("duration = %d, want %d", got.DurationMs, want.DurationMs)
	}
	if string(got.Data) != string(want.Data) {
		t.Errorf("data = %v, want %v", got.Data, want.Data)
	}
}

func TestLoadDiskMissing(t *testing.T) {
	r := New(t.TempDir(), nil)
	if _, ok := r.loadDisk("nope"); ok {
		t.Error("expected miss for absent key")
	}
}
