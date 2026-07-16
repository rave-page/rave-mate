package config

import "testing"

func TestMocapResolvedSource(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "desktop"}, {"desktop", "desktop"}, {"SPOUT", "spout"},
		{" dshow ", "dshow"}, {"webcam", "desktop"},
	}
	for _, c := range cases {
		if got := (MocapFeature{Source: c.in}).ResolvedSource(); got != c.want {
			t.Errorf("ResolvedSource(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMocapResolvedFPS(t *testing.T) {
	cases := []struct{ in, want int }{{0, 30}, {-5, 30}, {1, 1}, {45, 45}, {61, 60}, {1000, 60}}
	for _, c := range cases {
		if got := (MocapFeature{FPS: c.in}).ResolvedFPS(); got != c.want {
			t.Errorf("ResolvedFPS(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMocapResolvedBoneSlots(t *testing.T) {
	cases := []struct{ in, want int }{{0, 22}, {-1, 22}, {1, 1}, {22, 22}, {32, 32}, {33, 32}}
	for _, c := range cases {
		if got := (MocapFeature{BoneSlots: c.in}).ResolvedBoneSlots(); got != c.want {
			t.Errorf("ResolvedBoneSlots(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMocapResolvedStageMin(t *testing.T) {
	def := [3]float64{-8, 0, -6}
	if got := (MocapFeature{}).ResolvedStageMin(); got != def {
		t.Errorf("empty StageMin = %v, want %v", got, def)
	}
	if got := (MocapFeature{StageMin: []float64{1, 2}}).ResolvedStageMin(); got != def {
		t.Errorf("short StageMin = %v, want default %v", got, def)
	}
	want := [3]float64{-4, -1, -3}
	if got := (MocapFeature{StageMin: []float64{-4, -1, -3}}).ResolvedStageMin(); got != want {
		t.Errorf("StageMin = %v, want %v", got, want)
	}
}

func TestMocapResolvedStageSize(t *testing.T) {
	def := [3]float64{16, 4, 12}
	if got := (MocapFeature{}).ResolvedStageSize(); got != def {
		t.Errorf("empty StageSize = %v, want %v", got, def)
	}
	if got := (MocapFeature{StageSize: []float64{8}}).ResolvedStageSize(); got != def {
		t.Errorf("short StageSize = %v, want default %v", got, def)
	}
	// non-positive components fall back per axis
	want := [3]float64{10, 4, 12}
	if got := (MocapFeature{StageSize: []float64{10, 0, -2}}).ResolvedStageSize(); got != want {
		t.Errorf("StageSize with non-positive components = %v, want %v", got, want)
	}
	want = [3]float64{20, 6, 14}
	if got := (MocapFeature{StageSize: []float64{20, 6, 14}}).ResolvedStageSize(); got != want {
		t.Errorf("StageSize = %v, want %v", got, want)
	}
}
