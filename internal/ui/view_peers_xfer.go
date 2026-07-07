package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/filexfer"
)

// view_peers_xfer.go is the Peers tab's "File transfer" block: incoming pending accepts,
// active/queued transfers (progress + rate + cancel), and the receive settings row.

const helpFileXfer = "Send files or folders to a paired instance over the encrypted LAN " +
	"link. Transfers are chunked, verified per file (SHA-256), and resume where they left " +
	"off after a dropped connection. Receive files must be on for this instance to accept " +
	"incoming transfers; Ask holds each one here for confirmation, Auto saves straight to " +
	"the folder below."

// xferSettingsRow builds the persistent settings row ONCE per tab build (entries live in a
// 2 s-refreshed list - reusing the same instances keeps typing intact).
func (u *UI) xferSettingsRow() fyne.CanvasObject {
	if u.svc.Cfg == nil {
		return widget.NewSeparator()
	}
	f := &u.svc.Cfg.Features.FileXfer
	toggle := u.moduleToggle("filexfer", &f.Enabled)
	dir := newEntry()
	dir.SetText(f.DownloadDir)
	dir.SetPlaceHolder(f.ResolvedDownloadDir())
	dir.OnChanged = func(s string) { f.DownloadDir = s; u.saveCfg() }
	modeLabel := "Ask"
	if f.AutoAccept() {
		modeLabel = "Auto"
	}
	mode := newKitSegmented([]string{"Ask", "Auto"}, modeLabel, func(s string) {
		if s == "Auto" {
			f.AcceptMode = "auto"
		} else {
			f.AcceptMode = "ask"
		}
		u.saveCfg()
	})
	return container.NewVBox(
		container.NewHBox(widget.NewLabel("Receive files"), toggle, widget.NewLabel("Accept"), mode),
		container.NewBorder(nil, nil, widget.NewLabel("Save to"), nil, folderPickerRow(dir)),
	)
}

// xferSection renders the File transfer block (settings row instance passed in - see above).
func (u *UI) xferSection(resolve func(string) string, settings fyne.CanvasObject) []fyne.CanvasObject {
	fx := u.svc.FileXfer
	if fx == nil {
		return nil
	}
	objs := []fyne.CanvasObject{
		widget.NewSeparator(),
		container.NewHBox(sectionLabel("File transfer"), helpIcon(helpFileXfer)),
		settings,
	}
	trs := fx.Transfers()
	any := false
	for _, tr := range trs { // pending accepts first - they need a decision
		if !tr.Send && tr.State == filexfer.StatePending {
			objs = append(objs, u.xferPendingRow(tr, resolve))
			any = true
		}
	}
	for _, tr := range trs {
		if !tr.Send && tr.State == filexfer.StatePending {
			continue
		}
		objs = append(objs, u.xferRow(tr, resolve))
		any = true
	}
	if !any {
		objs = append(objs, mutedLabel("No transfers yet. Send from the Library, or have a paired instance send to this one."))
	}
	return objs
}

// xferPendingRow is an incoming transfer awaiting the user's decision.
func (u *UI) xferPendingRow(tr filexfer.Transfer, resolve func(string) string) fyne.CanvasObject {
	fx := u.svc.FileXfer
	accept := widget.NewButton("Accept", func() { goUI("xfer-accept", func() { fx.Accept(tr.ID, true) }) })
	accept.Importance = widget.HighImportance
	decline := widget.NewButton("Decline", func() { goUI("xfer-decline", func() { fx.Accept(tr.ID, false) }) })
	line := fmt.Sprintf("⇩ %s (%s, %d file(s)) - from %s", tr.Name, filexfer.FmtBytes(tr.Bytes), tr.Files, resolve(tr.Peer))
	return container.NewBorder(nil, nil, nil, container.NewHBox(accept, decline), widget.NewLabel(line))
}

// xferRow is one active/queued/finished transfer.
func (u *UI) xferRow(tr filexfer.Transfer, resolve func(string) string) fyne.CanvasObject {
	arrow, word := "⇧", "to"
	if !tr.Send {
		arrow, word = "⇩", "from"
	}
	title := widget.NewLabel(fmt.Sprintf("%s %s - %s %s", arrow, tr.Name, word, resolve(tr.Peer)))
	switch tr.State {
	case filexfer.StateActive:
		bar := widget.NewProgressBar()
		if tr.Bytes > 0 {
			bar.SetValue(float64(tr.Done) / float64(tr.Bytes))
		}
		detail := fmt.Sprintf("%s / %s · %s/s", filexfer.FmtBytes(tr.Done), filexfer.FmtBytes(tr.Bytes), filexfer.FmtBytes(int64(tr.Rate)))
		return container.NewVBox(
			container.NewBorder(nil, nil, nil, u.xferCancelBtn(tr.ID), title),
			container.NewBorder(nil, nil, nil, mutedLabel(detail), bar),
		)
	case filexfer.StateWaiting:
		return container.NewBorder(nil, nil, nil, u.xferCancelBtn(tr.ID),
			container.NewVBox(title, mutedLabel("   waiting for the paired instance…")))
	case filexfer.StateStalled:
		msg := "   interrupted - retrying"
		if tr.Error != "" {
			msg = "   interrupted (" + tr.Error + ") - retrying"
		}
		return container.NewBorder(nil, nil, nil, u.xferCancelBtn(tr.ID), container.NewVBox(title, mutedLabel(msg)))
	case filexfer.StateDone:
		return container.NewVBox(title, mutedLabel(fmt.Sprintf("   ✓ done · %d file(s), %s", tr.Files, filexfer.FmtBytes(tr.Bytes))))
	case filexfer.StateError:
		return container.NewVBox(title, mutedLabel("   ✗ failed: "+tr.Error))
	default: // canceled (+ receiver pending handled above)
		return container.NewVBox(title, mutedLabel("   canceled"))
	}
}

func (u *UI) xferCancelBtn(id string) *widget.Button {
	fx := u.svc.FileXfer
	b := widget.NewButton("Cancel", func() { goUI("xfer-cancel", func() { fx.Cancel(id) }) })
	b.Importance = widget.WarningImportance
	return b
}
