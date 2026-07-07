//go:build !windows

package debuglog

import "os"

// redirectStderr reassigns os.Stderr. (Capturing the Go runtime's own panic output to the
// file on Unix needs a dup2 onto fd 2 - a follow-up; the deferred Recover already logs
// panics with full stacks regardless of platform.)
func redirectStderr(f *os.File) { os.Stderr = f }
