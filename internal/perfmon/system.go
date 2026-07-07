package perfmon

// ProcStat is one sampled process: CPU% of one core over the sampling window + working set.
type ProcStat struct {
	PID    int
	Name   string
	CPUPct float64
	WSMB   float64
}

// SysStat is the system-wide sampling result (OK=false + Err on unsupported platforms).
type SysStat struct {
	OK         bool
	Err        string
	CPUPct     float64 // system CPU% across all cores
	MemUsedMB  float64
	MemTotalMB float64
	Procs      []ProcStat // sorted by CPUPct desc
}
