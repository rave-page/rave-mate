package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/testcard"
)

// Testcard drives the deterministic diagnostic source (ctl testcard <start|stop|stats|reset>).
//
// The card is a Spout sender ("rave-mate testcard") whose every frame carries its own seq +
// timestamp + session in-picture, so any stage that sees pixels can PROVE which frames were
// skipped, repeated (frozen) or delayed - the questions rate counters cannot answer. Run it two
// ways and diff: routed DIRECTLY over a media route (no third parties), and routed THROUGH OBS
// (add it as a Spout2 Capture source stretched to the canvas). Direct clean + via-OBS freezing
// pins the loss to the OBS leg; both freezing pins our own chain.
func (c *appControl) Testcard(args string) string {
	op, w, h, fps, err := parseTestcardArgs(args)
	if err != nil {
		return "error: " + err.Error()
	}
	if c.mediaRoutes == nil {
		return "error: media plane unavailable"
	}
	rep, err := c.mediaRoutes.Testcard(op, w, h, fps)
	if err != nil {
		return "error: " + err.Error()
	}
	return renderTestcardReport(rep, "")
}

// RemoteTestcard runs the same op on a PAIRED PEER - the generator must run on the SENDING machine
// of the chain under test, and this starts it there without physical access.
func (c *appControl) RemoteTestcard(nodeID, args string) string {
	op, w, h, fps, err := parseTestcardArgs(args)
	if err != nil {
		return "error: " + err.Error()
	}
	if c.peerMgr == nil || c.remoteCtl == nil {
		return "error: peer link unavailable"
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep, err := client.Testcard(ctx, op, w, h, fps)
	if err != nil {
		return "error: " + err.Error()
	}
	return renderTestcardReport(rep, nodeID)
}

// TestcardOp implements remotectl.TestcardController: a paired peer drives THIS machine's
// diagnostic source.
func (c *appControl) TestcardOp(op string, w, h, fps int) (testcard.Report, error) {
	if c.mediaRoutes == nil {
		return testcard.Report{}, fmt.Errorf("media plane unavailable")
	}
	return c.mediaRoutes.Testcard(op, w, h, fps)
}

// parseTestcardArgs: "<start|stop|stats|reset> [WxH@fps]" (spec only for start; empty op = stats).
func parseTestcardArgs(args string) (op string, w, h, fps int, err error) {
	f := strings.Fields(strings.TrimSpace(args))
	if len(f) == 0 {
		return "stats", 0, 0, 0, nil
	}
	op = f[0]
	if op == "start" && len(f) > 1 {
		if n, e := fmt.Sscanf(f[1], "%dx%d@%d", &w, &h, &fps); e != nil || n != 3 {
			return "", 0, 0, 0, fmt.Errorf("bad spec %q (want WxH@fps, e.g. 1280x720@30)", f[1])
		}
	}
	return op, w, h, fps, nil
}

// renderTestcardReport formats generator ground truth + every verifier stage.
func renderTestcardReport(rep testcard.Report, nodeID string) string {
	var b strings.Builder
	where := "local"
	if nodeID != "" {
		where = "peer " + nodeID
	}
	if rep.Gen != nil {
		g := rep.Gen
		fmt.Fprintf(&b, "%s generator %q %dx%d@%d session %d: %d frames, %d skips, %.1f fps achieved, worst send %dms, up %s\n",
			where, g.Name, g.W, g.H, g.FPS, g.Session, g.Frames, g.Skips,
			g.AchievedFPS(), g.SendMaxMs, time.Since(g.StartedAt).Round(time.Second))
	} else {
		fmt.Fprintf(&b, "%s generator: not running\n", where)
	}
	if len(rep.Stages) == 0 {
		fmt.Fprintln(&b, "no verifier stage has seen the card yet (stages appear once a card frame reaches CPU pixels;"+
			" note the GPU zero-copy decode path bypasses the CPU sink - disable zero-copy decode to verify a receive route)")
		return b.String()
	}
	names := make([]string, 0, len(rep.Stages))
	for n := range rep.Stages {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v := rep.Stages[n]
		fmt.Fprintf(&b, "stage %q session %d (target %d fps):\n", v.Stage, v.Session, v.GenFPS)
		fmt.Fprintf(&b, "  frames %d  decoded %d  crcFail %d  lowContrast %d  restarts %d\n",
			v.Frames, v.Decoded, v.CRCFail, v.LowContr, v.Restarts)
		fmt.Fprintf(&b, "  seq: last %d  unique %d (%.1f/s delivered)  dups %d (worst freeze %d frames)  "+
			"gaps %d missed (worst jump %d)  reorders %d  genBehind %d\n",
			v.LastSeq, v.Unique, v.SeqRate(), v.Dups, v.MaxDupRun, v.Gaps, v.MaxGap, v.Reorders, v.GenBehind)
		fmt.Fprintf(&b, "  latency: last %dms  min %dms  max %dms  drift %+dms (drift climbing = falling behind)\n",
			v.LastDeltaMs, v.MinDeltaMs, v.MaxDeltaMs, v.DriftMs())
	}
	return b.String()
}
