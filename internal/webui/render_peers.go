package webui

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"rave.page/mate/internal/filexfer"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/peerbridge"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/webcam"
)

// renderPeers: LAN peers at Fyne parity - control banner (MIDI forwarding) + per-connection Control
// toggle + bridged now-playing, media plane (clock/sync/TC-master + per-route pipeline telemetry),
// webcam panel (device/mode/start-stop/PTZ), file transfer (settings + progress), discovered +
// remembered peers, status-strip counts. peers-body is patched ~1 Hz (peers_actions.go).
func (u *UI) renderPeers() string {
	if u.svc.Peers == nil {
		return panel(i18n.T("peers.title"), "") + emptyState(i18n.T("peers.unavailable"))
	}
	return panel(i18n.T("peers.title"), i18n.T("peers.subtitle")) +
		`<div id=peers-body>` + u.peersBody() + `</div>`
}

func (u *UI) peersBody() string {
	conns := u.peerConns()
	byNode := map[string]peerlink.ConnInfo{}
	for _, c := range conns {
		byNode[c.NodeID] = c
	}
	remotes := map[string]peerbridge.RemoteState{}
	if u.svc.PeerBridge != nil {
		for _, s := range u.svc.PeerBridge.RemoteStates() {
			remotes[s.NodeID] = s
		}
	}
	resolve := func(id string) string {
		if c, ok := byNode[id]; ok {
			return peerName(c.Nickname, id)
		}
		return peerName("", id)
	}

	var b strings.Builder
	b.WriteString(`<div id=peers-strip class=peers-strip>` + u.peerStripHTML(conns) + `</div>`)
	b.WriteString(u.controlBannerHTML(byNode))
	b.WriteString(section(i18n.T("peers.connections"), u.peerConnsHTML(conns, remotes)))
	if u.svc.Media != nil && (len(conns) > 0 || len(u.svc.Media.Stats()) > 0) {
		b.WriteString(section(i18n.T("peers.mediaPlane"), u.peerMediaHTML(resolve)))
	}
	if camHTML := u.peerWebcamHTML(resolve); camHTML != "" {
		b.WriteString(section(i18n.T("peers.webcam"), camHTML))
	}
	if u.svc.FileXfer != nil {
		b.WriteString(section(i18n.T("peers.fileTransfer"), u.peerXferHTML(resolve)))
	}
	// two sibling lists share a row ≥1100px (.peers-2col)
	b.WriteString(`<div class=peers-2col>` + section(i18n.T("peers.onThisNetwork"), u.peerDiscoveredHTML(byNode)) +
		section(i18n.T("peers.rememberedOffline"), u.peerRememberedHTML(byNode)) + `</div>`)
	return b.String()
}

func (u *UI) peerConns() []peerlink.ConnInfo {
	if u.svc.Peers == nil {
		return nil
	}
	return u.svc.Peers.Connections()
}

// ── status strip ──

func (u *UI) peerStripHTML(conns []peerlink.ConnInfo) string {
	nFound := 0
	if u.svc.Discovery != nil {
		nFound = len(u.svc.Discovery.Peers())
	}
	var txt string
	if u.svc.Identity != nil {
		txt = i18n.T("peers.statusStripNode", i18n.A{
			"connected": fmt.Sprint(len(conns)),
			"found":     fmt.Sprint(nFound),
			"node":      shortID(u.svc.Identity.NodeID),
		})
	} else {
		txt = i18n.T("peers.statusStrip", i18n.A{
			"connected": fmt.Sprint(len(conns)),
			"found":     fmt.Sprint(nFound),
		})
	}
	return `<span data-label="peer counts" data-value="` + html.EscapeString(txt) + `">` + html.EscapeString(txt) + `</span>`
}

// ── control banner (MIDI forwarding) ──

func (u *UI) controlBannerHTML(byNode map[string]peerlink.ConnInfo) string {
	if u.svc.PeerBridge == nil {
		return ""
	}
	on, target := u.svc.PeerBridge.Forwarding()
	if !on {
		return ""
	}
	if _, live := byNode[target]; !live { // target dropped - auto-clear
		u.svc.PeerBridge.SetMIDIForwarding(false)
		u.svc.PeerBridge.SetControlTarget("")
		return ""
	}
	name := peerName(byNode[target].Nickname, target)
	return `<div class=ctl-banner data-label="controlling"><span class=ctl-banner-tx>🎛 ` +
		html.EscapeString(i18n.T("peers.controllingBanner", i18n.A{"name": name})) + `</span>` +
		btn(i18n.T("peers.stopControlling"), "warn", "peers-control:"+target, "0") + `</div>`
}

// ── connections ──

func (u *UI) peerConnsHTML(conns []peerlink.ConnInfo, remotes map[string]peerbridge.RemoteState) string {
	if len(conns) == 0 {
		return emptyState(i18n.T("peers.noActiveConnections"))
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	on, target := false, ""
	if u.svc.PeerBridge != nil {
		on, target = u.svc.PeerBridge.Forwarding()
	}
	for _, c := range conns {
		st := string(c.Status)
		if c.Status == peerlink.StatusAwaitSAS {
			b.WriteString(`<div class=row><span class=row-label>` + html.EscapeString(peerName(c.Nickname, c.NodeID)) +
				` <span class=np-artist>` + html.EscapeString(i18n.T("peers.pairingCode", i18n.A{"sas": spaceSAS(c.SAS)})) + `</span></span>` +
				btnRow(btn(i18n.T("peers.matches"), "go", "peer-sas:"+c.NodeID, "1"), btn(i18n.T("peers.doesntMatch"), "destructive", "peer-sas:"+c.NodeID, "0")) + `</div>`)
			continue
		}
		v := "muted"
		switch c.Status {
		case peerlink.StatusConnected:
			v = "success"
			if c.Trusted {
				st = i18n.T("peers.connectedPaired")
			}
		case peerlink.StatusConnecting:
			v = "warning"
		}
		// Control toggle (only for connected peers + a live bridge).
		var actions string
		if c.Status == peerlink.StatusConnected && u.svc.PeerBridge != nil {
			if on && target == c.NodeID {
				actions = btn(i18n.T("peers.stopControl"), "warn", "peers-control:"+c.NodeID, "0")
			} else {
				actions = btn(i18n.T("peers.control"), "outline", "peers-control:"+c.NodeID, "1")
			}
		}
		actions += btn(i18n.T("peers.forget"), "ghost", "peer-forget:"+c.NodeID, "")
		b.WriteString(`<div class=row><span class=row-label>` + dot(v) + ` ` + html.EscapeString(peerName(c.Nickname, c.NodeID)) +
			` <span class=np-artist>` + html.EscapeString(st) + `</span></span>` + btnRow(actions) + `</div>`)
		for _, ds := range remotes[c.NodeID].NowPlaying.AllDecks() {
			line := fmtRemoteDeck(ds)
			if line == "" {
				continue
			}
			mark, cls := "▷ ", "peer-np peer-np--quiet"
			if ds.Audible {
				mark, cls = "▶ ", "peer-np"
			}
			b.WriteString(`<div class="` + cls + `">` + mark + html.EscapeString(line) + `</div>`)
		}
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── discovered / remembered ──

func (u *UI) peerDiscoveredHTML(byNode map[string]peerlink.ConnInfo) string {
	if u.svc.Discovery == nil {
		return emptyState(i18n.T("peers.discoveryOff"))
	}
	var rows strings.Builder
	for _, p := range u.svc.Discovery.Peers() {
		if _, busy := byNode[p.NodeID]; busy {
			continue
		}
		verb, variant := i18n.T("peers.pair"), "primary"
		if u.svc.Peers.IsTrusted(p.NodeID) {
			verb, variant = i18n.T("peers.connect"), "outline"
		}
		rows.WriteString(`<div class=row><span class=row-label>` + html.EscapeString(peerName(p.Name, p.NodeID)) +
			` <span class=np-artist>` + html.EscapeString(p.Address.String()) + `</span></span>` +
			btnRow(btn(verb, variant, "peer-connect:"+p.NodeID, "")) + `</div>`)
	}
	if rows.Len() == 0 {
		return emptyState(i18n.T("peers.searchingHint"))
	}
	return `<div class="rp-card">` + rows.String() + `</div>`
}

func (u *UI) peerRememberedHTML(byNode map[string]peerlink.ConnInfo) string {
	online := map[string]bool{}
	for id := range byNode {
		online[id] = true
	}
	if u.svc.Discovery != nil {
		for _, p := range u.svc.Discovery.Peers() {
			online[p.NodeID] = true
		}
	}
	var rows strings.Builder
	for _, p := range u.svc.Peers.Remembered() {
		if online[p.NodeID] {
			continue
		}
		rows.WriteString(`<div class=row><span class=row-label>` + dot("muted") + ` ` + html.EscapeString(peerName(p.Nickname, p.NodeID)) +
			` <span class=np-artist>` + html.EscapeString(i18n.T("peers.offline")) + `</span></span>` + btnRow(btn(i18n.T("peers.forget"), "ghost", "peer-forget:"+p.NodeID, "")) + `</div>`)
	}
	if rows.Len() == 0 {
		return emptyState(i18n.T("peers.none"))
	}
	return `<div class="rp-card">` + rows.String() + `</div>`
}

// ── media plane (clock / sync / TC master / per-route telemetry) ──

func (u *UI) peerMediaHTML(resolve func(string) string) string {
	m := u.svc.Media
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)

	// Clock: active tier/lock/offset + per-peer sync estimates.
	b.WriteString(`<div class=media-clock>` + html.EscapeString(fmtClockLine(m.ClockQuality())) + `</div>`)
	syncs := m.SyncStats()
	sort.Slice(syncs, func(i, j int) bool { return syncs[i].Peer < syncs[j].Peer })
	for _, s := range syncs {
		b.WriteString(`<div class=media-sub>` + html.EscapeString(fmtSyncLine(s, resolve(s.Peer))) + `</div>`)
	}

	// Timecode master state.
	if p := u.svc.TCPlane; p != nil {
		b.WriteString(`<div class=media-clock>` + html.EscapeString(fmtTCLine(p.Status(), resolve)) + `</div>`)
	}

	// Routes.
	stats := m.Stats()
	if len(stats) == 0 {
		b.WriteString(`<div class=np-artist>` + html.EscapeString(i18n.T("peers.noActiveMediaRoutes")) + `</div>`)
	} else {
		sort.Slice(stats, func(i, j int) bool { return stats[i].Session < stats[j].Session })
		b.WriteString(`<div class=media-sub>` + html.EscapeString(i18n.T("peers.routes", i18n.A{"count": fmt.Sprint(len(stats))})) + `</div>`)
		for _, s := range stats {
			title, detail := fmtRouteStat(s, resolve)
			b.WriteString(`<div class=media-route>` + html.EscapeString(title) + `</div>`)
			b.WriteString(`<div class=media-sub>` + html.EscapeString(detail) + `</div>`)
			if pl := fmtPipeLine(s); pl != "" {
				b.WriteString(`<div class=media-sub>` + html.EscapeString(pl) + `</div>`)
			}
		}
	}
	b.WriteString(`</div>`)

	// Receivable remote sources + active receives (P4).
	if recv := u.peerMediaReceiveHTML(resolve); recv != "" {
		b.WriteString(recv)
	}
	return b.String()
}

func (u *UI) peerMediaReceiveHTML(resolve func(string) string) string {
	mr := u.svc.MediaRoutes
	if mr == nil {
		return ""
	}
	srcs := mr.RemoteVideoSources()
	recvs := mr.Receives()
	if len(srcs) == 0 && len(recvs) == 0 {
		return ""
	}
	receiving := map[string]bool{}
	var b strings.Builder
	b.WriteString(`<div class=media-recv-head>` + html.EscapeString(i18n.T("peers.receiveVideo")) + `</div><div class="rp-card">`)
	for _, r := range recvs {
		receiving[r.Peer+"\x00"+r.Name] = true
		line := i18n.T("peers.receiveVideoLine", i18n.A{"name": r.Name, "peer": resolve(r.Peer)})
		b.WriteString(`<div class=row><span class=row-label>◂ ` +
			html.EscapeString(line) +
			`</span>` + btnRow(btn(i18n.T("player.stop"), "destructive", "media-stop:"+r.Session, "")) + `</div>`)
	}
	for _, s := range srcs {
		if receiving[s.Peer+"\x00"+s.Desc.Name] {
			continue
		}
		b.WriteString(`<div class=row><span class=row-label>` + html.EscapeString(fmtRemoteSource(s, resolve)) + `</span>` +
			btnRow(btn(i18n.T("peers.receive"), "go", "media-recv:"+s.Peer+"\x1f"+s.Desc.ID, "")) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── webcam (device / mode / start-stop / PTZ) ──

func (u *UI) peerWebcamHTML(resolve func(string) string) string {
	w := u.svc.Webcam
	if w == nil || u.svc.Cfg == nil {
		return ""
	}
	if !u.svc.Cfg.Features.Webcam.Enabled {
		return hint("info", i18n.T("peers.webcamOff"))
	}
	insts := w.Instances()
	if len(insts) == 0 {
		return emptyState(i18n.T("peers.noCameraInstances"))
	}
	var b strings.Builder
	for _, in := range insts {
		b.WriteString(u.camNodeHTML(in, resolve))
	}
	return b.String()
}

func (u *UI) camNodeHTML(in webcam.Instance, resolve func(string) string) string {
	id := in.ID // owning node id = Cmd.Target (in.Node is publisher, used only for display)
	name := i18n.T("peers.thisInstance")
	if !in.Local {
		name = resolve(in.Node)
		if in.Label != "" {
			name = i18n.T("peers.pairedInstanceLabel", i18n.A{"label": in.Label})
		}
	}
	selDev := camPendingDevice(id, in.Device)
	// Device options.
	devOpts := [][2]string{}
	have := false
	for _, d := range in.Devices {
		devOpts = append(devOpts, [2]string{d.Name, d.Name})
		if d.Name == selDev {
			have = true
		}
	}
	if selDev != "" && !have {
		devOpts = append([][2]string{{selDev, selDev}}, devOpts...)
	}
	if len(devOpts) == 0 {
		devOpts = [][2]string{{"", i18n.T("peers.selectCameraPlaceholder")}}
	}
	// Mode options for the selected device.
	var modes []webcam.Mode
	for _, d := range in.Devices {
		if d.Name == selDev {
			modes = d.Modes
		}
	}
	modeStrs := camModeStrings(modes)
	selMode := camPendingMode(id)
	if selMode == "" {
		if in.Running {
			selMode = fmtCamMode(in.W, in.H, in.FPS)
		} else if len(modeStrs) > 0 {
			selMode = modeStrs[0]
		}
	}
	modeOpts := [][2]string{}
	for _, s := range modeStrs {
		modeOpts = append(modeOpts, [2]string{s, s})
	}
	if len(modeOpts) == 0 {
		lbl := selMode
		if lbl == "" {
			lbl = i18n.T("peers.sizeFpsPlaceholder")
		}
		modeOpts = [][2]string{{selMode, lbl}}
	}

	startLbl, startVal, startVar := i18n.T("peers.start"), "start", "go"
	if in.Running {
		startLbl, startVal, startVar = i18n.T("player.stop"), "stop", "warn"
	}

	var b strings.Builder
	b.WriteString(`<div class="rp-card cam-node">`)
	b.WriteString(`<div class=cam-head><span class=cam-title>` + html.EscapeString(name) + `</span>` +
		btn("↻", "ghost", "peers-cam-refresh:"+id, "") + `</div>`)
	b.WriteString(`<div class=cam-status>` + html.EscapeString(fmtCamStatus(in.Status)) + `</div>`)
	b.WriteString(`<div class=cam-ctls>` +
		selectBox(i18n.T("peers.device"), "peers-cam-device:"+id, devOpts, selDev) +
		selectBox(i18n.T("peers.mode"), "peers-cam-mode:"+id, modeOpts, selMode) +
		btn(startLbl, startVar, "peers-cam-start:"+id, startVal) + `</div>`)
	if in.Sender != "" {
		b.WriteString(`<div class=cam-sender data-label="spout sender" data-value="` + html.EscapeString(in.Sender) + `">` +
			html.EscapeString(i18n.T("peers.spoutSender", i18n.A{"sender": in.Sender})) + `</div>`)
	}
	if len(in.Props) > 0 {
		b.WriteString(`<div class=cam-props-h>` + html.EscapeString(i18n.T("peers.lensImage")) + `</div>`)
		for _, p := range in.Props {
			b.WriteString(camPropRowHTML(id, p))
		}
	}
	b.WriteString(`</div>`)
	return b.String()
}

// camPropRowHTML renders one UVC property: label + range + live value + optional auto checkbox.
func camPropRowHTML(node string, p webcam.PropState) string {
	step := int32(1)
	if p.Step > 0 {
		step = p.Step
	}
	dis := ""
	if p.Auto {
		dis = " disabled"
	}
	act := "peers-cam-prop:" + node + "\x1f" + p.ID
	oninput := `oninput="var v=this.parentNode.querySelector('.cam-prop-v');if(v)v.textContent=this.value"`
	var b strings.Builder
	b.WriteString(`<div class=cam-prop><span class=cam-prop-l>` + html.EscapeString(p.Label) + `</span>`)
	fmt.Fprintf(&b, `<input class="slider-input cam-prop-s" type=range min=%d max=%d step=%d value=%d data-act=%s data-value=%d%s %s>`,
		p.Min, p.Max, step, p.Value, attrQ(act), p.Value, dis, oninput)
	b.WriteString(`<span class=cam-prop-v>` + fmt.Sprintf("%d", p.Value) + `</span>`)
	if p.CanAuto {
		checked := ""
		if p.Auto {
			checked = " checked"
		}
		fmt.Fprintf(&b, `<label class=cam-prop-auto><input type=checkbox%s data-act=%s data-value=%s>%s</label>`,
			checked, attrQ("peers-cam-auto:"+node+"\x1f"+p.ID), attrQ(boolStr(p.Auto)), html.EscapeString(i18n.T("peers.auto")))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── file transfer (settings row + per-transfer progress) ──

func (u *UI) peerXferHTML(resolve func(string) string) string {
	var b strings.Builder
	b.WriteString(u.xferSettingsHTML())
	tr := u.svc.FileXfer.Transfers()
	if len(tr) == 0 {
		b.WriteString(hint("info", i18n.T("peers.noTransfersYet")))
		return b.String()
	}
	b.WriteString(`<div class="rp-card">`)
	// Pending incoming accepts first - they need a decision.
	for _, t := range tr {
		if !t.Send && string(t.State) == "pending" {
			line := i18n.T("peers.xferIncoming", i18n.A{
				"name":  t.Name,
				"size":  humanBytes(uint64(t.Bytes)),
				"files": i18n.Tn("peers.xferFile", t.Files),
				"peer":  resolve(t.Peer),
			})
			b.WriteString(`<div class=row><span class=row-label>` + html.EscapeString(line) + `</span>` +
				btnRow(btn(i18n.T("peers.accept"), "go", "xfer-accept:"+t.ID, "1"), btn(i18n.T("peers.decline"), "ghost", "xfer-accept:"+t.ID, "0")) + `</div>`)
		}
	}
	for _, t := range tr {
		if !t.Send && string(t.State) == "pending" {
			continue
		}
		b.WriteString(u.xferRowHTML(t, resolve))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func (u *UI) xferSettingsHTML() string {
	if u.svc.Cfg == nil {
		return ""
	}
	f := u.svc.Cfg.Features.FileXfer
	mode := "ask"
	if f.AutoAccept() {
		mode = "auto"
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card cam-node">`)
	b.WriteString(toggleRow(i18n.T("peers.receiveFiles"), "peers-xfer-enabled", f.Enabled))
	b.WriteString(`<div class=xfer-mode><span class=field-label>` + html.EscapeString(i18n.T("peers.accept")) + `</span>` +
		subTabs("peers-xfer-mode:", mode, [2]string{"ask", i18n.T("peers.ask")}, [2]string{"auto", i18n.T("peers.autoMode")}) + `</div>`)
	b.WriteString(field(i18n.T("peers.saveTo"), "peers-xfer-dir", f.DownloadDir, "text"))
	b.WriteString(`<div class=np-artist>` + html.EscapeString(i18n.T("peers.defaultDir", i18n.A{"dir": f.ResolvedDownloadDir()})) + `</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

func (u *UI) xferRowHTML(t filexfer.Transfer, resolve func(string) string) string {
	arrow := "⇧"
	titleKey := "peers.xferTo"
	if !t.Send {
		arrow = "⇩"
		titleKey = "peers.xferFrom"
	}
	title := i18n.T(titleKey, i18n.A{"arrow": arrow, "name": t.Name, "peer": resolve(t.Peer)})
	st := string(t.State)
	var right, sub string
	switch st {
	case "active":
		frac := 0.0
		if t.Bytes > 0 {
			frac = float64(t.Done) / float64(t.Bytes)
		}
		cap := fmt.Sprintf("%s / %s · %s/s", humanBytes(uint64(t.Done)), humanBytes(uint64(t.Bytes)), humanBytes(uint64(t.Rate)))
		sub = progressBar(frac, cap)
		right = btn(i18n.T("common.cancel"), "ghost", "xfer-cancel:"+t.ID, "")
	case "waiting":
		sub = `<span class=np-artist>` + html.EscapeString(i18n.T("peers.waitingForPeer")) + `</span>`
		right = btn(i18n.T("common.cancel"), "ghost", "xfer-cancel:"+t.ID, "")
	case "stalled":
		msg := i18n.T("peers.interruptedRetrying")
		if t.Error != "" {
			msg = i18n.T("peers.interruptedWithError", i18n.A{"error": t.Error})
		}
		sub = `<span class=np-artist>` + html.EscapeString(msg) + `</span>`
		right = btn(i18n.T("common.cancel"), "ghost", "xfer-cancel:"+t.ID, "")
	case "done":
		sub = `<span class=np-artist>` + html.EscapeString(i18n.T("peers.xferDone", i18n.A{
			"files": i18n.Tn("peers.xferFile", t.Files),
			"size":  humanBytes(uint64(t.Bytes)),
		})) + `</span>`
		right = badge(i18n.T("peers.done"), "success")
	case "error":
		sub = `<span class=np-artist>` + html.EscapeString(i18n.T("peers.xferFailed", i18n.A{"error": t.Error})) + `</span>`
		right = badge(i18n.T("peers.error"), "error")
	default: // canceled
		sub = `<span class=np-artist>` + html.EscapeString(i18n.T("peers.canceled")) + `</span>`
		right = badge(st, "secondary")
	}
	return `<div class=xfer-row><div class=row><span class=row-label>` + html.EscapeString(title) + `</span>` + btnRow(right) + `</div>` +
		`<div class=xfer-sub>` + sub + `</div></div>`
}

// ── ported formatters (webui copies of the Fyne view_peers_*.go pure helpers) ──

// fmtRemoteDeck formats one remote deck line: "Deck B · Artist - Title (128 BPM)".
func fmtRemoteDeck(ds peerbridge.DeckState) string {
	if ds.Title == "" && ds.Artist == "" {
		return ""
	}
	s := ds.Title
	if ds.Artist != "" {
		s = ds.Artist + " - " + ds.Title
	}
	if ds.Deck != "" {
		s = i18n.T("live.deck.name", i18n.A{"id": ds.Deck}) + " · " + s
	}
	if ds.BPM > 0 {
		s += fmt.Sprintf("  (%.0f BPM)", ds.BPM)
	}
	return s
}

func fmtRemoteSource(s mediaroute.RemoteSource, resolve func(string) string) string {
	d := s.Desc
	line := fmt.Sprintf("%s @ %s", d.Name, resolve(s.Peer))
	if d.Width > 0 {
		line += fmt.Sprintf(" · %dx%d", d.Width, d.Height)
		if d.FPS > 0 {
			line += fmt.Sprintf("@%.0f", d.FPS)
		}
	}
	return line
}

func fmtPipeLine(s medialink.RouteStat) string {
	var parts []string
	if s.Encoder != "" {
		p := i18n.T("peers.tier", i18n.A{"encoder": s.Encoder, "n": fmt.Sprint(s.Tier)})
		if s.Software {
			p += " " + i18n.T("peers.softwareEncodeWarning")
		}
		parts = append(parts, p)
	}
	if s.RateBps > 0 {
		parts = append(parts, fmt.Sprintf("%.1f Mbps", s.RateBps/1e6))
	}
	if s.Keyframes > 0 {
		parts = append(parts, fmt.Sprintf("kf %d", s.Keyframes))
	}
	if s.JB != nil {
		parts = append(parts, i18n.T("peers.jbStats", i18n.A{
			"depth": fmt.Sprint(s.JB.Depth),
			"late":  fmt.Sprintf("%.1f", s.JB.LateRate*100),
			"drops": fmt.Sprint(s.JB.PolicyDrops),
		}))
	}
	if s.Pipe != nil {
		p := i18n.T("peers.outFps", i18n.A{"fps": fmt.Sprintf("%.1f", s.Pipe.OutFPS)})
		if s.Pipe.HWAccel != "" {
			p += " · " + s.Pipe.HWAccel
		}
		if s.Pipe.Restarts > 0 {
			p += " · " + i18n.T("peers.restarts", i18n.A{"n": fmt.Sprint(s.Pipe.Restarts)})
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " · ")
}

func fmtClockLine(q medialink.ClockQuality) string {
	lock := i18n.T("peers.acquiring")
	if q.Locked {
		lock = i18n.T("peers.locked")
	}
	line := i18n.T("peers.clockLine", i18n.A{"tier": fmt.Sprint(q.Tier), "lock": lock})
	if q.OffsetNs != 0 {
		line += " · " + i18n.T("peers.offset", i18n.A{"value": fmtSignedMs(q.OffsetNs)})
	}
	return line
}

func fmtSyncLine(s medialink.SyncStat, name string) string {
	lock := i18n.T("peers.acquiring")
	if s.Locked {
		lock = i18n.T("peers.locked")
	}
	return i18n.T("peers.syncLine", i18n.A{
		"name":   name,
		"offset": fmtSignedMs(s.OffsetNs),
		"rtt":    fmtMsNs(float64(s.RTTNs)),
		"lock":   lock,
	})
}

func fmtTCLine(st medialink.TCStatus, resolve func(string) string) string {
	var line string
	if st.Role == medialink.TCRoleMaster {
		line = i18n.T("peers.tcMasterThisInstance")
	} else {
		line = i18n.T("peers.tcMaster", i18n.A{"name": resolve(st.Master)})
	}
	if st.Rate.Nominal != 0 {
		line += fmt.Sprintf(" · %s @%s", st.TC.String(), fmtRate(st.Rate))
		if !st.Running {
			line += " · " + i18n.T("peers.stopped")
		}
	}
	if st.Holdover {
		line += " · " + i18n.T("peers.holdover")
	}
	return line
}

func fmtRouteStat(s medialink.RouteStat, resolve func(string) string) (title, detail string) {
	dir := i18n.T("peers.receivingFrom")
	if s.Direction == "send" {
		dir = i18n.T("peers.sendingTo")
	}
	title = i18n.T("peers.routeTitle", i18n.A{
		"dir":    dir,
		"peer":   resolve(s.Peer),
		"stream": fmt.Sprint(s.Stream),
		"frames": fmt.Sprint(s.Frames),
		"bytes":  humanBytes(s.Bytes),
	})
	if s.Direction == "recv" {
		detail = i18n.T("peers.routeDetailRecv", i18n.A{
			"loss":      fmt.Sprint(s.LostEst),
			"recovered": fmt.Sprint(s.Recovered),
			"jitter":    fmtMsNs(s.JitterNs),
			"p50":       fmtMsNs(float64(s.LatencyP50Ns)),
			"p95":       fmtMsNs(float64(s.LatencyP95Ns)),
			"nack":      fmt.Sprint(s.NACKsSent),
		})
		return title, detail
	}
	if r := s.Remote; r != nil {
		detail = i18n.T("peers.routeDetailRemote", i18n.A{
			"loss":   fmt.Sprint(r.Lost),
			"pct":    fmt.Sprintf("%.2f", r.FractionLost*100),
			"jitter": fmtMsNs(r.Jitter),
			"retx":   fmt.Sprint(s.Retransmits),
			"pli":    fmt.Sprint(s.PLIRequests),
		})
	} else {
		detail = i18n.T("peers.routeDetailNoReport", i18n.A{
			"retx": fmt.Sprint(s.Retransmits),
			"pli":  fmt.Sprint(s.PLIRequests),
		})
	}
	return title, detail
}

func fmtRate(r medialink.Rate) string {
	if r.Drop {
		return fmt.Sprintf("%.2fdf", float64(r.Nominal)*1000/1001)
	}
	return fmt.Sprintf("%d", r.Nominal)
}

// fmtMsNs renders nanoseconds as adaptive milliseconds ("0.42 ms", "12.3 ms").
func fmtMsNs(ns float64) string {
	ms := ns / 1e6
	if ms < 0 {
		ms = -ms
	}
	if ms < 10 {
		return fmt.Sprintf("%.2f ms", ms)
	}
	return fmt.Sprintf("%.1f ms", ms)
}

func fmtSignedMs(ns int64) string {
	if ns < 0 {
		return "−" + fmtMsNs(float64(-ns))
	}
	return "+" + fmtMsNs(float64(ns))
}

// ── webcam formatters (ported from view_peers_webcam.go) ──

func fmtCamStatus(st webcam.Status) string {
	switch {
	case st.Running:
		s := i18n.T("peers.camLive", i18n.A{"mode": fmtCamMode(st.W, st.H, st.FPS)})
		if st.Err != "" {
			s += " · " + st.Err
		}
		return s
	case st.Err != "":
		return st.Err
	case st.Device == "":
		return i18n.T("peers.noCameraSelected")
	default:
		return i18n.T("peers.camReady", i18n.A{"device": st.Device})
	}
}

func fmtCamMode(w, h, fps int) string {
	s := fmt.Sprintf("%dx%d", w, h)
	if fps > 0 {
		s += fmt.Sprintf(" @ %d", fps)
	}
	return s
}

func camModeStrings(modes []webcam.Mode) []string {
	out := make([]string, 0, len(modes))
	seen := map[string]bool{}
	for _, m := range modes {
		s := fmtCamMode(m.W, m.H, int(m.FPS+0.5))
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ── small helpers ──

func peerName(nick, node string) string {
	if strings.TrimSpace(nick) != "" {
		return nick
	}
	return shortID(node)
}

func shortID(id string) string {
	if len(id) > 10 {
		return id[:10] + "…"
	}
	return id
}

func spaceSAS(s string) string {
	if len(s) == 6 {
		return s[:3] + " " + s[3:]
	}
	return s
}

func humanBytes(b uint64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(u), 0
	for n := b / u; n >= u; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
