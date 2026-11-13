package gpumem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCSVRowsAndRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gpumem.csv")
	c := newCSVLog(p)
	now := time.Date(2026, 11, 13, 3, 37, 0, 0, time.Local)
	c.write(adapterRows(now, []AdapterUsage{{Name: "RTX, 3060", UsedMB: 11859, FreeMB: 257, BudgetMB: 12288}}))
	c.write(processRows(now, []AdapterProcs{{Adapter: "RTX 3060", Procs: []ProcUsage{{Name: "Arena.exe", PID: 30492, MB: 10300}}}}))
	b, _ := os.ReadFile(p)
	s := string(b)
	if !strings.HasPrefix(s, csvHeader) || !strings.Contains(s, "2026-11-13T03:37:00,adapter,RTX  3060,11859,257,12288") ||
		!strings.Contains(s, "process,RTX 3060,,,,Arena.exe,30492,10300") {
		t.Fatalf("csv:\n%s", s)
	}
	c.size = csvMaxBytes // force rotation on the next write
	c.write(adapterRows(now, []AdapterUsage{{Name: "x"}}))
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("full csv must rotate to .1")
	}
	if b, _ := os.ReadFile(p); !strings.HasPrefix(string(b), csvHeader) {
		t.Fatal("rotated csv starts with a header")
	}
}
