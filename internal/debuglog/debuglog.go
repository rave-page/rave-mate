// Package debuglog writes a persistent, rotated, dated debug log (LogDir) and captures
// panic stack traces - essential for the Windows GUI build (-H windowsgui), which has no
// console, so an uncaught panic would otherwise vanish with the window. Mirrors every
// logbus entry to the file and redirects stderr (so worker-subprocess stderr + explicit
// writes land there too).
package debuglog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/logbus"
)

// FileName is the LEGACY cwd log name (pre 2026-11-13); the log now lives at LogDir()/LogName.
const FileName = "rave-mate-debug.log"

var logFile *os.File // test seam: Recover's sink when no rotator is open
var rot *rotator

// fileMinLevel gates which logbus entries reach the DISK mirror (default Info): a failing
// per-frame Debug path must not become a per-frame disk write (23MB log observed). The
// in-memory ring + Logs tab keep EVERY level. SetFileLevel / RAVE_MATE_FILE_LOG_DEBUG=1
// re-enable Debug-to-disk for field debugging.
var fileMinLevel atomic.Int32

func init() { fileMinLevel.Store(int32(logbus.Info)) }

// SetFileLevel sets the minimum logbus level mirrored to the debug file.
func SetFileLevel(l logbus.Level) { fileMinLevel.Store(int32(l)) }

// Init opens the rotating log at LogDir()/rave-mate.log (falls back to the cwd when no per-user dir
// resolves), redirects stderr to it, writes a dated startup banner and streams every logbus entry into
// it. Returns the live file or nil on failure. Call once at startup.
func Init(bus *logbus.Bus) *os.File {
	dir := LogDir()
	if dir == "" || os.MkdirAll(dir, 0o700) != nil {
		dir, _ = os.Getwd()
	}
	// 0o600: the log mirrors logbus entries + panic stacks (potentially sensitive) - owner-only.
	r, err := openRotator(filepath.Join(dir, LogName), logMaxBytes, logKeep)
	if err != nil {
		return nil
	}
	rot = r
	redirectStderr(r.f)
	r.syncWrite([]byte(fmt.Sprintf("\n==== rave-mate start %s pid=%d log=%s ====\n",
		time.Now().Format(time.RFC3339), os.Getpid(), filepath.Join(dir, LogName))))

	if os.Getenv("RAVE_MATE_FILE_LOG_DEBUG") == "1" {
		SetFileLevel(logbus.Debug)
	}
	if bus != nil {
		ch, _ := bus.Subscribe()
		go mirror(ch, r)
	}
	return r.f
}

// mirror streams bus entries into w, skipping levels below fileMinLevel (the ring buffer behind ch
// already retains every level for the Logs tab). Every line carries a full date + time.
func mirror(ch <-chan logbus.Entry, w io.Writer) {
	for e := range ch {
		if e.Level < logbus.Level(fileMinLevel.Load()) {
			continue
		}
		ts := e.Time.Format(tsLayout)
		if len(e.Fields) > 0 {
			fmt.Fprintf(w, "%s %-5s [%s] %s %v\n", ts, e.Level, e.Source, e.Msg, e.Fields)
		} else {
			fmt.Fprintf(w, "%s %-5s [%s] %s\n", ts, e.Level, e.Source, e.Msg)
		}
	}
}

// Go runs fn in a goroutine guarded by Recover (contained, not fatal) so a panic in a
// feature goroutine is logged with its stack instead of silently killing the daemon.
// Prefer this over a bare `go func()` for any user-triggered/background work.
func Go(bus *logbus.Bus, source string, fn func()) {
	go func() {
		defer Recover(bus, source, false)
		fn()
	}()
}

// Recover logs a recovered panic with its stack to the debug file (synchronously, so it's
// flushed before exit) and the bus. Defer it at the top of a goroutine or main; fatal=true
// exits the process after logging (use for the main/UI goroutine - the loop is dead
// anyway), fatal=false contains the panic so a worker goroutine can't take the app down.
func Recover(bus *logbus.Bus, source string, fatal bool) {
	r := recover()
	if r == nil {
		return
	}
	stack := debug.Stack()
	msg := fmt.Sprintf("panic: %v", r)
	line := fmt.Sprintf("%s ERROR [%s] %s\n%s\n", time.Now().Format(tsLayout), source, msg, stack)
	switch {
	case rot != nil:
		rot.syncWrite([]byte(line))
	case logFile != nil:
		_, _ = logFile.WriteString(line)
		_ = logFile.Sync()
	}
	if bus != nil {
		bus.Error(source, msg, map[string]any{"stack": string(stack)})
	}
	if fatal {
		os.Exit(1)
	}
}
