//go:build !windows

package perfmon

import "time"

// sysSnapshot: system-wide CPU/mem/top-process sampling is Windows-only for now.
func sysSnapshot(time.Duration) SysStat {
	return SysStat{Err: "system stats unsupported on this platform"}
}

// sysProbe: incremental system CPU/mem sampling is Windows-only; always not-ok.
type sysProbe struct{}

func (*sysProbe) tick() (cpuPct, usedMB, totalMB float64, ok bool) { return 0, 0, 0, false }
