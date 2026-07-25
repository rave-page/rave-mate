package webui

import (
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"

	"rave.page/mate/internal/filexfer"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/peerbridge"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/webcam"
	"rave.page/mate/internal/zigui"
)

// Peers is a Zig-rendered tab (native/zigui/src/peers.zig): Go resolves all state
// (peerlink/peerbridge/medialink/webcam/filexfer data + RESOLVED i18n strings + every
// number pre-formatted) into peersSt, the Zig lib renders HTML byte-identical to the Go
// renderers below (fallback + golden reference, zigui_golden_peers_test.go).

// ── resolved render state (JSON → Zig) ──

// peerDeckSt is one bridged remote deck line rendered under a connection row.
type peerDeckSt struct {
	Audible bool   `json:"audible"`
	Line    string `json:"line"`
}

// peerRowSt is one `.row` line of a peers list: optional status dot, name, muted tail,
// trailing buttons, plus (connections only) the bridged now-playing deck lines.
type peerRowSt struct {
	Dot   string       `json:"dot"` // "" = no dot prefix
	Name  string       `json:"name"`
	Sub   string       `json:"sub"`
	Btns  []uiBtn      `json:"btns,omitempty"`
	Decks []peerDeckSt `json:"decks,omitempty"`
}

// peerListSt is a rows-or-empty-state card (connections / discovered / remembered). Go
// picks Empty per reason (discovery off vs still searching), so the renderers stay pure.
type peerListSt struct {
	Empty string      `json:"empty"`
	Rows  []peerRowSt `json:"rows,omitempty"`
}

// peerBannerSt is the MIDI-forwarding control banner ("controlling <peer>" + Stop).
type peerBannerSt struct {
	Show bool   `json:"show"`
	Text string `json:"text"`
	Btn  uiBtn  `json:"btn"`
}

// peerRouteSt is one media-plane route (title + telemetry detail + optional pipeline line).
type peerRouteSt struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Pipe   string `json:"pipe"` // "" = omitted
}

// peerRecvRowSt is one receivable/active remote video source. Mark is a trusted literal
// prefix ("◂ " on an active receive) inserted raw, exactly like the Go source did.
type peerRecvRowSt struct {
	Mark string `json:"mark"`
	Line string `json:"line"`
	Btn  uiBtn  `json:"btn"`
}

// peerRecvSt is the "Receive video" block (hidden when nothing is offered or active).
type peerRecvSt struct {
	Show bool            `json:"show"`
	Head string          `json:"head"`
	Rows []peerRecvRowSt `json:"rows,omitempty"`
}

// peerMediaSt is the media plane: clock/sync/TC-master lines + per-route telemetry.
// Every number is pre-formatted Go-side (floats never cross the ABI).
type peerMediaSt struct {
	Show      bool          `json:"show"`
	ClockLine string        `json:"clockLine"`
	SyncLines []string      `json:"syncLines,omitempty"`
	HasTC     bool          `json:"hasTc"`
	TCLine    string        `json:"tcLine"`
	NoRoutes  string        `json:"noRoutes"`  // shown when Routes is empty
	RoutesHdr string        `json:"routesHdr"` // "Routes: N"
	Routes    []peerRouteSt `json:"routes,omitempty"`
	Recv      peerRecvSt    `json:"recv"`
}

// camPropSt is one UVC property row. Min/Max/Step/Val ride as strings (Go %d).
type camPropSt struct {
	Label    string `json:"label"`
	MinS     string `json:"minS"`
	MaxS     string `json:"maxS"`
	StepS    string `json:"stepS"`
	ValS     string `json:"valS"`
	Act      string `json:"act"`
	Disabled bool   `json:"disabled"`
	CanAuto  bool   `json:"canAuto"`
	Auto     bool   `json:"auto"`
	AutoAct  string `json:"autoAct"`
	AutoLbl  string `json:"autoLbl"`
}

// camNodeSt is one camera instance card (device/mode selects + start-stop + PTZ props).
type camNodeSt struct {
	Name       string      `json:"name"`
	RefreshAct string      `json:"refreshAct"`
	Status     string      `json:"status"`
	Dev        selState    `json:"dev"`
	Mode       selState    `json:"mode"`
	Start      uiBtn       `json:"start"`
	Sender     string      `json:"sender"`     // "" = omit the sender row
	SenderLine string      `json:"senderLine"` // localized "Spout sender: X"
	PropsHdr   string      `json:"propsHdr"`
	Props      []camPropSt `json:"props,omitempty"`
}

// peerCamSt is the webcam section. Show=false = no section at all (no webcam service or
// no config); Gated = the feature is off, so the section only carries the hint.
type peerCamSt struct {
	Show     bool        `json:"show"`
	Gated    bool        `json:"gated"`
	GateHint string      `json:"gateHint"`
	Empty    string      `json:"empty"`
	Nodes    []camNodeSt `json:"nodes,omitempty"`
}

// xferSetSt is the file-transfer settings card (Show=false when there is no config).
type xferSetSt struct {
	Show       bool     `json:"show"`
	Enabled    uiToggle `json:"enabled"`
	AcceptLbl  string   `json:"acceptLbl"`
	Mode       string   `json:"mode"` // "ask" | "auto"
	AskLbl     string   `json:"askLbl"`
	AutoLbl    string   `json:"autoLbl"`
	Dir        uiField  `json:"dir"`
	DefaultDir string   `json:"defaultDir"`
}

// xferPendSt is a pending incoming transfer awaiting an accept/decline decision.
type xferPendSt struct {
	Line string  `json:"line"`
	Btns []uiBtn `json:"btns,omitempty"`
}

// xferProgSt is one in-flight/finished transfer row. Exactly one right-hand control
// (IsBadge picks badge over button) and one sub-line (Bar picks the progress bar over
// the muted text); BarPct is progressBar's fill width pre-formatted Go-side.
type xferProgSt struct {
	Title    string `json:"title"`
	IsBadge  bool   `json:"isBadge"`
	Btn      uiBtn  `json:"btn"`
	Badge    string `json:"badge"`
	BadgeVar string `json:"badgeVar"`
	Bar      bool   `json:"bar"`
	BarPct   string `json:"barPct"`
	BarCap   string `json:"barCap"`
	SubText  string `json:"subText"`
}

// peerXferSt is the file-transfer section (Show=false when the service is off).
type peerXferSt struct {
	Show     bool         `json:"show"`
	Settings xferSetSt    `json:"settings"`
	None     bool         `json:"none"` // no transfers at all → NoneHint instead of the card
	NoneHint string       `json:"noneHint"`
	Pend     []xferPendSt `json:"pend,omitempty"`
	Rows     []xferProgSt `json:"rows,omitempty"`
}

// peersBodySt is the #peers-body inner state (patched ~1 Hz by peers_actions.go).
type peersBodySt struct {
	Strip           string       `json:"strip"`
	Banner          peerBannerSt `json:"banner"`
	ConnsTitle      string       `json:"connsTitle"`
	Conns           peerListSt   `json:"conns"`
	MediaTitle      string       `json:"mediaTitle"`
	Media           peerMediaSt  `json:"media"`
	CamTitle        string       `json:"camTitle"`
	Cam             peerCamSt    `json:"cam"`
	XferTitle       string       `json:"xferTitle"`
	Xfer            peerXferSt   `json:"xfer"`
	NetTitle        string       `json:"netTitle"`
	Discovered      peerListSt   `json:"discovered"`
	RememberedTitle string       `json:"rememberedTitle"`
	Remembered      peerListSt   `json:"remembered"`
}

// peersSt is the resolved render state for the Peers view (JSON → Zig).
type peersSt struct {
	Title       string      `json:"title"`
	Sub         string      `json:"sub"`
	Available   bool        `json:"available"`
	Unavailable string      `json:"unavailable"`
	Body        peersBodySt `json:"body"`
}

// ── bridges ──

// renderPeers: LAN peers at Fyne parity - control banner (MIDI forwarding) + per-connection Control
// toggle + bridged now-playing, media plane (clock/sync/TC-master + per-route pipeline telemetry),
// webcam panel (device/mode/start-stop/PTZ), file transfer (settings + progress), discovered +
// remembered peers, status-strip counts. peers-body is patched ~1 Hz (peers_actions.go).
func (u *UI) renderPeers() string {
	st := u.peersState()
	if zigui.Available() {
		if h, ok := zigWire("RenderPeersV2", wirePeers(st), zigui.RenderPeersV2,
			zigui.RenderPeers, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return peersHTML(st)
}

// peersBody is the #peers-body inner fragment (~1 Hz live tick).
func (u *UI) peersBody() string {
	st := u.peersBodyState()
	if zigui.Available() {
		if h, ok := zigWire("RenderPeersBodyV2", wirePeersBody(st), zigui.RenderPeersBodyV2,
			zigui.RenderPeersBody, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return peersBodyHTML(st)
}

// ── state builders (impure: services, locks, i18n) ──

func (u *UI) peersState() peersSt {
	st := peersSt{
		Title:       i18n.T("peers.title"),
		Sub:         i18n.T("peers.subtitle"),
		Available:   u.svc.Peers != nil,
		Unavailable: i18n.T("peers.unavailable"),
	}
	if st.Available {
		st.Body = u.peersBodyState()
	}
	return st
}

func (u *UI) peersBodyState() peersBodySt {
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

	st := peersBodySt{
		Strip:           u.peerStripText(conns),
		ConnsTitle:      i18n.T("peers.connections"),
		MediaTitle:      i18n.T("peers.mediaPlane"),
		CamTitle:        i18n.T("peers.webcam"),
		XferTitle:       i18n.T("peers.fileTransfer"),
		NetTitle:        i18n.T("peers.onThisNetwork"),
		RememberedTitle: i18n.T("peers.rememberedOffline"),
	}
	// Banner state FIRST: it auto-clears a control target whose peer dropped, and the
	// connection rows read Forwarding() afterwards (same order as the old renderer).
	st.Banner = u.peerBannerState(byNode)
	st.Conns = u.peerConnsState(conns, remotes)
	if u.svc.Media != nil && (len(conns) > 0 || len(u.svc.Media.Stats()) > 0) {
		st.Media = u.peerMediaState(resolve)
	}
	st.Cam = u.peerCamState(resolve)
	st.Xfer = u.peerXferState(resolve)
	st.Discovered = u.peerDiscoveredState(byNode)
	st.Remembered = u.peerRememberedState(byNode)
	return st
}

func (u *UI) peerConns() []peerlink.ConnInfo {
	if u.svc.Peers == nil {
		return nil
	}
	return u.svc.Peers.Connections()
}

// peerStripText resolves the status-strip counts line.
func (u *UI) peerStripText(conns []peerlink.ConnInfo) string {
	nFound := 0
	if u.svc.Discovery != nil {
		nFound = len(u.svc.Discovery.Peers())
	}
	if u.svc.Identity != nil {
		return i18n.T("peers.statusStripNode", i18n.A{
			"connected": fmt.Sprint(len(conns)),
			"found":     fmt.Sprint(nFound),
			"node":      shortID(u.svc.Identity.NodeID),
		})
	}
	return i18n.T("peers.statusStrip", i18n.A{
		"connected": fmt.Sprint(len(conns)),
		"found":     fmt.Sprint(nFound),
	})
}

// peerBannerState resolves the MIDI-forwarding banner. Side effect: a forwarding target
// that is no longer connected is cleared here (the banner must never name a dead peer).
func (u *UI) peerBannerState(byNode map[string]peerlink.ConnInfo) peerBannerSt {
	if u.svc.PeerBridge == nil {
		return peerBannerSt{}
	}
	on, target := u.svc.PeerBridge.Forwarding()
	if !on {
		return peerBannerSt{}
	}
	if _, live := byNode[target]; !live { // target dropped - auto-clear
		u.svc.PeerBridge.SetMIDIForwarding(false)
		u.svc.PeerBridge.SetControlTarget("")
		return peerBannerSt{}
	}
	name := peerName(byNode[target].Nickname, target)
	return peerBannerSt{
		Show: true,
		Text: i18n.T("peers.controllingBanner", i18n.A{"name": name}),
		Btn:  uiBtn{Label: i18n.T("peers.stopControlling"), Variant: "warn", Act: "peers-control:" + target, Val: "0"},
	}
}

func (u *UI) peerConnsState(conns []peerlink.ConnInfo, remotes map[string]peerbridge.RemoteState) peerListSt {
	lst := peerListSt{Empty: i18n.T("peers.noActiveConnections")}
	on, target := false, ""
	if u.svc.PeerBridge != nil {
		on, target = u.svc.PeerBridge.Forwarding()
	}
	for _, c := range conns {
		if c.Status == peerlink.StatusAwaitSAS {
			lst.Rows = append(lst.Rows, peerRowSt{
				Name: peerName(c.Nickname, c.NodeID),
				Sub:  i18n.T("peers.pairingCode", i18n.A{"sas": spaceSAS(c.SAS)}),
				Btns: []uiBtn{
					{Label: i18n.T("peers.matches"), Variant: "go", Act: "peer-sas:" + c.NodeID, Val: "1"},
					{Label: i18n.T("peers.doesntMatch"), Variant: "destructive", Act: "peer-sas:" + c.NodeID, Val: "0"},
				},
			})
			continue
		}
		st := string(c.Status)
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
		var btns []uiBtn
		// Control toggle (only for connected peers + a live bridge).
		if c.Status == peerlink.StatusConnected && u.svc.PeerBridge != nil {
			if on && target == c.NodeID {
				btns = append(btns, uiBtn{Label: i18n.T("peers.stopControl"), Variant: "warn", Act: "peers-control:" + c.NodeID, Val: "0"})
			} else {
				btns = append(btns, uiBtn{Label: i18n.T("peers.control"), Variant: "outline", Act: "peers-control:" + c.NodeID, Val: "1"})
			}
		}
		btns = append(btns, uiBtn{Label: i18n.T("peers.forget"), Variant: "ghost", Act: "peer-forget:" + c.NodeID})
		row := peerRowSt{Dot: v, Name: peerName(c.Nickname, c.NodeID), Sub: st, Btns: btns}
		for _, ds := range remotes[c.NodeID].NowPlaying.AllDecks() {
			line := fmtRemoteDeck(ds)
			if line == "" {
				continue
			}
			row.Decks = append(row.Decks, peerDeckSt{Audible: ds.Audible, Line: line})
		}
		lst.Rows = append(lst.Rows, row)
	}
	return lst
}

func (u *UI) peerDiscoveredState(byNode map[string]peerlink.ConnInfo) peerListSt {
	if u.svc.Discovery == nil {
		return peerListSt{Empty: i18n.T("peers.discoveryOff")}
	}
	lst := peerListSt{Empty: i18n.T("peers.searchingHint")}
	for _, p := range u.svc.Discovery.Peers() {
		if _, busy := byNode[p.NodeID]; busy {
			continue
		}
		verb, variant := i18n.T("peers.pair"), "primary"
		if u.svc.Peers.IsTrusted(p.NodeID) {
			verb, variant = i18n.T("peers.connect"), "outline"
		}
		lst.Rows = append(lst.Rows, peerRowSt{
			Name: peerName(p.Name, p.NodeID), Sub: p.Address.String(),
			Btns: []uiBtn{{Label: verb, Variant: variant, Act: "peer-connect:" + p.NodeID}},
		})
	}
	return lst
}

func (u *UI) peerRememberedState(byNode map[string]peerlink.ConnInfo) peerListSt {
	online := map[string]bool{}
	for id := range byNode {
		online[id] = true
	}
	if u.svc.Discovery != nil {
		for _, p := range u.svc.Discovery.Peers() {
			online[p.NodeID] = true
		}
	}
	lst := peerListSt{Empty: i18n.T("peers.none")}
	for _, p := range u.svc.Peers.Remembered() {
		if online[p.NodeID] {
			continue
		}
		lst.Rows = append(lst.Rows, peerRowSt{
			Dot: "muted", Name: peerName(p.Nickname, p.NodeID), Sub: i18n.T("peers.offline"),
			Btns: []uiBtn{{Label: i18n.T("peers.forget"), Variant: "ghost", Act: "peer-forget:" + p.NodeID}},
		})
	}
	return lst
}

func (u *UI) peerMediaState(resolve func(string) string) peerMediaSt {
	m := u.svc.Media
	st := peerMediaSt{
		Show:      true,
		ClockLine: fmtClockLine(m.ClockQuality()),
		NoRoutes:  i18n.T("peers.noActiveMediaRoutes"),
	}
	syncs := m.SyncStats()
	sort.Slice(syncs, func(i, j int) bool { return syncs[i].Peer < syncs[j].Peer })
	for _, s := range syncs {
		st.SyncLines = append(st.SyncLines, fmtSyncLine(s, resolve(s.Peer)))
	}
	if p := u.svc.TCPlane; p != nil {
		st.HasTC, st.TCLine = true, fmtTCLine(p.Status(), resolve)
	}
	stats := m.Stats()
	if len(stats) > 0 {
		sort.Slice(stats, func(i, j int) bool { return stats[i].Session < stats[j].Session })
		st.RoutesHdr = i18n.T("peers.routes", i18n.A{"count": fmt.Sprint(len(stats))})
		for _, s := range stats {
			title, detail := fmtRouteStat(s, resolve)
			st.Routes = append(st.Routes, peerRouteSt{Title: title, Detail: detail, Pipe: fmtPipeLine(s)})
		}
	}
	st.Recv = u.peerRecvState(resolve)
	return st
}

// peerRecvState resolves receivable remote sources + active receives (P4).
func (u *UI) peerRecvState(resolve func(string) string) peerRecvSt {
	mr := u.svc.MediaRoutes
	if mr == nil {
		return peerRecvSt{}
	}
	srcs := mr.RemoteVideoSources()
	recvs := mr.Receives()
	if len(srcs) == 0 && len(recvs) == 0 {
		return peerRecvSt{}
	}
	st := peerRecvSt{Show: true, Head: i18n.T("peers.receiveVideo")}
	receiving := map[string]bool{}
	for _, r := range recvs {
		receiving[r.Peer+"\x00"+r.Name] = true
		st.Rows = append(st.Rows, peerRecvRowSt{
			Mark: "◂ ",
			Line: i18n.T("peers.receiveVideoLine", i18n.A{"name": r.Name, "peer": resolve(r.Peer)}),
			Btn:  uiBtn{Label: i18n.T("player.stop"), Variant: "destructive", Act: "media-stop:" + r.Session},
		})
	}
	for _, s := range srcs {
		if receiving[s.Peer+"\x00"+s.Desc.Name] {
			continue
		}
		st.Rows = append(st.Rows, peerRecvRowSt{
			Line: fmtRemoteSource(s, resolve),
			Btn:  uiBtn{Label: i18n.T("peers.receive"), Variant: "go", Act: "media-recv:" + s.Peer + "\x1f" + s.Desc.ID},
		})
	}
	return st
}

func (u *UI) peerCamState(resolve func(string) string) peerCamSt {
	w := u.svc.Webcam
	if w == nil || u.svc.Cfg == nil {
		return peerCamSt{}
	}
	if !u.svc.Cfg.Features.Webcam.Enabled {
		return peerCamSt{Show: true, Gated: true, GateHint: i18n.T("peers.webcamOff")}
	}
	st := peerCamSt{Show: true, Empty: i18n.T("peers.noCameraInstances")}
	for _, in := range w.Instances() {
		st.Nodes = append(st.Nodes, camNodeState(in, resolve))
	}
	return st
}

func camNodeState(in webcam.Instance, resolve func(string) string) camNodeSt {
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

	n := camNodeSt{
		Name:       name,
		RefreshAct: "peers-cam-refresh:" + id,
		Status:     fmtCamStatus(in.Status),
		Dev:        resolveSelectBox(i18n.T("peers.device"), "peers-cam-device:"+id, devOpts, selDev),
		Mode:       resolveSelectBox(i18n.T("peers.mode"), "peers-cam-mode:"+id, modeOpts, selMode),
		Start:      uiBtn{Label: startLbl, Variant: startVar, Act: "peers-cam-start:" + id, Val: startVal},
		Sender:     in.Sender,
		PropsHdr:   i18n.T("peers.lensImage"),
	}
	if in.Sender != "" {
		n.SenderLine = i18n.T("peers.spoutSender", i18n.A{"sender": in.Sender})
	}
	for _, p := range in.Props {
		n.Props = append(n.Props, camPropState(id, p))
	}
	return n
}

// camPropState resolves one UVC property: label + range + live value + auto flag.
func camPropState(node string, p webcam.PropState) camPropSt {
	step := int32(1)
	if p.Step > 0 {
		step = p.Step
	}
	return camPropSt{
		Label:    p.Label,
		MinS:     strconv.FormatInt(int64(p.Min), 10),
		MaxS:     strconv.FormatInt(int64(p.Max), 10),
		StepS:    strconv.FormatInt(int64(step), 10),
		ValS:     strconv.FormatInt(int64(p.Value), 10),
		Act:      "peers-cam-prop:" + node + "\x1f" + p.ID,
		Disabled: p.Auto,
		CanAuto:  p.CanAuto,
		Auto:     p.Auto,
		AutoAct:  "peers-cam-auto:" + node + "\x1f" + p.ID,
		AutoLbl:  i18n.T("peers.auto"),
	}
}

func (u *UI) peerXferState(resolve func(string) string) peerXferSt {
	if u.svc.FileXfer == nil {
		return peerXferSt{}
	}
	st := peerXferSt{Show: true, Settings: u.xferSetState(), NoneHint: i18n.T("peers.noTransfersYet")}
	tr := u.svc.FileXfer.Transfers()
	if len(tr) == 0 {
		st.None = true
		return st
	}
	// Pending incoming accepts first - they need a decision.
	for _, t := range tr {
		if !t.Send && string(t.State) == "pending" {
			st.Pend = append(st.Pend, xferPendSt{
				Line: i18n.T("peers.xferIncoming", i18n.A{
					"name":  t.Name,
					"size":  humanBytes(uint64(t.Bytes)),
					"files": i18n.Tn("peers.xferFile", t.Files),
					"peer":  resolve(t.Peer),
				}),
				Btns: []uiBtn{
					{Label: i18n.T("peers.accept"), Variant: "go", Act: "xfer-accept:" + t.ID, Val: "1"},
					{Label: i18n.T("peers.decline"), Variant: "ghost", Act: "xfer-accept:" + t.ID, Val: "0"},
				},
			})
		}
	}
	for _, t := range tr {
		if !t.Send && string(t.State) == "pending" {
			continue
		}
		st.Rows = append(st.Rows, xferRowState(t, resolve))
	}
	return st
}

func (u *UI) xferSetState() xferSetSt {
	if u.svc.Cfg == nil {
		return xferSetSt{}
	}
	f := u.svc.Cfg.Features.FileXfer
	mode := "ask"
	if f.AutoAccept() {
		mode = "auto"
	}
	return xferSetSt{
		Show:       true,
		Enabled:    newToggle(i18n.T("peers.receiveFiles"), "peers-xfer-enabled", f.Enabled),
		AcceptLbl:  i18n.T("peers.accept"),
		Mode:       mode,
		AskLbl:     i18n.T("peers.ask"),
		AutoLbl:    i18n.T("peers.autoMode"),
		Dir:        newField(i18n.T("peers.saveTo"), "peers-xfer-dir", f.DownloadDir, "text"),
		DefaultDir: i18n.T("peers.defaultDir", i18n.A{"dir": f.ResolvedDownloadDir()}),
	}
}

func xferRowState(t filexfer.Transfer, resolve func(string) string) xferProgSt {
	arrow := "⇧"
	titleKey := "peers.xferTo"
	if !t.Send {
		arrow = "⇩"
		titleKey = "peers.xferFrom"
	}
	r := xferProgSt{Title: i18n.T(titleKey, i18n.A{"arrow": arrow, "name": t.Name, "peer": resolve(t.Peer)})}
	cancel := uiBtn{Label: i18n.T("common.cancel"), Variant: "ghost", Act: "xfer-cancel:" + t.ID}
	switch string(t.State) {
	case "active":
		frac := 0.0
		if t.Bytes > 0 {
			frac = float64(t.Done) / float64(t.Bytes)
		}
		r.Bar, r.BarPct = true, progressPct(frac)
		r.BarCap = fmt.Sprintf("%s / %s · %s/s", humanBytes(uint64(t.Done)), humanBytes(uint64(t.Bytes)), humanBytes(uint64(t.Rate)))
		r.Btn = cancel
	case "waiting":
		r.SubText = i18n.T("peers.waitingForPeer")
		r.Btn = cancel
	case "stalled":
		r.SubText = i18n.T("peers.interruptedRetrying")
		if t.Error != "" {
			r.SubText = i18n.T("peers.interruptedWithError", i18n.A{"error": t.Error})
		}
		r.Btn = cancel
	case "done":
		r.SubText = i18n.T("peers.xferDone", i18n.A{
			"files": i18n.Tn("peers.xferFile", t.Files),
			"size":  humanBytes(uint64(t.Bytes)),
		})
		r.IsBadge, r.Badge, r.BadgeVar = true, i18n.T("peers.done"), "success"
	case "error":
		r.SubText = i18n.T("peers.xferFailed", i18n.A{"error": t.Error})
		r.IsBadge, r.Badge, r.BadgeVar = true, i18n.T("peers.error"), "error"
	default: // canceled
		r.SubText = i18n.T("peers.canceled")
		r.IsBadge, r.Badge, r.BadgeVar = true, string(t.State), "secondary"
	}
	return r
}

// ── pure renderers (golden reference; byte-identical to native/zigui/src/peers.zig) ──

func peersHTML(st peersSt) string {
	if !st.Available {
		return panel(st.Title, "") + emptyState(st.Unavailable)
	}
	return panel(st.Title, st.Sub) + `<div id=peers-body>` + peersBodyHTML(st.Body) + `</div>`
}

func peersBodyHTML(st peersBodySt) string {
	var b strings.Builder
	b.WriteString(`<div id=peers-strip class=peers-strip>` + peerStripHTML(st.Strip) + `</div>`)
	b.WriteString(peerBannerHTML(st.Banner))
	b.WriteString(section(st.ConnsTitle, peerListHTML(st.Conns)))
	if st.Media.Show {
		b.WriteString(section(st.MediaTitle, peerMediaHTML(st.Media)))
	}
	if st.Cam.Show {
		b.WriteString(section(st.CamTitle, peerCamHTML(st.Cam)))
	}
	if st.Xfer.Show {
		b.WriteString(section(st.XferTitle, peerXferHTML(st.Xfer)))
	}
	// two sibling lists share a row ≥1100px (.peers-2col)
	b.WriteString(`<div class=peers-2col>` + section(st.NetTitle, peerListHTML(st.Discovered)) +
		section(st.RememberedTitle, peerListHTML(st.Remembered)) + `</div>`)
	return b.String()
}

func peerStripHTML(txt string) string {
	return `<span data-label="peer counts" data-value="` + html.EscapeString(txt) + `">` + html.EscapeString(txt) + `</span>`
}

func peerBannerHTML(st peerBannerSt) string {
	if !st.Show {
		return ""
	}
	return `<div class=ctl-banner data-label="controlling"><span class=ctl-banner-tx>🎛 ` +
		html.EscapeString(st.Text) + `</span>` + st.Btn.html() + `</div>`
}

func peerListHTML(st peerListSt) string {
	if len(st.Rows) == 0 {
		return emptyState(st.Empty)
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	for _, r := range st.Rows {
		b.WriteString(peerRowHTML(r))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func peerRowHTML(r peerRowSt) string {
	var b strings.Builder
	b.WriteString(`<div class=row><span class=row-label>`)
	if r.Dot != "" {
		b.WriteString(dot(r.Dot) + ` `)
	}
	b.WriteString(html.EscapeString(r.Name) + ` <span class=np-artist>` + html.EscapeString(r.Sub) +
		`</span></span>` + uiBtnRow(r.Btns) + `</div>`)
	for _, d := range r.Decks {
		mark, cls := "▷ ", "peer-np peer-np--quiet"
		if d.Audible {
			mark, cls = "▶ ", "peer-np"
		}
		b.WriteString(`<div class="` + cls + `">` + mark + html.EscapeString(d.Line) + `</div>`)
	}
	return b.String()
}

func peerMediaHTML(st peerMediaSt) string {
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)

	// Clock: active tier/lock/offset + per-peer sync estimates.
	b.WriteString(`<div class=media-clock>` + html.EscapeString(st.ClockLine) + `</div>`)
	for _, s := range st.SyncLines {
		b.WriteString(`<div class=media-sub>` + html.EscapeString(s) + `</div>`)
	}

	// Timecode master state.
	if st.HasTC {
		b.WriteString(`<div class=media-clock>` + html.EscapeString(st.TCLine) + `</div>`)
	}

	// Routes.
	if len(st.Routes) == 0 {
		b.WriteString(`<div class=np-artist>` + html.EscapeString(st.NoRoutes) + `</div>`)
	} else {
		b.WriteString(`<div class=media-sub>` + html.EscapeString(st.RoutesHdr) + `</div>`)
		for _, r := range st.Routes {
			b.WriteString(`<div class=media-route>` + html.EscapeString(r.Title) + `</div>`)
			b.WriteString(`<div class=media-sub>` + html.EscapeString(r.Detail) + `</div>`)
			if r.Pipe != "" {
				b.WriteString(`<div class=media-sub>` + html.EscapeString(r.Pipe) + `</div>`)
			}
		}
	}
	b.WriteString(`</div>`)

	if st.Recv.Show {
		b.WriteString(peerRecvHTML(st.Recv))
	}
	return b.String()
}

func peerRecvHTML(st peerRecvSt) string {
	var b strings.Builder
	b.WriteString(`<div class=media-recv-head>` + html.EscapeString(st.Head) + `</div><div class="rp-card">`)
	for _, r := range st.Rows {
		b.WriteString(`<div class=row><span class=row-label>` + r.Mark + html.EscapeString(r.Line) + `</span>` +
			btnRow(r.Btn.html()) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func peerCamHTML(st peerCamSt) string {
	if st.Gated {
		return hint("info", st.GateHint)
	}
	if len(st.Nodes) == 0 {
		return emptyState(st.Empty)
	}
	var b strings.Builder
	for _, n := range st.Nodes {
		b.WriteString(camNodeHTML(n))
	}
	return b.String()
}

func camNodeHTML(n camNodeSt) string {
	var b strings.Builder
	b.WriteString(`<div class="rp-card cam-node">`)
	b.WriteString(`<div class=cam-head><span class=cam-title>` + html.EscapeString(n.Name) + `</span>` +
		btn("↻", "ghost", n.RefreshAct, "") + `</div>`)
	b.WriteString(`<div class=cam-status>` + html.EscapeString(n.Status) + `</div>`)
	b.WriteString(`<div class=cam-ctls>` + selHTML(n.Dev) + selHTML(n.Mode) + n.Start.html() + `</div>`)
	if n.Sender != "" {
		b.WriteString(`<div class=cam-sender data-label="spout sender" data-value="` + html.EscapeString(n.Sender) + `">` +
			html.EscapeString(n.SenderLine) + `</div>`)
	}
	if len(n.Props) > 0 {
		b.WriteString(`<div class=cam-props-h>` + html.EscapeString(n.PropsHdr) + `</div>`)
		for _, p := range n.Props {
			b.WriteString(camPropHTML(p))
		}
	}
	b.WriteString(`</div>`)
	return b.String()
}

// camPropHTML renders one UVC property: label + range + live value + optional auto checkbox.
func camPropHTML(p camPropSt) string {
	dis := ""
	if p.Disabled {
		dis = " disabled"
	}
	oninput := `oninput="var v=this.parentNode.querySelector('.cam-prop-v');if(v)v.textContent=this.value"`
	var b strings.Builder
	b.WriteString(`<div class=cam-prop><span class=cam-prop-l>` + html.EscapeString(p.Label) + `</span>`)
	b.WriteString(`<input class="slider-input cam-prop-s" type=range min=` + p.MinS + ` max=` + p.MaxS +
		` step=` + p.StepS + ` value=` + p.ValS + ` data-act=` + attrQ(p.Act) + ` data-value=` + p.ValS + dis + ` ` + oninput + `>`)
	b.WriteString(`<span class=cam-prop-v>` + p.ValS + `</span>`)
	if p.CanAuto {
		checked := ""
		if p.Auto {
			checked = " checked"
		}
		b.WriteString(`<label class=cam-prop-auto><input type=checkbox` + checked + ` data-act=` + attrQ(p.AutoAct) +
			` data-value=` + attrQ(boolStr(p.Auto)) + `>` + html.EscapeString(p.AutoLbl) + `</label>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func peerXferHTML(st peerXferSt) string {
	var b strings.Builder
	b.WriteString(xferSetHTML(st.Settings))
	if st.None {
		b.WriteString(hint("info", st.NoneHint))
		return b.String()
	}
	b.WriteString(`<div class="rp-card">`)
	for _, p := range st.Pend {
		b.WriteString(`<div class=row><span class=row-label>` + html.EscapeString(p.Line) + `</span>` +
			uiBtnRow(p.Btns) + `</div>`)
	}
	for _, r := range st.Rows {
		b.WriteString(xferProgHTML(r))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func xferSetHTML(st xferSetSt) string {
	if !st.Show {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card cam-node">`)
	b.WriteString(st.Enabled.html())
	b.WriteString(`<div class=xfer-mode><span class=field-label>` + html.EscapeString(st.AcceptLbl) + `</span>` +
		subTabs("peers-xfer-mode:", st.Mode, [2]string{"ask", st.AskLbl}, [2]string{"auto", st.AutoLbl}) + `</div>`)
	b.WriteString(st.Dir.html())
	b.WriteString(`<div class=np-artist>` + html.EscapeString(st.DefaultDir) + `</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

func xferProgHTML(r xferProgSt) string {
	right := r.Btn.html()
	if r.IsBadge {
		right = badge(r.Badge, r.BadgeVar)
	}
	sub := `<span class=np-artist>` + html.EscapeString(r.SubText) + `</span>`
	if r.Bar {
		sub = progressBarStr(r.BarPct, r.BarCap)
	}
	return `<div class=xfer-row><div class=row><span class=row-label>` + html.EscapeString(r.Title) + `</span>` + btnRow(right) + `</div>` +
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
