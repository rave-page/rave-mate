package webui

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/transcode"
)

// Shared transcode-preset builder core: ONE field-mutation function + container-aware
// option lists, used by the Library encode builder (libPF) AND the export preset editor
// modal (mp-pedit, below). Extend HERE; never fork a per-surface field switch.

// applyPresetField applies one builder field change to d. Container/codec changes coerce
// the dependent fields to compatible values (codec allowed in container, bitrate on the
// codec's ladder) so the UI can only ever describe an encodable preset.
func applyPresetField(d *transcode.Preset, field, val string) {
	switch field {
	case "id":
		d.ID = strings.TrimSpace(val)
	case "label":
		d.Label = val
	case "desc":
		d.Desc = val
	case "container":
		d.Container = val
		if vOpts := transcode.VideoCodecsForContainer(val, true); !containsStr(vOpts, d.VideoCodec) {
			d.VideoCodec = vOpts[0]
		}
		if aOpts := transcode.AudioCodecsForContainer(val); !containsStr(aOpts, d.AudioCodec) {
			d.AudioCodec = aOpts[0]
			d.AudioBitrateK = transcode.RecommendAudioBitrateK(d.AudioCodec, d.AudioBitrateK)
		}
	case "vcodec":
		d.VideoCodec = val
	case "accel":
		d.Accel = val
	case "profile":
		transcode.ApplyProfile(d, val)
	case "ratemode":
		d.RateMode = val
	case "crf":
		d.CRF = atoi(val)
	case "bitratek":
		d.BitrateK = atoi(val)
	case "res":
		switch val {
		case "720":
			d.Width, d.Height = 1280, 720
		case "1080":
			d.Width, d.Height = 1920, 1080
		case "1440":
			d.Width, d.Height = 2560, 1440
		case "2160":
			d.Width, d.Height = 3840, 2160
		default:
			d.Width, d.Height = 0, 0
		}
	case "fps":
		d.FPS = atof(val)
	case "acodec":
		d.AudioCodec = val
		if ladder := transcode.AudioBitrateLadder(val); len(ladder) == 0 {
			d.AudioBitrateK, d.AudioVBR, d.AudioVBRQuality = 0, false, 0
		} else if d.AudioBitrateK > 0 {
			d.AudioBitrateK = transcode.RecommendAudioBitrateK(val, d.AudioBitrateK)
		} else {
			d.AudioBitrateK = transcode.RecommendAudioBitrateK(val, 0)
		}
		if val != "mp3" {
			d.AudioVBR, d.AudioVBRQuality = false, 0
		}
	case "abitratek":
		d.AudioBitrateK = atoi(val)
	case "avbr":
		d.AudioVBR = val == "true"
	case "avbrq":
		d.AudioVBRQuality = atoi(val)
	case "channels":
		d.Channels = atoi(val)
	case "samplerate":
		d.SampleRate = atoi(val)
	case "loudon":
		d.LoudnessOn = val == "true"
	case "loudi":
		d.LoudnessI = atof(val)
	case "loudtp":
		d.LoudnessTP = atof(val)
	case "loudraise":
		d.LoudnessRaiseOnly = val == "true"
	case "loudtarget": // quick-pick chip: "<I>|<TP>"
		iS, tpS, _ := strings.Cut(val, "|")
		d.LoudnessI, d.LoudnessTP, d.LoudnessOn = atof(iS), atof(tpS), true
	}
}

// pbVideoCodecOptsFor returns the labelled video-codec options valid in container.
func pbVideoCodecOptsFor(container string) [][2]string {
	return pbFilterOpts(videoCodecOpts, transcode.VideoCodecsForContainer(container, true))
}

// pbAudioCodecOptsFor returns the labelled audio-codec options valid in container.
func pbAudioCodecOptsFor(container string) [][2]string {
	all := append([][2]string{}, audioCodecOpts...)
	all = append(all, [2]string{"pcm-s16le", "PCM 16-bit LE"}, [2]string{"pcm-s16be", "PCM 16-bit BE"})
	return pbFilterOpts(all, transcode.AudioCodecsForContainer(container))
}

// pbFilterOpts keeps labelled pairs whose value the container allows (allowed order kept).
func pbFilterOpts(labelled [][2]string, allowed []string) [][2]string {
	byVal := map[string][2]string{}
	for _, p := range labelled {
		byVal[p[0]] = p
	}
	out := make([][2]string, 0, len(allowed))
	for _, v := range allowed {
		if p, ok := byVal[v]; ok {
			out = append(out, p)
		} else {
			out = append(out, [2]string{v, v})
		}
	}
	return out
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// ── export-surface preset editor (modal over the unified player's export block) ──

func init() {
	// open the editor seeded from the media's active preset
	onPrefix("mp-pedit:", func(u *UI, m actMsg) {
		host, idxS := mpArgs(m.arg("mp-pedit:"))
		idx := atoi(idxS)
		t := u.mpMut(host, func(v *mpSt) {
			if idx < 0 || idx >= len(v.media) {
				return
			}
			md := &v.media[idx]
			if md.draft == nil {
				d := transcode.MigrateLoudness(u.mpActivePreset(md))
				md.draft = &d
			}
		})
		u.mpPresetModal(t, idx)
	})
	// one builder field changed
	onPrefix("mp-pf:", func(u *UI, m actMsg) {
		host, rest := mpArgs(m.arg("mp-pf:"))
		idxS, f, _ := strings.Cut(rest, "\x1f")
		idx := atoi(idxS)
		t := u.mpMut(host, func(v *mpSt) {
			if idx >= 0 && idx < len(v.media) && v.media[idx].draft != nil {
				applyPresetField(v.media[idx].draft, f, m.Val)
			}
		})
		u.mpPresetModal(t, idx)
	})
	// apply without saving: the draft becomes this export's inline preset
	onPrefix("mp-papply:", func(u *UI, m actMsg) {
		host, idxS := mpArgs(m.arg("mp-papply:"))
		idx := atoi(idxS)
		t := u.mpMut(host, func(v *mpSt) {
			if idx >= 0 && idx < len(v.media) && v.media[idx].draft != nil {
				md := &v.media[idx]
				md.inline, md.draft = md.draft, nil
				if md.outPath != "" {
					md.outPath = swapExt(md.outPath, mpExt(md.path, *md.inline))
				}
			}
		})
		u.closeModal()
		u.mpPatchExport(t)
		u.mpKickMeasure(host)
		u.mpSyncMonitor(host)
	})
	// save (upsert into config presets) + select it
	onPrefix("mp-psave:", func(u *UI, m actMsg) {
		host, idxS := mpArgs(m.arg("mp-psave:"))
		idx := atoi(idxS)
		var saved *transcode.Preset
		t := u.mpMut(host, func(v *mpSt) {
			if idx >= 0 && idx < len(v.media) && v.media[idx].draft != nil {
				md := &v.media[idx]
				d := *md.draft
				if d.ID == "" {
					d.ID = "custom"
				}
				if _, builtin := transcode.Find(d.ID); builtin && !u.hasCustomPreset(d.ID) {
					d.ID += "-custom" // never shadow a builtin silently
				}
				if strings.TrimSpace(d.Label) == "" {
					d.Label = d.ID
				}
				saved = &d
				md.presetID, md.inline, md.draft = d.ID, nil, nil
				if md.outPath != "" {
					md.outPath = swapExt(md.outPath, mpExt(md.path, d))
				}
			}
		})
		if saved == nil {
			u.closeModal()
			return
		}
		if u.svc.Cfg == nil {
			u.toast(i18n.T("library.toast.configUnavailable"))
			return
		}
		u.libUpsertPreset(*saved)
		u.closeModal()
		u.toast(i18n.T("library.toast.presetSavedName") + saved.Label)
		u.mpPatchExport(t)
		u.mpKickMeasure(host)
		u.mpSyncMonitor(host)
	})
	onPrefix("mp-pcancel:", func(u *UI, m actMsg) {
		host, idxS := mpArgs(m.arg("mp-pcancel:"))
		idx := atoi(idxS)
		u.mpMut(host, func(v *mpSt) {
			if idx >= 0 && idx < len(v.media) {
				v.media[idx].draft = nil
			}
		})
		u.closeModal()
	})
}

// hasCustomPreset reports whether a user preset with id exists in config.
func (u *UI) hasCustomPreset(id string) bool {
	if u.svc.Cfg == nil {
		return false
	}
	for _, p := range u.svc.Cfg.Features.Transcode.Presets {
		if p.ID == id {
			return true
		}
	}
	return false
}

// mpPresetModal renders the export preset editor for media idx of snapshot t.
func (u *UI) mpPresetModal(t mpSt, idx int) {
	if idx < 0 || idx >= len(t.media) {
		return
	}
	if t.media[idx].draft == nil {
		return
	}
	u.openModal(mpPresetDlgHTML(mpPresetModalState(t, idx)))
}

// mpPresetModalState resolves the preset editor: i18n, every number (strconv/trimNum), the
// container-aware option lists, the smart-select registrations (in RENDER order) and the
// shared loudness block. The loudness block stays RAW markup - components.go owns it and
// every surface that edits transcode loudness renders that one implementation.
func mpPresetModalState(t mpSt, idx int) mpPresetDlgSt {
	m := &t.media[idx]
	d := *m.draft
	host := t.host
	act := func(f string) string { return fmt.Sprintf("mp-pf:%s\x1f%d\x1f%s", host, idx, f) }
	audioOnly := m.kind == "audio"

	st := mpPresetDlgSt{
		Title: i18n.T("player.pedit.title"),
		// identity row: id + label (save uses these; apply ignores them)
		IDField:    newPBField(i18n.T("library.label.id"), act("id"), d.ID, "text", ""),
		LabelField: newPBField(i18n.T("library.label.label"), act("label"), d.Label, "text", ""),
		Accel:      emptySel(), Res: emptySel(), VBRQ: emptySel(),
	}
	// source line: what we're encoding FROM (probe-backed)
	if m.src != nil {
		st.HasSrc, st.SrcHint = true, i18n.T("library.hints.source", i18n.A{"detail": m.src.Summary()})
	}
	st.Container = resolvePbSelectTip(i18n.T("library.enc.container"), act("container"), pbContainerOptsFor(audioOnly), d.Container, "enc-container")

	if !audioOnly {
		st.HasVideo = true
		st.VCodec = resolvePbSelectTip(i18n.T("library.enc.videoCodec"), act("vcodec"), pbVideoCodecOptsFor(d.Container), d.VideoCodec, "enc-video-codec")
		if d.VideoCodec != "copy" && d.VideoCodec != "none" && d.VideoCodec != "" {
			st.HasVEnc = true
			st.Accel = resolvePbSelect(i18n.T("library.enc.accel"), act("accel"), accelOpts(), d.Accel)
			st.RateMode = resolvePbSelectTip(i18n.T("library.enc.rateMode"), act("ratemode"),
				[][2]string{{"crf", i18n.T("library.enc.rateCRF")}, {"bitrate", i18n.T("library.enc.rateBitrate")}}, d.RateMode, "enc-rate")
			if d.RateMode == "bitrate" {
				st.RateField = newPBField(i18n.T("library.enc.bitrateK"), act("bitratek"), strconv.Itoa(d.BitrateK), "number", i18n.T("library.enc.bitrateKHint"))
			} else {
				st.RateField = newPBField(i18n.T("library.enc.crf"), act("crf"), strconv.Itoa(d.CRF), "number", crfHint(d.VideoCodec))
			}
			st.Res = resolvePbSelect(i18n.T("library.enc.resolution"), act("res"), resOpts, resLabel(d.Width, d.Height))
			st.FPS = newPBField(i18n.T("library.enc.fps"), act("fps"), trimNum(d.FPS), "number", "")
		}
	}

	// audio group: codec (container-compatible only) + ladder bitrate chips
	st.ACodec = resolvePbSelectTip(i18n.T("library.enc.audioCodec"), act("acodec"), pbAudioCodecOptsFor(d.Container), d.AudioCodec, "enc-audio-codec")
	if ladder := transcode.AudioBitrateLadder(d.AudioCodec); len(ladder) > 0 {
		st.HasLadder = true
		if d.AudioCodec == "mp3" {
			st.HasVBRTgl, st.VBR = true, newToggle(i18n.T("library.enc.vbr"), act("avbr"), d.AudioVBR)
		}
		if d.AudioCodec == "mp3" && d.AudioVBR {
			var q [][2]string
			for i := 0; i <= 9; i++ {
				q = append(q, [2]string{strconv.Itoa(i), fmt.Sprintf("V%d", i)})
			}
			st.HasVBRQ = true
			st.VBRQ = resolvePbSelect(i18n.T("library.enc.vbrQuality"), act("avbrq"), q, strconv.Itoa(d.AudioVBRQuality))
		} else {
			st.HasChips, st.BitrateLbl = true, i18n.T("library.enc.audioBitrate")
			for _, k := range ladder {
				st.Chips = append(st.Chips, newChip(fmt.Sprintf("%dk", k), strconv.Itoa(k), act("abitratek"), d.AudioBitrateK == k))
			}
			st.MaxHint = i18n.T("library.enc.maxForCodec",
				i18n.A{"max": fmt.Sprintf("%dk", ladder[len(ladder)-1]), "codec": strings.ToUpper(d.AudioCodec)})
		}
	} else if d.AudioCodec != "copy" && d.AudioCodec != "none" {
		st.HasLossles, st.LosslessTx = true, i18n.T("library.enc.losslessNoBitrate")
	}
	st.Channels = resolvePbSelect(i18n.T("library.enc.channels"), act("channels"),
		[][2]string{{"0", i18n.T("library.enc.source")}, {"1", i18n.T("library.enc.mono")}, {"2", i18n.T("library.enc.stereo")}}, strconv.Itoa(d.Channels))
	st.SampleRate = resolvePbSelect(i18n.T("library.enc.sampleRate"), act("samplerate"),
		[][2]string{{"0", i18n.T("library.enc.source")}, {"44100", "44.1 kHz"}, {"48000", "48 kHz"}, {"96000", "96 kHz"}}, strconv.Itoa(d.SampleRate))

	// loudness: the draft IS the preset here (no override framing), compact layout
	st.Loudness = loudnessFields(loudnessOpts{
		act:       act,
		toggleLbl: i18n.T("library.enc.normalize"),
		topic:     "enc-loudness",
		vals:      loudnessVals{On: d.LoudnessOn, I: d.LoudnessI, TP: d.LoudnessTP, RaiseOnly: d.LoudnessRaiseOnly},
		preset:    &d,
		compact:   true,
	})

	// source-aware warnings (up-encode, wasted bitrate, remux hint)
	if m.src != nil {
		for _, w := range transcode.CompareQuality(d, *m.src) {
			tone := "warn"
			if w.Severity == "info" {
				tone = "info"
			}
			st.Warns = append(st.Warns, libHintSt{Tone: tone, Text: w.Message})
		}
	}
	st.Foot = []uiBtn{
		{Label: i18n.T("player.pedit.apply"), Variant: "outline", Act: fmt.Sprintf("mp-papply:%s\x1f%d", host, idx)},
		{Label: i18n.T("library.enc.savePreset"), Variant: "primary", Act: fmt.Sprintf("mp-psave:%s\x1f%d", host, idx)},
		{Label: i18n.T("common.cancel"), Variant: "ghost", Act: fmt.Sprintf("mp-pcancel:%s\x1f%d", host, idx)},
	}
	return st
}

// pbContainerOptsFor filters containers for the media kind (audio captures only get
// audio-capable containers - no .mp4 offers for a FLAC set).
func pbContainerOptsFor(audioOnly bool) [][2]string {
	if !audioOnly {
		return containerOpts
	}
	var out [][2]string
	for _, p := range containerOpts {
		if transcode.IsAudioOnlyContainer(p[0]) || p[0] == "mkv" {
			out = append(out, p)
		}
	}
	return out
}
