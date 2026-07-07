// Package guardian is the last-resort crash supervisor. The app spawns `rave-mate guardian` as a
// detached child holding a pipe from the parent; if the pipe hits EOF WITHOUT the disarm line the
// parent died hard (cgo/OpenVR fault, kill) and the guardian relaunches the exe. Survives exactly
// what the in-process GPU watchdog cannot: whole-process death (live crash 2026-07-02, SteamVR +
// rave-mate down together, manual restart needed). Clean shutdown / self-update disarm it.
package guardian

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/sysexec"
)

const (
	disarmLine   = "disarm"
	restartState = "guardian_restarts.json" // hard-restart timestamps (crash-loop brake)
	loopWindow   = 10 * time.Minute
	loopMax      = 4 // ≥5 hard restarts inside the window → give up (crash loop)
	settleDelay  = 2 * time.Second
)

// Spawn launches the guardian child watching THIS process and returns a disarm func (idempotent;
// call on every clean-shutdown path). stateDir holds the crash-loop brake file.
func Spawn(stateDir string) (func(), error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c := exec.Command(exe, "guardian", stateDir)
	c.Stdin = r
	sysexec.Hide(c)
	// Deliberately NOT AssignToJob: the kill-on-close job would take the guardian down with us.
	if err := c.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, err
	}
	_ = r.Close() // child inherited the read end
	_ = c.Process.Release()
	var once sync.Once
	return func() {
		once.Do(func() {
			_, _ = w.WriteString(disarmLine + "\n")
			_ = w.Close()
		})
	}, nil
}

// Run is the guardian child body (exit code for main). Blocks on stdin: disarm → exit clean;
// EOF without disarm → parent crashed → relaunch the exe (post-update the swapped exe is picked
// up automatically via os.Executable at spawn time - the path is stable).
func Run(stateDir string) int {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == disarmLine {
			return 0
		}
	}
	if !allowRestart(stateDir, time.Now()) {
		return 1 // crash loop - leave the machine alone
	}
	exe, err := os.Executable()
	if err != nil {
		return 1
	}
	time.Sleep(settleDelay) // let the dead instance's locks/ports/GPU settle
	if err := sysexec.StartDetached(exe, nil, filepath.Dir(exe)); err != nil {
		return 1
	}
	return 0
}

// allowRestart records a hard restart at now and reports whether it may proceed: at most loopMax
// restarts inside loopWindow (the brake against a crash-on-startup loop). Best-effort file state -
// unreadable/corrupt state fails open (restart allowed).
func allowRestart(stateDir string, now time.Time) bool {
	path := filepath.Join(stateDir, restartState)
	var stamps []time.Time
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &stamps)
	}
	fresh := stamps[:0]
	for _, t := range stamps {
		if now.Sub(t) < loopWindow {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= loopMax {
		return false
	}
	fresh = append(fresh, now)
	if b, err := json.Marshal(fresh); err == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
	return true
}
