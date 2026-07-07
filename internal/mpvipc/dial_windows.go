//go:build windows

package mpvipc

import (
	"io"
	"os"
)

// dial opens the client end of mpv's named pipe (addr = `\\.\pipe\<name>`). os.OpenFile maps to
// CreateFile, which connects to the duplex pipe mpv created with --input-ipc-server.
func dial(addr string) (io.ReadWriteCloser, error) {
	return os.OpenFile(addr, os.O_RDWR, 0)
}
