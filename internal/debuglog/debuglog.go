// Package debuglog writes a persistent debug log to the working directory and captures
// panic stack traces - essential for the Windows GUI build (-H windowsgui), which has no
// console, so an uncaught panic would otherwise vanish with the window. Mirrors every
// logbus entry to the file and redirects stderr (so worker-subprocess stderr + explicit
// writes land there too).
package debuglog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"rave.page/mate/internal/logbus"
)

// FileName is the debug log, written in the process working directory.
const FileName = "rave-mate-debug.log"

var logFile *os.File

// Init opens (appends) the debug log in the cwd, redirects stderr to it, writes a startup
// banner, and streams every logbus entry into it. Returns the file (keep it open for the
// process lifetime) or nil on failure. Safe to call once at startup.
func Init(bus *logbus.Bus) *os.File {
	path := FileName
	if wd, err := os.Getwd(); err == nil {
		path = filepath.Join(wd, FileName)
	}
	// 0o600: the log mirrors logbus entries + panic stacks (potentially sensitive); keep
	// it owner-only. Stays in the cwd by request (the cwd itself may be broader, but the
	// file isn't world-readable).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	logFile = f
	redirectStderr(f)
	fmt.Fprintf(f, "\n==== rave-mate start %s pid=%d ====\n", time.Now().Format(time.RFC3339), os.Getpid())
	_ = f.Sync()

	if bus != nil {
		ch, _ := bus.Subscribe()
		go func() {
			for e := range ch {
				ts := e.Time.Format("15:04:05.000")
				if len(e.Fields) > 0 {
					fmt.Fprintf(f, "%s %-5s [%s] %s %v\n", ts, e.Level, e.Source, e.Msg, e.Fields)
				} else {
					fmt.Fprintf(f, "%s %-5s [%s] %s\n", ts, e.Level, e.Source, e.Msg)
				}
			}
		}()
	}
	return f
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
	if logFile != nil {
		fmt.Fprintf(logFile, "%s ERROR [%s] %s\n%s\n", time.Now().Format("15:04:05.000"), source, msg, stack)
		_ = logFile.Sync()
	}
	if bus != nil {
		bus.Error(source, msg, map[string]any{"stack": string(stack)})
	}
	if fatal {
		os.Exit(1)
	}
}
