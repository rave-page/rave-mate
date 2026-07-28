package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
)

// FrameShot samples a LOCAL video-share sender's content and writes the last frame to path.
//
// The point is attribution. Route counters are all rate- or volume-shaped, so a dead source and a
// live one look identical on them; this reads the sender's own texture N times and reports whether
// the picture changed. "8 grabs, 0 changed" is a frozen source, and no downstream number can say it.
func (c *appControl) FrameShot(path, sender string, n int, crop [4]int) string {
	if c.mediaRoutes == nil {
		return "error: media plane unavailable"
	}
	shot, err := c.mediaRoutes.FrameShot(sender, n, 0, 0, crop)
	if err != nil {
		return "error: " + err.Error()
	}
	return renderFrameShot(shot, path, "")
}

// RemoteFrameShot runs the same sample on a PAIRED PEER and writes the PNG here. This is the verb
// that removes the need for physical access to the sending machine: the peer reads ITS sender's
// texture, so the verdict is formed at the origin rather than inferred from what arrives.
func (c *appControl) RemoteFrameShot(nodeID, path, sender string, n int, crop [4]int) string {
	if c.peerMgr == nil || c.remoteCtl == nil {
		return "error: peer link unavailable"
	}
	if path == "" {
		return "usage: remote-frame-shot <path> <n> <sender...>"
	}
	if nodeID == "" {
		for _, p := range c.peerMgr.Connections() {
			if p.Status == peerlink.StatusConnected {
				nodeID = p.NodeID
				break
			}
		}
	}
	if nodeID == "" {
		return "error: no connected peer (run `ctl list-peers`)"
	}
	client := remotectl.NewClient(c.remoteCtl, nodeID)
	if client == nil {
		return "error: invalid peer"
	}
	// The peer samples for up to its own budget before answering; a timeout shorter than that would
	// abandon the call exactly when the answer is being produced.
	ctx, cancel := context.WithTimeout(context.Background(), mediaroute.FrameShotBudget()+10*time.Second)
	defer cancel()
	shot, err := client.FrameShot(ctx, sender, n, crop)
	if err != nil {
		return "error: " + err.Error()
	}
	return renderFrameShot(shot, path, nodeID)
}

// renderFrameShot writes the PNG (when there is one) and formats the verdict.
func renderFrameShot(shot mediaroute.FrameShot, path, nodeID string) string {
	var b strings.Builder
	where := "local"
	if nodeID != "" {
		where = "peer " + nodeID
	}
	if len(shot.Senders) > 0 && shot.Grabs == 0 {
		fmt.Fprintf(&b, "%s: %s\nsenders on %s:\n", where, shot.Err, where)
		for _, s := range shot.Senders {
			fmt.Fprintf(&b, "  %q\n", s)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "%s sender %q %dx%d\n%s\n", where, shot.Sender, shot.W, shot.H, shot.Verdict())
	if len(shot.Hashes) > 0 {
		fmt.Fprintf(&b, "hashes: ")
		for i, h := range shot.Hashes {
			if i > 0 {
				fmt.Fprintf(&b, " ")
			}
			fmt.Fprintf(&b, "%016x", h)
		}
		fmt.Fprintln(&b)
	}
	if shot.Err != "" && shot.Grabs > 0 {
		fmt.Fprintf(&b, "note: %s\n", shot.Err) // partial sample: report it, do not hide it
	}
	switch {
	case len(shot.PNG) == 0:
		fmt.Fprintln(&b, "no PNG (nothing grabbed)")
	case path == "":
		fmt.Fprintf(&b, "%d PNG bytes (no path given)\n", len(shot.PNG))
	default:
		if err := os.WriteFile(path, shot.PNG, 0o644); err != nil {
			fmt.Fprintf(&b, "write %s: %v\n", path, err)
		} else {
			fmt.Fprintf(&b, "wrote %s (%d bytes)\n", path, len(shot.PNG))
		}
	}
	return b.String()
}

// FrameShotSample implements remotectl.FrameShotter: a paired peer asks THIS machine to sample one
// of its own senders. Read-only - it reads a texture we already publish and changes nothing.
func (c *appControl) FrameShotSample(sender string, n int, crop [4]int) (mediaroute.FrameShot, error) {
	if c.mediaRoutes == nil {
		return mediaroute.FrameShot{}, fmt.Errorf("media plane unavailable")
	}
	return c.mediaRoutes.FrameShot(sender, n, 0, 0, crop)
}
