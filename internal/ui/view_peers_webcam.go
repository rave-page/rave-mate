package ui

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/webcam"
)

// view_peers_webcam.go is the Peers tab's "Webcam" block (medialink P5, MEDIALINK_DESIGN.md §5):
// this instance's camera (device/mode pick, start/stop, Spout sender name, PTZ/exposure sliders)
// plus the same controls mirrored for every paired instance's camera - commands ride the
// media.cam.* bus and execute on the instance that owns the device. Panels persist across the
// Peers tab's 2 s list rebuilds so selects/sliders never reset mid-interaction.

const helpWebcam = "A camera on this or a paired instance, published as a local Spout sender - " +
	"add a Spout2 Capture source in OBS/Resolume/TouchDesigner and pick the sender by name. " +
	"Start/stop and lens controls work across instances: the camera always runs on the instance " +
	"it is plugged into. Enable the Webcam feature on each instance that should show up here."

const helpWebcamProps = "UVC lens/image controls (pan/tilt/zoom/focus/exposure…). Auto hands a " +
	"property back to the camera's own logic; dragging a slider sets it manually. Sliders only " +
	"appear for properties this camera supports."

// webcamPanel keeps one persistent control panel per instance across list rebuilds.
type webcamPanel struct {
	u     *UI
	nodes map[string]*camNodePanel
}

func (u *UI) newWebcamPanel() *webcamPanel {
	return &webcamPanel{u: u, nodes: map[string]*camNodePanel{}}
}

// section renders the webcam block (nil when the webcam service is absent or off).
func (p *webcamPanel) section(resolve func(string) string) []fyne.CanvasObject {
	w := p.u.svc.Webcam
	if w == nil || p.u.svc.Cfg == nil || !p.u.svc.Cfg.Features.Webcam.Enabled {
		return nil
	}
	insts := w.Instances()
	objs := []fyne.CanvasObject{
		widget.NewSeparator(),
		container.NewHBox(sectionLabel("Webcam"), helpIcon(helpWebcam)),
	}
	live := map[string]bool{}
	for _, in := range insts {
		live[in.ID] = true
		np := p.nodes[in.ID]
		if np == nil {
			np = newCamNodePanel(p.u, in.ID)
			p.nodes[in.ID] = np
		}
		np.update(in, resolve)
		objs = append(objs, np.box)
	}
	for id := range p.nodes { // drop panels for departed instances
		if !live[id] {
			delete(p.nodes, id)
		}
	}
	return objs
}

// camNodePanel is one instance's camera controls (local or a paired instance).
type camNodePanel struct {
	u  *UI
	id string // owning node id (Cmd.Target)

	box       *fyne.Container
	title     *widget.Label
	status    *widget.Label
	deviceSel *widget.Select
	modeSel   *widget.Select
	startBtn  *widget.Button
	senderBox *fyne.Container
	propsBox  *fyne.Container
	propRows  map[string]*camPropRow

	running  bool
	modes    []webcam.Mode // for the selected device
	topoSig  string        // props topology signature (device + ids + ranges)
	updating bool          // guard: programmatic widget updates must not fire commands
}

func newCamNodePanel(u *UI, id string) *camNodePanel {
	np := &camNodePanel{u: u, id: id, propRows: map[string]*camPropRow{}}
	np.title = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	np.status = mutedLabel("")
	np.deviceSel = widget.NewSelect(nil, func(string) { np.onDevicePicked() })
	np.deviceSel.PlaceHolder = "(select camera)"
	np.modeSel = widget.NewSelect(nil, nil)
	np.modeSel.PlaceHolder = "(size / fps)"
	np.startBtn = widget.NewButton("Start", np.onStartStop)
	np.senderBox = container.NewVBox() // VBox, not HBox: the sender line is a wrapping mutedLabel - HBox would starve it to 1 char wide (vertical text)
	np.propsBox = container.NewVBox()
	refresh := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), func() {
		np.command(webcam.Cmd{Target: np.id, Action: webcam.ActRefresh})
	})
	np.box = container.NewVBox(
		container.NewHBox(np.title, refresh),
		np.status,
		container.NewBorder(nil, nil, mutedInline("Device"), np.startBtn, np.deviceSel),
		container.NewBorder(nil, nil, mutedInline("Mode  "), nil, np.modeSel),
		np.senderBox,
		np.propsBox,
	)
	return np
}

// command publishes a camera command on the bus (executes locally or on the paired instance).
func (np *camNodePanel) command(cmd webcam.Cmd) {
	if np.u.svc.Webcam == nil {
		return
	}
	goUI("webcam-cmd", func() {
		if err := np.u.svc.Webcam.Command(cmd); err != nil {
			np.u.svc.Log.Warn("webcam", "command failed", map[string]any{"error": err.Error()})
		}
	})
}

// onDevicePicked persists a local device pick; remote picks apply on Start.
func (np *camNodePanel) onDevicePicked() {
	if np.updating {
		return
	}
	if np.u.svc.Identity != nil && np.id == np.u.svc.Identity.NodeID && np.u.svc.Cfg != nil {
		np.u.svc.Cfg.Features.Webcam.Device = np.deviceSel.Selected
		np.u.saveCfg()
	}
}

func (np *camNodePanel) onStartStop() {
	if np.running {
		np.command(webcam.Cmd{Target: np.id, Action: webcam.ActStop})
		return
	}
	cmd := webcam.Cmd{Target: np.id, Action: webcam.ActStart, Device: np.deviceSel.Selected}
	cmd.W, cmd.H, cmd.FPS = parseCamMode(np.modeSel.Selected)
	np.command(cmd)
}

// update applies the latest status to the persistent widgets (no rebuild unless topology changed).
func (np *camNodePanel) update(in webcam.Instance, resolve func(string) string) {
	np.updating = true
	defer func() { np.updating = false }()
	np.running = in.Running

	name := "This instance"
	if !in.Local {
		name = resolve(in.Node)
		if in.Label != "" {
			name = in.Label + " - a paired instance"
		}
	}
	np.title.SetText(name)
	np.status.SetText(fmtCamStatus(in.Status))

	// Device options (keep the user's pending selection; seed from status/config).
	opts := make([]string, 0, len(in.Devices))
	for _, d := range in.Devices {
		opts = append(opts, d.Name)
	}
	if in.Device != "" && !hasStr(opts, in.Device) {
		opts = append([]string{in.Device}, opts...)
	}
	if !slices.Equal(np.deviceSel.Options, opts) {
		np.deviceSel.Options = opts
		np.deviceSel.Refresh()
	}
	if np.deviceSel.Selected == "" && in.Device != "" {
		np.deviceSel.SetSelected(in.Device)
	}

	// Mode options for the selected device.
	var modes []webcam.Mode
	for _, d := range in.Devices {
		if d.Name == np.deviceSel.Selected {
			modes = d.Modes
			break
		}
	}
	mopts := camModeStrings(modes)
	if !slices.Equal(np.modeSel.Options, mopts) {
		np.modes = modes
		np.modeSel.Options = mopts
		np.modeSel.Refresh()
	}
	if np.modeSel.Selected == "" {
		switch {
		case in.Running:
			np.modeSel.Selected = fmtCamMode(in.W, in.H, in.FPS)
			np.modeSel.Refresh()
		case len(mopts) > 0:
			np.modeSel.SetSelected(mopts[0])
		}
	}

	if in.Running {
		np.startBtn.SetText("Stop")
		np.startBtn.Importance = widget.WarningImportance
	} else {
		np.startBtn.SetText("Start")
		np.startBtn.Importance = widget.HighImportance
	}
	np.startBtn.Refresh()

	// Spout sender row (visible while running; name is what a receiver picks).
	np.senderBox.RemoveAll()
	if in.Sender != "" {
		sender := in.Sender
		np.senderBox.Add(newKitCopyable("Spout sender name",
			mutedLabel("Spout sender: "+sender), func() string { return sender }))
	}
	np.senderBox.Refresh()

	np.updateProps(in.Props)
}

// updateProps rebuilds the slider rows when the property topology changes, else refreshes values.
func (np *camNodePanel) updateProps(props []webcam.PropState) {
	sig := camTopoSig(np.deviceSel.Selected, props)
	if sig != np.topoSig {
		np.topoSig = sig
		np.propsBox.RemoveAll()
		np.propRows = map[string]*camPropRow{}
		if len(props) > 0 {
			np.propsBox.Add(container.NewHBox(mutedInline("Lens / image"), helpIcon(helpWebcamProps)))
			for _, p := range props {
				row := newCamPropRow(np, p)
				np.propRows[p.ID] = row
				np.propsBox.Add(row.obj)
			}
		}
		np.propsBox.Refresh()
		return
	}
	for _, p := range props {
		if row := np.propRows[p.ID]; row != nil {
			row.refresh(p)
		}
	}
}

// camPropRow is one UVC property: label + slider + value + optional auto check.
type camPropRow struct {
	np     *camNodePanel
	id     string
	obj    fyne.CanvasObject
	slider *widget.Slider
	value  *widget.Label
	auto   *widget.Check
}

func newCamPropRow(np *camNodePanel, p webcam.PropState) *camPropRow {
	r := &camPropRow{np: np, id: p.ID}
	r.slider = widget.NewSlider(float64(p.Min), float64(p.Max))
	if p.Step > 0 {
		r.slider.Step = float64(p.Step)
	}
	r.slider.Value = float64(p.Value)
	r.value = mutedInline(strconv.Itoa(int(p.Value)))
	r.slider.OnChanged = func(v float64) { r.value.SetText(strconv.Itoa(int(v))) }
	r.slider.OnChangeEnded = func(v float64) {
		if np.updating {
			return
		}
		np.command(webcam.Cmd{Target: np.id, Action: webcam.ActSet, Prop: r.id, Value: int32(v)})
	}
	right := []fyne.CanvasObject{r.value}
	if p.CanAuto {
		r.auto = widget.NewCheck("auto", func(on bool) {
			if np.updating {
				return
			}
			cmd := webcam.Cmd{Target: np.id, Action: webcam.ActSet, Prop: r.id, Auto: on}
			if !on {
				cmd.Value = int32(r.slider.Value) // manual takes over at the slider position
			}
			np.command(cmd)
		})
		r.auto.SetChecked(p.Auto)
		right = append(right, r.auto)
	}
	if p.Auto {
		r.slider.Disable()
	}
	label := mutedInline(p.Label)
	r.obj = container.NewBorder(nil, nil, shrinkWidth(96, label), container.NewHBox(right...), r.slider)
	return r
}

// refresh applies a fresh PropState without recreating widgets (guarded - no command feedback).
func (r *camPropRow) refresh(p webcam.PropState) {
	r.np.updating = true
	defer func() { r.np.updating = false }()
	if r.auto != nil && r.auto.Checked != p.Auto {
		r.auto.SetChecked(p.Auto)
	}
	if p.Auto {
		r.slider.Disable()
		if r.slider.Value != float64(p.Value) { // track the camera's auto motion
			r.slider.SetValue(float64(p.Value))
		}
		r.value.SetText(strconv.Itoa(int(p.Value)))
	} else {
		r.slider.Enable()
	}
}

// ── pure formatters (unit-tested) ────────────────────────────────────────────

// fmtCamStatus renders one instance's camera state line.
func fmtCamStatus(st webcam.Status) string {
	switch {
	case st.Running:
		s := "LIVE - " + fmtCamMode(st.W, st.H, st.FPS)
		if st.Err != "" {
			s += " · " + st.Err
		}
		return s
	case st.Err != "":
		return st.Err
	case st.Device == "":
		return "No camera selected."
	default:
		return "Ready - " + st.Device
	}
}

// fmtCamMode renders "1280x720 @ 30" (fps omitted when 0).
func fmtCamMode(w, h, fps int) string {
	s := fmt.Sprintf("%dx%d", w, h)
	if fps > 0 {
		s += fmt.Sprintf(" @ %d", fps)
	}
	return s
}

// camModeStrings renders a device's advertised modes for the mode select.
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

// parseCamMode parses "1280x720 @ 30" back into w/h/fps (zeros on blank/garbage → device default).
func parseCamMode(s string) (w, h, fps int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, 0
	}
	size, rate, _ := strings.Cut(s, "@")
	xw, xh, ok := strings.Cut(strings.TrimSpace(size), "x")
	if !ok {
		return 0, 0, 0
	}
	w, _ = strconv.Atoi(strings.TrimSpace(xw))
	h, _ = strconv.Atoi(strings.TrimSpace(xh))
	fps, _ = strconv.Atoi(strings.TrimSpace(rate))
	if w <= 0 || h <= 0 {
		return 0, 0, 0
	}
	return w, h, fps
}

// camTopoSig folds the property set's identity + ranges (not values) into a rebuild key.
func camTopoSig(device string, props []webcam.PropState) string {
	var b strings.Builder
	b.WriteString(device)
	ids := make([]string, 0, len(props))
	byID := map[string]webcam.PropState{}
	for _, p := range props {
		ids = append(ids, p.ID)
		byID[p.ID] = p
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := byID[id]
		fmt.Fprintf(&b, "|%s:%d:%d:%d:%t", p.ID, p.Min, p.Max, p.Step, p.CanAuto)
	}
	return b.String()
}
