package encoderscan

import "rave.page/mate/internal/sysactivity"

// OBSEncoderFunc reports OBS's configured stream/record encoder ids + whether it is actively
// encoding (streaming or recording). Wired from the app's OBS surface; nil if OBS isn't connected.
type OBSEncoderFunc func() (stream, record string, active bool, err error)

// Detect assembles a live Report: OBS encoder (obsEnc, may be nil), the running process list, the
// per-process GPU video-engine utilization (Windows; nil elsewhere), and Parsec's log. Read-only.
func Detect(obsEnc OBSEncoderFunc) Report {
	return Scan(Deps{
		OBSEncoder:    obsEnc,
		Processes:     listProcs,
		GPU:           sampleGPU,
		ParsecEncoder: ParsecEncoder,
		AdapterNames:  adapterNames,
		AdapterVRAM:   adapterVRAMFree,
	})
}

// listProcs adapts sysactivity's Toolhelp snapshot to the scan's Proc list.
func listProcs() ([]Proc, error) {
	infos, ok := sysactivity.ListProcesses()
	if !ok {
		return nil, nil
	}
	out := make([]Proc, 0, len(infos))
	for _, p := range infos {
		out = append(out, Proc{Name: p.Name, PID: int(p.PID)})
	}
	return out, nil
}
