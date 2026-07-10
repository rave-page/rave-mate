package libsync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/seratolib"
)

func TestApplyTargetSeratoWriteback(t *testing.T) {
	dir := t.TempDir()
	// Bare MPEG bytes, no ID3 tag - seratolib creates a fresh one.
	mp3 := filepath.Join(dir, "one.mp3")
	if err := os.WriteFile(mp3, []byte{0xFF, 0xFB, 0x90, 0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []musiclib.Track{
		{Path: mp3, BPM: 172, Beatgrid: []musiclib.GridMarker{{PositionMs: 250, BPM: 172}}},
		{Path: filepath.Join(dir, "vt.mp3"), Beatgrid: []musiclib.GridMarker{{PositionMs: 0, BPM: 120}, {PositionMs: 4000, BPM: 121}}}, // variable: skipped
		{Path: filepath.Join(dir, "nogrid.mp3")}, // no grid: skipped
	}
	out, err := applyTarget(config.SyncTarget{App: AppSerato, Mode: ModeWriteback, OutputPath: dir}, tracks)
	if errors.Is(err, seratolib.ErrSeratoRunning) {
		t.Skip("Serato DJ running on this machine")
	}
	if err != nil {
		t.Fatal(err)
	}
	if out.Updated != 1 || out.Path != dir {
		t.Fatalf("outcome %+v", out)
	}
	markers, found, err := seratolib.ReadBeatgrid(mp3)
	if err != nil || !found || len(markers) != 1 {
		t.Fatalf("grid not written: %v %v %v", markers, found, err)
	}
	if markers[0].BPM < 171.99 || markers[0].BPM > 172.01 {
		t.Fatalf("bpm %v", markers[0].BPM)
	}

	// Only skippable tracks: no-op outcome, no error.
	out, err = applyTarget(config.SyncTarget{App: AppSerato, Mode: ModeWriteback, OutputPath: dir}, tracks[1:])
	if err != nil || out.Updated != 0 {
		t.Fatalf("no-op outcome %+v err %v", out, err)
	}

	// Serato has no importable-file mode.
	if _, err := applyTarget(config.SyncTarget{App: AppSerato, Mode: ModeFile, OutputPath: filepath.Join(dir, "out.xml")}, tracks); err == nil {
		t.Fatal("file mode not refused")
	}
}
