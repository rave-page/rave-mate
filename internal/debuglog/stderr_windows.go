//go:build windows

package debuglog

import (
	"os"

	"golang.org/x/sys/windows"
)

// redirectStderr points the process stderr at the log file. SetStdHandle makes child
// processes (worker subprocesses, whose Stderr = os.Stderr) inherit it; reassigning
// os.Stderr routes this process's explicit stderr writes there too.
func redirectStderr(f *os.File) {
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
	os.Stderr = f
}
