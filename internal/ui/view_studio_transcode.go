package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/transcode"
)

// transcodeBuilder holds the live encoder-builder widget state for one selected file. It
// mirrors the web Local Studio panel: logical codec + hardware-accel + quality profile,
// rate control, resolution/fps/speed/GOP/tune, and a full audio section.
type transcodeBuilder struct {
	sv  *studioView
	e   fileEntry
	pre transcode.Preset

	forceVideo bool // preset editor: always offer the video section (any mode)

	src *transcode.SourceInfo // probed source (nil = unknown/not yet probed)

	currentProfile string // name of the active quality profile, drives the SegmentedButtons highlight

	resolvedLbl *widget.Label
	hintLbl     *widget.Label // source summary + up-encode warnings
	loudLbl     *widget.Label // measured loudness + planned gain
	loudMeas    *transcode.Measurement
	trimS       *widget.Entry
	trimE       *widget.Entry
	rebuildHook func(transcode.Preset)
}

// availablePresets returns the presets offered for the current mode (audio-only in Music).
func (sv *studioView) availablePresets() []transcode.Preset {
	all := transcode.AllPresets(sv.u.svc.Cfg.Features.Transcode.Presets)
	if sv.mode != "music" {
		return all
	}
	out := all[:0:0]
	for _, p := range all {
		if p.IsAudioOnly() {
			out = append(out, p)
		}
	}
	return out
}

// transcodePanel renders the preset chooser + live builder + Start for a media file.
func (sv *studioView) transcodePanel(e fileEntry) fyne.CanvasObject {
	presets := sv.availablePresets()
	if len(presets) == 0 {
		return mutedLabel("No presets available.")
	}

	seed := presets[0]
	if sv.seedPreset != nil {
		seed = *sv.seedPreset
	}
	b := &transcodeBuilder{sv: sv, e: e, pre: seed}
	b.resolvedLbl = mutedLabel("")
	b.hintLbl = mutedLabel("")
	if si, ok := sv.srcCache[e.path]; ok {
		b.src = si
	} else {
		sv.ensureSource(e.path) // async; re-renders the detail when done
	}

	// Preset picker: collapsed = SplitButton showing the current preset (and acting
	// as the primary "transcode with this" affordance). An inline expand toggle next
	// to it reveals all presets as a wrap-row (no horizontal overflow, no scroll).
	presetLabels := make([]string, 0, len(presets))
	for _, p := range presets {
		presetLabels = append(presetLabels, p.Label)
	}
	pickPreset := func(label string) {
		for _, p := range presets {
			if p.Label == label {
				if p.ID != b.pre.ID {
					b.pre = p
					sv.rebuildTranscodeDetail(e, b.pre)
				}
				return
			}
		}
	}
	presetPicker := SplitButton(b.pre.Label, presetLabels, pickPreset)
	expandBtn := widget.NewButtonWithIcon("Show all", theme.MenuExpandIcon(), nil)
	allPresets := WrapActions()
	for _, p := range presets {
		btn := widget.NewButton(p.Label, func() { pickPreset(p.Label) })
		btn.Importance = lowOrHigh(p.ID == b.pre.ID)
		allPresets.Add(btn) // *fyne.Container, OK because WrapActions returned a container
	}
	allPresets.Hide()
	expandBtn.OnTapped = func() {
		if allPresets.Visible() {
			allPresets.Hide()
			expandBtn.SetIcon(theme.MenuExpandIcon())
		} else {
			allPresets.Show()
			expandBtn.SetIcon(theme.MenuDropDownIcon())
		}
	}
	presetRow := container.NewBorder(nil, nil, nil, expandBtn, presetPicker)

	form := b.buildForm()
	b.refreshResolved()

	start := widget.NewButtonWithIcon("Start Transcode", theme.MediaPlayIcon(), func() {
		sv.startTranscode(e, b.pre, b.trimS.Text, b.trimE.Text)
	})
	start.Importance = widget.HighImportance
	saveAs := widget.NewButtonWithIcon("Save as new…", theme.DocumentSaveIcon(), func() { sv.savePresetAs(b.pre) })

	b.refreshHints()
	rows := []fyne.CanvasObject{
		presetRow,
		allPresets, // hidden until expand toggle; WrapActions wraps to multiple rows
		mutedLabel(b.pre.Desc),
		widget.NewSeparator(),
		form,
		b.resolvedLbl,
		b.hintLbl,
	}
	if sv.mode == "media" {
		b.trimS = newEntry()
		b.trimS.SetText("0")
		b.trimE = newEntry()
		b.trimE.SetPlaceHolder("end (s, optional)")
		silence := widget.NewButtonWithIcon("Detect silence", theme.SearchIcon(), func() { sv.detectSilence(e, b.trimS) })
		rows = append(rows,
			b.objectPair(b.field("Trim start", b.trimS), b.field("Trim end", b.trimE)),
			silence)
	} else {
		b.trimS, b.trimE = newEntry(), newEntry() // unused in music mode
	}
	rows = append(rows,
		b.objectPair(start, saveAs),
		mutedLabel("Output → a new ‘rave-mate-transcoded’ folder next to the source - the original is untouched."),
	)
	return container.NewVBox(rows...)
}

// rebuildTranscodeDetail re-renders the whole detail with a chosen preset preloaded.
func (sv *studioView) rebuildTranscodeDetail(e fileEntry, p transcode.Preset) {
	// Re-select the file but seed the builder by temporarily overriding the first preset.
	sv.selectFileWithPreset(e, &p)
}

// buildForm constructs the live builder widgets, wiring each to mutate b.pre.
func (b *transcodeBuilder) buildForm() fyne.CanvasObject {
	b.pre = transcode.NormalizePreset(b.pre)
	rows := []fyne.CanvasObject{}

	containerSel := selectValue(withSelected(transcode.Containers(), b.pre.Container), b.pre.Container, func(s string) {
		b.pre.Container = s
		b.pre = transcode.NormalizePreset(b.pre)
		b.applyAndRebuild()
	})
	rows = append(rows, b.field("Container", containerSel), mutedLabel(b.containerGuide()))

	if b.sv.mode == "media" || b.forceVideo {
		rows = append(rows, b.videoSection()...)
	} else {
		b.pre.VideoCodec = "none"
	}
	rows = append(rows, b.audioSection()...)
	return container.NewVBox(rows...)
}

func (b *transcodeBuilder) videoSection() []fyne.CanvasObject {
	codecOpts := transcode.VideoCodecsForContainer(b.pre.Container, true)
	if !contains(codecOpts, b.pre.VideoCodec) {
		b.pre.VideoCodec = codecOpts[0]
	}
	encoding := b.videoEncoding()
	codecSel := selectValue(codecOpts, b.pre.VideoCodec, func(s string) {
		b.pre.VideoCodec = s
		b.pre.EncoderOverride = ""
		b.pre = transcode.NormalizePreset(b.pre)
		b.applyAndRebuild()
	})
	if len(codecOpts) == 1 {
		codecSel.Disable()
	}

	accelOpts := b.availableAccels()
	accel := orDefault(b.pre.Accel, "auto")
	if !contains(accelOpts, accel) {
		accel = "auto"
	}
	b.pre.Accel = accel
	accelSel := selectValue(accelOpts, accel, func(s string) {
		b.pre.Accel = s
		b.pre.EncoderOverride = ""
		b.applyAndRebuild()
	})
	if !encoding || len(accelOpts) == 1 {
		accelSel.Disable()
	}

	rateMode := orDefault(b.pre.RateMode, "crf")
	rateModeSel := selectValue([]string{"crf", "bitrate"}, rateMode, func(s string) {
		b.pre.RateMode = s
		if s == "bitrate" && b.pre.BitrateK <= 0 {
			b.pre.BitrateK = b.videoRecommendK("youtube-hq")
		}
		b.applyAndRebuild()
	})
	if !encoding {
		rateModeSel.Disable()
	}

	crfEnt := newEntry()
	crfEnt.SetPlaceHolder(b.crfGuide())
	if b.pre.CRF > 0 {
		crfEnt.SetText(strconv.Itoa(b.pre.CRF))
	}
	crfEnt.OnChanged = func(s string) {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			b.pre.RateMode, b.pre.CRF = "crf", n
			b.refreshHints()
		}
	}
	if !encoding || rateMode != "crf" {
		crfEnt.Disable()
	}

	bitrateEnt := newEntry()
	if recK := b.videoRecommendK("youtube-hq"); recK > 0 {
		bitrateEnt.SetPlaceHolder(formatBitrateK(recK) + " suggested")
	} else {
		bitrateEnt.SetPlaceHolder("kbps or Mbps")
	}
	if b.pre.BitrateK > 0 {
		bitrateEnt.SetText(strconv.Itoa(b.pre.BitrateK))
	}
	bitrateEnt.OnChanged = func(s string) {
		if n, ok := parseBitrateK(s); ok && n > 0 {
			b.pre.RateMode, b.pre.BitrateK = "bitrate", n
			b.refreshHints()
		}
	}
	if !encoding || rateMode != "bitrate" {
		bitrateEnt.Disable()
	}

	heightEnt := newEntry()
	heightEnt.SetPlaceHolder("source")
	if b.pre.Height > 0 {
		heightEnt.SetText(strconv.Itoa(b.pre.Height))
	}
	heightEnt.OnChanged = func(s string) { b.pre.Height = atoiOr(s, 0); b.refreshHints() }
	widthEnt := newEntry()
	widthEnt.SetPlaceHolder("source")
	if b.pre.Width > 0 {
		widthEnt.SetText(strconv.Itoa(b.pre.Width))
	}
	widthEnt.OnChanged = func(s string) { b.pre.Width = atoiOr(s, 0); b.refreshHints() }
	if !encoding {
		widthEnt.Disable()
		heightEnt.Disable()
	}

	fpsEnt := newEntry()
	fpsEnt.SetPlaceHolder("source")
	if b.pre.FPS > 0 {
		fpsEnt.SetText(strconv.FormatFloat(b.pre.FPS, 'f', -1, 64))
	}
	fpsEnt.OnChanged = func(s string) {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			b.pre.FPS = f
			b.refreshHints()
		}
	}
	if !encoding {
		fpsEnt.Disable()
	}

	speedOpts := speedOptionsForEncoder(b.resolvedEncoderName())
	if !contains(speedOpts, b.pre.SpeedPreset) {
		b.pre.SpeedPreset = ""
	}
	speedSel := selectValue(speedOpts, b.pre.SpeedPreset, func(s string) { b.pre.SpeedPreset = s })
	if !encoding || len(speedOpts) <= 1 {
		speedSel.Disable()
	}

	gopEnt := newEntry()
	gopEnt.SetText(strconv.FormatFloat(orFloat(b.pre.GOPSeconds, 2), 'f', -1, 64))
	gopEnt.OnChanged = func(s string) {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			b.pre.GOPSeconds = f
		}
	}
	if !encoding {
		gopEnt.Disable()
	}

	tuneSel := selectValue([]string{"", "film", "animation", "grain"}, b.pre.Tune, func(s string) { b.pre.Tune = s })
	if !encoding || !b.tuneAvailable() {
		b.pre.Tune = ""
		tuneSel.Disable()
	}

	deint := checkValue("Deinterlace (bwdif)", b.pre.Deinterlace, func(v bool) { b.pre.Deinterlace = v })
	if !encoding {
		deint.Disable()
	}

	return []fyne.CanvasObject{
		smallCaps("VIDEO"),
		mutedLabel(b.videoGuide()),
		b.objectPair(b.field("Codec", codecSel), b.field("HW accel", accelSel)),
		b.field("Profiles", b.profileButtons(encoding)),
		mutedLabel(b.rateGuide()),
		b.objectPair(b.field("Rate mode", rateModeSel), b.field("CRF/CQ", crfEnt)),
		b.field("Force bitrate", bitrateEnt),
		b.field("Resolution presets", b.resolutionButtons(encoding)),
		b.objectPair(b.field("Width", widthEnt), b.field("Height", heightEnt)),
		b.objectPair(b.field("fps", fpsEnt), b.field("GOP (s)", gopEnt)),
		b.objectPair(b.field("Speed", speedSel), b.field("Tune", tuneSel)),
		deint,
	}
}

func (b *transcodeBuilder) audioSection() []fyne.CanvasObject {
	ac := orDefault(b.pre.AudioCodec, "aac")
	codecOpts := transcode.AudioCodecsForContainer(b.pre.Container)
	if !contains(codecOpts, ac) {
		ac = codecOpts[0]
		b.pre.AudioCodec = ac
		b.pre = transcode.NormalizePreset(b.pre)
	}
	encoding := ac != "copy" && ac != "none" && ac != ""
	codecSel := selectValue(codecOpts, ac, func(s string) {
		b.pre.AudioCodec = s
		b.pre = transcode.NormalizePreset(b.pre)
		b.applyAndRebuild()
	})
	if len(codecOpts) == 1 {
		codecSel.Disable()
	}

	bitrateOpts, bitrateByLabel, bitrateSelected := b.audioBitrateOptions(ac)
	bitrateSel := selectValue(bitrateOpts, bitrateSelected, func(s string) {
		if k, ok := bitrateByLabel[s]; ok {
			b.pre.AudioBitrateK = k
			b.refreshHints()
		}
	})
	if len(bitrateOpts) == 0 || !encoding || (ac == "mp3" && b.pre.AudioVBR) {
		bitrateSel.Disable()
	}

	vbr := checkValue("MP3 VBR (-q:a)", b.pre.AudioVBR, func(v bool) {
		b.pre.AudioVBR = v
		b.pre = transcode.NormalizePreset(b.pre)
		b.applyAndRebuild()
	})
	if ac != "mp3" || !encoding {
		vbr.Disable()
	}

	chanSel := selectValue([]string{"source", "1", "2"}, chanLabel(b.pre.Channels), func(s string) { b.pre.Channels = atoiOr(s, 0) })
	rateSel := selectValue([]string{"source", "44100", "48000", "96000"}, rateLabel(b.pre.SampleRate), func(s string) { b.pre.SampleRate = atoiOr(s, 0) })
	if !encoding {
		chanSel.Disable()
		rateSel.Disable()
	}

	rows := []fyne.CanvasObject{
		smallCaps("AUDIO"),
		mutedLabel(b.audioGuide(ac)),
		b.objectPair(b.field("Codec", codecSel), b.field("Bitrate", bitrateSel)),
		b.objectPair(b.field("Channels", chanSel), b.field("Sample rate", rateSel)),
		vbr,
	}
	return append(rows, b.loudnessSection(encoding)...)
}

// loudnessSection renders the whole-track linear normalization controls: enable, target
// (industry presets + granular LUFS), true-peak ceiling, raise-only - plus the measured
// source loudness and the exact gain that will be applied.
func (b *transcodeBuilder) loudnessSection(encoding bool) []fyne.CanvasObject {
	on := checkValue("Normalize loudness - whole-track gain, no compression", b.pre.LoudnessOn, func(v bool) {
		b.pre.LoudnessOn = v
		b.applyAndRebuild()
	})
	if !encoding {
		on.Disable()
	}
	rows := []fyne.CanvasObject{smallCaps("LOUDNESS"), on}
	if !encoding {
		return append(rows, mutedLabel("Normalization needs an audio re-encode - pick an audio codec other than copy/none."))
	}
	if !b.pre.LoudnessOn {
		return append(rows, mutedLabel("Off - the output keeps the source's loudness exactly as it is."))
	}
	if b.pre.LoudnessI == 0 {
		b.pre.LoudnessI = -14
	}

	b.loudLbl = mutedLabel("")
	iEnt := newEntry()
	iEnt.SetText(strconv.FormatFloat(b.pre.LoudnessI, 'f', -1, 64))
	iEnt.OnChanged = func(s string) {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && f < 0 {
			b.pre.LoudnessI = f
			b.refreshLoudness()
		}
	}
	tpEnt := newEntry()
	tpEnt.SetPlaceHolder(fmt.Sprintf("%.1f (default)", transcode.DefaultLoudnessTP))
	if b.pre.LoudnessTP != 0 {
		tpEnt.SetText(strconv.FormatFloat(b.pre.LoudnessTP, 'f', -1, 64))
	}
	tpEnt.OnChanged = func(s string) {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && f <= 0 {
			b.pre.LoudnessTP = f
			b.refreshLoudness()
		}
	}

	targets := transcode.LoudnessTargets()
	labels := make([]string, 0, len(targets))
	cur := ""
	for _, t := range targets {
		labels = append(labels, t.Label)
		if t.I == b.pre.LoudnessI {
			cur = t.Label
		}
	}
	targetSel := selectValue(labels, cur, func(s string) {
		for _, t := range targets {
			if t.Label == s {
				b.pre.LoudnessI, b.pre.LoudnessTP = t.I, t.TP
				iEnt.SetText(strconv.FormatFloat(t.I, 'f', -1, 64))
				tpEnt.SetText(strconv.FormatFloat(t.TP, 'f', -1, 64))
				b.refreshLoudness()
				return
			}
		}
	})
	targetSel.PlaceHolder = "(custom target)"

	raise := checkValue("Only raise quiet tracks - never turn a loud track down", b.pre.LoudnessRaiseOnly, func(v bool) {
		b.pre.LoudnessRaiseOnly = v
		b.refreshLoudness()
	})

	rows = append(rows,
		mutedLabel(loudnessGuide()),
		b.field("Target", targetSel),
		b.objectPair(b.field("Integrated target (LUFS)", iEnt), b.field("True-peak ceiling (dBTP)", tpEnt)),
		raise,
		b.loudLbl,
	)
	if !b.forceVideo && b.e.path != "" {
		remeasure := widget.NewButtonWithIcon("Measure again", theme.ViewRefreshIcon(), func() {
			delete(b.sv.loudCache, b.e.path)
			b.loudMeas = nil
			b.refreshLoudness()
			b.ensureLoudness(true)
		})
		remeasure.Importance = widget.LowImportance
		rows = append(rows, container.NewHBox(remeasure))
		b.ensureLoudness(false)
	}
	b.refreshLoudness()
	return rows
}

// loudnessGuide explains exactly what normalization does + the industry targets.
func loudnessGuide() string {
	return "Two-pass and fully transparent: pass 1 measures the whole track (EBU R128 integrated loudness + true peak), " +
		"pass 2 applies ONE constant volume change to the entire track - no compression, limiting or dynamics processing, " +
		"so the mix and transients stay untouched. Gain up is capped so the true peak never exceeds the ceiling " +
		"(a track that can't reach the target without clipping gets less gain, never a limiter).\n" +
		"Industry targets: Spotify / YouTube / Tidal / Amazon normalize playback to −14 LUFS, Apple Music −16, Deezer −15, " +
		"EBU R128 broadcast −23. Club/DJ-pool masters typically run −8…−9 LUFS. " +
		"−1 dBTP ceiling is the streaming norm; use −2 dBTP for lossy encodes ≤ 128 kbps."
}

// refreshLoudness renders the measured source loudness + the exact planned gain.
func (b *transcodeBuilder) refreshLoudness() {
	if b.loudLbl == nil || !b.pre.LoudnessOn {
		return
	}
	if b.forceVideo || b.e.path == "" {
		b.loudLbl.SetText("Each file is measured at transcode time; the applied gain is reported per job.")
		return
	}
	if b.loudMeas == nil {
		if m, ok := b.sv.loudCache[b.e.path]; ok {
			b.loudMeas = &m
		} else {
			b.loudLbl.SetText("Measuring source loudness (EBU R128)…")
			return
		}
	}
	m := *b.loudMeas
	plan := transcode.PlanGain(m, b.pre.LoudnessI, b.pre.EffectiveTP(), b.pre.LoudnessRaiseOnly)
	line := fmt.Sprintf("Source: %.1f LUFS integrated · %.1f dBTP true peak · LRA %.1f LU", m.I, m.TP, m.LRA)
	switch {
	case plan.Skipped && b.pre.LoudnessRaiseOnly && m.I > -70:
		line += fmt.Sprintf("\n→ already at/above %.1f LUFS - left untouched (raise-only).", b.pre.LoudnessI)
	case plan.Skipped:
		line += "\n→ source is silent - nothing to normalize."
	case plan.PeakCapped:
		line += fmt.Sprintf("\n→ %+.1f dB will be applied (target wants %+.1f dB; capped by the %.1f dBTP ceiling - no limiter is used).",
			plan.GainDB, b.pre.LoudnessI-m.I, b.pre.EffectiveTP())
	default:
		line += fmt.Sprintf("\n→ %+.1f dB constant gain over the whole track → %.1f LUFS.", plan.GainDB, b.pre.LoudnessI)
	}
	b.loudLbl.SetText(line)
}

// ensureLoudness fetches the EBU R128 measurement for the selected file (store-cached
// across restarts, out-of-process ffmpeg via the transcode worker).
func (b *transcodeBuilder) ensureLoudness(force bool) {
	sv, path := b.sv, b.e.path
	if path == "" || sv.u.svc.Workers == nil {
		return
	}
	if m, ok := sv.loudCache[path]; ok && !force {
		b.loudMeas = &m
		return
	}
	if sv.loudLoading[path] {
		return
	}
	sv.loudLoading[path] = true
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-loudness", false)
		mtime := fileMtime(path)
		raw, cached := sv.u.svc.Store.GetAnalysis(store.KindLoudness, path, mtime)
		if force || !cached {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			var err error
			raw, err = sv.u.svc.Workers.RunBackground(ctx, "transcode", "transcode.measure", map[string]any{"input": path})
			if err != nil {
				fyne.Do(func() {
					sv.loudLoading[path] = false
					if b.loudLbl != nil && b.pre.LoudnessOn {
						b.loudLbl.SetText("Loudness measurement failed: " + err.Error())
					}
				})
				return
			}
			sv.u.svc.Store.PutAnalysis(store.KindLoudness, path, mtime, raw)
		}
		var m transcode.Measurement
		if json.Unmarshal(raw, &m) != nil {
			fyne.Do(func() { sv.loudLoading[path] = false })
			return
		}
		fyne.Do(func() {
			sv.loudLoading[path] = false
			sv.loudCache[path] = m
			b.loudMeas = &m
			b.refreshLoudness()
		})
	}()
}

// refreshResolved updates the "resolved encoder" hint from the current codec/accel + the
// detected working encoders.
func (b *transcodeBuilder) refreshResolved() {
	if b.resolvedLbl == nil {
		return
	}
	if b.sv.workingEnc == nil {
		b.resolvedLbl.SetText("Detecting hardware encoders…")
		return
	}
	hw := 0
	for _, ok := range b.sv.workingEnc {
		if ok {
			hw++
		}
	}
	enc, ok := transcode.ResolveEncoder(b.pre.VideoCodec, b.pre.Accel, b.sv.workingEnc)
	switch {
	case !ok:
		b.resolvedLbl.SetText(fmt.Sprintf("· %d HW encoders available", hw))
	default:
		b.resolvedLbl.SetText(fmt.Sprintf("Resolved encoder → %s · %d HW available", enc, hw))
	}
}

// applyAndRebuild re-renders the per-file panel seeded with the mutated preset so dependent
// widgets (bitrate fields, enable/disable gates) reflect the change. In the preset-editor
// dialog (forceVideo, no file) there's nothing to rebuild - the change still lives on b.pre.
func (b *transcodeBuilder) applyAndRebuild() {
	b.pre = transcode.NormalizePreset(b.pre)
	if b.rebuildHook != nil {
		b.rebuildHook(b.pre)
		return
	}
	b.refreshResolved()
	b.refreshHints()
	if b.forceVideo {
		return
	}
	b.sv.rebuildTranscodeDetail(b.e, b.pre)
}

// availableAccels lists the accel paths offered for the current codec: auto + software always,
// plus each HW vendor that has a working encoder for this codec on this machine. Before
// detection completes it offers the full set (resolution happens at start anyway).
func (b *transcodeBuilder) availableAccels() []string {
	if b.sv.workingEnc == nil {
		return []string{"auto", "software"}
	}
	out := []string{"auto"}
	for _, v := range []string{"nvenc", "qsv", "amf", "videotoolbox", "vaapi"} {
		enc, ok := transcode.ResolveEncoder(b.pre.VideoCodec, v, b.sv.workingEnc)
		if ok && accelMatchesEncoder(v, enc) {
			out = append(out, v)
		}
	}
	return append(out, "software")
}

// refreshHints fills the source summary + up-encode warnings line under the builder.
func (b *transcodeBuilder) refreshHints() {
	if b.hintLbl == nil {
		return
	}
	if b.src == nil {
		if b.forceVideo {
			b.hintLbl.SetText("No source file loaded here; bitrate suggestions use common 1080p/stereo defaults.")
		} else {
			b.hintLbl.SetText("Reading source...")
		}
		return
	}
	var sb strings.Builder
	if s := b.src.Summary(); s != "" {
		sb.WriteString("Source: " + s)
	}
	if b.src.HasVideo && b.videoEncoding() {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("Video targets: Streaming %s, YouTube HQ %s, Mobile %s.",
			formatBitrateK(b.videoRecommendK("streaming")),
			formatBitrateK(b.videoRecommendK("youtube-hq")),
			formatBitrateK(b.videoRecommendK("mobile"))))
	}
	if b.src.HasAudio && b.pre.AudioCodec != "" && b.pre.AudioCodec != "copy" && b.pre.AudioCodec != "none" {
		if ladder := transcode.AudioBitrateLadder(b.pre.AudioCodec); len(ladder) > 0 {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			rec := transcode.RecommendAudioBitrateK(b.pre.AudioCodec, b.src.AudioKbps)
			sb.WriteString(fmt.Sprintf("Audio target: %s %s suggested; codec ceiling is %s.",
				strings.ToUpper(b.pre.AudioCodec), formatBitrateK(rec), formatBitrateK(ladder[len(ladder)-1])))
		}
	}
	for _, w := range transcode.CompareQuality(b.pre, *b.src) {
		mark := "!"
		if w.Severity == "info" {
			mark = "i"
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(mark + " " + w.Message)
	}
	if sb.Len() == 0 {
		sb.WriteString("Source analyzed - target looks good.")
	}
	b.hintLbl.SetText(sb.String())
}

func (b *transcodeBuilder) videoEncoding() bool {
	return b.pre.VideoCodec != "" && b.pre.VideoCodec != "copy" && b.pre.VideoCodec != "none"
}

func (b *transcodeBuilder) containerGuide() string {
	switch b.pre.Container {
	case "mp4":
		return "MP4 is the safest delivery container: H.264/H.265/AV1 video with AAC-style audio compatibility."
	case "webm":
		return "WebM is browser-friendly for open codecs: VP9 or AV1 video with Opus/Vorbis audio."
	case "mkv":
		return "MKV accepts nearly every codec; useful for archives and remuxes, less universal for playback."
	case "ogg":
		return "Ogg is audio-only here, usually Vorbis or Opus. It allows higher lossy audio ceilings than AAC."
	case "mp3":
		return "MP3 is audio-only and universally playable, capped at 320 kbps for CBR."
	case "m4a", "aac":
		return "M4A/AAC is audio-only and broadly compatible; AAC's practical top bitrate is 320 kbps."
	case "wav", "aiff":
		return "WAV/AIFF are uncompressed PCM containers: large files, no lossy bitrate control."
	case "flac":
		return "FLAC is lossless compression: smaller than WAV, still bit-perfect."
	case "opus":
		return "Opus is audio-only and very efficient at lower bitrates."
	default:
		return "Container choice decides which codec combinations are valid."
	}
}

func (b *transcodeBuilder) videoGuide() string {
	if transcode.IsAudioOnlyContainer(b.pre.Container) {
		return "This container is audio-only, so video controls are disabled."
	}
	switch b.pre.VideoCodec {
	case "copy":
		return "Copy keeps the original video bytes. CRF, bitrate, scaling, speed and tune do not apply."
	case "none", "":
		return "Video is disabled; the output will only contain audio."
	case "h264":
		return "H.264 is the compatibility default. It is larger than newer codecs, but plays almost everywhere."
	case "h265":
		return "H.265/HEVC is more efficient than H.264, especially at 4K, but older devices may need help."
	case "vp9":
		return "VP9 fits WebM well and saves bitrate versus H.264, usually with slower software encoding."
	case "av1":
		return "AV1 is the most efficient option here; expect slower encodes unless hardware is available."
	default:
		return "Choose a codec based on playback support, file size, and encode time."
	}
}

func (b *transcodeBuilder) rateGuide() string {
	if !b.videoEncoding() {
		return "Rate controls apply only when video is being encoded."
	}
	msg := "CRF/CQ spends bits where the picture needs them; lower values are larger and cleaner. Bitrate mode is for delivery caps and predictable file size."
	if b.src != nil && b.src.VideoKbps > 0 {
		msg += " Source video is " + formatBitrateK(b.src.VideoKbps) + "; non-master profiles avoid up-encoding beyond it."
	}
	return msg
}

func (b *transcodeBuilder) crfGuide() string {
	switch b.pre.VideoCodec {
	case "av1":
		return "AV1: 24-34 common"
	case "vp9":
		return "VP9: 28-34 common"
	default:
		return "18-23 common"
	}
}

func (b *transcodeBuilder) profileButtons(enabled bool) fyne.CanvasObject {
	profiles := []string{"streaming", "youtube-hq", "master", "mobile", "match-source"}
	labels := make([]string, 0, len(profiles))
	disabled := map[string]bool{}
	for _, p := range profiles {
		labels = append(labels, p)
		disabled[p] = !enabled || (p == "match-source" && (b.src == nil || b.src.VideoKbps <= 0))
	}
	selected := map[string]bool{b.currentProfile: true}
	onPick := func(p string) {
		b.currentProfile = p
		transcode.ApplyProfileSrc(&b.pre, p, b.src)
		b.pre = transcode.NormalizePreset(b.pre)
		b.applyAndRebuild()
	}
	row := SegmentedButtons(labels, disabled, selected, onPick)
	// Append the bitrate/CRF hint per profile so the labels carry the same
	// detail the old buttons did (e.g. "streaming · 6400 kbps"). We rebuild
	// the row on each profile change via applyAndRebuild, so this is static
	// per row. Keep the row compact by putting the hint underneath.
	hints := container.NewHBox()
	for _, p := range profiles {
		hints.Add(mutedInline(b.profileLabel(p) + "  "))
	}
	return container.NewVBox(row, hints)
}

func (b *transcodeBuilder) profileLabel(profile string) string {
	switch profile {
	case "streaming":
		return "Streaming " + formatBitrateK(b.videoRecommendK(profile))
	case "youtube-hq":
		return "YouTube HQ " + formatBitrateK(b.videoRecommendK(profile))
	case "master":
		return "Master CRF 16"
	case "mobile":
		return "Mobile " + formatBitrateK(b.videoRecommendK(profile))
	case "match-source":
		if b.src != nil && b.src.VideoKbps > 0 {
			return "Match " + formatBitrateK(b.src.VideoKbps)
		}
		return "Match source"
	default:
		return profile
	}
}

func (b *transcodeBuilder) videoRecommendK(profile string) int {
	if !b.videoEncoding() {
		return 0
	}
	srcK := 0
	if b.src != nil {
		srcK = b.src.VideoKbps
	}
	return transcode.RecommendVideoBitrateK(profile, b.pre.VideoCodec, b.targetHeight(), b.targetFPS(), srcK)
}

func (b *transcodeBuilder) targetHeight() int {
	if b.pre.Height > 0 {
		return b.pre.Height
	}
	if b.src != nil && b.src.Height > 0 {
		return b.src.Height
	}
	return 1080
}

func (b *transcodeBuilder) targetFPS() float64 {
	if b.pre.FPS > 0 {
		return b.pre.FPS
	}
	if b.src != nil && b.src.FPS > 0 {
		return b.src.FPS
	}
	return 30
}

func (b *transcodeBuilder) resolutionButtons(enabled bool) fyne.CanvasObject {
	opts := []struct {
		label string
		h     int
	}{
		{"Source", 0},
		{"720p", 720},
		{"1080p", 1080},
		{"1440p", 1440},
		{"2160p", 2160},
	}
	labels := make([]string, 0, len(opts))
	disabled := map[string]bool{}
	for _, o := range opts {
		labels = append(labels, o.label)
		disabled[o.label] = !enabled
	}
	selected := map[string]bool{resolutionLabelFor(b.pre.Height): true}
	onPick := func(label string) {
		for _, o := range opts {
			if o.label == label {
				b.pre.Width = 0
				b.pre.Height = o.h
				b.applyAndRebuild()
				return
			}
		}
	}
	return SegmentedButtons(labels, disabled, selected, onPick)
}

// resolutionLabelFor returns the resolution-button label for a given height.
func resolutionLabelFor(h int) string {
	switch h {
	case 720:
		return "720p"
	case 1080:
		return "1080p"
	case 1440:
		return "1440p"
	case 2160:
		return "2160p"
	default:
		return "Source"
	}
}

func (b *transcodeBuilder) resolvedEncoderName() string {
	if b.pre.EncoderOverride != "" {
		return b.pre.EncoderOverride
	}
	enc, _ := transcode.ResolveEncoder(b.pre.VideoCodec, b.pre.Accel, b.sv.workingEnc)
	return enc
}

func (b *transcodeBuilder) tuneAvailable() bool {
	return strings.HasPrefix(b.resolvedEncoderName(), "libx26")
}

func speedOptionsForEncoder(enc string) []string {
	switch {
	case strings.HasSuffix(enc, "_nvenc"):
		return []string{"", "p1", "p2", "p3", "p4", "p5", "p6", "p7"}
	case enc == "libx264" || enc == "libx265":
		return []string{"", "ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"}
	case enc == "libsvtav1":
		return []string{"", "4", "6", "8", "10"}
	default:
		return []string{""}
	}
}

func (b *transcodeBuilder) audioBitrateOptions(codec string) ([]string, map[string]int, string) {
	ladder := transcode.AudioBitrateLadder(codec)
	if len(ladder) == 0 {
		return []string{"n/a"}, map[string]int{}, "n/a"
	}
	srcK := 0
	if b.src != nil {
		srcK = b.src.AudioKbps
	}
	suggested := transcode.RecommendAudioBitrateK(codec, srcK)
	current := b.pre.AudioBitrateK
	if current <= 0 {
		current = suggested
	}
	byLabel := map[string]int{}
	var opts []string
	selected := ""
	for i, k := range ladder {
		label := formatBitrateK(k)
		var tags []string
		if k == suggested {
			tags = append(tags, "suggested")
		}
		if i == len(ladder)-1 {
			tags = append(tags, "max")
		}
		if len(tags) > 0 {
			label += " (" + strings.Join(tags, ", ") + ")"
		}
		opts = append(opts, label)
		byLabel[label] = k
		if k == current {
			selected = label
		}
	}
	if selected == "" {
		selected = opts[0]
	}
	return opts, byLabel, selected
}

func (b *transcodeBuilder) audioGuide(codec string) string {
	switch codec {
	case "copy":
		return "Copy keeps the original audio stream; bitrate, sample-rate, channel and loudness changes do not apply."
	case "none", "":
		return "Audio is disabled; the output will only contain video if video is enabled."
	case "flac":
		return "FLAC is lossless: quality is preserved, bitrate is determined by the source and compression level."
	case "pcm-s16le", "pcm-s16be":
		return "PCM is uncompressed lossless audio. It is large and ignores bitrate settings."
	}
	ladder := transcode.AudioBitrateLadder(codec)
	if len(ladder) == 0 {
		return "This audio codec does not use a lossy bitrate setting."
	}
	srcK := 0
	if b.src != nil {
		srcK = b.src.AudioKbps
	}
	rec := transcode.RecommendAudioBitrateK(codec, srcK)
	capK := ladder[len(ladder)-1]
	base := ""
	if srcK > 0 {
		base = "Source audio is " + formatBitrateK(srcK) + ". "
	}
	switch codec {
	case "aac":
		return base + "AAC is broadly compatible; " + formatBitrateK(rec) + " is suggested and " + formatBitrateK(capK) + " is the useful ceiling."
	case "opus":
		return base + "Opus is efficient; " + formatBitrateK(rec) + " is generous for stereo music and the ladder tops at " + formatBitrateK(capK) + "."
	case "vorbis":
		return base + "Vorbis can go higher than AAC; " + formatBitrateK(rec) + " is suggested and the ceiling is " + formatBitrateK(capK) + "."
	case "mp3":
		return base + "MP3 CBR tops out at " + formatBitrateK(capK) + "; V0 VBR often sounds transparent with smaller files."
	default:
		return base + formatBitrateK(rec) + " is suggested; this codec tops out at " + formatBitrateK(capK) + "."
	}
}

func accelMatchesEncoder(accel, enc string) bool {
	suffix := map[string]string{
		"nvenc":        "_nvenc",
		"qsv":          "_qsv",
		"amf":          "_amf",
		"videotoolbox": "_videotoolbox",
		"vaapi":        "_vaapi",
	}
	return strings.HasSuffix(enc, suffix[accel])
}

func selectValue(options []string, selected string, changed func(string)) *widget.Select {
	sel := widget.NewSelect(options, nil)
	if contains(options, selected) {
		sel.SetSelected(selected)
	}
	sel.OnChanged = changed
	return sel
}

func checkValue(label string, checked bool, changed func(bool)) *widget.Check {
	chk := widget.NewCheck(label, nil)
	chk.SetChecked(checked)
	chk.OnChanged = changed
	return chk
}

func withSelected(options []string, selected string) []string {
	if selected == "" || contains(options, selected) {
		return options
	}
	out := append([]string{}, options...)
	return append(out, selected)
}

func formatBitrateK(k int) string {
	if k <= 0 {
		return "n/a"
	}
	if k >= 1000 {
		return fmt.Sprintf("%.1f Mbps", float64(k)/1000)
	}
	return fmt.Sprintf("%d kbps", k)
}

func parseBitrateK(s string) (int, bool) {
	v := strings.ToLower(strings.TrimSpace(s))
	v = strings.ReplaceAll(v, " ", "")
	if v == "" {
		return 0, false
	}
	mult := 1.0
	for _, suffix := range []string{"mbps", "mb", "m"} {
		if strings.HasSuffix(v, suffix) {
			mult = 1000
			v = strings.TrimSuffix(v, suffix)
			break
		}
	}
	for _, suffix := range []string{"kbps", "kb", "k"} {
		if strings.HasSuffix(v, suffix) {
			v = strings.TrimSuffix(v, suffix)
			break
		}
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return int(n*mult + 0.5), true
}

// contains reports whether s is in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// selectFileWithPreset re-renders the detail for e, seeding the transcode builder with seed.
func (sv *studioView) selectFileWithPreset(e fileEntry, seed *transcode.Preset) {
	sv.seedPreset = seed
	sv.selectFile(e)
	sv.seedPreset = nil
}

// ── Presets section (manage custom presets) ───────────────────────────────────

func (sv *studioView) presetsSection() fyne.CanvasObject {
	custom := sv.u.svc.Cfg.Features.Transcode.Presets
	list := container.NewVBox()

	add := widget.NewButtonWithIcon("New preset", theme.ContentAddIcon(), func() {
		sv.editPreset(transcode.Preset{ID: "custom", Label: "Custom", Container: "mp4", VideoCodec: "h264", Accel: "auto", CRF: 21, AudioCodec: "aac", AudioBitrateK: 160}, -1)
	})
	add.Importance = widget.HighImportance

	list.Add(smallCaps("CUSTOM PRESETS"))
	if len(custom) == 0 {
		list.Add(mutedLabel("No custom presets yet. Build one from a file’s Transcode panel (Save as new…) or click New preset."))
	}
	for i, p := range custom {
		edit := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), func() { sv.editPreset(p, i) })
		edit.Importance = widget.LowImportance
		dup := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
			cp := p
			cp.ID = uniquePresetID(p.ID, custom)
			cp.Label = p.Label + " copy"
			sv.editPreset(cp, -1) // append as a new preset
		})
		dup.Importance = widget.LowImportance
		del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() { sv.deletePreset(i) })
		del.Importance = widget.LowImportance
		name := widget.NewLabel(fmt.Sprintf("%s  (%s)", p.Label, p.ID))
		name.Truncation = fyne.TextTruncateEllipsis
		list.Add(container.NewBorder(nil, nil, nil, container.NewHBox(edit, dup, del), name))
	}

	list.Add(widget.NewSeparator())
	list.Add(smallCaps("BUILT-IN PRESETS"))
	for _, p := range transcode.Builtins {
		list.Add(mutedLabel(fmt.Sprintf("%s - %s", p.Label, p.Desc)))
	}

	head := container.NewVBox(
		mutedLabel("Custom presets are saved in your config and appear in every file’s Transcode panel."),
		add, widget.NewSeparator(),
	)
	return container.NewBorder(head, nil, nil, nil, container.NewVScroll(list))
}

// savePresetAs prompts for an ID + Label and persists p as a custom preset.
func (sv *studioView) savePresetAs(p transcode.Preset) {
	sv.editPreset(p, -1)
}

// editPreset shows the full encoder builder (container + video + audio) plus identity fields to
// edit/create a custom preset; idx<0 appends, else replaces. Mirrors the per-file Transcode
// panel so every preset field is editable here, not just the label.
func (sv *studioView) editPreset(p transcode.Preset, idx int) {
	win := currentWindow()
	if win == nil {
		return
	}
	sv.ensureDetect() // so the resolved-encoder hint is populated

	b := &transcodeBuilder{sv: sv, pre: p, forceVideo: true}
	b.resolvedLbl = mutedLabel("")
	b.hintLbl = mutedLabel("")

	idEnt := newEntry()
	idEnt.SetText(p.ID)
	labelEnt := newEntry()
	labelEnt.SetText(p.Label)
	descEnt := newEntry()
	descEnt.SetText(p.Desc)
	formSlot := container.NewVBox()
	b.rebuildHook = func(p transcode.Preset) {
		b.pre = p
		formSlot.Objects = []fyne.CanvasObject{
			b.buildForm(),
			b.resolvedLbl,
			b.hintLbl,
			mutedInline("Set a video/audio codec to 'none' to disable that stream."),
		}
		b.refreshResolved()
		b.refreshHints()
		formSlot.Refresh()
	}

	body := container.NewVBox(
		smallCaps("IDENTITY"),
		labeled("ID", idEnt),
		labeled("Label", labelEnt),
		labeled("Description", descEnt),
		widget.NewSeparator(),
		formSlot,
	)
	b.rebuildHook(b.pre)
	content := container.NewVScroll(body)
	content.SetMinSize(fyne.NewSize(540, 460))

	d := dialog.NewCustomConfirm("Edit preset", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		b.pre.ID = strings.TrimSpace(idEnt.Text)
		b.pre.Label = strings.TrimSpace(labelEnt.Text)
		b.pre.Desc = strings.TrimSpace(descEnt.Text)
		if b.pre.ID == "" || b.pre.Label == "" {
			sv.u.Notify("rave-mate", "Preset needs an ID and label.")
			return
		}
		sv.persistPreset(b.pre, idx)
	}, win)
	d.Resize(fyne.NewSize(580, 540))
	d.Show()
}

func (sv *studioView) persistPreset(p transcode.Preset, idx int) {
	p = transcode.NormalizePreset(p)
	f := &sv.u.svc.Cfg.Features.Transcode
	if idx >= 0 && idx < len(f.Presets) {
		f.Presets[idx] = p
	} else {
		// Replace an existing custom preset with the same ID, else append.
		replaced := false
		for i := range f.Presets {
			if f.Presets[i].ID == p.ID {
				f.Presets[i] = p
				replaced = true
				break
			}
		}
		if !replaced {
			f.Presets = append(f.Presets, p)
		}
	}
	sv.u.saveCfg()
	sv.u.Notify("rave-mate", "Saved preset "+p.Label)
	if sv.section == "Presets" {
		sv.showSection("Presets")
	}
}

func (sv *studioView) deletePreset(idx int) {
	f := &sv.u.svc.Cfg.Features.Transcode
	if idx < 0 || idx >= len(f.Presets) {
		return
	}
	f.Presets = append(f.Presets[:idx], f.Presets[idx+1:]...)
	sv.u.saveCfg()
	sv.showSection("Presets")
}

// ── Quick actions (move/rename) ───────────────────────────────────────────────

func (sv *studioView) quickActions(e fileEntry) fyne.CanvasObject {
	move := widget.NewButtonWithIcon("Move to folder…", theme.FolderOpenIcon(), func() {
		win := currentWindow()
		if win == nil {
			return
		}
		showFolderOpen(win, func(u fyne.ListableURI, _ error) {
			if u == nil {
				return
			}
			dst := filepath.Join(u.Path(), e.name)
			if err := os.Rename(e.path, dst); err != nil {
				sv.u.Notify("rave-mate", "Move failed: "+err.Error())
				return
			}
			sv.u.Notify("rave-mate", "Moved → "+dst)
			sv.navigate(sv.cwd)
		})
	})
	rename := widget.NewButtonWithIcon("Rename…", theme.DocumentCreateIcon(), func() {
		win := currentWindow()
		if win == nil {
			return
		}
		ent := newEntry()
		ent.SetText(e.name)
		dialog.ShowForm("Rename file", "Rename", "Cancel",
			[]*widget.FormItem{widget.NewFormItem("New name", ent)}, func(ok bool) {
				if !ok || strings.TrimSpace(ent.Text) == "" {
					return
				}
				dst := filepath.Join(filepath.Dir(e.path), strings.TrimSpace(ent.Text))
				if err := os.Rename(e.path, dst); err != nil {
					sv.u.Notify("rave-mate", "Rename failed: "+err.Error())
					return
				}
				sv.navigate(sv.cwd)
			}, win)
	})
	return container.NewVBox(container.NewGridWithColumns(2, rename, move))
}

// ── loose-file tag retrieval ──────────────────────────────────────────────────

// loadTags fetches embedded tags + codec info for a loose audio file (out-of-process) and,
// when still selected, re-renders the detail enriched with artist/title/key/BPM.
func (sv *studioView) loadTags(path string) {
	if sv.u.svc.Workers == nil {
		return
	}
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "studio-tags", false)
		mtime := fileMtime(path)
		raw, ok := sv.u.svc.Store.GetAnalysis(store.KindTags, path, mtime) // persisted across restarts
		if !ok {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var err error
			raw, err = sv.u.svc.Workers.RunBackground(ctx, "probe", "probe.tags", map[string]any{"path": path})
			if err != nil {
				return
			}
		}
		var t musiclib.Track
		if json.Unmarshal(raw, &t) != nil {
			return
		}
		t.Path = path
		if !ok {
			sv.u.svc.Store.PutAnalysis(store.KindTags, path, mtime, raw)
		}
		fyne.Do(func() {
			sv.tagCache[path] = t
			if sv.curFile != nil && sv.curFile.path == path {
				sv.selectFile(*sv.curFile)
			}
		})
	}()
}

// ── small helpers ─────────────────────────────────────────────────────────────

func labeled(label string, w fyne.CanvasObject) fyne.CanvasObject {
	return container.NewBorder(nil, nil, mutedInline(label), nil, w)
}

func (b *transcodeBuilder) field(label string, w fyne.CanvasObject) fyne.CanvasObject {
	if b.forceVideo {
		return labeled(label, w)
	}
	return container.NewVBox(mutedInline(label), w)
}

func (b *transcodeBuilder) objectPair(a, c fyne.CanvasObject) fyne.CanvasObject {
	if b.forceVideo {
		return container.NewGridWithColumns(2, a, c)
	}
	return container.NewVBox(a, c)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orFloat(f, def float64) float64 {
	if f <= 0 {
		return def
	}
	return f
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func chanLabel(n int) string {
	if n <= 0 {
		return "source"
	}
	return strconv.Itoa(n)
}

func rateLabel(n int) string {
	if n <= 0 {
		return "source"
	}
	return strconv.Itoa(n)
}

// uniquePresetID returns base with a -copy/-copyN suffix not already used by an existing custom
// preset, so a duplicate gets a fresh id without collision.
func uniquePresetID(base string, existing []transcode.Preset) string {
	used := map[string]bool{}
	for _, p := range existing {
		used[p.ID] = true
	}
	cand := base + "-copy"
	for n := 2; used[cand]; n++ {
		cand = fmt.Sprintf("%s-copy%d", base, n)
	}
	return cand
}
