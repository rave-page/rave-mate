package app

// peerctl.go: headless `ctl` front-ends for peer pairing + media sources/routes. These are thin
// adapters over the SAME peerlink / mediaroute functions the webui drives (actions.go:
// peerConnect/peerSAS/peerForget, mediaReceive/mediaStop) - no new trust logic lives here. Pairing
// still requires an explicit SAS confirm on BOTH ends; nothing here auto-accepts or bypasses the SAS.
// ctl is loopback-only (instance.go), so these do not widen any surface. Never prints secrets -
// node ids + the SAS digits only.

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/discovery"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/peers"
)

// pairWait bounds how long `ctl pair` blocks waiting for the handshake to reach the SAS prompt
// before telling the caller to poll `ctl peers`. No ctl call may hang.
const pairWait = 8 * time.Second

// ── peers ─────────────────────────────────────────────────────────────────────

// peerRow is one merged peer for `ctl peers` (discovered ∪ remembered ∪ connected).
type peerRow struct {
	NodeID    string
	Name      string
	Address   string
	State     string // discovered | connecting | pairing-pending | paired | connected | offline
	SAS       string // only while pairing-pending
	Initiator string // "local" | "remote", only while pairing-pending
}

// mergePeers folds the three peer sources into one row per node id. Connection status wins over the
// remembered/discovered baseline (connected > pairing-pending > connecting > paired > discovered).
func mergePeers(remembered []peers.Peer, discovered []discovery.Peer, conns []peerlink.ConnInfo) []peerRow {
	rows := map[string]*peerRow{}
	get := func(id string) *peerRow {
		if r, ok := rows[id]; ok {
			return r
		}
		r := &peerRow{NodeID: id, State: "offline"}
		rows[id] = r
		return r
	}
	for _, p := range remembered {
		r := get(p.NodeID)
		if p.Nickname != "" {
			r.Name = p.Nickname
		}
		if p.LastAddress != "" {
			r.Address = p.LastAddress
		}
		if p.Trusted {
			r.State = "paired"
		}
	}
	for _, d := range discovered {
		r := get(d.NodeID)
		if r.Name == "" {
			r.Name = d.Name
		}
		if r.Address == "" && d.Address != nil {
			r.Address = net.JoinHostPort(d.Address.String(), strconv.Itoa(d.Port))
		}
		if r.State == "offline" {
			r.State = "discovered"
		}
	}
	for _, cn := range conns {
		r := get(cn.NodeID)
		if cn.Nickname != "" {
			r.Name = cn.Nickname
		}
		if cn.Address != "" {
			r.Address = cn.Address
		}
		switch cn.Status {
		case peerlink.StatusConnected:
			r.State = "connected"
		case peerlink.StatusAwaitSAS:
			r.State = "pairing-pending"
			r.SAS = cn.SAS
			r.Initiator = "local"
			if cn.Inbound {
				r.Initiator = "remote"
			}
		case peerlink.StatusConnecting:
			if r.State != "connected" {
				r.State = "connecting"
			}
		}
	}
	out := make([]peerRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// formatPeerRows renders the merged rows, one tab-separated line each.
func formatPeerRows(rows []peerRow) string {
	if len(rows) == 0 {
		return "no peers (none discovered, paired or connected)\n"
	}
	var b strings.Builder
	for _, r := range rows {
		name := r.Name
		if name == "" {
			name = "(unnamed)"
		}
		addr := r.Address
		if addr == "" {
			addr = "-"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s", r.NodeID, name, addr, r.State)
		if r.State == "pairing-pending" {
			fmt.Fprintf(&b, "\tsas=%s\tinitiated=%s", r.SAS, r.Initiator)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Peers lists discovered + remembered + connected peers.
func (c *appControl) Peers() string {
	if c.peerMgr == nil {
		return "peer link unavailable\n"
	}
	var disc []discovery.Peer
	if c.disc != nil {
		disc = c.disc.Peers()
	}
	return formatPeerRows(mergePeers(c.peerMgr.Remembered(), disc, c.peerMgr.Connections()))
}

// ── pairing ─────────────────────────────────────────────────────────────────

// isHostPort reports a literal dial target ("host:port" with a numeric port).
func isHostPort(s string) bool {
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" {
		return false
	}
	n, aerr := strconv.Atoi(port)
	return aerr == nil && n > 0 && n < 65536
}

// resolveDiscovered picks one discovered peer by exact node id, exact (case-insensitive) name, then
// an unambiguous node-id prefix or name substring. ok=false on no/ambiguous match.
func resolveDiscovered(list []discovery.Peer, token string) (discovery.Peer, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return discovery.Peer{}, false
	}
	for _, p := range list {
		if p.NodeID == token {
			return p, true
		}
	}
	lt := strings.ToLower(token)
	uniq := func(pred func(discovery.Peer) bool) (discovery.Peer, bool) {
		var hit discovery.Peer
		n := 0
		for _, p := range list {
			if pred(p) {
				hit = p
				n++
			}
		}
		return hit, n == 1
	}
	if p, ok := uniq(func(p discovery.Peer) bool { return strings.EqualFold(p.Name, token) }); ok {
		return p, true
	}
	if p, ok := uniq(func(p discovery.Peer) bool { return strings.HasPrefix(strings.ToLower(p.NodeID), lt) }); ok {
		return p, true
	}
	if p, ok := uniq(func(p discovery.Peer) bool { return strings.Contains(strings.ToLower(p.Name), lt) }); ok {
		return p, true
	}
	return discovery.Peer{}, false
}

// nodeCand is an (id,name) candidate for token→node-id resolution.
type nodeCand struct{ ID, Name string }

// matchNodeID resolves a token to one node id among candidates (exact id/name, else unambiguous
// id-prefix / name substring). ok=false on no/ambiguous match.
func matchNodeID(cands []nodeCand, token string) (string, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	for _, c := range cands {
		if c.ID == token {
			return c.ID, true
		}
	}
	lt := strings.ToLower(token)
	uniq := func(pred func(nodeCand) bool) (string, bool) {
		seen := map[string]bool{}
		var hit string
		for _, c := range cands {
			if pred(c) && !seen[c.ID] {
				seen[c.ID] = true
				hit = c.ID
			}
		}
		if len(seen) == 1 {
			return hit, true
		}
		return "", false
	}
	if id, ok := uniq(func(c nodeCand) bool { return strings.EqualFold(c.Name, token) }); ok {
		return id, true
	}
	if id, ok := uniq(func(c nodeCand) bool { return strings.HasPrefix(strings.ToLower(c.ID), lt) }); ok {
		return id, true
	}
	if id, ok := uniq(func(c nodeCand) bool { return strings.Contains(strings.ToLower(c.Name), lt) }); ok {
		return id, true
	}
	return "", false
}

// Pair initiates pairing with a discovered node id|name or a direct host:port, then blocks (bounded)
// until the handshake reaches the SAS prompt, printing the code. Same dial path as the UI's Connect.
func (c *appControl) Pair(target string) string {
	if c.peerMgr == nil {
		return "peer link unavailable\n"
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "usage: pair <nodeID|name|host:port>\n"
	}
	var wantNode, wantAddr string
	switch {
	case isHostPort(target):
		wantAddr = target
		c.peerMgr.ConnectAddr(target) // manual/seed dial - trust + SAS flow identical to a discovered dial
	case c.disc != nil:
		p, ok := resolveDiscovered(c.disc.Peers(), target)
		if !ok {
			return fmt.Sprintf("no discovered peer matches %q - run `ctl peers`, or pass a host:port\n", target)
		}
		wantNode = p.NodeID
		c.peerMgr.Connect(p)
	default:
		return fmt.Sprintf("discovery unavailable - pass a host:port to pair with %q\n", target)
	}
	// Bounded wait for the SAS to appear (or an already-trusted silent reconnect).
	deadline := time.Now().Add(pairWait)
	for {
		for _, cn := range c.peerMgr.Connections() {
			if (wantNode != "" && cn.NodeID == wantNode) || (wantAddr != "" && cn.Address == wantAddr) {
				switch cn.Status {
				case peerlink.StatusAwaitSAS:
					if cn.SAS != "" {
						who := "you initiated"
						if cn.Inbound {
							who = "peer initiated"
						}
						return fmt.Sprintf("SAS %s with %s (%s)\n"+
							"compare it on BOTH screens, then: ctl pair-confirm %s yes\n",
							cn.SAS, peerLabel(cn.NodeID, cn.Nickname), who, cn.NodeID)
					}
				case peerlink.StatusConnected:
					return fmt.Sprintf("connected to %s (already paired - no SAS needed)\n", peerLabel(cn.NodeID, cn.Nickname))
				}
			}
		}
		if !time.Now().Before(deadline) {
			return "pairing pending (handshake not yet at the SAS step) - run `ctl peers` for the code, then `ctl pair-confirm <nodeID> yes`\n"
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// PairConfirm records the local human's SAS verdict. Same function the UI's confirm button calls;
// pairing still completes only when the OTHER end also confirms. Never auto-accepts.
func (c *appControl) PairConfirm(nodeID string, accept bool) string {
	if c.peerMgr == nil {
		return "peer link unavailable"
	}
	conns := c.peerMgr.Connections()
	cands := make([]nodeCand, 0, len(conns))
	for _, cn := range conns {
		cands = append(cands, nodeCand{ID: cn.NodeID, Name: cn.Nickname})
	}
	id, ok := matchNodeID(cands, nodeID)
	if !ok {
		return fmt.Sprintf("no active pairing matches %q (run `ctl peers`)", nodeID)
	}
	c.peerMgr.ConfirmSAS(id, accept)
	if accept {
		return "confirmed - pairing completes once the OTHER end also confirms (both ends must)"
	}
	return "rejected - pairing aborted"
}

// Unpair forgets a remembered peer (and drops any live link). Resolves a node id|name token.
func (c *appControl) Unpair(nodeID string) string {
	if c.peerMgr == nil {
		return "peer link unavailable"
	}
	cands := map[string]nodeCand{}
	for _, p := range c.peerMgr.Remembered() {
		cands[p.NodeID] = nodeCand{ID: p.NodeID, Name: p.Nickname}
	}
	for _, cn := range c.peerMgr.Connections() {
		if _, ok := cands[cn.NodeID]; !ok {
			cands[cn.NodeID] = nodeCand{ID: cn.NodeID, Name: cn.Nickname}
		}
	}
	list := make([]nodeCand, 0, len(cands))
	for _, v := range cands {
		list = append(list, v)
	}
	id, ok := matchNodeID(list, nodeID)
	if !ok {
		return fmt.Sprintf("no remembered peer matches %q (run `ctl peers`)", nodeID)
	}
	c.peerMgr.Forget(id)
	return "forgotten " + id
}

// peerLabel formats "nick (shortID)" or just the short id.
func peerLabel(nodeID, nick string) string {
	s := peerShort(nodeID)
	if nick != "" {
		return nick + " (" + s + ")"
	}
	return s
}

// peerShort abbreviates a node id for human-facing lines (full ids stay in `ctl peers`).
func peerShort(nodeID string) string {
	if len(nodeID) > 12 {
		return nodeID[:12] + "…"
	}
	return nodeID
}

// ── media sources + routes ────────────────────────────────────────────────────

// codecName maps a medialink.Codec to a short label for ctl output.
func codecName(c medialink.Codec) string {
	switch c {
	case medialink.CodecNone:
		return "none"
	case medialink.CodecBGRA:
		return "bgra"
	case medialink.CodecNRGBA:
		return "rgba"
	case medialink.CodecJPEG:
		return "mjpeg"
	case medialink.CodecH264:
		return "h264"
	case medialink.CodecHEVC:
		return "hevc"
	case medialink.CodecAV1:
		return "av1"
	case medialink.CodecPCMS16, medialink.CodecPCMS24, medialink.CodecPCMF32:
		return "pcm"
	case medialink.CodecOpus:
		return "opus"
	}
	return fmt.Sprintf("codec%d", c)
}

// srcGeom formats a source descriptor's geometry + native codec.
func srcGeom(s medialink.SourceDesc) string {
	if s.Width > 0 && s.Height > 0 {
		return fmt.Sprintf("%dx%d@%.0f %s", s.Width, s.Height, s.FPS, codecName(s.Codec))
	}
	return codecName(s.Codec)
}

// MediaSources lists local advertised sources + remote source adverts (per peer).
func (c *appControl) MediaSources() string {
	if c.mediaCtl == nil && c.mediaRoutes == nil {
		return "media plane unavailable\n"
	}
	var b strings.Builder
	b.WriteString("local sources (advertised to connected peers):\n")
	var local []medialink.SourceDesc
	if c.mediaCtl != nil {
		local = c.mediaCtl.LocalSources()
	}
	if len(local) == 0 {
		b.WriteString("  (none - the media child runs only while a peer is connected; or no capturable source yet)\n")
	} else {
		for _, s := range local {
			fmt.Fprintf(&b, "  %s\t%s\t%s\n", s.ID, s.Name, srcGeom(s))
		}
	}
	b.WriteString("remote sources (adverts received, per peer):\n")
	var remote []mediaroute.RemoteSource
	if c.mediaRoutes != nil {
		remote = c.mediaRoutes.RemoteVideoSources()
	}
	if len(remote) == 0 {
		b.WriteString("  (none)\n")
	} else {
		for _, r := range remote {
			fmt.Fprintf(&b, "  %s\t%s\t%s\t%s\n", peerShort(r.Peer), r.Desc.ID, r.Desc.Name, srcGeom(r.Desc))
		}
	}
	return b.String()
}

// formatRoutes renders the route telemetry as a focused human-readable view (reuses RouteStat, the
// same JSON `ctl perf` exposes). One block per route.
// writeStage prints one pipeline stage's inter-frame interval distribution (ms) if it saw frames.
func writeStage(b *strings.Builder, name string, s medialink.StageSnapshot) {
	if s.Count == 0 {
		return
	}
	fmt.Fprintf(b, "    stage %s ifi(ms) p50=%.1f p99=%.1f max=%.1f mean=%.1f  >25/50/100=%d/%d/%d  n=%d\n",
		name, s.P50Ms, s.P99Ms, s.MaxMs, s.MeanMs, s.Over25, s.Over50, s.Over100, s.Count)
}

func formatRoutes(stats []medialink.RouteStat) string {
	if len(stats) == 0 {
		return "no active routes\n"
	}
	var b strings.Builder
	for _, s := range stats {
		transport := s.Transport
		if transport == "" {
			transport = "tcp"
		}
		enc := "plaintext"
		if s.Encrypted {
			enc = "encrypted"
		}
		impl := s.TransportImpl
		if impl == "" {
			impl = "go"
		}
		line1 := fmt.Sprintf("%s  peer=%s  stream=%d  %s/%s  impl=%s",
			s.Direction, peerShort(s.Peer), s.Stream, transport, enc, impl)
		if transport == medialink.TransportTCP && s.TCPReason != "" { // why not UDP (brief item 2)
			line1 += "  (" + s.TCPReason + ")"
		}
		fmt.Fprintln(&b, line1)
		// Codec + engine.
		engine := s.Encoder
		if engine == "" {
			engine = "raw/echo"
		}
		eng := engine
		if s.Pipe != nil && s.Pipe.Encoder != "" {
			eng = s.Pipe.Encoder
			if s.Pipe.HWAccel != "" {
				eng += " hwaccel=" + s.Pipe.HWAccel
			}
		}
		sw := ""
		if s.Software {
			sw = " (SOFTWARE - CPU)"
		}
		fmt.Fprintf(&b, "    codec=%s tier=%d engine=%s%s\n", engine, s.Tier, eng, sw)
		// Geometry (coded vs nominal) + fps in/out.
		if s.Pipe != nil {
			fmt.Fprintf(&b, "    fps wire=%.0f out=%.0f  dropped=%d (rate-capped=%d) restarts=%d\n",
				s.WireFPS, s.Pipe.OutFPS, s.Pipe.Dropped, s.Pipe.RateCapped, s.Pipe.Restarts)
		} else {
			fmt.Fprintf(&b, "    fps wire=%.0f\n", s.WireFPS)
		}
		// Bitrate.
		fmt.Fprintf(&b, "    bitrate=%.0f kbps  frames=%d bytes=%d keyframes=%d\n",
			s.RateBps/1000, s.Frames, s.Bytes, s.Keyframes)
		// Adaptive budget, recv-governor view (item 5): mode + live target + model + binding reason.
		if s.BudgetMode != "" && s.BudgetTargetKbps > 0 {
			line := fmt.Sprintf("    budget mode=%s target=%d kbps model=%d kbps  reason=%s",
				s.BudgetMode, s.BudgetTargetKbps, s.BudgetModelKbps, s.BudgetReason)
			if s.BudgetLinkKbps > 0 {
				line += fmt.Sprintf("  linkCeil=%d kbps", s.BudgetLinkKbps)
			}
			b.WriteString(line + "\n")
		}
		// Adaptive budget, encoder (sender) view: mode + ceiling + quality feedback (best value per system).
		if s.Pipe != nil && (s.Pipe.BudgetKbps > 0 || s.Pipe.QualityTarget > 0) {
			mode := "fixed"
			if s.Pipe.QualityTarget > 0 {
				mode = fmt.Sprintf("auto (quality q%d, bitrate is the ceiling)", s.Pipe.QualityTarget)
			}
			line := fmt.Sprintf("    budget mode=%s ceil=%d kbps", mode, s.Pipe.BudgetKbps)
			if fb := s.Pipe.Feedback; fb.Present {
				line += fmt.Sprintf("  encoder qp=%.0f", fb.AvgQP)
				if fb.RateLimited {
					line += " RATE-LIMITED (content wants more than the ceiling)"
				}
			}
			b.WriteString(line + "\n")
		}
		// Loss / recovery.
		if s.SeqGaps > 0 || s.LostEst > 0 || s.Recovered > 0 || s.NACKsSent > 0 || s.NACKsRecv > 0 || s.Retransmits > 0 {
			fmt.Fprintf(&b, "    seqGaps=%d lostEst=%d recovered=%d nacksSent=%d nacksRecv=%d retransmits=%d pli=%d\n",
				s.SeqGaps, s.LostEst, s.Recovered, s.NACKsSent, s.NACKsRecv, s.Retransmits, s.PLIRequests)
		}
		// UDP transport telemetry.
		if u := s.UDP; u != nil {
			fmt.Fprintf(&b, "    udp loss=%.1f%% rtt=%.1fms owd=%.1fms jitter=%.1fms nack(s/r)=%d/%d retrans=%d reasmDrops=%d pacer(depth/drops)=%d/%d\n",
				u.LossPct, float64(u.RTTns)/1e6, float64(u.OwdNs)/1e6, float64(u.OwdJitterNs)/1e6,
				u.NacksSent, u.NacksRecv, u.Retransmits, u.ReasmDrops, u.PacerDepth, u.PacerDrops)
		}
		// Native receive path (socket -> playout -> decode -> publish inside the decode child).
		if n := s.NativeRX; n != nil {
			fmt.Fprintf(&b, "    native rx delivered=%d published=%d keyWaits=%d nack(recovered/expired)=%d/%d gapDrops=%d rebinds=%d decErrors=%d rxErrors=%d\n",
				n.Delivered, n.Published, n.KeyWaits, n.NackRecovered, n.NackExpired, n.GapDrops, n.Rebinds, n.DecErrors, n.RxErrors)
		}
		// Jitter buffer (recv video).
		if jb := s.JB; jb != nil {
			fmt.Fprintf(&b, "    jitterbuf depth=%d buffered=%d lateRate=%.2f policyDrops=%d\n",
				jb.Depth, jb.Buffered, jb.LateRate, jb.PolicyDrops)
			fmt.Fprintf(&b, "    playout preset=%s delay=%.1fms target=%.1fms max=%.0fms misses10m=%d repeated10m=%d grows=%d decays=%d rebases=%d\n",
				jb.Preset, jb.PlayoutMs, jb.TargetMs, jb.MaxMs, jb.Misses10m, jb.Repeats10m, jb.Grows, jb.Decays, jb.Rebases)
		}
		// STEP-0 per-stage inter-frame interval histograms (hang localization).
		if st := s.Stages; st != nil {
			writeStage(&b, "net", st.NetArrive)  // first bytes off the socket (TCP)
			writeStage(&b, "rx ", st.RxComplete) // transport output
			writeStage(&b, "jb ", st.JBRelease)  // jitter release
			writeStage(&b, "dec", st.DecSubmit)  // decode submit (backpressure)
			writeStage(&b, "enc", st.EncDone)    // encoded AU available (send)
			writeStage(&b, "tx ", st.TxQueue)    // packetized + queued (send)
			writeStage(&b, "pub", st.Publish)    // decoded frame published (native receive, in-child)
		}
	}
	return b.String()
}

// Routes renders active routes both directions (ctl routes).
func (c *appControl) Routes() string {
	if c.mediaCtl == nil {
		return "media plane unavailable\n"
	}
	return formatRoutes(c.mediaCtl.Stats())
}

// MediaPerf reports the media-plane process's Go GC/scheduler pauses (STEP-0: whether the Go runtime
// is the source of frame-path micro-hangs). In child mode this is the media child's runtime.
func (c *appControl) MediaPerf() string {
	if c.mediaCtl == nil {
		return "media plane unavailable\n"
	}
	p := c.mediaCtl.ProcRuntime()
	var b strings.Builder
	b.WriteString("media-plane Go runtime pauses (delta since last sample):\n")
	writePause(&b, "gc stop-the-world", p.GCPause)
	writePause(&b, "sched latency    ", p.SchedLat)
	return b.String()
}

// writePause prints one runtime pause distribution (ms).
func writePause(b *strings.Builder, name string, p medialink.PauseDist) {
	fmt.Fprintf(b, "  %s  p50=%.3f p99=%.3f max=%.3f  >1/5/10ms=%d/%d/%d  n=%d\n",
		name, p.P50Ms, p.P99Ms, p.MaxMs, p.Over1ms, p.Over5ms, p.Over10ms, p.Count)
}

// resolveRemoteSource resolves (peer token, source token) to a (peerNodeID, sourceID) among the
// remote adverts. source matches a Desc.Name (case-insensitive) or an exact Desc.ID.
func resolveRemoteSource(srcs []mediaroute.RemoteSource, peerTok, srcTok string) (peerID, sourceID string, err error) {
	peerTok, srcTok = strings.TrimSpace(peerTok), strings.TrimSpace(srcTok)
	// Resolve peer against the advertising nodes.
	cands := map[string]nodeCand{}
	for _, s := range srcs {
		cands[s.Peer] = nodeCand{ID: s.Peer, Name: s.Peer}
	}
	list := make([]nodeCand, 0, len(cands))
	for _, v := range cands {
		list = append(list, v)
	}
	pid, ok := matchNodeID(list, peerTok)
	if !ok {
		return "", "", fmt.Errorf("no peer advertising sources matches %q (run `ctl media-sources`)", peerTok)
	}
	// Resolve the source within that peer.
	var hits []medialink.SourceDesc
	for _, s := range srcs {
		if s.Peer != pid {
			continue
		}
		if s.Desc.ID == srcTok || strings.EqualFold(s.Desc.Name, srcTok) {
			hits = append(hits, s.Desc)
		}
	}
	if len(hits) == 0 {
		// Fall back to a substring match on the name.
		lt := strings.ToLower(srcTok)
		for _, s := range srcs {
			if s.Peer == pid && strings.Contains(strings.ToLower(s.Desc.Name), lt) {
				hits = append(hits, s.Desc)
			}
		}
	}
	if len(hits) == 0 {
		return "", "", fmt.Errorf("peer %s advertises no source %q", peerShort(pid), srcTok)
	}
	if len(hits) > 1 {
		return "", "", fmt.Errorf("source %q is ambiguous on %s (%d matches) - use the exact source id", srcTok, peerShort(pid), len(hits))
	}
	return pid, hits[0].ID, nil
}

// RouteStart requests a peer's source into a local sender (receiving side; same path as the UI).
func (c *appControl) RouteStart(peer, source string) string {
	if c.mediaRoutes == nil {
		return "media plane unavailable"
	}
	source, transport := cutTransportFlag(source)
	pid, sourceID, err := resolveRemoteSource(c.mediaRoutes.RemoteVideoSources(), peer, source)
	if err != nil {
		return "error: " + err.Error()
	}
	if transport != "" {
		tov, ok := c.mediaRoutes.(mediaroute.TransportOverrider)
		if !ok {
			return "error: per-route transport override unsupported by this media plane"
		}
		if err := tov.SetTransportOverride(pid, sourceID, transport); err != nil {
			return "error: " + err.Error()
		}
	}
	sess, err := c.mediaRoutes.StartReceive(pid, sourceID)
	if err != nil {
		return "error: " + err.Error()
	}
	out := fmt.Sprintf("route started: peer=%s source=%s session=%s", peerShort(pid), sourceID, sess)
	if transport != "" {
		out += " transport-override=" + transport + " (applies to new offers; a live route keeps its transport - route-stop first)"
	}
	return out
}

// cutTransportFlag strips a "--transport <udp|tcp|auto>" token pair from a route-start source arg.
func cutTransportFlag(s string) (rest, transport string) {
	f := strings.Fields(s)
	keep := f[:0]
	for i := 0; i < len(f); i++ {
		if f[i] == "--transport" && i+1 < len(f) {
			transport = strings.ToLower(f[i+1])
			i++
			continue
		}
		if v, ok := strings.CutPrefix(f[i], "--transport="); ok {
			transport = strings.ToLower(v)
			continue
		}
		keep = append(keep, f[i])
	}
	return strings.Join(keep, " "), transport
}

// RouteStop stops a receive route by source name, or every route from a peer with "all".
func (c *appControl) RouteStop(peer, source string) string {
	if c.mediaRoutes == nil {
		return "media plane unavailable"
	}
	recv := c.mediaRoutes.Receives()
	// Resolve the peer against the receiving routes' peers.
	cands := map[string]nodeCand{}
	for _, r := range recv {
		cands[r.Peer] = nodeCand{ID: r.Peer, Name: r.Peer}
	}
	list := make([]nodeCand, 0, len(cands))
	for _, v := range cands {
		list = append(list, v)
	}
	pid, ok := matchNodeID(list, peer)
	if !ok {
		return fmt.Sprintf("no active route from a peer matching %q (run `ctl routes`)", peer)
	}
	all := strings.EqualFold(strings.TrimSpace(source), "all")
	var stopped []string
	for _, r := range recv {
		if r.Peer != pid {
			continue
		}
		if all || strings.EqualFold(r.Name, source) || strings.Contains(strings.ToLower(r.Name), strings.ToLower(source)) {
			c.mediaRoutes.StopReceive(r.Session)
			stopped = append(stopped, r.Name)
		}
	}
	if len(stopped) == 0 {
		return fmt.Sprintf("no matching route from %s for %q", peerShort(pid), source)
	}
	return fmt.Sprintf("stopped %d route(s) from %s: %s", len(stopped), peerShort(pid), strings.Join(stopped, ", "))
}
