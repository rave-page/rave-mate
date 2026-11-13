package debuglog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Log location + rotation (incident 2026-11-13: the log followed the launch CWD - the install-dir copy
// silently went stale and grew to 388 MB unrotated, lines had no date). Fixed per-user dir, size
// rotation, ISO timestamps. Bounded: keep logKeep files of at most logMaxBytes each.
const (
	LogName     = "rave-mate.log"
	logMaxBytes = 50 << 20
	logKeep     = 5 // rave-mate.log + .1 .. .4
	tsLayout    = "2006-01-02T15:04:05.000"
)

// LogDir is the fixed log directory: RAVE_MATE_LOG_DIR, else %LOCALAPPDATA%\rave-mate\logs on Windows,
// $XDG_STATE_HOME (default ~/.local/state)/rave-mate/logs elsewhere. "" when nothing resolves.
func LogDir() string {
	if d := os.Getenv("RAVE_MATE_LOG_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "rave-mate", "logs")
		}
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "rave-mate", "logs")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "rave-mate", "logs")
	}
	return ""
}

// rotator is the size-rotated log file. One writer goroutine (mirror) plus Recover; mu serializes.
type rotator struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
	max  int64
	keep int
}

func openRotator(path string, max int64, keep int) (*rotator, error) {
	r := &rotator{path: path, max: max, keep: keep}
	if st, err := os.Stat(path); err == nil && st.Size() >= max {
		r.shift()
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotator) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err == nil {
		r.size = st.Size()
	}
	r.f = f
	return nil
}

// shift renames rave-mate.log -> .1 -> ... dropping the oldest (keep files total).
func (r *rotator) shift() {
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep-1))
	for i := r.keep - 2; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	_ = os.Rename(r.path, r.path+".1")
}

// Write appends p, rotating first when it would exceed max.
func (r *rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil && r.size+int64(len(p)) > r.max {
		_ = r.f.Close()
		r.f = nil
		r.shift()
		if err := r.open(); err == nil {
			redirectStderr(r.f) // child stderr follows the live file
		}
	}
	if r.f == nil {
		return 0, os.ErrClosed
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// syncWrite writes + fsyncs (panic stacks before exit).
func (r *rotator) syncWrite(p []byte) {
	_, _ = r.Write(p)
	r.mu.Lock()
	if r.f != nil {
		_ = r.f.Sync()
	}
	r.mu.Unlock()
}
