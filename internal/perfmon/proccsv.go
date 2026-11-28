package perfmon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// Per-process CPU history beside gpumem.csv. Incident 2026-11-28: the VRAM log pinned who filled the
// card, but "how much CPU did the media child burn" had no answer - the process was gone and nothing
// had recorded it. One sweep a minute over a procCSVWindow sample; rows = system total, the top
// procCSVTop processes by CPU, and every rave-mate/ffmpeg process regardless of rank.
// Bounded: rotated to <path>.1 at procCSVMaxBytes (two files total). Read-only kernel calls, 1/min:
// the in-proc low-throughput class (same as gpumem's sweep).
const (
	procCSVInterval = time.Minute
	procCSVWindow   = 5 * time.Second
	procCSVTop      = 10
	procCSVMaxBytes = 20 << 20
	procCSVHeader   = "time,kind,sys_cpu_pct,mem_used_mb,mem_total_mb,process,pid,cpu_pct,ws_mb\n"
)

// RunProcCSV appends the per-minute CPU history to path until ctx is done. No-op where system
// sampling is unsupported (first sample not OK) or path is empty.
func RunProcCSV(ctx context.Context, path string) {
	if path == "" {
		return
	}
	var size int64
	if st, err := os.Stat(path); err == nil {
		size = st.Size()
	}
	t := time.NewTicker(procCSVInterval)
	defer t.Stop()
	for {
		st := sysSnapshot(procCSVWindow)
		if !st.OK {
			return
		}
		size = appendProcCSV(path, size, procCSVRows(time.Now(), st))
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ours reports a process worth a row at any CPU rank (rave-mate's own tree + its ffmpeg children).
func ours(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "rave-mate") || strings.HasPrefix(n, "ffmpeg")
}

// procCSVRows renders one sweep (st.Procs is sorted by CPU desc).
func procCSVRows(now time.Time, st SysStat) []string {
	ts := now.Format("2006-01-02T15:04:05")
	out := []string{fmt.Sprintf("%s,system,%.1f,%.0f,%.0f,,,,\n", ts, st.CPUPct, st.MemUsedMB, st.MemTotalMB)}
	for i, p := range st.Procs {
		if i >= procCSVTop && !ours(p.Name) {
			continue
		}
		name := strings.NewReplacer(",", " ", "\n", " ").Replace(p.Name)
		out = append(out, fmt.Sprintf("%s,process,,,,%s,%d,%.1f,%.0f\n", ts, name, p.PID, p.CPUPct, p.WSMB))
	}
	return out
}

// appendProcCSV appends rows (rotating at the cap) and returns the new size.
func appendProcCSV(path string, size int64, rows []string) int64 {
	body := strings.Join(rows, "")
	if size > 0 && size+int64(len(body)) > procCSVMaxBytes {
		_ = os.Rename(path, path+".1") // best effort: on failure we keep appending to the old file
		size = 0
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return size
	}
	defer f.Close()
	if size == 0 {
		n, _ := f.WriteString(procCSVHeader)
		size += int64(n)
	}
	n, _ := f.WriteString(body)
	return size + int64(n)
}
