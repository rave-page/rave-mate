package ui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/stt"
)

// sttCard configures Whisper speech-to-chat: enable, mic + output device, model, auto-submit, and
// a one-click install of the whisper.cpp binary + chosen model. Keybinds (record/send/discard) are
// assigned in the VR Keybinds dialog.
func (u *UI) sttCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.STT

	st := u.newStatus(func(s *cardStatus) {
		switch {
		case stt.Installed(f.Model):
			s.set(colBrandMint, "ready")
		case stt.BinInstalled():
			s.set(colMuted, "model not downloaded")
		default:
			s.set(colMuted, "not installed")
		}
	})

	// Mic input device (ffmpeg dshow enumeration). "" = system default.
	inSel := widget.NewSelect(nil, func(s string) {
		if s == defaultDev {
			s = ""
		}
		f.InputDevice = s
		u.saveCfg()
	})
	inSel.PlaceHolder = "Refresh to list mics"
	// Output device for cues (stored; used for future confirmation/sidetone playback).
	outEntry := widget.NewEntry()
	outEntry.SetPlaceHolder("(system default)")
	outEntry.SetText(f.OutputDevice)
	outEntry.OnChanged = func(s string) { f.OutputDevice = s; u.saveCfg() }

	refresh := widget.NewButtonWithIcon("Refresh mics", theme.ViewRefreshIcon(), func() {
		devs, err := stt.InputDevices()
		if err != nil {
			u.Notify("Speak-to-chat", err.Error())
			return
		}
		inSel.Options = append([]string{defaultDev}, devs...)
		if f.InputDevice == "" {
			inSel.SetSelected(defaultDev)
		} else {
			inSel.SetSelected(f.InputDevice)
		}
		inSel.Refresh()
	})

	// Model picker (default = base.en, performant + good).
	var modelOpts []string
	dispToFile := map[string]string{}
	for _, m := range stt.Models {
		modelOpts = append(modelOpts, m.Display)
		dispToFile[m.Display] = m.File
	}
	modelSel := widget.NewSelect(modelOpts, func(s string) {
		f.Model = dispToFile[s]
		u.saveCfg()
	})
	modelSel.SetSelected(stt.ResolvedModel(f.Model).Display)

	// Auto-submit on silence + timeout.
	silence := widget.NewEntry()
	silence.SetText(strconv.Itoa(f.ResolvedSilenceMs()))
	silence.OnChanged = func(s string) {
		if ms, err := strconv.Atoi(s); err == nil && ms > 0 {
			f.SilenceMs = ms
			u.saveCfg()
		}
	}
	auto := widget.NewCheck("Auto-send after silence (ms):", func(v bool) { f.AutoSubmit = v; u.saveCfg() })
	auto.SetChecked(f.AutoSubmit)

	// Install whisper.cpp + the selected model.
	var installBtn *widget.Button
	installBtn = widget.NewButtonWithIcon("Install Whisper + model", theme.DownloadIcon(), func() {
		if !stt.CanInstall() {
			u.Notify("Speak-to-chat", "Whisper auto-install is Windows-only.")
			return
		}
		installBtn.Disable()
		installBtn.SetText("Downloading…")
		model := f.Model
		debuglog.Go(nil, "stt", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			err := stt.Install(ctx, model, func(done, total int64) {
				if total > 0 {
					goUI("stt", func() { installBtn.SetText(fmt.Sprintf("Downloading… %d%%", done*100/total)) })
				}
			})
			goUI("stt", func() {
				installBtn.Enable()
				installBtn.SetText("Install Whisper + model")
				if err != nil {
					u.Notify("Speak-to-chat", "Install failed: "+err.Error())
				} else {
					u.Notify("Speak-to-chat", "Whisper ready.")
				}
			})
		})
	})

	body := container.NewVBox(
		mutedLabel("Speak into your mic to post to Twitch chat. Assign record / send / discard / copy to controller buttons or MIDI in VR ▸ Keybinds."),
		labeled("Microphone", container.NewBorder(nil, nil, nil, refresh, inSel)),
		labeled("Output (cues)", outEntry),
		labeled("Model", modelSel),
		container.NewHBox(auto, container.NewGridWrap(fyne.NewSize(72, 24), silence)),
		installBtn,
		mutedLabel("The live transcript + copy/send/retry actions live on the Live tab (Speak-to-chat card - enable it via Edit dashboard)."),
	)
	return featureCard("Speak-to-chat (Whisper STT)", "Local speech-to-text → Twitch chat.", u.simpleToggle(&f.Enabled), st, body)
}

// buildSTTContent is the "stt" Live card: the live transcript preview + quick actions.
// nil when the controller is unavailable.
func (u *UI) buildSTTContent() fyne.CanvasObject {
	if u.svc.STT == nil {
		return nil
	}
	return u.sttPreview()
}

// sttPreview shows the last recognized transcript live + quick actions (copy / send / clear-retry),
// so the user can review what STT heard and re-dictate fast. Updates via the controller's
// SetOnUpdate hook; the same hook is available to a future VR-overlay preview.
func (u *UI) sttPreview() fyne.CanvasObject {
	const placeholder = "(nothing dictated yet)"
	text := widget.NewLabel(placeholder)
	text.Wrapping = fyne.TextWrapWord

	set := func(s string) {
		if s == "" {
			text.SetText(placeholder)
		} else {
			text.SetText(s)
		}
	}
	if u.svc.STT != nil {
		set(u.svc.STT.LastTranscript())
		u.svc.STT.SetOnUpdate(func(s string) { goUI("stt", func() { set(s) }) })
	}

	copyBtn := widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		if u.svc.STT == nil || !u.svc.STT.CopyToClipboard() {
			u.Notify("Speak-to-chat", "Nothing to copy yet.")
			return
		}
		u.Notify("Speak-to-chat", "Transcript copied to clipboard.")
	})
	sendBtn := widget.NewButtonWithIcon("Send to chat", theme.MailSendIcon(), func() {
		if u.svc.STT != nil {
			u.svc.STT.SendLast()
		}
	})
	clearBtn := widget.NewButtonWithIcon("Clear / retry", theme.ViewRefreshIcon(), func() {
		if u.svc.STT != nil {
			u.svc.STT.Clear()
		}
	})

	return container.NewVBox(
		mutedLabel("Last recognized:"),
		text,
		container.NewHBox(copyBtn, sendBtn, clearBtn),
	)
}

// defaultDev is the Select label for "use the system default device".
const defaultDev = "(system default)"
