//go:build manual

package gridfix

import (
	"context"
	"os"
	"testing"
	"time"
)

// Live engine round-trip against a real Python env with beat-this installed:
//
//	GRIDFIX_TEST_PYTHON=<venv python> GRIDFIX_TEST_AUDIO=<track> \
//	  go test -tags manual -run TestEngineLive ./internal/gridfix/
func TestEngineLive(t *testing.T) {
	py := os.Getenv("GRIDFIX_TEST_PYTHON")
	audio := os.Getenv("GRIDFIX_TEST_AUDIO")
	if py == "" || audio == "" {
		t.Skip("set GRIDFIX_TEST_PYTHON + GRIDFIX_TEST_AUDIO")
	}
	eng := &Engine{Python: py, DataDir: t.TempDir(), Device: "cpu",
		OnLog: func(s string) { t.Log("engine:", s) }}
	defer eng.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	v, _, err := eng.Ping(ctx, false)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Logf("versions: %+v", v)
	det, err := eng.Analyze(ctx, audio)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if len(det.Beats) < 16 || len(det.Downbeats) < 4 {
		t.Fatalf("suspicious detection: %d beats, %d downbeats", len(det.Beats), len(det.Downbeats))
	}
	t.Logf("device=%s beats=%d downbeats=%d", det.Device, len(det.Beats), len(det.Downbeats))
	fit := FitConstantGrid(det.Beats, det.Downbeats, 0)
	if fit == nil {
		t.Fatal("no grid fit on real detection")
	}
	t.Logf("fitted bpm=%.3f coverage=%.2f explained=%.2f", fit.BPM(), fit.Coverage, fit.Explained)
}
