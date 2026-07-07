package ui

import (
	"context"
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/flipbook"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/vrchat"
)

// buildVRChat is the VRChat tab: status/bio editing (presets + {event} variables, live counters)
// and the animated-emoji "flipbook" sprite-sheet generator. Sign-in itself stays on the Settings
// VRChat card; this tab acts on the logged-in account.
func (u *UI) buildVRChat() fyne.CanvasObject {
	intro := widget.NewLabelWithStyle("VRChat", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	acc := container.NewVBox(
		intro,
		u.vrchatStatusBioCard(),
		u.vrchatEmotesCard(),
	)
	return container.NewVScroll(acc)
}

// ── Status & Bio ──────────────────────────────────────────────────────────────

// vrchatStatusBioCard edits presence + status text + bio with live char counters, presets, and
// {placeholder} variables resolved from rave.page upcoming events (manual fallbacks otherwise).
func (u *UI) vrchatStatusBioCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.VRChat
	mgr := u.svc.Vrchat
	if mgr == nil {
		return widget.NewCard("Status & bio", "", mutedLabel("VRChat link unavailable."))
	}

	// Cached event-derived variables (fetched once; Refresh re-pulls). Manual cfg.BioVars are the
	// fallback base; event values overlay them.
	eventVars := map[string]string{}

	// Status.
	statusSel := widget.NewSelect(vrchat.Statuses, nil)
	statusSel.PlaceHolder = "(presence)"
	descEntry := newEntry()
	descEntry.SetPlaceHolder("status text (≤32)")
	descCount := canvas.NewText("", colMuted)
	descCount.TextSize = 12
	updateDescCount := func() {
		n := utf8.RuneCountInString(descEntry.Text)
		descCount.Text = fmt.Sprintf("%d / %d", n, vrchat.MaxStatusDescription)
		descCount.Color = overColor(n, vrchat.MaxStatusDescription)
		descCount.Refresh()
	}
	descEntry.OnChanged = func(string) { updateDescCount() }
	updateDescCount()

	saveStatus := widget.NewButtonWithIcon("Save status", theme.ConfirmIcon(), func() {
		status, desc := statusSel.Selected, descEntry.Text
		if status == "" {
			u.Notify("VRChat", "Pick a presence first")
			return
		}
		goUI("vrchat-status", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := mgr.UpdateStatus(ctx, status, desc); err != nil {
				u.Notify("VRChat", "Status update failed: "+err.Error())
			} else {
				u.Notify("VRChat", "Status updated")
			}
		})
	})
	saveStatus.Importance = widget.HighImportance

	// Status presets.
	statusPresetSel := widget.NewSelect(statusPresetNames(f.StatusPresets), nil)
	statusPresetSel.PlaceHolder = "(status presets)"
	applyStatusPreset := widget.NewButtonWithIcon("Load", theme.ContentPasteIcon(), func() {
		p := findStatusPreset(f.StatusPresets, statusPresetSel.Selected)
		if p == nil {
			return
		}
		statusSel.SetSelected(p.Status)
		descEntry.SetText(p.Description)
	})
	manageStatusPresets := widget.NewButtonWithIcon("Presets…", theme.SettingsIcon(), func() {
		u.vrchatStatusPresetsDialog(func() { statusPresetSel.SetOptions(statusPresetNames(f.StatusPresets)) },
			func() (string, string) { return statusSel.Selected, descEntry.Text })
	})

	// Bio.
	bioEntry := widget.NewMultiLineEntry()
	bioEntry.Wrapping = fyne.TextWrapWord
	bioEntry.SetPlaceHolder("Bio - use {next_event}, {next_event_date}, {next_event_venue} placeholders…")
	bioCount := canvas.NewText("", colMuted)
	bioCount.TextSize = 12
	preview := widget.NewLabel("")
	preview.Wrapping = fyne.TextWrapWord
	refreshBio := func() {
		resolved := resolveBioText(bioEntry.Text, f.BioVars, eventVars)
		preview.SetText(resolved)
		n := utf8.RuneCountInString(resolved)
		bioCount.Text = fmt.Sprintf("%d / %d (after variables)", n, vrchat.MaxBio)
		bioCount.Color = overColor(n, vrchat.MaxBio)
		bioCount.Refresh()
	}
	bioEntry.OnChanged = func(string) { refreshBio() }
	refreshBio()

	saveBio := widget.NewButtonWithIcon("Save bio", theme.ConfirmIcon(), func() {
		resolved := resolveBioText(bioEntry.Text, f.BioVars, eventVars)
		if utf8.RuneCountInString(resolved) > vrchat.MaxBio {
			u.Notify("VRChat", fmt.Sprintf("Bio is over %d characters", vrchat.MaxBio))
			return
		}
		goUI("vrchat-bio", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := mgr.UpdateBio(ctx, resolved, nil); err != nil {
				u.Notify("VRChat", "Bio update failed: "+err.Error())
			} else {
				u.Notify("VRChat", "Bio updated")
			}
		})
	})
	saveBio.Importance = widget.HighImportance

	// Bio presets + variables.
	bioPresetSel := widget.NewSelect(bioPresetNames(f.BioPresets), nil)
	bioPresetSel.PlaceHolder = "(bio presets)"
	applyBioPreset := widget.NewButtonWithIcon("Load", theme.ContentPasteIcon(), func() {
		p := findBioPreset(f.BioPresets, bioPresetSel.Selected)
		if p == nil {
			return
		}
		bioEntry.SetText(p.Template)
		refreshBio()
	})
	manageBioPresets := widget.NewButtonWithIcon("Presets…", theme.SettingsIcon(), func() {
		u.vrchatBioPresetsDialog(func() { bioPresetSel.SetOptions(bioPresetNames(f.BioPresets)) },
			func() string { return bioEntry.Text })
	})
	varsBtn := widget.NewButtonWithIcon("Variables…", theme.DocumentCreateIcon(), func() {
		u.vrchatBioVarsDialog(bioEntry.Text, refreshBio)
	})

	refreshEvents := widget.NewButtonWithIcon("Refresh events", theme.ViewRefreshIcon(), func() {
		goUI("vrchat-events", func() {
			ev := u.fetchBioEventVars()
			fyne.Do(func() {
				eventVars = ev
				refreshBio()
			})
		})
	})

	// Seed fields from the live account + events on first build.
	goUI("vrchat-seed", func() {
		ev := u.fetchBioEventVars()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		usr, _ := mgr.FetchUser(ctx)
		fyne.Do(func() {
			eventVars = ev
			if usr != nil {
				if vrchat.ValidStatus(usr.Status) {
					statusSel.SetSelected(usr.Status)
				}
				descEntry.SetText(usr.StatusDescription)
				bioEntry.SetText(usr.Bio)
			}
			updateDescCount()
			refreshBio()
		})
	})

	statusBlock := container.NewVBox(
		mutedLabel("Presence + status text:"),
		statusSel,
		container.NewBorder(nil, nil, nil, container.NewHBox(container.NewCenter(descCount)), descEntry),
		container.NewHBox(saveStatus),
		container.NewBorder(nil, nil, nil, container.NewHBox(applyStatusPreset, manageStatusPresets), statusPresetSel),
	)
	bioBlock := container.NewVBox(
		widget.NewSeparator(),
		mutedLabel("Bio (placeholders resolve from rave.page upcoming events; manual fallbacks via Variables):"),
		bioEntry,
		container.NewHBox(container.NewCenter(bioCount)),
		mutedLabel("Preview:"),
		preview,
		container.NewHBox(saveBio, varsBtn, refreshEvents),
		container.NewBorder(nil, nil, nil, container.NewHBox(applyBioPreset, manageBioPresets), bioPresetSel),
	)
	return widget.NewCard("Status & bio", "", container.NewVBox(statusBlock, bioBlock))
}

// ── Emotes (flipbook generator) ───────────────────────────────────────────────

// vrchatEmotesCard is the animated-emoji sprite-sheet generator: pick a video/GIF, choose tier +
// FPS + trim + crop + ping-pong, generate the 1024² sheet, then upload it on the VRChat website.
func (u *UI) vrchatEmotesCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.VRChat

	srcEntry := newEntry()
	srcEntry.SetPlaceHolder("video or GIF file")
	nameEntry := newEntry()
	nameEntry.SetPlaceHolder("emoji name")

	// Tier select.
	tierByLabel := map[string]int{}
	var tierOpts []string
	for _, t := range flipbook.Tiers() {
		lbl := fmt.Sprintf("%d frames (%d×%d, %dpx)", t.Frames, t.Grid, t.Grid, t.FrameRes)
		tierOpts = append(tierOpts, lbl)
		tierByLabel[lbl] = t.Frames
	}
	tierSel := widget.NewSelect(tierOpts, nil)
	tierSel.SetSelectedIndex(1) // 16-frame default

	fpsEntry := newEntry()
	fpsEntry.SetText("20")
	trimStart := newEntry()
	trimStart.SetPlaceHolder("trim start s (optional)")
	trimEnd := newEntry()
	trimEnd.SetPlaceHolder("trim end s (optional)")

	pingPong := widget.NewCheck("Bake ping-pong (forward then reversed)", nil)

	cropChk := widget.NewCheck("Crop", nil)
	cropX, cropY := newEntry(), newEntry()
	cropW, cropH := newEntry(), newEntry()
	cropX.SetPlaceHolder("x")
	cropY.SetPlaceHolder("y")
	cropW.SetPlaceHolder("w")
	cropH.SetPlaceHolder("h")
	cropRow := container.NewGridWithColumns(4, cropX, cropY, cropW, cropH)
	cropRow.Hide()
	cropChk.OnChanged = func(on bool) {
		if on {
			cropRow.Show()
		} else {
			cropRow.Hide()
		}
	}

	outEntry := newEntry()
	outEntry.SetText(f.ResolvedFlipbookDir())
	outEntry.OnChanged = func(s string) {
		f.FlipbookDir = strings.TrimSpace(s)
		u.saveCfg()
	}

	result := widget.NewLabel("")
	result.Wrapping = fyne.TextWrapWord
	openFolderBtn := widget.NewButtonWithIcon("Open output folder", theme.FolderOpenIcon(), func() {
		openInExplorer(u, f.ResolvedFlipbookDir())
	})
	uploadBtn := widget.NewButtonWithIcon("Open VRChat emoji upload page", theme.ComputerIcon(), func() {
		if uri := mustURL(flipbook.EmojiUploadURL); uri != nil {
			_ = u.app.OpenURL(uri)
		}
	})

	generate := widget.NewButtonWithIcon("Generate sprite sheet", theme.MediaPlayIcon(), nil)
	generate.Importance = widget.HighImportance
	generate.OnTapped = func() {
		opts, err := buildFlipbookOptions(srcEntry.Text, nameEntry.Text, tierByLabel[tierSel.Selected],
			fpsEntry.Text, trimStart.Text, trimEnd.Text, cropChk.Checked, cropX.Text, cropY.Text, cropW.Text, cropH.Text,
			pingPong.Checked, f.ResolvedFlipbookDir())
		if err != nil {
			u.Notify("VRChat emoji", err.Error())
			return
		}
		ffmpeg, ok := mediatools.Resolve("ffmpeg")
		if !ok {
			u.Notify("VRChat emoji", "ffmpeg not found - install it from Settings ▸ Media tools")
			return
		}
		generate.Disable()
		result.SetText("Generating…")
		goUI("flipbook-gen", func() {
			out, genErr := flipbook.Generate(ffmpeg, opts)
			fyne.Do(func() {
				generate.Enable()
				if genErr != nil {
					result.SetText("Failed: " + genErr.Error())
					return
				}
				result.SetText("Saved: " + out + "\nUpload on the VRChat website (Gallery ▸ Emoji), enable Sprite Sheet Mode. Custom emoji need VRC+.")
			})
		})
	}

	form := formGrid(
		fieldLabel("Source"), filePickerRow(srcEntry, ".mp4", ".mov", ".webm", ".gif", ".mkv", ".avi"),
		fieldLabel("Name"), nameEntry,
		fieldLabel("Frames"), tierSel,
		fieldLabel("FPS"), fpsEntry,
		fieldLabel("Trim start"), trimStart,
		fieldLabel("Trim end"), trimEnd,
		fieldLabel("Output dir"), folderPickerRow(outEntry),
	)
	body := container.NewVBox(
		mutedLabel("Generate a 1024×1024 VRChat animated-emoji sprite sheet (named <name>_<N>frames_<fps>fps.png so VRChat reads the defaults)."),
		form,
		cropChk, cropRow,
		pingPong,
		container.NewHBox(generate),
		result,
		container.NewHBox(openFolderBtn, uploadBtn),
	)
	return widget.NewCard("Emotes - animated-emoji generator", "", body)
}

// buildFlipbookOptions parses + validates the emote form into flipbook.Options.
func buildFlipbookOptions(src, name string, frames int, fpsStr, startStr, endStr string,
	doCrop bool, cx, cy, cw, ch string, pingPong bool, outDir string) (flipbook.Options, error) {
	if strings.TrimSpace(src) == "" {
		return flipbook.Options{}, fmt.Errorf("pick a source video/GIF first")
	}
	if frames == 0 {
		return flipbook.Options{}, fmt.Errorf("pick a frame tier")
	}
	fps, err := strconv.ParseFloat(strings.TrimSpace(fpsStr), 64)
	if err != nil || fps <= 0 {
		return flipbook.Options{}, fmt.Errorf("FPS must be a positive number")
	}
	o := flipbook.Options{
		Input: strings.TrimSpace(src), OutName: name, Frames: frames, FPS: fps,
		TrimStart: parseSecs(startStr), TrimEnd: parseSecs(endStr), PingPong: pingPong, OutDir: outDir,
	}
	if doCrop {
		r := flipbook.Rect{X: parseInt(cx), Y: parseInt(cy), W: parseInt(cw), H: parseInt(ch)}
		o.Crop = &r
	}
	if err := o.Validate(); err != nil {
		return flipbook.Options{}, err
	}
	return o, nil
}

func parseSecs(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func parseInt(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}

// ── bio variable resolution ───────────────────────────────────────────────────

// fetchBioEventVars resolves {next_event}/{next_event_date} from the soonest upcoming rave.page
// event. {next_event_venue} isn't carried by the events list - it stays a manual BioVars value.
// Returns an empty map when events are unavailable (caller falls back to manual vars).
func (u *UI) fetchBioEventVars() map[string]string {
	out := map[string]string{}
	if u.svc.API == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	events, err := u.svc.API.ListEvents(ctx, u.getToken(), "", "", 50)
	if err != nil || len(events) == 0 {
		return out
	}
	now := time.Now()
	var next *api.Event
	for i := range events {
		e := &events[i]
		if e.Start.Before(now) {
			continue
		}
		if next == nil || e.Start.Before(next.Start) {
			next = e
		}
	}
	if next == nil {
		return out
	}
	out["next_event"] = next.Title
	out["next_event_date"] = next.Start.Format("Mon Jan 2")
	return out
}

// resolveBioText substitutes {placeholders} in tmpl. Manual vars are the base; event-derived
// values overlay them (non-empty only). Reuses the twitch template helpers (pure string funcs).
func resolveBioText(tmpl string, manual, event map[string]string) string {
	vars := map[string]string{}
	for k, v := range manual {
		vars[k] = v
	}
	for k, v := range event {
		if v != "" {
			vars[k] = v
		}
	}
	return twitch.ResolveTemplate(tmpl, vars)
}

// overColor returns the brand "hot" tint when n exceeds max (over-limit warning), else muted.
func overColor(n, max int) color.Color {
	if n > max {
		return colBrandHot
	}
	return colMuted
}

// ── preset dialogs ────────────────────────────────────────────────────────────

// vrchatStatusPresetsDialog manages status presets; current() yields the live status+desc for "Add".
func (u *UI) vrchatStatusPresetsDialog(onChange func(), current func() (string, string)) {
	f := &u.svc.Cfg.Features.VRChat
	list := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		list.RemoveAll()
		for i := range f.StatusPresets {
			p := &f.StatusPresets[i]
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.StatusPresets = append(f.StatusPresets[:i], f.StatusPresets[i+1:]...)
				u.saveCfg()
				rebuild()
				onChange()
			})
			row := container.NewBorder(nil, nil, nil, del,
				widget.NewLabel(fmt.Sprintf("%s - %s / %s", p.Name, p.Status, p.Description)))
			list.Add(row)
		}
		if len(f.StatusPresets) == 0 {
			list.Add(mutedLabel("No status presets yet."))
		}
		list.Refresh()
	}
	rebuild()

	nameEntry := newEntry()
	nameEntry.SetPlaceHolder("preset name")
	addBtn := widget.NewButtonWithIcon("Add from current", theme.ContentAddIcon(), func() {
		status, desc := current()
		name := strings.TrimSpace(nameEntry.Text)
		if name == "" || status == "" {
			u.Notify("VRChat", "Set a preset name + pick a presence first")
			return
		}
		f.StatusPresets = append(f.StatusPresets, config.VRChatStatusPreset{Name: name, Status: status, Description: desc})
		u.saveCfg()
		nameEntry.SetText("")
		rebuild()
		onChange()
	})
	content := container.NewBorder(nil, container.NewBorder(nil, nil, nil, addBtn, nameEntry), nil, nil, container.NewVScroll(list))
	d := dialog.NewCustom("Status presets", "Done", content, u.win)
	d.Resize(fyne.NewSize(520, 420))
	d.Show()
}

// vrchatBioPresetsDialog manages bio presets; current() yields the live bio text for "Add".
func (u *UI) vrchatBioPresetsDialog(onChange func(), current func() string) {
	f := &u.svc.Cfg.Features.VRChat
	list := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		list.RemoveAll()
		for i := range f.BioPresets {
			p := &f.BioPresets[i]
			edit := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), func() {
				u.vrchatEditBioPresetDialog(p, func() { rebuild(); onChange() })
			})
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.BioPresets = append(f.BioPresets[:i], f.BioPresets[i+1:]...)
				u.saveCfg()
				rebuild()
				onChange()
			})
			row := container.NewBorder(nil, nil, nil, container.NewHBox(edit, del),
				widget.NewLabel(p.Name))
			list.Add(row)
		}
		if len(f.BioPresets) == 0 {
			list.Add(mutedLabel("No bio presets yet."))
		}
		list.Refresh()
	}
	rebuild()

	nameEntry := newEntry()
	nameEntry.SetPlaceHolder("preset name")
	addBtn := widget.NewButtonWithIcon("Add from current bio", theme.ContentAddIcon(), func() {
		name := strings.TrimSpace(nameEntry.Text)
		if name == "" {
			u.Notify("VRChat", "Set a preset name first")
			return
		}
		f.BioPresets = append(f.BioPresets, config.VRChatBioPreset{Name: name, Template: current()})
		u.saveCfg()
		nameEntry.SetText("")
		rebuild()
		onChange()
	})
	content := container.NewBorder(nil, container.NewBorder(nil, nil, nil, addBtn, nameEntry), nil, nil, container.NewVScroll(list))
	d := dialog.NewCustom("Bio presets", "Done", content, u.win)
	d.Resize(fyne.NewSize(560, 460))
	d.Show()
}

// vrchatEditBioPresetDialog edits one bio preset's name/template.
func (u *UI) vrchatEditBioPresetDialog(p *config.VRChatBioPreset, onSave func()) {
	name := newEntry()
	name.SetText(p.Name)
	tmpl := widget.NewMultiLineEntry()
	tmpl.Wrapping = fyne.TextWrapWord
	tmpl.SetText(p.Template)
	content := container.NewBorder(
		widget.NewForm(widget.NewFormItem("Name", name)),
		mutedLabel("Use {next_event}, {next_event_date}, {next_event_venue} or your own {placeholders}."),
		nil, nil, tmpl)
	d := dialog.NewCustomConfirm("Edit bio preset", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		p.Name, p.Template = name.Text, tmpl.Text
		u.saveCfg()
		onSave()
	}, u.win)
	d.Resize(fyne.NewSize(520, 380))
	d.Show()
}

// vrchatBioVarsDialog edits the manual bio variable fallbacks. Placeholders found in the current
// bio template are pre-listed; event-resolved vars (next_event/next_event_date) are noted as auto.
func (u *UI) vrchatBioVarsDialog(currentBio string, onSave func()) {
	f := &u.svc.Cfg.Features.VRChat
	if f.BioVars == nil {
		f.BioVars = map[string]string{}
	}
	// Union of placeholders in the bio + already-saved manual vars.
	keys := map[string]bool{}
	for _, k := range twitch.TemplateVars(currentBio) {
		keys[k] = true
	}
	for k := range f.BioVars {
		keys[k] = true
	}
	var ordered []string
	for k := range keys {
		ordered = append(ordered, k)
	}
	entries := map[string]*widget.Entry{}
	var items []*widget.FormItem
	for _, k := range ordered {
		e := newEntry()
		e.SetText(f.BioVars[k])
		entries[k] = e
		items = append(items, widget.NewFormItem(k, e))
	}
	if len(items) == 0 {
		items = append(items, widget.NewFormItem("", mutedLabel("Add {placeholders} to your bio first.")))
	}
	content := container.NewVBox(
		mutedLabel("Manual fallback values. {next_event} and {next_event_date} auto-resolve from rave.page events when available; {next_event_venue} is always taken from here."),
		widget.NewForm(items...),
	)
	d := dialog.NewCustomConfirm("Bio variables", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		for k, e := range entries {
			v := strings.TrimSpace(e.Text)
			if v == "" {
				delete(f.BioVars, k)
			} else {
				f.BioVars[k] = v
			}
		}
		u.saveCfg()
		onSave()
	}, u.win)
	d.Resize(fyne.NewSize(480, 360))
	d.Show()
}

// ── preset name/lookup helpers ────────────────────────────────────────────────

func statusPresetNames(ps []config.VRChatStatusPreset) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func findStatusPreset(ps []config.VRChatStatusPreset, name string) *config.VRChatStatusPreset {
	for i := range ps {
		if ps[i].Name == name {
			return &ps[i]
		}
	}
	return nil
}

func bioPresetNames(ps []config.VRChatBioPreset) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func findBioPreset(ps []config.VRChatBioPreset, name string) *config.VRChatBioPreset {
	for i := range ps {
		if ps[i].Name == name {
			return &ps[i]
		}
	}
	return nil
}
