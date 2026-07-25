package webui

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/midimap"
	"rave.page/mate/internal/zigui"
)

// MIDI-in (native MIDI-learn): connect physical DJ controllers, learn each control per channel,
// and feed the shared deck/channel model. Multiple controllers on different ports run at once,
// each with its own map. Optional per-controller THRU re-emits the raw input to a MIDI-OUT
// (a loopMIDI cable the DJ app reads) so rave-mate can read the controller AND the DJ app still
// gets it on single-client Windows MIDI. The two-port DJ bridge routes peer control out to the
// DJ app. Rekordbox can't emit play/cue state over a loopback (Button LED == input → self-loop),
// so we read it straight from the controller instead.
//
// Renderers are split state-builder (impure: config + probe + i18n) vs pure HTML, so the Zig
// port (native/zigui/src/midictl_ctls.zig) renders the same state byte-identically.

// midiCtlRenderCtx is per-render shared state built ONCE per render (not per controller): the
// cached OS-probe snapshot (input/output port enum + driver presence/status) plus latest monitor
// activity by source. Every card + per-controller block reads this - it must never enumerate ports
// or open the driver itself. Port enum + driver ioctls come from midiCtlProbe (off-thread, TTL).
type midiCtlRenderCtx struct {
	inPorts      []string                          // MIDISource.InputPorts() (cached)
	outPorts     []string                          // MIDIEmit.Ports() (cached)
	drvInstalled bool                              // midi.DriverInstalled() (cached)
	oneWay       bool                              // midi.OneWayAvailable() (cached)
	virtualAvail bool                              // midi.VirtualAvailable() (cached)
	drv          map[string]midi.DriverInputStatus // managed-input status by input id (per-controller lookup)
	drvInputs    []midi.DriverInputStatus          // managed inputs in order (driver card list)
	drvQueryErr  bool                              // installed but QueryDriverInputs errored
	ready        bool                              // probe has landed at least once
	act          map[string]logbus.Entry
}

// midiCtlCtx builds the shared render context from the cached OS probe (kicks an off-thread
// refresh when stale). Cheap + pure - no winmm enum, no driver ioctl on the caller's goroutine.
func (u *UI) midiCtlCtx() midiCtlRenderCtx {
	p := u.midiCtlProbeSnapshot()
	ctx := midiCtlRenderCtx{
		inPorts:      p.inPorts,
		outPorts:     p.outPorts,
		drvInstalled: p.drvInstalled,
		oneWay:       p.oneWay,
		virtualAvail: p.virtualAvail,
		drvInputs:    p.drvInputs,
		drvQueryErr:  p.drvQueryErr,
		ready:        p.ready,
		act:          u.midiLastActivity(),
	}
	if len(p.drvInputs) > 0 {
		ctx.drv = make(map[string]midi.DriverInputStatus, len(p.drvInputs))
		for _, st := range p.drvInputs {
			ctx.drv[st.ID] = st // keyed by input id (preserves existing lookup semantics)
		}
	}
	return ctx
}

// ── resolved render state ──

// midiLinkState is one virtual-MIDI driver download link.
type midiLinkState struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// midiPortStat is the #midi-ctlstat-<i> inner state (~1 Hz patch target).
type midiPortStat struct {
	HasRow  bool   `json:"hasRow"` // child reported: render the status row
	Variant string `json:"variant"`
	Label   string `json:"label"`
	LabelDL string `json:"labelDl"`
	Line    string `json:"line"`
	Hint    string `json:"hint"` // "" = none (only the port-in-use branch has one)
	HasAct  bool   `json:"hasAct"`
	Act     string `json:"act"` // resolved "last input {ago}"
	ActMsg  string `json:"actMsg"`
}

// midiChipState is one driver fan-out message-filter chip.
type midiChipState struct {
	Label  string `json:"label"`
	Act    string `json:"act"`
	Active bool   `json:"active"`
}

// midiDrvThru is the driver-managed routing block of one controller.
type midiDrvThru struct {
	Show      bool            `json:"show"`
	UseInDJ   string          `json:"useInDj"`
	Port      string          `json:"port"` // DJ-facing port name
	CloneLbl  string          `json:"cloneLbl"`
	CloneDL   string          `json:"cloneDl"`
	CloneAct  string          `json:"cloneAct"`
	CloneOn   bool            `json:"cloneOn"`
	CloneNote string          `json:"cloneNote"`
	DrvNote   string          `json:"drvNote"`
	HasState  bool            `json:"hasState"`
	StVariant string          `json:"stVariant"`
	StLabel   string          `json:"stLabel"`
	StLabelDL string          `json:"stLabelDl"`
	StLine    string          `json:"stLine"`
	FilterLbl string          `json:"filterLbl"`
	FilterTip string          `json:"filterTip"` // pre-rendered tooltip HTML
	Chips     []midiChipState `json:"chips"`
}

// midiWarnState is a warn statusRow + explanation note (THRU port clash).
type midiWarnState struct {
	Show    bool   `json:"show"`
	Label   string `json:"label"`
	LabelDL string `json:"labelDl"`
	Line    string `json:"line"`
	Hint    string `json:"hint"`
}

// midiLearnCell is one learn chip: bound (readout + clear) or empty.
type midiLearnCell struct {
	Act      string `json:"act"`      // midi-learn:<ctlIdx>:<control>:<ch>
	ClearAct string `json:"clearAct"` // midi-unlearn:<same arg>
	Tid      string `json:"tid"`
	Set      bool   `json:"set"`
	Readout  string `json:"readout"`
}

// midiLearnRow is one control row of the learn grid.
type midiLearnRow struct {
	Label string          `json:"label"`
	Cells []midiLearnCell `json:"cells"`
}

// midiLearnGridState is the controls×channels learn grid.
type midiLearnGridState struct {
	Hdr     string         `json:"hdr"`
	HdrTip  string         `json:"hdrTip"` // pre-rendered tooltip HTML
	Cols    string         `json:"cols"`
	ChHdrs  []string       `json:"chHdrs"`
	Rows    []midiLearnRow `json:"rows"`
	Learn   string         `json:"learn"`
	Relearn string         `json:"relearn"`
	Clear   string         `json:"clear"`
}

// midiCtlBlock is one controller: port + enable + routing + remove, then the learn grid.
type midiCtlBlock struct {
	Tid       string             `json:"tid"`
	Title     string             `json:"title"`
	StatID    string             `json:"statId"` // midi-ctlstat-<i> (tick patch target)
	Port      selState           `json:"port"`
	PortLbl   string             `json:"portLbl"` // pre-rendered ss-label (label + tooltip)
	Stat      midiPortStat       `json:"stat"`
	EnableLbl string             `json:"enableLbl"`
	EnableDL  string             `json:"enableDl"`
	EnableAct string             `json:"enableAct"`
	EnableOn  bool               `json:"enableOn"`
	Thru      selState           `json:"thru"`
	ThruLbl   string             `json:"thruLbl"`
	DrvThru   midiDrvThru        `json:"drvThru"`
	Warn      midiWarnState      `json:"warn"`
	Remove    string             `json:"remove"`
	RemoveAct string             `json:"removeAct"`
	Grid      midiLearnGridState `json:"grid"`
}

// midiCtlsState is the resolved render state for the controllers card.
type midiCtlsState struct {
	Show     bool            `json:"show"` // MIDISource + Cfg wired
	Card     string          `json:"card"`
	Badge    string          `json:"badge"`
	Intro    string          `json:"intro"`
	IntroTip string          `json:"introTip"` // pre-rendered tooltip HTML
	LinksLbl string          `json:"linksLbl"`
	Links    []midiLinkState `json:"links"`
	Empty    string          `json:"empty"`
	Blocks   []midiCtlBlock  `json:"blocks"`
	Add      string          `json:"add"`
}

// midiCtlsState resolves config + probe + i18n into the controllers-card render state.
func (u *UI) midiCtlsState(ctx midiCtlRenderCtx) midiCtlsState {
	st := midiCtlsState{
		Show:     u.svc.MIDISource != nil && u.svc.Cfg != nil,
		Card:     i18n.T("midictl.in.card"),
		Badge:    i18n.T("midictl.in.badge"),
		Intro:    i18n.T("midictl.in.intro"),
		IntroTip: tipTopic("midi-learn-controllers"),
		LinksLbl: i18n.T("midictl.in.getPort"),
		Links:    []midiLinkState{},
		Empty:    i18n.T("midictl.in.empty"),
		Blocks:   []midiCtlBlock{},
		Add:      i18n.T("midictl.in.add"),
	}
	if !st.Show {
		return st
	}
	for _, l := range virtualMIDILinks() {
		st.Links = append(st.Links, midiLinkState{Label: l.Label, URL: l.URL})
	}
	ctrls := u.svc.Cfg.Features.MIDI.Controllers
	for i := range ctrls {
		st.Blocks = append(st.Blocks, u.midiCtlBlockState(i, ctrls[i], ctx))
	}
	return st
}

// midiCtlsHTML is the pure controllers-card renderer (golden reference).
func midiCtlsHTML(st midiCtlsState) string {
	if !st.Show {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<p class=midi-help-note>` + htmlEscape(st.Intro) + ` ` + st.IntroTip + `</p>`)
	b.WriteString(midiLinksHTML(st.LinksLbl, st.Links))
	if len(st.Blocks) == 0 {
		b.WriteString(emptyState(st.Empty))
	}
	for _, bl := range st.Blocks {
		b.WriteString(midiCtlBlockHTML(bl))
	}
	b.WriteString(btnRow(btn(st.Add, "primary", "midi-ctl-add", "")))
	return card(st.Card, badge(st.Badge, "info"), b.String())
}

// midiLinksHTML renders the "need a virtual MIDI port?" line with the driver download
// links (same list as the tooltips). Gives users flexibility over which loopback driver to use.
func midiLinksHTML(lbl string, links []midiLinkState) string {
	var b strings.Builder
	b.WriteString(`<p class=midi-driver-links><span class=midi-driver-lbl>` + htmlEscape(lbl) + `</span> `)
	for i, l := range links {
		if i > 0 {
			b.WriteString(` · `)
		}
		b.WriteString(`<a href=` + attrQ(l.URL) + ` target=_blank rel=noopener>` + htmlEscape(l.Label) + `</a>`)
	}
	b.WriteString(`</p>`)
	return b.String()
}

// midiOwnDriverPorts collects the names of ports the ravemidi driver derives from the
// current config (reserved + fan-outs) - internal/DJ-facing, never controller INPUTs.
func (u *UI) midiOwnDriverPorts() map[string]bool {
	out := map[string]bool{}
	for _, c := range u.svc.Cfg.Features.MIDI.Controllers {
		if c.ThruPort == midi.DriverSentinel && c.Name != "" {
			out[strings.ToLower(midi.ReservedPortName(c.Name))] = true
			// Only hide the fan-out when it has a DISTINCT name ("<Name> THRU"). In clone
			// mode the fan-out is named after the real device (== c.Port), which the user
			// legitimately picks as the input - hiding it would drop the real controller
			// from the picker. Loop-safe either way: driver-managed reads the reserved port.
			if dj := midi.DJPortName(c.Name, c.Port, c.ThruDistinctName); !strings.EqualFold(dj, c.Port) {
				out[strings.ToLower(dj)] = true
			}
		}
	}
	return out
}

// midiCtlBlockState resolves one controller's block (port/thru pickers registered here).
func (u *UI) midiCtlBlockState(i int, c config.MIDIControllerMap, ctx midiCtlRenderCtx) midiCtlBlock {
	idx := strconv.Itoa(i)
	own := u.midiOwnDriverPorts()
	portOpts := [][2]string{{"", i18n.T("midictl.in.pickPort")}}
	seen := map[string]bool{}
	for _, p := range ctx.inPorts { // cached enum - never enumerate winmm per controller
		lp := strings.ToLower(p)
		if p == midi.VirtualDJPortName || p == midi.VirtualMixerPortName ||
			own[lp] || strings.Contains(p, "(rave-mate)") {
			continue // our own virtual ports - reading them back would loop through rave-mate
		}
		if seen[lp] {
			continue // dedup: a clone-mode driver fan-out shares the real device's winmm name
		}
		seen[lp] = true
		portOpts = append(portOpts, [2]string{p, p})
	}
	thruOpts := [][2]string{{"", i18n.T("midictl.in.thruNone")}}
	// ravemidi driver first (recommended): the DRIVER taps the hardware and fans it
	// out loop-free; forwarding survives rave-mate exit and reboots.
	if ctx.drvInstalled {
		thruOpts = append(thruOpts, [2]string{midi.DriverSentinel, i18n.T("midictl.in.thruDriver")})
	}
	// Built-in one-way port: the DJ app sees an input-only port, so its automatic
	// LED echo has no output endpoint to loop back through.
	if ctx.oneWay {
		thruOpts = append(thruOpts, [2]string{midi.VirtualDJSentinel, i18n.T("midictl.in.thruVirtual")})
	}
	for _, p := range ctx.outPorts { // cached enum
		thruOpts = append(thruOpts, [2]string{p, p})
	}
	portSel, portLbl := resolveSelectBoxTip(i18n.T("midictl.in.port"), "midi-ctl-port:"+idx, portOpts, c.Port, "midi-in-port")
	thruSel, thruLbl := resolveSelectBoxTip(i18n.T("midictl.in.thru"), "midi-ctl-thru:"+idx, thruOpts, c.ThruPort, "midi-thru")
	title := c.Name
	if title == "" {
		title = i18n.T("midictl.in.newCtl")
	}
	enLbl := i18n.T("midictl.in.enabled")
	return midiCtlBlock{
		Tid: "midi-ctl-" + idx, Title: title, StatID: "midi-ctlstat-" + idx,
		Port: portSel, PortLbl: portLbl,
		Stat:      u.midiPortStatState(c, ctx),
		EnableLbl: enLbl, EnableDL: strings.ToLower(enLbl),
		EnableAct: "midi-ctl-enable:" + idx, EnableOn: c.Enabled,
		Thru: thruSel, ThruLbl: thruLbl,
		DrvThru:   u.midiDrvThruState(i, c, ctx),
		Warn:      u.midiThruWarnState(i, c),
		Remove:    i18n.T("midictl.in.remove"),
		RemoveAct: "midi-ctl-remove:" + idx,
		Grid:      u.midiLearnGridState(i, c),
	}
}

// midiCtlBlockHTML is the pure one-controller renderer.
func midiCtlBlockHTML(c midiCtlBlock) string {
	head := selHTMLRaw(c.Port, c.PortLbl) +
		`<div id="` + c.StatID + `">` + midiPortStatHTML(c.Stat) + `</div>` +
		toggleRowDL(c.EnableLbl, c.EnableDL, c.EnableAct, c.EnableOn) +
		selHTMLRaw(c.Thru, c.ThruLbl) +
		midiDrvThruHTML(c.DrvThru) +
		midiWarnHTML(c.Warn) +
		btnRow(btn(c.Remove, "warn", c.RemoveAct, ""))
	return `<div class=midi-ctlblock data-testid=` + attrQ(c.Tid) + `>` +
		`<div class=midi-ctlhead>` + htmlEscape(c.Title) + `</div>` + head +
		midiLearnGridHTML(c.Grid) + `</div>`
}

// midiDrvThruState resolves the driver-managed routing block for a controller whose
// THRU is the ravemidi driver: which port the DJ software must use, live bind state,
// and the fan-out message-filter chips. Show=false for other THRU modes.
func (u *UI) midiDrvThruState(i int, c config.MIDIControllerMap, ctx midiCtlRenderCtx) midiDrvThru {
	if c.ThruPort != midi.DriverSentinel {
		return midiDrvThru{Chips: []midiChipState{}}
	}
	idx := strconv.Itoa(i)
	cloneLbl := i18n.T("midictl.in.cloneName")
	st := midiDrvThru{
		Show: true,
		// the ONE port to select in the DJ software - the core "which device do I use" answer.
		// Name tracks the clone toggle below: the device's own name (clone) or "<Name> THRU".
		UseInDJ: i18n.T("midictl.in.useInDJ"),
		Port:    midi.DJPortName(c.Name, c.Port, c.ThruDistinctName),
		// clone toggle (default ON): mirror the controller's own name to DJ software so name-keyed
		// mappings (Serato) match. Off = a distinct "<Name> THRU" port. Explained inline (the "why").
		CloneLbl: cloneLbl, CloneDL: strings.ToLower(cloneLbl),
		CloneAct: "midi-ctl-clone:" + idx, CloneOn: !c.ThruDistinctName,
		CloneNote: i18n.T("midictl.in.cloneNote"),
		DrvNote:   i18n.T("midictl.in.driverNote"),
		FilterLbl: i18n.T("midictl.in.filterLbl"),
		FilterTip: tipTopic("midi-drv-filter"),
		Chips:     []midiChipState{},
	}
	drvLbl := i18n.T("midictl.in.driverState")
	if ds, ok := ctx.drv[c.Name]; ok {
		variant, line := "warning", i18n.T("midictl.drv.retrying", i18n.A{"n": strconv.Itoa(int(ds.RetryCount))})
		if ds.Bound {
			variant, line = "success", i18n.T("midictl.drv.bound")
			if ds.FeedbackBound {
				line += " · " + i18n.T("midictl.drv.feedback")
			}
		}
		st.HasState, st.StVariant, st.StLine = true, variant, line
	} else if ctx.drvInstalled {
		st.HasState, st.StVariant, st.StLine = true, "muted", i18n.T("midictl.in.driverPending")
	}
	if st.HasState {
		st.StLabel, st.StLabelDL = drvLbl, strings.ToLower(drvLbl)
	}
	// fan-out filter chips: default drops mapping-hostile clutter (aftertouch caught by
	// MIDI-learn = "every key fires the binding")
	fl := c.DriverFilter
	if fl == nil {
		fl = midi.DefaultDriverFilter()
	}
	on := map[string]bool{}
	for _, k := range fl {
		on[k] = true
	}
	for _, f := range midi.FilterKeys {
		st.Chips = append(st.Chips, midiChipState{
			Label: i18n.T("midictl.filter." + f.Key), Act: "midi-ctl-filter:" + idx + ":" + f.Key, Active: on[f.Key],
		})
	}
	return st
}

// midiDrvThruHTML is the pure driver-managed routing renderer.
func midiDrvThruHTML(st midiDrvThru) string {
	if !st.Show {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=midi-drvthru>`)
	b.WriteString(`<div class=midi-drvuse>` + htmlEscape(st.UseInDJ) + ` <code>` + htmlEscape(st.Port) + `</code></div>`)
	b.WriteString(toggleRowDL(st.CloneLbl, st.CloneDL, st.CloneAct, st.CloneOn))
	b.WriteString(`<p class=midi-help-note>` + htmlEscape(st.CloneNote) + `</p>`)
	b.WriteString(`<p class=midi-help-note>` + htmlEscape(st.DrvNote) + `</p>`)
	if st.HasState {
		b.WriteString(statusRowDL(st.StVariant, st.StLabel, st.StLabelDL, st.StLine))
	}
	b.WriteString(`<div class=midi-drvfilters><span class=midi-steplbl>` +
		htmlEscape(st.FilterLbl) + ` ` + st.FilterTip + `</span>`)
	for _, ch := range st.Chips {
		b.WriteString(fchip(ch.Label, "", ch.Act, ch.Active))
	}
	b.WriteString(`</div></div>`)
	return b.String()
}

// midiChildPort is the input the midi child actually opens for c: the driver's hidden
// reserved endpoint when driver-managed (the kernel holds the hardware), else the
// configured port. Every UI surface that talks to the child about "this controller's
// port" (status, learn) MUST use this - the raw hardware name never matches.
func midiChildPort(c config.MIDIControllerMap) string {
	return midiChildPortFor(c, midi.DriverInstalled())
}

// midiChildPortFor is midiChildPort with the driver-installed flag supplied by the caller, so the
// render + ~1 Hz tick path resolve the child port from the cached probe (ctx.drvInstalled) instead
// of opening the driver per call. The one-shot action path keeps the direct probe via midiChildPort.
func midiChildPortFor(c config.MIDIControllerMap, drvInstalled bool) string {
	if c.ThruPort == midi.DriverSentinel && drvInstalled {
		return midi.ReservedPortName(c.Name)
	}
	return c.Port
}

// midiPortStatState resolves the open/failed status for the controller's input port. Only
// shown once the MIDI child has reported (some port opened or failed); "in use" points at the
// exact fix (Windows single-client MIDI: close the other app, or route via loopMIDI THRU). No
// tooltip here - this region is live-patched (~1 Hz), which would wipe a pinned tooltip; the
// full explanation lives on the port select's ⓘ. It flips to "reading" when auto-retry recovers
// the port after the holding app releases it. Driver-managed controllers read the reserved
// per-input port (the driver holds the hardware), so status checks that port instead.
// A live activity line (latest decoded message + age) answers "which device is THIS one".
func (u *UI) midiPortStatState(c config.MIDIControllerMap, ctx midiCtlRenderCtx) midiPortStat {
	var st midiPortStat
	if c.Port == "" || !c.Enabled || u.svc.MIDISource == nil {
		return st
	}
	want := midiChildPortFor(c, ctx.drvInstalled)
	open := u.svc.MIDISource.OpenInputPorts()
	failed := u.svc.MIDISource.FailedInputPorts()
	switch {
	case len(open) == 0 && len(failed) == 0:
		// child hasn't reported yet - don't guess
	case portContains(open, want):
		st.HasRow, st.Variant, st.Line = true, "ok", i18n.T("midictl.in.portReading")
	default:
		st.HasRow, st.Variant, st.Line = true, "warn", i18n.T("midictl.in.portInUseShort")
		st.Hint = i18n.T("midictl.in.portInUseHint")
	}
	if st.HasRow {
		lbl := i18n.T("midictl.in.portStatus")
		st.Label, st.LabelDL = lbl, strings.ToLower(lbl)
	}
	if e, ok := ctx.act[c.Name]; ok {
		st.HasAct = true
		st.Act = i18n.T("midictl.in.lastInput", i18n.A{"ago": agoShort(e.Time)})
		st.ActMsg = e.Msg
	}
	return st
}

// midiCtlPortStatusInner renders the #midi-ctlstat-<i> inner fragment (~1 Hz tick).
func (u *UI) midiCtlPortStatusInner(c config.MIDIControllerMap, ctx midiCtlRenderCtx) string {
	st := u.midiPortStatState(c, ctx)
	if zigui.Available() {
		if h, ok := zigui.RenderMIDICtlStat(stateJSON(st)); ok {
			return h
		}
	}
	return midiPortStatHTML(st)
}

// midiPortStatHTML is the pure controller-port-status renderer.
func midiPortStatHTML(st midiPortStat) string {
	out := ""
	if st.HasRow {
		out = statusRowDL(st.Variant, st.Label, st.LabelDL, st.Line)
		if st.Hint != "" {
			out += `<p class=midi-help-note>` + htmlEscape(st.Hint) + `</p>`
		}
	}
	if st.HasAct {
		out += `<div class=midi-activity><span class=midi-actdot></span>` + htmlEscape(st.Act) +
			` <span class=midi-actmsg>` + htmlEscape(st.ActMsg) + `</span></div>`
	}
	return out
}

// midiThruWarnState warns when a controller's THRU port is one rave-mate itself reads (its Traktor
// custom-map / Denon input, this or another controller's input, or the bridge from-DJ port).
// Windows MIDI-in is single-client, so a port rave-mate holds can't ALSO be opened by the DJ
// app - the THRU lands somewhere the DJ app can never read, and MIDI-learn silently sees
// nothing. The fix is a DEDICATED virtual cable for the THRU. Show=false = no clash.
func (u *UI) midiThruWarnState(self int, c config.MIDIControllerMap) midiWarnState {
	if c.ThruPort == "" || u.svc.Cfg == nil {
		return midiWarnState{}
	}
	tp := strings.ToLower(strings.TrimSpace(c.ThruPort))
	if tp == "" {
		return midiWarnState{}
	}
	warn := func() midiWarnState {
		lbl := i18n.T("midictl.in.thruClash")
		return midiWarnState{
			Show: true, Label: lbl, LabelDL: strings.ToLower(lbl),
			Line: i18n.T("midictl.in.thruClashShort"), Hint: i18n.T("midictl.in.thruClashHint"),
		}
	}
	if tp == strings.ToLower(midi.VirtualDJSentinel) {
		// One-way port: no output endpoint exists, so nothing can loop - unless the
		// controller INPUT is (hand-configured to) our own virtual port.
		if !strings.EqualFold(strings.TrimSpace(c.Port), midi.VirtualDJPortName) {
			return midiWarnState{}
		}
		return warn()
	}
	same := func(p string) bool {
		p = strings.ToLower(strings.TrimSpace(p))
		return p != "" && p == tp
	}
	m := u.svc.Cfg.Features.MIDI
	clash := same(m.CustomPort) || same(m.DenonPort) || same(m.Bridge.FromDJPort) || same(c.Port)
	if !clash {
		for i, o := range m.Controllers {
			if i != self && same(o.Port) {
				clash = true
				break
			}
		}
	}
	if !clash {
		return midiWarnState{}
	}
	return warn()
}

// midiWarnHTML is the pure THRU-clash warning renderer.
func midiWarnHTML(st midiWarnState) string {
	if !st.Show {
		return ""
	}
	return statusRowDL("warn", st.Label, st.LabelDL, st.Line) +
		`<p class=midi-help-note>` + htmlEscape(st.Hint) + `</p>`
}

// midiLearnGridState resolves the controls×channels grid of learn chips (rows = controls,
// cols = channels).
func (u *UI) midiLearnGridState(ctlIdx int, c config.MIDIControllerMap) midiLearnGridState {
	n := u.midiChannels()
	st := midiLearnGridState{
		Hdr: i18n.T("midictl.in.learnHdr"), HdrTip: tipTopic("midi-learn-grid"),
		Cols: strconv.Itoa(n), ChHdrs: []string{}, Rows: []midiLearnRow{},
		Learn: i18n.T("midictl.in.learn"), Relearn: i18n.T("midictl.in.relearn"), Clear: i18n.T("midictl.in.clear"),
	}
	for ch := 1; ch <= n; ch++ {
		st.ChHdrs = append(st.ChHdrs, i18n.T("midictl.in.chShort", i18n.A{"n": strconv.Itoa(ch)}))
	}
	for _, ctl := range midimap.Controls {
		row := midiLearnRow{Label: i18n.T("midictl.ctl." + ctl.LabelKey), Cells: []midiLearnCell{}}
		for ch := 1; ch <= n; ch++ {
			arg := fmt.Sprintf("%d:%s:%d", ctlIdx, ctl.ID, ch)
			cell := midiLearnCell{
				Act: "midi-learn:" + arg, ClearAct: "midi-unlearn:" + arg,
				Tid: fmt.Sprintf("midi-learn-%d-%s-%d", ctlIdx, ctl.ID, ch),
			}
			if bd, ok := findBinding(c, ctl.ID, ch); ok {
				cell.Set, cell.Readout = true, bindingReadout(bd)
			}
			row.Cells = append(row.Cells, cell)
		}
		st.Rows = append(st.Rows, row)
	}
	return st
}

// midiLearnGridHTML is the pure learn-grid renderer.
func midiLearnGridHTML(st midiLearnGridState) string {
	var b strings.Builder
	b.WriteString(`<div class=midi-learnhdr>` + htmlEscape(st.Hdr) + ` ` + st.HdrTip + `</div>`)
	b.WriteString(`<div class=midi-learngrid style="--cols:` + st.Cols + `">`)
	b.WriteString(`<div class=mlg-h></div>`)
	for _, h := range st.ChHdrs {
		b.WriteString(`<div class=mlg-h>` + htmlEscape(h) + `</div>`)
	}
	for _, r := range st.Rows {
		b.WriteString(`<div class=mlg-rowlbl>` + htmlEscape(r.Label) + `</div>`)
		for _, cell := range r.Cells {
			b.WriteString(midiLearnCellHTML(st, cell))
		}
	}
	b.WriteString(`</div>`)
	return b.String()
}

// midiLearnCellHTML renders one learn chip: bound (shows the MIDI + a clear ✕) or an empty "Learn".
func midiLearnCellHTML(g midiLearnGridState, c midiLearnCell) string {
	if c.Set {
		return `<span class=mlg-cell>` +
			`<button class="mlg-chip mlg-chip--set" data-act=` + attrQ(c.Act) + ` data-testid=` + attrQ(c.Tid) +
			` title=` + attrQ(g.Relearn) + `>` + htmlEscape(c.Readout) + `</button>` +
			`<button class="mlg-clear" data-act=` + attrQ(c.ClearAct) + ` aria-label=` + attrQ(g.Clear) + `>✕</button>` +
			`</span>`
	}
	return `<button class=mlg-chip data-act=` + attrQ(c.Act) + ` data-testid=` + attrQ(c.Tid) + `>` +
		htmlEscape(g.Learn) + `</button>`
}

// findBinding returns the binding for (control, channel) on c, if learned.
func findBinding(c config.MIDIControllerMap, control string, ch int) (config.MIDIBinding, bool) {
	for _, b := range c.Bindings {
		if b.Control == control && b.Channel == ch {
			return b, true
		}
	}
	return config.MIDIBinding{}, false
}

// bindingReadout formats a learned binding as "CC24" / "N20" for the chip.
func bindingReadout(b config.MIDIBinding) string {
	if b.Status&0xF0 == 0xB0 {
		return "CC" + strconv.Itoa(int(b.Data1))
	}
	return "N" + strconv.Itoa(int(b.Data1))
}

// midiBridgeState is the resolved render state for the two-port DJ bridge card.
type midiBridgeState struct {
	Show      bool     `json:"show"`
	Card      string   `json:"card"`
	Badge     string   `json:"badge"`
	Intro     string   `json:"intro"`
	IntroTip  string   `json:"introTip"` // pre-rendered tooltip HTML
	EnableLbl string   `json:"enableLbl"`
	EnableDL  string   `json:"enableDl"`
	EnableAct string   `json:"enableAct"`
	EnableOn  bool     `json:"enableOn"`
	EnableTip string   `json:"enableTip"`
	ToDJ      selState `json:"toDj"`
	ToDJLbl   string   `json:"toDjLbl"`
	FromDJ    selState `json:"fromDj"`
	FromDJLbl string   `json:"fromDjLbl"`
}

// midiBridgeState resolves the two-port loopMIDI DJ router (peer control → DJ; DJ output → us).
func (u *UI) midiBridgeState(ctx midiCtlRenderCtx) midiBridgeState {
	if u.svc.Cfg == nil || u.svc.MIDISource == nil {
		return midiBridgeState{ToDJ: emptySel(), FromDJ: emptySel()}
	}
	br := u.svc.Cfg.Features.MIDI.Bridge
	inOpts := [][2]string{{"", i18n.T("midictl.in.thruNone")}}
	for _, p := range ctx.inPorts { // cached enum
		if p == midi.VirtualDJPortName || p == midi.VirtualMixerPortName {
			continue // our own one-way ports - reading them back would loop through rave-mate
		}
		inOpts = append(inOpts, [2]string{p, p})
	}
	outOpts := [][2]string{{"", i18n.T("midictl.in.thruNone")}}
	if ctx.oneWay { // same one-way port as THRU (shared instance in the child)
		outOpts = append(outOpts, [2]string{midi.VirtualDJSentinel, i18n.T("midictl.in.thruVirtual")})
	}
	for _, p := range ctx.outPorts { // cached enum
		outOpts = append(outOpts, [2]string{p, p})
	}
	enLbl := i18n.T("midictl.bridge.enable")
	toSel, toLbl := resolveSelectBoxTip(i18n.T("midictl.bridge.todj"), "midi-bridge-todj", outOpts, br.ToDJPort, "midi-bridge")
	fromSel, fromLbl := resolveSelectBoxTip(i18n.T("midictl.bridge.fromdj"), "midi-bridge-fromdj", inOpts, br.FromDJPort, "midi-bridge")
	return midiBridgeState{
		Show: true, Card: i18n.T("midictl.bridge.card"), Badge: i18n.T("midictl.bridge.badge"),
		Intro: i18n.T("midictl.bridge.intro"), IntroTip: tipTopic("midi-bridge"),
		EnableLbl: enLbl, EnableDL: strings.ToLower(enLbl), EnableAct: "midi-bridge-enable",
		EnableOn: br.Enabled, EnableTip: tipTopic("midi-bridge"),
		ToDJ: toSel, ToDJLbl: toLbl, FromDJ: fromSel, FromDJLbl: fromLbl,
	}
}

// midiBridgeHTML is the pure bridge-card renderer.
func midiBridgeHTML(st midiBridgeState) string {
	if !st.Show {
		return ""
	}
	body := `<p class=midi-help-note>` + htmlEscape(st.Intro) + ` ` + st.IntroTip + `</p>` +
		toggleRowTipDL(st.EnableLbl, st.EnableDL, st.EnableAct, st.EnableOn, st.EnableTip) +
		selHTMLRaw(st.ToDJ, st.ToDJLbl) +
		selHTMLRaw(st.FromDJ, st.FromDJLbl)
	return card(st.Card, badge(st.Badge, "info"), body)
}
