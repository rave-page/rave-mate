package gpumem

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// csvLog is the per-minute VRAM history (incident 2026-11-13: a 5-min process sweep could not pin
// Resolume's steps; "we should've set up logging"). One row per adapter sample and per process per
// minute. Bounded: rotated to <path>.1 at csvMaxBytes (two files total).
type csvLog struct {
	mu   sync.Mutex
	path string
	size int64
}

const csvMaxBytes = 20 << 20

const csvHeader = "time,kind,adapter,used_mb,free_mb,budget_mb,process,pid,proc_mb\n"

func newCSVLog(path string) *csvLog {
	if path == "" {
		return nil
	}
	c := &csvLog{path: path}
	if st, err := os.Stat(path); err == nil {
		c.size = st.Size()
	}
	return c
}

func (c *csvLog) write(rows []string) {
	if c == nil || len(rows) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	body := strings.Join(rows, "")
	if c.size > 0 && c.size+int64(len(body)) > csvMaxBytes {
		_ = os.Rename(c.path, c.path+".1")
		c.size = 0
	}
	f, err := os.OpenFile(c.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if c.size == 0 {
		n, _ := f.WriteString(csvHeader)
		c.size += int64(n)
	}
	n, _ := f.WriteString(body)
	c.size += int64(n)
}

func csvTime(t time.Time) string { return t.Format("2006-01-02T15:04:05") }

func csvEsc(s string) string { return strings.NewReplacer(",", " ", "\n", " ").Replace(s) }

func adapterRows(now time.Time, ads []AdapterUsage) []string {
	out := make([]string, 0, len(ads))
	for _, a := range ads {
		out = append(out, fmt.Sprintf("%s,adapter,%s,%d,%d,%d,,,\n", csvTime(now), csvEsc(a.Name), a.UsedMB, a.FreeMB, a.BudgetMB))
	}
	return out
}

func processRows(now time.Time, aps []AdapterProcs) []string {
	var out []string
	for _, ap := range aps {
		for _, p := range ap.Procs {
			out = append(out, fmt.Sprintf("%s,process,%s,,,,%s,%d,%d\n", csvTime(now), csvEsc(ap.Adapter), csvEsc(p.Name), p.PID, p.MB))
		}
	}
	return out
}
