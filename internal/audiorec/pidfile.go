// Capture-ffmpeg pidfile: remembers the child we launched so the NEXT session can reap a
// survivor of a daemon crash. Belt over the kill-on-close job (which is best-effort): an
// orphan that escapes both records silence into the set file until someone notices
// (2026-08-10: 12h). One file - one capture is active at a time.
package audiorec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/sysactivity"
	"rave.page/mate/internal/sysexec"
)

// pidRecord is the persisted identity of the active capture child.
type pidRecord struct {
	PID       int       `json:"pid"`
	Path      string    `json:"path"` // capture output file
	StartedAt time.Time `json:"startedAt"`
}

// pidFilePath is <configDir>/audiorec.pid.json ("" when no config dir resolves).
func pidFilePath() string {
	d, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "audiorec.pid.json")
}

// writePidFile persists the active capture child's identity (best-effort).
func writePidFile(pid int, path string, started time.Time) {
	p := pidFilePath()
	if p == "" {
		return
	}
	b, err := json.Marshal(pidRecord{PID: pid, Path: path, StartedAt: started})
	if err != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o644)
}

// removePidFile clears the record once the capture finalized (best-effort).
func removePidFile() {
	if p := pidFilePath(); p != "" {
		_ = os.Remove(p)
	}
}

// ReapOrphan kills a capture ffmpeg a previous session left behind. Identity-checked: the
// recorded PID must currently be an ffmpeg process - PIDs get reused, and killing a stranger
// is worse than leaving an orphan. Call once at startup, before any new capture. The file
// itself is recovered by the caprecover sweep (unfinalized header repair + registration).
func (r *Recorder) ReapOrphan() {
	p := pidFilePath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return // no record - nothing to reap
	}
	_ = os.Remove(p) // one shot either way
	var rec pidRecord
	if json.Unmarshal(b, &rec) != nil || rec.PID <= 0 {
		return
	}
	if !pidIsFfmpeg(rec.PID) {
		return // exited on its own, or the PID was reused - leave it be
	}
	proc, err := os.FindProcess(rec.PID)
	if err != nil {
		return
	}
	sysexec.KillTree(proc)
	r.log.Warn(source, "killed orphaned capture ffmpeg from a previous session - its file will be recovered on this startup",
		map[string]any{"pid": rec.PID, "path": rec.Path, "startedAt": rec.StartedAt.Format(time.RFC3339)})
}

// pidIsFfmpeg reports whether pid currently belongs to an ffmpeg process.
func pidIsFfmpeg(pid int) bool {
	procs, ok := sysactivity.ListProcesses()
	if !ok {
		return false // cannot verify identity → do not kill
	}
	for _, pr := range procs {
		if int(pr.PID) == pid {
			return strings.HasPrefix(strings.ToLower(pr.Name), "ffmpeg")
		}
	}
	return false
}
