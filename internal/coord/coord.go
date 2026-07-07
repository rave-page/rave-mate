// Package coord is rave-mate's cross-app update coordination: after rave-mate applies its own
// update, tell a co-located rave-app to self-update too. Leaf package (no rave-mate imports)
// so both the Fyne UI and the daemon control surface can call it without an import cycle.
package coord

import (
	"net"
	"time"
)

// raveAppCtl is rave-app's single-instance / control socket (rave-app/internal/instance).
const raveAppCtl = "127.0.0.1:47622"

// NotifyRaveApp asks a co-located rave-app to self-update (fire-and-forget; no-op if rave-app
// isn't running). One-directional - rave-app applies WITHOUT triggering us back, so no
// ping-pong.
func NotifyRaveApp() {
	conn, err := net.DialTimeout("tcp", raveAppCtl, 2*time.Second)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("UPDATE\n"))
}
