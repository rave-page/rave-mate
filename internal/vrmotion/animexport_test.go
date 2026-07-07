package vrmotion

import (
	"os"
	"strings"
	"testing"
)

func quatIdentity() [4]float32 { return [4]float32{0, 0, 0, 1} }

func sampleRec() *Recording {
	return &Recording{
		Name: "take1",
		Hz:   30,
		Frames: []Frame{
			{T: 0, Poses: map[int]Pose{0: {Pos: [3]float32{0, 1, 0}, Rot: quatIdentity()}, 1: {Pos: [3]float32{1, 0, 0}, Rot: quatIdentity()}}},
			{T: 0.5, Poses: map[int]Pose{0: {Pos: [3]float32{0, 2, 0}, Rot: quatIdentity()}, 1: {Pos: [3]float32{2, 0, 0}, Rot: quatIdentity()}}},
		},
		Duration: 0.5,
	}
}

func TestBuildAnimStructure(t *testing.T) {
	y := BuildAnim(sampleRec(), nil)
	for _, want := range []string{
		"%YAML 1.1",
		"--- !u!74 &7400000",
		"AnimationClip:",
		"m_Name: take1",
		"m_SampleRate: 30",
		"m_RotationOrder: 4", // ZXY
		"m_StopTime: 0.5",
		"path: head",
		"path: tracker1",
		"m_EulerCurves:",
		"m_PositionCurves:",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("anim missing %q", want)
		}
	}
	// CompressedRotationCurves must appear exactly once (regression: was duplicated).
	if n := strings.Count(y, "m_CompressedRotationCurves: []"); n != 1 {
		t.Errorf("m_CompressedRotationCurves count = %d, want 1", n)
	}
}

func TestBuildAnimLinearTangents(t *testing.T) {
	// tracker1 X goes 1→2 over 0.5s → slope 2. Both keys share that linear slope.
	y := BuildAnim(sampleRec(), nil)
	if strings.Count(y, "inSlope: {x: 2,") < 1 || strings.Count(y, "outSlope: {x: 2,") < 1 {
		t.Errorf("expected linear slope 2 on tracker1 X curve; got:\n%s", y)
	}
}

func TestEulerUnwrap(t *testing.T) {
	if got := unwrap(170, -170); got <= 180 || got != 190 {
		t.Errorf("unwrap(170,-170)=%v, want 190", got)
	}
	if got := unwrap(-170, 170); got != -190 {
		t.Errorf("unwrap(-170,170)=%v, want -190", got)
	}
	if got := unwrap(10, 20); got != 20 {
		t.Errorf("unwrap(10,20)=%v, want 20 (no shift)", got)
	}
}

func TestExportAnimRoundTripFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/take1.anim"
	if err := ExportAnim(path, sampleRec(), nil); err != nil {
		t.Fatalf("ExportAnim: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	data := string(raw)
	if !strings.HasPrefix(data, "%YAML 1.1") {
		t.Errorf("file does not start with YAML header")
	}
}

func TestBuildAnimEmpty(t *testing.T) {
	// Nil/empty recording must still produce a valid, parseable shell.
	y := BuildAnim(nil, nil)
	if !strings.Contains(y, "AnimationClip:") || !strings.Contains(y, "m_Name: motion") {
		t.Errorf("empty anim malformed:\n%s", y)
	}
}

func TestSlopeAtSingleKey(t *testing.T) {
	ks := []key{{t: 0, v: vec3{1, 1, 1}}}
	if got := slopeAt(ks, 0, true); got != (vec3{}) {
		t.Errorf("single-key inSlope = %+v, want zero", got)
	}
	if got := slopeAt(ks, 0, false); got != (vec3{}) {
		t.Errorf("single-key outSlope = %+v, want zero", got)
	}
}
