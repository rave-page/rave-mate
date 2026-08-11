package caprecover

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
)

func openLib(t *testing.T) *libdb.DB {
	t.Helper()
	d, err := libdb.Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("libdb.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// oldFile writes b to dir/name with an mtime safely past settleAge.
func oldFile(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return p
}

func TestSweepBackfillsCrashOpenRow(t *testing.T) {
	lib := openLib(t)
	dir := t.TempDir()
	p := oldFile(t, dir, "cap.flac", make([]byte, minBytes+1))
	started := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	if err := lib.SaveSetRecording(libdb.SetRecording{
		ID: "ice-1", Path: p, Kind: libdb.SetKindIcecast, StartedAt: started,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	n := Sweep(context.Background(), logbus.New(64), lib, nil)
	if n < 1 {
		t.Fatalf("Sweep changed %d rows, want >= 1 (backfill)", n)
	}
	rows, err := lib.ListSetRecordings(10)
	if err != nil || len(rows) == 0 {
		t.Fatalf("list: %v (%d rows)", err, len(rows))
	}
	var got libdb.SetRecording
	for _, r := range rows {
		if r.ID == "ice-1" {
			got = r
		}
	}
	if got.ID == "" || got.EndedAt.IsZero() {
		t.Fatalf("crash-open row not backfilled: %+v", got)
	}
	if got.Bytes != minBytes+1 {
		t.Fatalf("bytes = %d, want %d", got.Bytes, minBytes+1)
	}
}

func TestSweepSkipsFreshAndTrackedAndStubs(t *testing.T) {
	lib := openLib(t)
	dir := t.TempDir()

	// Tracked file: row exists (case-different path must still count as tracked on Windows).
	tracked := oldFile(t, dir, "2026-08-01 20-00-00.mp4", make([]byte, minBytes+1))
	if err := lib.SaveSetRecording(libdb.SetRecording{
		ID: "obs-1", Path: tracked, Kind: libdb.SetKindOBS,
		StartedAt: time.Now().Add(-3 * time.Hour), EndedAt: time.Now().Add(-2 * time.Hour), Bytes: 1,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Fresh file (mtime now): may still be written.
	if err := os.WriteFile(filepath.Join(dir, "2026-08-02 20-00-00.mp4"), make([]byte, minBytes+1), 0o644); err != nil {
		t.Fatalf("fresh: %v", err)
	}
	// Stub (< minBytes).
	oldFile(t, dir, "2026-08-03 20-00-00.flac", []byte("tiny"))
	// Wrong extension / user-made cut / free-named export: not capture outputs.
	oldFile(t, dir, "notes.txt", make([]byte, minBytes+1))
	oldFile(t, dir, "2026-08-04 20-00-00-cut.mp4", make([]byte, minBytes+1))
	oldFile(t, dir, "My Set Export.flac", make([]byte, minBytes+1))

	_ = Sweep(context.Background(), logbus.New(64), lib, []string{dir})
	rows, _ := lib.ListSetRecordings(20)
	for _, r := range rows {
		if r.ID != "obs-1" {
			t.Fatalf("unexpected recovered row: %+v", r)
		}
	}
}

func TestSweepRecoversUntrackedFile(t *testing.T) {
	if _, ok := mediatools.Resolve("ffprobe"); !ok {
		t.Skip("ffprobe unavailable - probe path cannot run")
	}
	if _, ok := mediatools.Resolve("ffmpeg"); !ok {
		t.Skip("ffmpeg unavailable")
	}
	lib := openLib(t)
	dir := t.TempDir()

	// Real 2s FLAC via ffmpeg so the probe returns a genuine duration.
	p := filepath.Join(dir, "2026-08-05 21-30-00.flac")
	ff, _ := mediatools.Resolve("ffmpeg")
	if out, err := execCommand(ff, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:a", "flac", "-y", p); err != nil {
		t.Fatalf("gen flac: %v (%s)", err, out)
	}
	// Pad to clear the stub filter, then age it.
	fi, _ := os.Stat(p)
	if fi.Size() < minBytes {
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		// FLAC ignores trailing garbage for duration (STREAMINFO already finalized).
		_, _ = f.Write(make([]byte, minBytes))
		_ = f.Close()
	}
	end := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, end, end); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	n := Sweep(context.Background(), logbus.New(64), lib, []string{dir})
	if n != 1 {
		t.Fatalf("Sweep = %d rows, want 1", n)
	}
	rows, _ := lib.ListSetRecordings(10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Kind != libdb.SetKindNative || r.RecordingID != "" {
		t.Fatalf("row shape wrong: %+v", r)
	}
	if r.EndedAt.Unix() != end.Unix() {
		t.Fatalf("ended = %v, want file mtime %v", r.EndedAt, end)
	}
	if d := r.EndedAt.Sub(r.StartedAt); d < time.Second || d > 3*time.Second {
		t.Fatalf("derived span = %v, want ~2s", d)
	}
}

func TestNormPathFoldsCaseAndSlashes(t *testing.T) {
	if normPath(`E:\Media\Rec\a.FLAC`) != normPath(`e:/media/rec/A.flac`) {
		t.Fatal("normPath must fold case + separators")
	}
}

func TestCaptureNameConvention(t *testing.T) {
	yes := []string{"2026-08-10 22-00-42.flac", "2026-08-10 22-00-41.mp4", "2026-01-01 00-00-00.MKV"}
	no := []string{"2026-08-10 22-00-42-cut.flac", "UMC_Rock_x_DnB-cut.flac", "My Set.mp4",
		"2026-08-10 22-00-42.txt", "2026-08-10.flac", "2026-08-10 22-00-42.flac.orig"}
	for _, n := range yes {
		if !captureName(n) {
			t.Fatalf("captureName(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if captureName(n) {
			t.Fatalf("captureName(%q) = true, want false", n)
		}
	}
}

// execCommand runs a command and returns combined output (test helper).
func execCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
