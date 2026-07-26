package webui

import (
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediapipe"
	"rave.page/mate/internal/mfenc"
)

// settings_medialink.go - the Media-link card body. Two things it has to get right beyond the plain
// knobs:
//
//   - WHICH PC a knob acts on. MediaLink settings are split across the two machines: PreferCodec +
//     BitrateKbps are read where the route is REQUESTED (the receiving instance builds the Offer
//     from them), while MaxFPS/MaxHeight/SWOnly/device/engine are read where the route is SERVED
//     (the sending instance encodes with them). Setting the "wrong" one on the wrong box silently
//     does nothing, so the card is split into a sender group and a receiver group and every help
//     body names the PC it belongs on.
//   - Device preference. The encode GPU picker's options come from the DXGI adapter list plus the
//     live per-adapter video-encode load and who is holding it, sampled OFF the render lane
//     (settings probe pkGPUEnc) - the render path only reads the retained slot.

// deviceAvoidValue is the sentinel option value for the avoid-busiest policy (not a LUID).
const deviceAvoidValue = "avoid"

// mediaLinkBlocks builds the Media-link card body.
func (u *UI) mediaLinkBlocks(mf *config.MediaLinkFeature) []setBlock {
	fpsCapSel := func(v int) int { // stored -1 = uncapped, 0 = default 60
		if v == 0 {
			return 60
		}
		return v
	}
	return []setBlock{
		sbNote(i18n.T("settings.body.medialink.senderSection")),
		sbFpair(
			sbSelectTip(i18n.T("settings.body.medialink.device"), "set:ml-device",
				u.encodeDeviceOptions(mf), encodeDeviceCurrent(mf), "ml-device"),
			sbSelectTip(i18n.T("settings.body.medialink.encoder"), "set:ml-encoder",
				encodeEngineOptions(mf), mf.PinnedEncoder(), "ml-engine")),
		sbFpair(
			sbSelectTip(i18n.T("settings.body.medialink.maxFps"), "set:ml-maxfps",
				[][2]string{{"30", "30"}, {"60", "60"}, {"-1", i18n.T("settings.body.medialink.uncapped")}},
				strconv.Itoa(fpsCapSel(mf.MaxFPS)), "ml-fps"),
			sbSelectTip(i18n.T("settings.body.medialink.maxHeight"), "set:ml-maxheight",
				[][2]string{{"0", i18n.T("settings.body.medialink.auto")}, {"720", "720p"}, {"1080", "1080p"},
					{"1440", "1440p"}, {"-1", i18n.T("settings.body.medialink.native")}},
				strconv.Itoa(mf.MaxHeight), "ml-height")),
		sbToggle(i18n.T("settings.body.medialink.swOnly"), "set:ml-swonly", mf.SWOnly),

		sbNote(i18n.T("settings.body.medialink.receiverSection")),
		sbFpair(
			sbSelectTip(i18n.T("settings.body.medialink.codec"), "set:ml-codec",
				[][2]string{{"", "auto"}, {"hevc", "hevc"}, {"h264", "h264"}, {"mjpeg", "mjpeg"}},
				mf.PreferCodec, "ml-accel"),
			sbSelectTip(i18n.T("settings.body.medialink.bitrate"), "set:ml-bitrate",
				[][2]string{{"8", "8"}, {"12", "12"}, {"20", "20"}, {"30", "30"}, {"50", "50"}},
				strconv.Itoa(mf.Bitrate()/1000), "ml-budget")),

		sbToggleTip(i18n.T("settings.body.medialink.subprocess"), "set:ml-subprocess",
			mf.MediaSubprocess(), tipTopicSt("ml-isolation")),
		sbNote(i18n.T("settings.body.medialink.note")),
	}
}

// encodeDeviceCurrent maps the stored policy+adapter pair to the picker's current value.
func encodeDeviceCurrent(mf *config.MediaLinkFeature) string {
	policy, adapter := mf.DevicePref()
	switch encoderscan.NormalizePolicy(policy) {
	case encoderscan.PolicyAvoid:
		return deviceAvoidValue
	case encoderscan.PolicyPin:
		return adapter
	}
	return ""
}

// encodeDeviceOptions lists the encode-GPU picks: automatic, avoid-the-busiest, then one entry per
// DXGI adapter labelled with its live encode load and who is holding it ("RTX 4090 - enc 62% - OBS").
// A pinned adapter that is no longer present is kept as an option so the setting stays visible
// instead of silently reading as "automatic".
func (u *UI) encodeDeviceOptions(mf *config.MediaLinkFeature) [][2]string {
	opts := [][2]string{
		{"", i18n.T("settings.body.medialink.deviceAuto")},
		{deviceAvoidValue, i18n.T("settings.body.medialink.deviceAvoid")},
	}
	rows := u.gpuAdaptersCached()
	seen := map[string]bool{}
	for _, a := range rows {
		seen[a.LUID] = true
		opts = append(opts, [2]string{a.LUID, a.Label})
	}
	if _, pinned := mf.DevicePref(); pinned != "" && !seen[pinned] {
		opts = append(opts, [2]string{pinned, pinned + " " + i18n.T("settings.body.medialink.deviceMissing")})
	}
	return opts
}

// encodeEngineOptions lists the encoder pin: automatic (negotiate per the §3.2 matrix), the native
// pipe-free engine when this machine has it, then every encoder the ffmpeg probe proved WORKING. A
// pinned encoder that no longer probes is kept so the setting stays visible.
func encodeEngineOptions(mf *config.MediaLinkFeature) [][2]string {
	opts := [][2]string{{"", i18n.T("settings.body.medialink.encoderAuto")}}
	seen := map[string]bool{}
	add := func(val, label string) {
		if val == "" || seen[val] {
			return
		}
		seen[val] = true
		opts = append(opts, [2]string{val, label})
	}
	if mfenc.Available() {
		add(medialink.EncoderMFNative, i18n.T("settings.body.medialink.encoderNative"))
	}
	if caps, ok := mediapipe.Cached(); ok {
		for _, e := range caps.Encoders {
			add(e, e)
		}
	}
	if pin := mf.PinnedEncoder(); pin != "" {
		add(pin, pin+" "+i18n.T("settings.body.medialink.deviceMissing"))
	}
	return opts
}

// applyEncodeDevice writes the picker's value back as the policy+adapter pair.
func applyEncodeDevice(mf *config.MediaLinkFeature, v string) {
	switch strings.TrimSpace(v) {
	case "":
		mf.DevicePolicy, mf.EncoderDevice = "", ""
	case deviceAvoidValue:
		mf.DevicePolicy, mf.EncoderDevice = encoderscan.PolicyAvoid, ""
	default:
		mf.DevicePolicy, mf.EncoderDevice = encoderscan.PolicyPin, v
	}
}
