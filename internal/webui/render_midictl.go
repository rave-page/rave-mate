package webui

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/midimap"
	"rave.page/mate/internal/zigui"
)

// MIDI Mixer panel: a visual DJ-mixer surface (per-channel EQ Hi/Mid/Low, Filter, Trim knobs +
// a vertical Fader + Play/Cue) that SENDS MIDI to a loopback port so a DJ app (Rekordbox / Serato
// etc.) can MIDI-learn custom mappings. Send-on-interaction only - never a continuous stream. The
// send CCs are the shared midimap contract, byte-identical to the receive decoder
// (session/sources/midisrc/custom.go), so a learned mapping's output/feedback echoes back on the
// same CC and drives the overlays. ctl-drivable: every control carries data-testid=midi-ch<n>-<id>.
//
// midictl is a Zig-rendered tab (native/zigui/src/midictl.zig + midictl_ctls.zig +
// midictl_uimap.zig + midimon.zig): Go resolves the whole tab into midiCtlState (data + probe +
// RESOLVED i18n + pre-rendered tooltips), Zig renders HTML byte-identical to the pure Go
// renderers below, which stay as fallback + golden reference (zigui_golden_midictl_test.go).

// midiActiveState is the #midi-active status line (~1 Hz patch target).
type midiActiveState struct {
	Variant string `json:"variant"`
	Label   string `json:"label"`
	LabelDL string `json:"labelDl"`
	Line    string `json:"line"`
}

// midiPortCard is the output-port card: selector, live active-port line, Panic.
type midiPortCard struct {
	Card   string          `json:"card"`
	Sub    string          `json:"sub"`
	Port   selState        `json:"port"`
	Active midiActiveState `json:"active"`
	Panic  string          `json:"panic"`
}

// midiDrvInput is one driver-managed input's live status + per-port controls.
type midiDrvInput struct {
	Variant   string `json:"variant"`
	Name      string `json:"name"`
	NameDL    string `json:"nameDl"`
	Line      string `json:"line"`
	FbHint    string `json:"fbHint"` // "" = none (bound without feedback names WHY)
	HasBtns   bool   `json:"hasBtns"`
	TraceLbl  string `json:"traceLbl"`
	TraceAct  string `json:"traceAct"`
	FbTest    bool   `json:"fbTest"`
	FbTestLbl string `json:"fbTestLbl"`
	FbTestAct string `json:"fbTestAct"`
	FbTip     string `json:"fbTip"`             // legacy RAW pre-rendered tooltip markup (bridge)
	FbTipS    *tipSt `json:"fbTipSt,omitempty"` // structured tooltip - wins over FbTip
	FbRes     bool   `json:"fbRes"`
	FbResVar  string `json:"fbResVar"`
	FbResLbl  string `json:"fbResLbl"`
	FbResDL   string `json:"fbResDl"`
	FbResLine string `json:"fbResLine"`
}

// midiDrvManaged is the managed-input block (driver installed).
type midiDrvManaged struct {
	Hdr         string         `json:"hdr"`
	Sub         string         `json:"sub"`
	SyncErr     string         `json:"syncErr"` // "" = none
	HasQueryErr bool           `json:"hasQueryErr"`
	QueryErr    string         `json:"queryErr"`
	NoneManaged string         `json:"noneManaged"`
	Inputs      []midiDrvInput `json:"inputs"`
	ShowTrace   bool           `json:"showTrace"`
	Trace       midiTraceState `json:"trace"`
	Reapply     string         `json:"reapply"`
	Reload      string         `json:"reload"`
}

// midiDrvCard is the ravemidi kernel-driver card (Windows only).
type midiDrvCard struct {
	Show        bool           `json:"show"`
	Card        string         `json:"card"`
	Badge       string         `json:"badge"`
	BadgeVar    string         `json:"badgeVar"`
	Why         string         `json:"why"`
	StVariant   string         `json:"stVariant"`
	StLabel     string         `json:"stLabel"`
	StLabelDL   string         `json:"stLabelDl"`
	StLine      string         `json:"stLine"`
	Installed   bool           `json:"installed"`
	TestSign    string         `json:"testSign"`
	Steps       string         `json:"steps"`
	Cmds        string         `json:"cmds"`
	SmartScreen string         `json:"smartScreen"`
	Managed     midiDrvManaged `json:"managed"`
	Docs        string         `json:"docs"`
	DocsURL     string         `json:"docsUrl"`
}

// midiKnobState is one continuous control (knob or fader). Numeric CSS values are
// formatted Go-side (trimNum) so the Zig renderer never re-derives a float.
type midiKnobState struct {
	DL         string `json:"dl"`
	V          string `json:"v"`
	Rot        string `json:"rot"` // knob only
	Val        string `json:"val"`
	Act        string `json:"act"`
	Tid        string `json:"tid"`
	Aria       string `json:"aria"`
	Label      string `json:"label"`
	CC         string `json:"cc"`
	SweepAct   string `json:"sweepAct"`
	SweepTitle string `json:"sweepTitle"`
	SweepAria  string `json:"sweepAria"`
	SweepGlyph string `json:"sweepGlyph"`
}

// midiMomState is one momentary Play/Cue pill.
type midiMomState struct {
	Cls   string `json:"cls"`
	Act   string `json:"act"`
	Tid   string `json:"tid"`
	DL    string `json:"dl"`
	Aria  string `json:"aria"`
	Label string `json:"label"`
	CC    string `json:"cc"`
}

// midiStripState is one channel strip: head, knobs, fader(s), Play/Cue.
type midiStripState struct {
	Head   string          `json:"head"`
	Knobs  []midiKnobState `json:"knobs"`
	Faders []midiKnobState `json:"faders"`
	Btns   []midiMomState  `json:"btns"`
}

// midiRackState is the channel-count stepper + the channel rack.
type midiRackState struct {
	Card     string           `json:"card"`
	StepLbl  string           `json:"stepLbl"`
	N        string           `json:"n"`
	Dec      string           `json:"dec"`
	Inc      string           `json:"inc"`
	MinusOff bool             `json:"minusOff"`
	PlusOff  bool             `json:"plusOff"`
	Sub      string           `json:"sub"`
	Strips   []midiStripState `json:"strips"`
}

// midiSwRow is one per-DJ-software maturity row.
type midiSwRow struct {
	Name     string `json:"name"`
	Badge    string `json:"badge"`
	BadgeVar string `json:"badgeVar"`
	Note     string `json:"note"`
}

// midiHelpState is the send-to-learn walkthrough + software matrix.
type midiHelpState struct {
	Card     string      `json:"card"`
	Badge    string      `json:"badge"`
	Step1    string      `json:"step1"`
	Step2    string      `json:"step2"`
	Step3    string      `json:"step3"`
	Feedback string      `json:"feedback"`
	Caveat   string      `json:"caveat"`
	Link     string      `json:"link"`
	SwHdr    string      `json:"swHdr"`
	Rows     []midiSwRow `json:"rows"`
}

// midiCtlState is the resolved render state for the whole MIDI tab.
type midiCtlState struct {
	Title   string          `json:"title"`
	Sub     string          `json:"sub"`
	Ctls    midiCtlsState   `json:"ctls"`
	UIMap   umState         `json:"uimap"`
	ShowMon bool            `json:"showMon"`
	Mon     midiMonState    `json:"mon"`
	Port    midiPortCard    `json:"port"`
	Driver  midiDrvCard     `json:"driver"`
	Rack    midiRackState   `json:"rack"`
	Bridge  midiBridgeState `json:"bridge"`
	Help    midiHelpState   `json:"help"`
}

// midiCtlState resolves the whole tab (ONE cached OS-probe snapshot for ports + driver status).
func (u *UI) midiCtlState() midiCtlState {
	ctx := u.midiCtlCtx()
	return midiCtlState{
		Title:   i18n.T("tab.midictl"),
		Sub:     i18n.T("midictl.subtitle"),
		Ctls:    u.midiCtlsState(ctx), // native MIDI-learn: read physical controllers (input)
		UIMap:   u.umState(),          // map controller notes/CCs/encoders to app actions
		ShowMon: u.svc.MIDIMon != nil, // live input monitor ("which device is which")
		Mon:     u.midiMonitorState(),
		Port:    u.midiPortCardState(ctx),
		Driver:  u.midiDrvCardState(ctx),
		Rack:    u.midiRackState(),
		Bridge:  u.midiBridgeState(ctx),
		Help:    midiHelpStateOf(),
	}
}

func (u *UI) renderMIDICtl() string {
	if u.svc.MIDIEmit == nil {
		return u.renderPlaceholder("midictl")
	}
	st := u.midiCtlState()
	if zigui.Available() {
		if h, ok := zigWire("RenderMIDICtlV2", wireMidiCtl(st), zigui.RenderMIDICtlV2,
			zigui.RenderMIDICtl, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return midiCtlHTML(st)
}

// midiCtlHTML is the pure whole-tab renderer (golden reference; byte-identical to Zig).
func midiCtlHTML(st midiCtlState) string {
	var b strings.Builder
	b.WriteString(panel(st.Title, st.Sub))
	b.WriteString(midiCtlsHTML(st.Ctls))
	b.WriteString(umHTML(st.UIMap))
	if st.ShowMon {
		b.WriteString(midiMonHTML(st.Mon))
	}
	// output + driver / bridge + help: small cards pair up ≥1100px (.midi-2col)
	b.WriteString(`<div class=midi-2col>` + midiPortCardHTML(st.Port) + midiDrvCardHTML(st.Driver) + `</div>`)
	b.WriteString(midiRackHTML(st.Rack))
	b.WriteString(`<div class=midi-2col>` + midiBridgeHTML(st.Bridge) + midiHelpHTML(st.Help) + `</div>`)
	return b.String()
}

// midiPortCardState resolves the output-port selector + active-port line + Panic.
func (u *UI) midiPortCardState(ctx midiCtlRenderCtx) midiPortCard {
	e := u.svc.MIDIEmit
	opts := [][2]string{{"", i18n.T("midictl.autoPort")}}
	// Built-in one-way port first: kills the DJ app's LED-echo self-loop (ravemidi driver
	// preferred, teVirtualMIDI fallback - see midi.OpenOneWayOut).
	if ctx.oneWay {
		opts = append(opts, [2]string{midi.VirtualDJSentinel, i18n.T("midictl.in.thruVirtual")})
	}
	for _, p := range ctx.outPorts {
		opts = append(opts, [2]string{p, p})
	}
	return midiPortCard{
		Card:   i18n.T("midictl.outputCard"),
		Sub:    i18n.T("midictl.out.sub"),
		Port:   resolveSelectBox(i18n.T("midictl.port"), "midi-port", opts, e.Want()),
		Active: midiActiveStateOf(e.ActivePort()),
		Panic:  i18n.T("midictl.panic"),
	}
}

// midiPortCardHTML is the pure output-port-card renderer.
func midiPortCardHTML(st midiPortCard) string {
	body := `<p class=page-sub>` + htmlEscape(st.Sub) + `</p>` +
		selHTML(st.Port) +
		`<div id=midi-active>` + midiActiveRowHTML(st.Active) + `</div>` +
		btnRow(btn(st.Panic, "warn", "midi-panic", ""))
	return card(st.Card, "", body)
}

// midiActiveStateOf resolves the active-port status line.
func midiActiveStateOf(active string) midiActiveState {
	variant := "ok"
	if active == "" {
		variant, active = "off", i18n.T("midictl.portNotOpen")
	}
	lbl := i18n.T("midictl.activePort")
	return midiActiveState{Variant: variant, Label: lbl, LabelDL: strings.ToLower(lbl), Line: active}
}

// midiActiveRow renders the resolved active-port status line (patched ~1 Hz via #midi-active).
func midiActiveRow(active string) string {
	st := midiActiveStateOf(active)
	if zigui.Available() {
		if h, ok := zigWire("RenderMIDIActiveV2", wireMidiActive(st), zigui.RenderMIDIActiveV2,
			zigui.RenderMIDIActive, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return midiActiveRowHTML(st)
}

// midiActiveRowHTML is the pure active-port-line renderer.
func midiActiveRowHTML(st midiActiveState) string {
	return statusRowDL(st.Variant, st.Label, st.LabelDL, st.Line)
}

// midiDrvCardState resolves ravemidi kernel-driver status + the self-signed install walkthrough.
// Only rendered on Windows (the driver is a Windows WDM/PortCls component).
func (u *UI) midiDrvCardState(ctx midiCtlRenderCtx) midiDrvCard {
	st := midiDrvCard{Managed: midiDrvManaged{Inputs: []midiDrvInput{}, Trace: midiTraceState{Rows: []midiTraceRow{}}}}
	if runtime.GOOS != "windows" {
		return st
	}
	installed := ctx.drvInstalled
	drvLbl := i18n.T("midictl.drv.status")
	st.Show, st.Installed = true, installed
	st.Card = i18n.T("midictl.drv.card")
	st.Why = i18n.T("midictl.drv.why")
	st.StLabel, st.StLabelDL = drvLbl, strings.ToLower(drvLbl)
	switch {
	case installed:
		st.StVariant, st.StLine = "success", i18n.T("midictl.drv.installed")
	case ctx.virtualAvail:
		st.StVariant, st.StLine = "warning", i18n.T("midictl.drv.fallback")
	default:
		st.StVariant, st.StLine = "muted", i18n.T("midictl.drv.none")
	}
	if !installed {
		st.TestSign = i18n.T("midictl.drv.testsign")
		st.Steps = i18n.T("midictl.drv.steps")
		st.Cmds = driverInstallCmds
		st.SmartScreen = i18n.T("midictl.drv.smartscreen")
	} else {
		st.Managed = u.midiDrvManagedState(ctx)
	}
	st.Docs = i18n.T("midictl.drv.docs")
	st.DocsURL = "https://github.com/rave-page/rave-mate/tree/development/driver/ravemidi"
	st.Badge, st.BadgeVar = i18n.T("midictl.drv.badgePreview"), "warning"
	if installed {
		st.Badge, st.BadgeVar = i18n.T("midictl.drv.badgeOn"), "success"
	}
	return st
}

// midiDrvCardHTML is the pure driver-card renderer.
func midiDrvCardHTML(st midiDrvCard) string {
	if !st.Show {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + htmlEscape(st.Why) + `</p>`)
	b.WriteString(statusRowDL(st.StVariant, st.StLabel, st.StLabelDL, st.StLine))
	if !st.Installed {
		b.WriteString(hint("info", st.TestSign))
		b.WriteString(`<p class=midi-help-note>` + htmlEscape(st.Steps) + `</p>`)
		b.WriteString(`<pre class=midi-cmds>` + htmlEscape(st.Cmds) + `</pre>`)
		b.WriteString(hint("warn", st.SmartScreen))
	} else {
		b.WriteString(midiDrvManagedHTML(st.Managed))
	}
	b.WriteString(btnRow(btn(st.Docs, "outline", "open-url", st.DocsURL)))
	return card(st.Card, badge(st.Badge, st.BadgeVar), b.String())
}

// midiDrvManagedState resolves managed-input live status + wire trace + re-apply/reload.
// Forwarding lives IN the driver (persisted kernel-side) - it survives rave-mate exit
// and reboots. Config sync is AUTOMATIC (driver-managed THRU on a controller +
// every MIDI config change re-syncs); the buttons are manual fallbacks.
func (u *UI) midiDrvManagedState(ctx midiCtlRenderCtx) midiDrvManaged {
	st := midiDrvManaged{
		Hdr: i18n.T("midictl.drv.managedHdr"), Sub: i18n.T("midictl.drv.managedSub"),
		NoneManaged: i18n.T("midictl.drv.noneManaged"), Inputs: []midiDrvInput{},
		Trace:   midiTraceState{Rows: []midiTraceRow{}},
		Reapply: i18n.T("midictl.drv.reapply"), Reload: i18n.T("midictl.drv.reload"),
	}
	if e := midi.DriverSyncErr(); e != "" { // covers boot sync AND webui apply sync
		st.SyncErr = i18n.T("midictl.drv.syncFailed", i18n.A{"err": e})
	}
	// Driver inputs come from the cached probe (midiCtlProbe), never a fresh ioctl on the render
	// path. Reached only when ctx.drvInstalled, so the probe has landed + drvInputs/drvQueryErr valid.
	if ctx.drvQueryErr {
		// older driver build without the config plane - honest degradation
		st.HasQueryErr, st.QueryErr = true, i18n.T("midictl.drv.queryFailed")
	} else {
		fbLbl := i18n.T("midictl.drv.fbLabel")
		for _, ds := range ctx.drvInputs {
			variant, line := "warning", i18n.T("midictl.drv.retrying", i18n.A{"n": strconv.Itoa(int(ds.RetryCount))})
			if ds.Bound {
				variant, line = "success", i18n.T("midictl.drv.bound")
				if ds.FeedbackBound {
					line += " · " + i18n.T("midictl.drv.feedback")
				}
			}
			in := midiDrvInput{Variant: variant, Name: ds.Name, NameDL: strings.ToLower(ds.Name), Line: line}
			if ds.Bound && !ds.FeedbackBound {
				// name WHY the LED test is absent: device render pin not bound
				in.FbHint = i18n.T("midictl.drv.fbNotBound")
			}
			if ds.ReservedPortID != 0 {
				id := strconv.Itoa(int(ds.ReservedPortID))
				in.HasBtns = true
				in.TraceLbl, in.TraceAct = i18n.T("midictl.trace.open"), "midi-drv-trace:"+id
				if ds.Bound && ds.FeedbackBound {
					in.FbTest = true
					in.FbTestLbl, in.FbTestAct = i18n.T("midictl.drv.fbTest"), "midi-fbtest:"+id
					in.FbTipS = tipTopicSt("led-feedback")
				}
				if r := u.fbtResultFor(ds.ReservedPortID); r.line != "" {
					in.FbRes, in.FbResVar, in.FbResLine = true, r.variant, r.line
					in.FbResLbl, in.FbResDL = fbLbl, strings.ToLower(fbLbl)
				}
			}
			st.Inputs = append(st.Inputs, in)
		}
	}
	if u.midiTrace != 0 {
		st.ShowTrace, st.Trace = true, midiTraceStateFor(u.midiTrace)
	}
	return st
}

// midiDrvManagedHTML is the pure managed-input renderer.
func midiDrvManagedHTML(st midiDrvManaged) string {
	var b strings.Builder
	b.WriteString(`<div class=pb-label>` + htmlEscape(st.Hdr) + `</div>`)
	b.WriteString(`<p class=page-sub>` + htmlEscape(st.Sub) + `</p>`)
	if st.SyncErr != "" {
		b.WriteString(hint("warn", st.SyncErr))
	}
	switch {
	case st.HasQueryErr:
		b.WriteString(hint("warn", st.QueryErr))
	case len(st.Inputs) == 0:
		b.WriteString(`<p class=page-sub>` + htmlEscape(st.NoneManaged) + `</p>`)
	default:
		for _, in := range st.Inputs {
			b.WriteString(statusRowDL(in.Variant, in.Name, in.NameDL, in.Line))
			if in.FbHint != "" {
				b.WriteString(hint("info", in.FbHint))
			}
			if in.HasBtns {
				btns := []string{btn(in.TraceLbl, "ghost", in.TraceAct, "")}
				if in.FbTest {
					btns = append(btns, btn(in.FbTestLbl, "ghost", in.FbTestAct, "")+tipOr(in.FbTipS, in.FbTip))
				}
				b.WriteString(btnRow(btns...))
				if in.FbRes {
					b.WriteString(statusRowDL(in.FbResVar, in.FbResLbl, in.FbResDL, in.FbResLine))
				}
			}
		}
	}
	if st.ShowTrace {
		b.WriteString(midiTraceHTML(st.Trace))
	}
	b.WriteString(btnRow(
		btn(st.Reapply, "outline", "midi-drv-sync", ""),
		btn(st.Reload, "ghost", "midi-drv-reload", "")))
	return b.String()
}

// driverInstallCmds: dev/test-signed install, mirrors driver/ravemidi/README.md.
const driverInstallCmds = `# 1  elevated PowerShell, inside the driver package folder
certutil -addstore Root ravemidi-test.cer
certutil -addstore TrustedPublisher ravemidi-test.cer
bcdedit /set testsigning on
#    reboot Windows now
# 2  after the reboot (elevated again)
pnputil /add-driver ravemidi.inf /install
devcon install ravemidi.inf Root\ravemidi`

// midiRackState resolves the channel-count stepper + the horizontally-scrollable channel rack.
func (u *UI) midiRackState() midiRackState {
	n := u.midiChannels()
	st := midiRackState{
		Card: i18n.T("midictl.rackCard"), StepLbl: i18n.T("midictl.channels.label"),
		N: strconv.Itoa(n), Dec: strconv.Itoa(n - 1), Inc: strconv.Itoa(n + 1),
		MinusOff: n <= 1, PlusOff: n >= midimap.MaxChannels,
		Sub: i18n.T("midictl.rack.sub"), Strips: []midiStripState{},
	}
	for ch := 1; ch <= n; ch++ {
		st.Strips = append(st.Strips, midiStripStateOf(ch))
	}
	return st
}

// midiRackHTML is the pure rack-card renderer.
func midiRackHTML(st midiRackState) string {
	var rack strings.Builder
	rack.WriteString(`<div class="midi-mixer" data-testid=midi-mixer>`)
	for _, s := range st.Strips {
		rack.WriteString(midiStripHTML(s))
	}
	rack.WriteString(`</div>`)
	return card(st.Card, midiStepperHTML(st),
		`<p class=page-sub>`+htmlEscape(st.Sub)+`</p>`+rack.String())
}

// midiStepperHTML renders the -/+ channel-count control (1..MaxChannels).
func midiStepperHTML(st midiRackState) string {
	minus := btn("-", "outline", "midi-channels", st.Dec)
	plus := btn("+", "outline", "midi-channels", st.Inc)
	if st.MinusOff {
		minus = `<button class="rp-btn rp-btn--outline" disabled>-</button>`
	}
	if st.PlusOff {
		plus = `<button class="rp-btn rp-btn--outline" disabled>+</button>`
	}
	count := `<span class=midi-chcount data-testid=midi-channels data-label=` + attrQ(st.StepLbl) +
		` data-value=` + attrQ(st.N) + `>` + st.N + `</span>`
	return `<span class=midi-stepper><span class=midi-steplbl>` + htmlEscape(st.StepLbl) +
		`</span>` + minus + count + plus + `</span>`
}

// midiStripStateOf resolves one channel strip (1-based ch): label, EQ/filter/trim knobs, a
// vertical fader, then Play + Cue. Every control's assigned CC is shown and stamped for ctl.
func midiStripStateOf(ch int) midiStripState {
	wire := int(midimap.WireChannel(ch))
	letter := midimap.Letters[wire]
	st := midiStripState{
		Head:  i18n.T("midictl.channelLabel", i18n.A{"n": strconv.Itoa(ch), "letter": letter}),
		Knobs: []midiKnobState{}, Faders: []midiKnobState{}, Btns: []midiMomState{},
	}
	for _, c := range midimap.Controls {
		switch {
		case c.Kind == midimap.Momentary:
			continue // Play/Cue rendered below the fader
		case c.ID == "fader":
			continue // rendered after the knobs
		default:
			st.Knobs = append(st.Knobs, midiKnobStateOf(ch, wire, c))
		}
	}
	for _, c := range midimap.Controls {
		if c.ID == "fader" {
			st.Faders = append(st.Faders, midiKnobStateOf(ch, wire, c))
		}
	}
	for _, c := range midimap.Controls {
		if c.Kind == midimap.Momentary {
			st.Btns = append(st.Btns, midiMomStateOf(ch, wire, c))
		}
	}
	return st
}

// midiStripHTML is the pure channel-strip renderer.
func midiStripHTML(st midiStripState) string {
	var b strings.Builder
	b.WriteString(`<div class=midi-strip>`)
	b.WriteString(`<div class=midi-striphead>` + htmlEscape(st.Head) + `</div>`)
	b.WriteString(`<div class=midi-knobs>`)
	for _, k := range st.Knobs {
		b.WriteString(midiKnobHTML(k))
	}
	b.WriteString(`</div>`)
	for _, f := range st.Faders {
		b.WriteString(midiFaderHTML(f))
	}
	b.WriteString(`<div class=midi-btns>`)
	for _, m := range st.Btns {
		b.WriteString(midiMomBtnHTML(m))
	}
	b.WriteString(`</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// knobInitial is the starting value for a continuous control (EQ/filter/trim centred; fader down).
func knobInitial(id string) int {
	if id == "fader" {
		return 0
	}
	return 64
}

// midiKnobStateOf resolves a continuous control's render state (knob + fader share it).
func midiKnobStateOf(ch, wire int, c midimap.Control) midiKnobState {
	val := knobInitial(c.ID)
	label := i18n.T("midictl.ctl." + c.LabelKey)
	cc := ctlReadout(c, ch)
	sweep := i18n.T("midictl.sweep")
	return midiKnobState{
		DL:         strings.ToLower(fmt.Sprintf("ch%d %s", ch, label)),
		V:          trimNum(float64(val) / 127),
		Rot:        trimNum(float64(val)/127*270 - 135),
		Val:        strconv.Itoa(val),
		Act:        fmt.Sprintf("midi-send:%d:%d", wire, c.CC),
		Tid:        fmt.Sprintf("midi-ch%d-%s", ch, c.ID),
		Aria:       label + " " + cc,
		Label:      label,
		CC:         cc,
		SweepAct:   fmt.Sprintf("midi-sweep:%d:%d", wire, c.CC),
		SweepTitle: sweep, SweepAria: sweep + " " + label,
		SweepGlyph: i18n.T("midictl.sweepGlyph"),
	}
}

// knobOnInput keeps the dial pointer + fill in sync during a drag (display only).
const knobOnInput = `oninput="var l=this.closest('.midi-knob');var v=this.value/127;` +
	`l.style.setProperty('--v',v);l.style.setProperty('--rot',(v*270-135)+'deg')"`

// faderOnInput mirrors knobOnInput for the vertical fader fill.
const faderOnInput = `oninput="this.closest('.midi-vfader').style.setProperty('--v',this.value/127)"`

// midiKnobHTML renders a rotary knob: an SVG-free circular dial whose pointer rotates with the
// value, an overlaid vertical range input (drag = live CC send), a caption, the CC readout, and
// a Sweep.
func midiKnobHTML(k midiKnobState) string {
	return `<label class=midi-knob data-label=` + attrQ(k.DL) + ` style="--v:` + k.V + `;--rot:` + k.Rot + `deg">` +
		`<span class=mk-dial aria-hidden=true><span class=mk-ptr></span></span>` +
		`<input class=mk-in type=range min=0 max=127 step=1 value=` + k.Val + ` data-value=` + k.Val + ` ` +
		`data-actinput=` + attrQ(k.Act) + ` data-testid=` + attrQ(k.Tid) + ` aria-label=` + attrQ(k.Aria) + ` ` + knobOnInput + `>` +
		`<span class=mk-cap>` + htmlEscape(k.Label) + `</span><span class=mk-cc>` + htmlEscape(k.CC) + `</span>` +
		`<button class="mk-sweep rp-btn rp-btn--ghost" data-act=` + attrQ(k.SweepAct) + ` title=` + attrQ(k.SweepTitle) +
		` aria-label=` + attrQ(k.SweepAria) + `>` + htmlEscape(k.SweepGlyph) + `</button>` +
		`</label>`
}

// midiFaderHTML renders a vertical fader: a track + level fill (mint, driven by --v) with an
// overlaid vertical range input (drag = live CC send), a caption, the CC readout, and a Sweep.
func midiFaderHTML(k midiKnobState) string {
	return `<label class=midi-vfader data-label=` + attrQ(k.DL) + ` style="--v:` + k.V + `">` +
		`<span class=mf-track aria-hidden=true><span class=mf-fill></span></span>` +
		`<input class=mf-in type=range min=0 max=127 step=1 value=` + k.Val + ` data-value=` + k.Val + ` ` +
		`data-actinput=` + attrQ(k.Act) + ` data-testid=` + attrQ(k.Tid) + ` aria-label=` + attrQ(k.Aria) + ` ` + faderOnInput + `>` +
		`<span class=mf-cap>` + htmlEscape(k.Label) + `</span><span class=mf-cc>` + htmlEscape(k.CC) + `</span>` +
		`<button class="mf-sweep rp-btn rp-btn--ghost" data-act=` + attrQ(k.SweepAct) + ` title=` + attrQ(k.SweepTitle) +
		` aria-label=` + attrQ(k.SweepAria) + `>` + htmlEscape(k.SweepGlyph) + `</button>` +
		`</label>`
}

// midiMomStateOf resolves a Play/Cue pill: a press sends a momentary Note On + Note Off (a DJ app
// learns a Note as a Button on the Note-On; the Note-Off is the release - one clean learn event).
func midiMomStateOf(ch, wire int, c midimap.Control) midiMomState {
	label := i18n.T("midictl.ctl." + c.LabelKey)
	cc := ctlReadout(c, ch)
	return midiMomState{
		Cls:  "midi-btn midi-btn--" + c.ID,
		Act:  fmt.Sprintf("midi-note:%d:%d", wire, c.CC),
		Tid:  fmt.Sprintf("midi-ch%d-%s", ch, c.ID),
		DL:   strings.ToLower(fmt.Sprintf("ch%d %s", ch, label)),
		Aria: label + " " + cc, Label: label, CC: cc,
	}
}

// midiMomBtnHTML is the pure Play/Cue pill renderer.
func midiMomBtnHTML(m midiMomState) string {
	return `<button class=` + attrQ(m.Cls) + ` data-act=` + attrQ(m.Act) + ` data-testid=` + attrQ(m.Tid) +
		` data-label=` + attrQ(m.DL) + ` aria-label=` + attrQ(m.Aria) + `>` +
		`<span class=midi-btn-lbl>` + htmlEscape(m.Label) + `</span>` +
		`<span class=midi-btn-cc>` + htmlEscape(m.CC) + `</span></button>`
}

// ctlReadout formats a control's assigned MIDI as "CC24·ch1" (continuous) or "Note20·ch1" (button).
func ctlReadout(c midimap.Control, ch int) string {
	kind := "CC"
	if c.Note {
		kind = "Note"
	}
	return kind + strconv.Itoa(int(c.CC)) + "·ch" + strconv.Itoa(ch)
}

// midiHelpStateOf resolves the send-to-learn round-trip walkthrough + honest per-software status.
func midiHelpStateOf() midiHelpState {
	row := func(name, badgeKey, badgeVar, noteKey string) midiSwRow {
		return midiSwRow{
			Name: name, Badge: i18n.T("midictl.sw." + badgeKey), BadgeVar: badgeVar,
			Note: i18n.T("midictl.sw." + noteKey),
		}
	}
	return midiHelpState{
		Card: i18n.T("midictl.helpCard"), Badge: i18n.T("midictl.help.badge"),
		Step1: i18n.T("midictl.help.step1"), Step2: i18n.T("midictl.help.step2"), Step3: i18n.T("midictl.help.step3"),
		Feedback: i18n.T("midictl.help.feedback"), Caveat: i18n.T("midictl.help.caveat"),
		Link: i18n.T("midictl.help.link"), SwHdr: i18n.T("midictl.sw.hdr"),
		Rows: []midiSwRow{
			row("Traktor Pro", "stable", "success", "traktor"),
			row("Rekordbox", "experimental", "warning", "rekordbox"),
			row("VirtualDJ", "untested", "warning", "virtualdj"),
			row("Serato", "unfinished", "error", "serato"),
		},
	}
}

// midiHelpHTML is the pure help-card renderer (mapping FAQ link + software matrix).
func midiHelpHTML(st midiHelpState) string {
	steps := `<ol class=midi-help><li>` + htmlEscape(st.Step1) + `</li><li>` +
		htmlEscape(st.Step2) + `</li><li>` +
		htmlEscape(st.Step3) + `</li></ol>` +
		`<p class=midi-help-note>` + htmlEscape(st.Feedback) + `</p>` +
		`<p class=midi-help-note>` + htmlEscape(st.Caveat) + ` ` +
		`<a href="https://rekordbox.com/en/support/faq/mapping-6/" target=_blank rel=noopener>` +
		htmlEscape(st.Link) + `</a></p>`
	var m strings.Builder
	m.WriteString(`<div class=pb-label>` + htmlEscape(st.SwHdr) + `</div>`)
	for _, r := range st.Rows {
		m.WriteString(`<div class=midi-sw><span class=midi-sw-name>` + htmlEscape(r.Name) + `</span>` +
			badge(r.Badge, r.BadgeVar) +
			`<span class=midi-sw-note>` + htmlEscape(r.Note) + `</span></div>`)
	}
	return card(st.Card, badge(st.Badge, "info"), steps+m.String())
}
