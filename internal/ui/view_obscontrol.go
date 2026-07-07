package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/obscontrol"
	"rave.page/mate/internal/twitch"
)

// buildObsCockpitContent is the "obs" Live card: the streaming cockpit without its own
// heading (the card frame provides one). nil when OBS control is unavailable.
func (u *UI) buildObsCockpitContent() fyne.CanvasObject {
	if u.svc.OBSControl == nil {
		return nil
	}
	bar, stop := u.obsControlBar("Live", "")
	u.closers = append(u.closers, stop)
	return bar
}

// obsControlBar is the streaming cockpit (Live card + atop the Twitch tab): live viewer count +
// per-instance OBS stream/recording control with bitrate/health. Works across instances (drives a
// peer's OBS over the bus). gateTab limits the 1 s re-render to while that tab is showing; heading
// "" drops the bold title row. Returns the widget + a teardown func (stops the ticker + bus subs).
func (u *UI) obsControlBar(gateTab, heading string) (fyne.CanvasObject, func()) {
	oc := u.svc.OBSControl

	viewers := canvas.NewText("- viewers", colMuted)
	viewers.TextStyle = fyne.TextStyle{Bold: true}

	instBox := container.NewVBox()

	rebuildInst := func() {
		instBox.RemoveAll()
		if oc == nil {
			instBox.Add(mutedLabel("OBS control unavailable"))
			instBox.Refresh()
			return
		}
		insts := oc.Statuses()
		if len(insts) == 0 {
			instBox.Add(mutedLabel("No OBS instance connected (enable the OBS bridge here or on a peer)."))
			instBox.Refresh()
			return
		}
		for _, in := range insts {
			instBox.Add(u.obsInstanceRow(oc, in))
		}
		instBox.Refresh()
	}
	rebuildInst()

	// Viewer count from the bus (local or peer-owned Twitch).
	if u.svc.EventBus != nil {
		unsub := u.svc.EventBus.Subscribe(twitch.TopicViewers, func(e eventbus.Event) {
			var vi twitch.ViewerInfo
			if json.Unmarshal(e.Data, &vi) != nil {
				return
			}
			fyne.Do(func() {
				if vi.Live {
					viewers.Text = fmt.Sprintf("%s viewers · LIVE", commaInt(vi.ViewerCount))
					viewers.Color = colBrandMint
				} else {
					viewers.Text = "offline"
					viewers.Color = colMuted
				}
				viewers.Refresh()
			})
		})
		u.closers = append(u.closers, unsub)
	}

	// 1s ticker re-renders instance status (bitrate/state in place) - only while the Twitch tab is
	// actually showing, so a hidden cockpit costs nothing.
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				fyne.Do(func() {
					if u.tabs == nil { // tick can land before New() assigns u.tabs
						return
					}
					if sel := u.tabs.Selected(); sel != nil && sel.Text == gateTab {
						rebuildInst()
					}
				})
			}
		}
	}()
	teardown := func() { close(stop) }

	var headLeft fyne.CanvasObject
	if heading != "" {
		headLeft = widget.NewLabelWithStyle(heading, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	head := container.NewBorder(nil, nil, headLeft, viewers, nil)
	if heading == "" {
		return container.NewVBox(head, instBox), teardown
	}
	return container.NewVBox(head, instBox, widget.NewSeparator()), teardown
}

// commaInt formats n with thousands separators.
func commaInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b []byte
	pre := len(s) % 3
	if pre > 0 {
		b = append(b, s[:pre]...)
	}
	for i := pre; i < len(s); i += 3 {
		if len(b) > 0 {
			b = append(b, ',')
		}
		b = append(b, s[i:i+3]...)
	}
	return string(b)
}

// obsInstanceRow is one OBS instance's status + stream/record toggle buttons.
func (u *UI) obsInstanceRow(oc *obscontrol.Manager, in obscontrol.Instance) fyne.CanvasObject {
	label := in.Label
	if label == "" {
		label = in.Node
	}
	if in.Local {
		label += " (this PC)"
	}

	status := canvas.NewText("OBS offline", colMuted)
	switch {
	case !in.Connected:
		status.Text, status.Color = "OBS offline", colMuted
	case in.Streaming:
		s := fmt.Sprintf("LIVE  %d kbps  net %.0f%%  drop %.1f%%", in.BitrateKbps, in.Congestion*100, in.DropPct())
		if in.Reconnecting {
			s += "  (reconnecting)"
		}
		status.Text, status.Color = s, colBrandMint
	default:
		status.Text, status.Color = "ready", colBrandAmber
	}

	target := in.ID
	cmd := func(action string) func() {
		return func() {
			goUI("obs-cmd", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				if err := oc.Command(ctx, obscontrol.Cmd{Target: target, Action: action}); err != nil {
					u.Notify("OBS", "Command failed: "+err.Error())
				}
			})
		}
	}

	streamLabel, streamAct := "Start stream", obscontrol.ActStreamStart
	if in.Streaming {
		streamLabel, streamAct = "Stop stream", obscontrol.ActStreamStop
	}
	streamBtn := widget.NewButtonWithIcon(streamLabel, theme.MediaRecordIcon(), cmd(streamAct))
	streamBtn.Importance = widget.HighImportance

	recLabel, recAct := "Start rec", obscontrol.ActRecordStart
	if in.Recording {
		recLabel, recAct = "Stop rec", obscontrol.ActRecordStop
	}
	recBtn := widget.NewButtonWithIcon(recLabel, theme.MediaVideoIcon(), cmd(recAct))
	if in.Recording {
		recBtn.Importance = widget.DangerImportance
	}

	if !in.Connected {
		streamBtn.Disable()
		recBtn.Disable()
	}

	name := widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	btns := container.NewHBox(streamBtn, recBtn)
	return container.NewBorder(nil, nil, name, btns, status)
}
