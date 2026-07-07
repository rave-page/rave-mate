package vrmotion

import (
	"math"
	"path/filepath"
	"testing"
)

func TestObserveRespectsInterval(t *testing.T) {
	r := NewRecorder(10) // interval 0.1s
	p := map[int]Pose{0: {}}
	r.Observe(0, p)    // keep (first)
	r.Observe(0.01, p) // drop (<0.1 since 0)
	r.Observe(0.1, p)  // keep
	r.Observe(0.2, p)  // keep
	rec := r.Recording("x")
	if rec == nil {
		t.Fatal("nil recording")
	}
	if len(rec.Frames) != 3 {
		t.Fatalf("want 3 frames, got %d", len(rec.Frames))
	}
}

func TestObserveCopiesMap(t *testing.T) {
	r := NewRecorder(30)
	m := map[int]Pose{0: {Pos: [3]float32{1, 2, 3}}}
	r.Observe(0, m)
	m[0] = Pose{Pos: [3]float32{9, 9, 9}} // mutate caller map
	rec := r.Recording("x")
	if got := rec.Frames[0].Poses[0].Pos; got != [3]float32{1, 2, 3} {
		t.Fatalf("recorder retained caller map: %v", got)
	}
}

func TestRecordingNilWhenEmpty(t *testing.T) {
	if NewRecorder(30).Recording("x") != nil {
		t.Fatal("want nil for empty recorder")
	}
}

func TestReset(t *testing.T) {
	r := NewRecorder(30)
	r.Observe(0, map[int]Pose{0: {}})
	r.Reset()
	if r.Recording("x") != nil {
		t.Fatal("want nil after reset")
	}
}

func TestSampleInterpolatesPosition(t *testing.T) {
	rec := &Recording{
		Name: "x", Hz: 1, Duration: 1,
		Frames: []Frame{
			{T: 0, Poses: map[int]Pose{0: {Pos: [3]float32{0, 0, 0}, Rot: [4]float32{0, 0, 0, 1}}}},
			{T: 1, Poses: map[int]Pose{0: {Pos: [3]float32{0, 0, 10}, Rot: [4]float32{0, 0, 0, 1}}}},
		},
	}
	got := NewPlayer(rec).Sample(0.5)[0].Pos
	if math.Abs(float64(got[2])-5) > 1e-4 {
		t.Fatalf("want z≈5, got %v", got)
	}
}

func TestSampleClamps(t *testing.T) {
	rec := &Recording{
		Frames: []Frame{
			{T: 0, Poses: map[int]Pose{0: {Pos: [3]float32{1, 0, 0}}}},
			{T: 1, Poses: map[int]Pose{0: {Pos: [3]float32{2, 0, 0}}}},
		},
		Duration: 1,
	}
	pl := NewPlayer(rec)
	if pl.Sample(-5)[0].Pos[0] != 1 {
		t.Fatal("before-first clamp failed")
	}
	if pl.Sample(99)[0].Pos[0] != 2 {
		t.Fatal("after-last clamp failed")
	}
}

func TestSampleNilEmpty(t *testing.T) {
	if NewPlayer(nil).Sample(0) != nil {
		t.Fatal("want nil for nil recording")
	}
	if NewPlayer(&Recording{}).Sample(0) != nil {
		t.Fatal("want nil for empty recording")
	}
}

func TestNlerpNormalizedMidpoint(t *testing.T) {
	// 90° apart about Z: identity → (0,0,sin45,cos45)
	a := [4]float32{0, 0, 0, 1}
	s := float32(math.Sqrt2 / 2)
	b := [4]float32{0, 0, s, s}
	q := nlerp(a, b, 0.5)
	n := math.Sqrt(float64(q[0]*q[0] + q[1]*q[1] + q[2]*q[2] + q[3]*q[3]))
	if math.Abs(n-1) > 1e-5 {
		t.Fatalf("want unit quaternion, |q|=%v", n)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	rec := &Recording{
		Name: "rt", Hz: 30, Duration: 2,
		Frames: []Frame{
			{T: 0, Poses: map[int]Pose{0: {Pos: [3]float32{1, 2, 3}, Rot: [4]float32{0, 0, 0, 1}}}},
			{T: 2, Poses: map[int]Pose{1: {Pos: [3]float32{4, 5, 6}, Rot: [4]float32{0, 1, 0, 0}}}},
		},
	}
	path := filepath.Join(t.TempDir(), "rec.json")
	if err := Save(path, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Name != rec.Name || got.Hz != rec.Hz || got.Duration != rec.Duration {
		t.Fatalf("meta mismatch: %+v", got)
	}
	if len(got.Frames) != 2 {
		t.Fatalf("want 2 frames, got %d", len(got.Frames))
	}
	if got.Frames[0].Poses[0].Pos != [3]float32{1, 2, 3} {
		t.Fatalf("frame0 pos mismatch: %v", got.Frames[0].Poses[0].Pos)
	}
	if got.Frames[1].Poses[1].Rot != [4]float32{0, 1, 0, 0} {
		t.Fatalf("frame1 rot mismatch: %v", got.Frames[1].Poses[1].Rot)
	}
}
