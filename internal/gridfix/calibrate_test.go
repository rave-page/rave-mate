package gridfix

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// golden vectors generated from the Python engine (fix_grids.py calibrate()) over
// real cached beat detections; anonymous float arrays only.
type calGolden struct {
	Tracks []struct {
		Beats     []float64 `json:"beats"`
		Downbeats []float64 `json:"downbeats"`
	} `json:"tracks"`
	Offsets []struct {
		Track     int      `json:"track"`
		Scenario  string   `json:"scenario"`
		OldBPM    float64  `json:"old_bpm"`
		OldStartS float64  `json:"old_start_s"`
		OK        bool     `json:"ok"`
		OffsetS   *float64 `json:"offset_s"`
	} `json:"offsets"`
	Sampling []struct {
		Counts map[string]int   `json:"counts"`
		N      int              `json:"n"`
		PerExt int              `json:"per_ext"`
		Picks  map[string][]int `json:"picks"`
	} `json:"sampling"`
	Summary []struct {
		Offsets map[string][]float64 `json:"offsets"`
		Bias    map[string]float64   `json:"bias"`
		Stats   map[string]struct {
			N       int     `json:"n"`
			MedianS float64 `json:"median_s"`
			MADS    float64 `json:"mad_s"`
		} `json:"stats"`
	} `json:"summary"`
}

func loadCalGolden(t *testing.T) calGolden {
	t.Helper()
	raw, err := os.ReadFile("testdata/calibrate_golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g calGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(g.Tracks) == 0 || len(g.Offsets) == 0 {
		t.Fatal("empty golden")
	}
	return g
}

func TestCalibrationOffsetGolden(t *testing.T) {
	g := loadCalGolden(t)
	const tol = 1e-6
	for i, r := range g.Offsets {
		tr := g.Tracks[r.Track]
		off, ok := CalibrationOffset(tr.Beats, tr.Downbeats, r.OldBPM, r.OldStartS)
		if ok != r.OK {
			t.Errorf("case %d (%s): ok got %v want %v", i, r.Scenario, ok, r.OK)
			continue
		}
		if r.OK && math.Abs(off-*r.OffsetS) > tol {
			t.Errorf("case %d (%s): offset got %.9f want %.9f", i, r.Scenario, off, *r.OffsetS)
		}
	}
}

func TestCalibrationSamplingGolden(t *testing.T) {
	g := loadCalGolden(t)
	for i, s := range g.Sampling {
		q := CalibrationQuota(len(s.Counts), s.N)
		if q != s.PerExt {
			t.Errorf("case %d: quota got %d want %d", i, q, s.PerExt)
		}
		for ext, want := range s.Picks {
			got := StrideIndices(s.Counts[ext], q)
			if len(got) != len(want) {
				t.Errorf("case %d %s: %d picks want %d", i, ext, len(got), len(want))
				continue
			}
			for j := range got {
				if got[j] != want[j] {
					t.Errorf("case %d %s[%d]: got %d want %d", i, ext, j, got[j], want[j])
				}
			}
		}
	}
}

func TestSummarizeCalibrationGolden(t *testing.T) {
	g := loadCalGolden(t)
	const tol = 1e-12
	for i, s := range g.Summary {
		bias, stats := SummarizeCalibration(s.Offsets)
		if len(bias) != len(s.Bias) {
			t.Errorf("case %d: bias size got %d want %d", i, len(bias), len(s.Bias))
		}
		for ext, want := range s.Bias {
			if got, ok := bias[ext]; !ok || math.Abs(got-want) > tol {
				t.Errorf("case %d bias[%s]: got %.12f want %.12f", i, ext, got, want)
			}
		}
		for ext, w := range s.Stats {
			st, ok := stats[ext]
			if !ok || st.N != w.N || math.Abs(st.MedianS-w.MedianS) > tol || math.Abs(st.MADS-w.MADS) > tol {
				t.Errorf("case %d stats[%s]: got %+v want %+v", i, ext, st, w)
			}
		}
	}
}

func TestCalibrationForPath(t *testing.T) {
	c := Calibration{".mp3": 0.04, "*": -0.01}
	for _, tc := range []struct {
		path string
		want float64
	}{
		{`C:\music\a.MP3`, 0.04}, {`/m/b.flac`, -0.01}, {`noext`, -0.01},
	} {
		if got := c.ForPath(tc.path); got != tc.want {
			t.Errorf("ForPath(%s) = %v want %v", tc.path, got, tc.want)
		}
	}
	if got := (Calibration{}).ForPath("a.mp3"); got != 0 {
		t.Errorf("empty calibration = %v want 0", got)
	}
}
