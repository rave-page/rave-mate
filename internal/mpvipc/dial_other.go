//go:build !windows

package mpvipc

import (
	"io"
	"net"
)

// dial connects to mpv's unix-socket IPC endpoint (addr = socket path).
func dial(addr string) (io.ReadWriteCloser, error) {
	return net.Dial("unix", addr)
}
