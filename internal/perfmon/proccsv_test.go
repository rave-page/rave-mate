package perfmon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcCSVRows(t *testing.T) {
	st := SysStat{OK: true, CPUPct: 41.26, MemUsedMB: 30000, MemTotalMB: 65400}
	for i := 0; i < 14; i++ {
		st.Procs = append(st.Procs, ProcStat{PID: 100 + i, Name: "app,x.exe", CPUPct: float64(200 - i), WSMB: 50})
	}
	st.Procs = append(st.Procs, ProcStat{PID: 7, Name: "rave-mate-feature-media.exe", CPUPct: 0.4, WSMB: 80},
		ProcStat{PID: 8, Name: "FFmpeg.exe", CPUPct: 0.1, WSMB: 30})
	rows := procCSVRows(time.Date(2026, 11, 28, 5, 22, 0, 0, time.UTC), st)
	if len(rows) != 1+procCSVTop+2 {
		t.Fatalf("rows = %d, want system + top %d + 2 own", len(rows), procCSVTop)
	}
	if rows[0] != "2026-11-28T05:22:00,system,41.3,30000,65400,,,,\n" {
		t.Fatalf("system row %q", rows[0])
	}
	if rows[1] != "2026-11-28T05:22:00,process,,,,app x.exe,100,200.0,50\n" {
		t.Fatalf("process row %q", rows[1])
	}
	cols := strings.Count(procCSVHeader, ",")
	for _, r := range rows {
		if strings.Count(r, ",") != cols {
			t.Fatalf("column count differs from header: %q", r)
		}
	}
	if !strings.Contains(rows[len(rows)-2], "rave-mate-feature-media.exe,7,0.4,80") {
		t.Fatalf("own low-CPU process dropped: %q", rows[len(rows)-2])
	}
}

func TestAppendProcCSVHeaderAndRotate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "procs.csv")
	size := appendProcCSV(p, 0, []string{"a\n"})
	b, _ := os.ReadFile(p)
	if string(b) != procCSVHeader+"a\n" || size != int64(len(b)) {
		t.Fatalf("first write %q size %d", b, size)
	}
	size = appendProcCSV(p, procCSVMaxBytes, []string{"b\n"}) // at the cap: rotate
	b, _ = os.ReadFile(p)
	if string(b) != procCSVHeader+"b\n" || size != int64(len(b)) {
		t.Fatalf("rotated write %q", b)
	}
	if old, err := os.ReadFile(p + ".1"); err != nil || !strings.HasSuffix(string(old), "a\n") {
		t.Fatalf("rotated file missing: %v %q", err, old)
	}
}
