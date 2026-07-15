package lightcue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// uni builds a [512]byte with the given slot→value pairs set.
func uni(kv map[int]byte) [512]byte {
	var a [512]byte
	for k, v := range kv {
		a[k] = v
	}
	return a
}

func TestObserveRespectsInterval(t *testing.T) {
	r := NewRecorder(10) // interval 0.1s
	s := map[uint16][512]byte{0: {}}
	r.Observe(0, s)    // keep (first)
	r.Observe(0.01, s) // drop (<0.1 since 0)
	r.Observe(0.1, s)  // keep
	r.Observe(0.2, s)  // keep
	rec := r.Recording("x")
	if rec == nil {
		t.Fatal("nil recording")
	}
	if len(rec.Frames) != 3 {
		t.Fatalf("want 3 frames, got %d", len(rec.Frames))
	}
}

func TestObserveCopiesSnapshot(t *testing.T) {
	r := NewRecorder(30)
	m := map[uint16][512]byte{0: uni(map[int]byte{5: 100})}
	r.Observe(0, m)
	// mutate caller map after Observe
	m[0] = uni(map[int]byte{5: 9})
	rec := r.Recording("x")
	if got := rec.Frames[0].Universes[0][5]; got != 100 {
		t.Fatalf("recorder retained caller map: slot5=%d", got)
	}
}

func TestRecordingNilWhenEmpty(t *testing.T) {
	if NewRecorder(30).Recording("x") != nil {
		t.Fatal("want nil for empty recorder")
	}
}

func TestReset(t *testing.T) {
	r := NewRecorder(30)
	r.Observe(0, map[uint16][512]byte{0: {}})
	r.Reset()
	if r.Recording("x") != nil {
		t.Fatal("want nil after reset")
	}
}

func TestUniverseSpanContiguous(t *testing.T) {
	r := NewRecorder(30)
	r.Observe(0, map[uint16][512]byte{2: {}, 4: {}}) // gap at 3
	rec := r.Recording("x")
	if rec.BaseUniverse != 2 || rec.UniverseCount != 3 {
		t.Fatalf("want base=2 count=3, got base=%d count=%d", rec.BaseUniverse, rec.UniverseCount)
	}
}

// full-span frames round-trip exactly (real captures feed one snapshot per configured universe).
func TestSaveLoadRoundTrip(t *testing.T) {
	rec := &Recording{
		Name: "rt", Hz: 30, Duration: 0.2, BaseUniverse: 0, UniverseCount: 1,
		Frames: []Frame{
			{T: 0, Universes: map[uint16][512]byte{0: uni(map[int]byte{0: 255, 12: 128})}},
			{T: 0.033, Universes: map[uint16][512]byte{0: uni(map[int]byte{0: 255, 12: 96})}},
			{T: 0.2, Universes: map[uint16][512]byte{0: uni(map[int]byte{0: 0, 12: 96})}},
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
	if got.Name != rec.Name || got.Hz != rec.Hz || got.Duration != rec.Duration ||
		got.BaseUniverse != rec.BaseUniverse || got.UniverseCount != rec.UniverseCount {
		t.Fatalf("meta mismatch: %+v", got)
	}
	if len(got.Frames) != len(rec.Frames) {
		t.Fatalf("want %d frames, got %d", len(rec.Frames), len(got.Frames))
	}
	for i := range rec.Frames {
		if got.Frames[i].T != rec.Frames[i].T {
			t.Fatalf("frame %d T mismatch: %v vs %v", i, got.Frames[i].T, rec.Frames[i].T)
		}
		if !reflect.DeepEqual(got.Frames[i].Universes, rec.Frames[i].Universes) {
			t.Fatalf("frame %d universe state mismatch", i)
		}
	}
}

// delta-encoding: frame 0 = full non-zero state; later frames = only changed channels;
// value 0 = channel off.
func TestDeltaEncoding(t *testing.T) {
	rec := &Recording{
		Name: "d", Hz: 30, Duration: 0.2, BaseUniverse: 0, UniverseCount: 1,
		Frames: []Frame{
			{T: 0, Universes: map[uint16][512]byte{0: uni(map[int]byte{0: 255, 12: 128})}},
			{T: 0.033, Universes: map[uint16][512]byte{0: uni(map[int]byte{0: 255, 12: 96})}}, // only 12 changed
			{T: 0.2, Universes: map[uint16][512]byte{0: uni(map[int]byte{0: 0, 12: 96})}},     // only 0 changed → 0
		},
	}
	data, err := Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var c contract
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.V != 1 {
		t.Fatalf("want v=1, got %d", c.V)
	}
	// frame 0: full initial non-zero state {0:255, 12:128}
	if !reflect.DeepEqual(c.Frames[0].D, map[string]int{"0": 255, "12": 128}) {
		t.Fatalf("frame0 delta wrong: %v", c.Frames[0].D)
	}
	// frame 1: only slot 12 changed
	if !reflect.DeepEqual(c.Frames[1].D, map[string]int{"12": 96}) {
		t.Fatalf("frame1 delta wrong: %v", c.Frames[1].D)
	}
	// frame 2: only slot 0 changed, to 0 (off)
	if !reflect.DeepEqual(c.Frames[2].D, map[string]int{"0": 0}) {
		t.Fatalf("frame2 delta wrong: %v", c.Frames[2].D)
	}
}

// flat index = (universe-base)*512 + slot for multi-universe takes.
func TestFlatIndexMultiUniverse(t *testing.T) {
	rec := &Recording{
		Name: "m", Hz: 30, Duration: 0, BaseUniverse: 1, UniverseCount: 2,
		Frames: []Frame{
			{T: 0, Universes: map[uint16][512]byte{
				1: uni(map[int]byte{3: 10}), // flat 3
				2: uni(map[int]byte{0: 20}), // flat 512
			}},
		},
	}
	data, _ := Marshal(rec)
	var c contract
	_ = json.Unmarshal(data, &c)
	if !reflect.DeepEqual(c.Frames[0].D, map[string]int{"3": 10, "512": 20}) {
		t.Fatalf("flat index wrong: %v", c.Frames[0].D)
	}
}

// step/hold: Sample returns the last frame with T<=t, no interpolation.
func TestSampleStepHold(t *testing.T) {
	rec := &Recording{
		Name: "s", Hz: 1, Duration: 2, BaseUniverse: 0, UniverseCount: 1,
		Frames: []Frame{
			{T: 0, Universes: map[uint16][512]byte{0: uni(map[int]byte{0: 10})}},
			{T: 1, Universes: map[uint16][512]byte{0: uni(map[int]byte{0: 200})}},
			{T: 2, Universes: map[uint16][512]byte{0: uni(map[int]byte{0: 50})}},
		},
	}
	pl := NewPlayer(rec)
	if v := pl.Sample(-1)[0][0]; v != 10 {
		t.Fatalf("before-first: want 10, got %d", v)
	}
	if v := pl.Sample(0.5)[0][0]; v != 10 { // hold frame0 (no interp toward 200)
		t.Fatalf("mid 0.5: want 10 (step/hold), got %d", v)
	}
	if v := pl.Sample(1.0)[0][0]; v != 200 {
		t.Fatalf("at 1.0: want 200, got %d", v)
	}
	if v := pl.Sample(1.99)[0][0]; v != 200 { // hold frame1
		t.Fatalf("mid 1.99: want 200, got %d", v)
	}
	if v := pl.Sample(99)[0][0]; v != 50 { // clamp to last
		t.Fatalf("after-last: want 50, got %d", v)
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

// Load clamps values and skips out-of-range / malformed flatIndex entries.
func TestLoadValidation(t *testing.T) {
	c := contract{
		V: 1, Name: "v", Hz: 30, Duration: 0, BaseUniverse: 0, UniverseCount: 1,
		Frames: []contractFrame{
			{T: 0, D: map[string]int{
				"5":      300, // clamp → 255
				"6":      -4,  // clamp → 0
				"7":      42,  // kept
				"512":    99,  // out of range (width 512, valid 0..511) → skip
				"-1":     99,  // negative → skip
				"notnum": 99,  // malformed key → skip
			}},
		},
	}
	data, _ := json.MarshalIndent(&c, "", "  ")
	path := filepath.Join(t.TempDir(), "v.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rec, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	u := rec.Frames[0].Universes[0]
	if u[5] != 255 {
		t.Fatalf("slot5 clamp: want 255, got %d", u[5])
	}
	if u[6] != 0 {
		t.Fatalf("slot6 clamp: want 0, got %d", u[6])
	}
	if u[7] != 42 {
		t.Fatalf("slot7: want 42, got %d", u[7])
	}
}
