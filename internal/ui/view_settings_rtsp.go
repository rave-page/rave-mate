package ui

// Local RTSP performer chain settings card: ffmpeg encodes a configured video source and
// rave-mate serves it as rtspt:// for VRChat's AVPro player (sub-second LAN latency, no
// OBS/MediaMTX relay). Education-first tooltips throughout.

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// rtspCard configures the local RTSP server (module "rtspserve").
func (u *UI) rtspCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.RTSPServe

	detail := widget.NewLabel("")
	detail.Wrapping = fyne.TextWrapWord
	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled {
			s.set(colMuted, "off")
			detail.SetText("")
			return
		}
		if u.svc.RTSP == nil {
			s.set(colBrandAmber, "unavailable")
			return
		}
		snap := u.svc.RTSP.Status()
		port := f.ResolvedListenAddr()
		if i := strings.LastIndex(snap.Addr, ":"); i >= 0 {
			port = snap.Addr[i:]
		}
		detail.SetText(fmt.Sprintf("Play in VRChat / a LAN player: rtspt://<this machine's IP>%s%s", port, f.ResolvedPath()))
		switch {
		case !snap.Running:
			s.set(colBrandAmber, "not running (port busy?)")
		case snap.LastErr != "":
			s.set(colBrandAmber, snap.LastErr)
		case snap.SourceUp && snap.Clients > 0:
			s.set(colBrandMint, fmt.Sprintf("streaming to %d client(s) · %d frames · %d source restart(s)", snap.Clients, snap.AUs, snap.Restarts))
		case snap.SourceUp:
			s.set(colBrandMint, fmt.Sprintf("encoding - waiting for players · %d frames", snap.AUs))
		default:
			s.set(colBrandAmber, "starting video source…")
		}
	})
	toggle := u.moduleToggle("rtspserve", &f.Enabled)

	src := newEntry()
	src.SetPlaceHolder("file, URL or device - e.g. desktop (with format gdigrab)")
	src.SetText(f.Source)
	src.OnChanged = func(s string) { f.Source = s; u.saveCfg() }

	inFmt := newEntry()
	inFmt.SetPlaceHolder("auto")
	inFmt.SetText(f.InputFormat)
	inFmt.OnChanged = func(s string) { f.InputFormat = s; u.saveCfg() }

	listen := newEntry()
	listen.SetPlaceHolder(":8554")
	listen.SetText(f.ListenAddr)
	listen.OnChanged = func(s string) { f.ListenAddr = s; u.saveCfg() }

	path := newEntry()
	path.SetPlaceHolder("/live")
	path.SetText(f.Path)
	path.OnChanged = func(s string) { f.Path = s; u.saveCfg() }

	fps := newEntry()
	fps.SetPlaceHolder("30")
	if f.FPS > 0 {
		fps.SetText(strconv.Itoa(f.FPS))
	}
	fps.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 120 {
			f.FPS = n
			u.saveCfg()
		}
	}

	bitrate := newEntry()
	bitrate.SetPlaceHolder("6000")
	if f.BitrateKbps > 0 {
		bitrate.SetText(strconv.Itoa(f.BitrateKbps))
	}
	bitrate.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n >= 250 && n <= 50000 {
			f.BitrateKbps = n
			u.saveCfg()
		}
	}

	passthrough := widget.NewCheck("Source is already H.264 (pass through, no re-encode)", func(v bool) { f.Passthrough = v; u.saveCfg() })
	passthrough.SetChecked(f.Passthrough)

	body := container.NewVBox(
		mutedLabel("Serve a local video source as a low-latency RTSP stream on your network - VRChat's video player takes the rtspt:// address directly, so a world can show your screen/camera/grid without streaming through the internet."),
		labelWithHelp("Why RTSP?",
			"VRChat can't receive Spout or NDI, but its AVPro video player accepts rtspt:// (RTSP over TCP) with roughly half a second of delay - the fastest local path into a world. This replaces the usual OBS → RTMP → MediaMTX relay chain with one built-in server. Needs ffmpeg (install it under Library & media)."),

		widget.NewSeparator(),
		container.NewBorder(nil, nil, labelWithHelp("Video source",
			"What to stream, as an ffmpeg input: a video file, a network URL (rtmp/http/rtsp), or a capture device. Screen capture: set Input format to gdigrab and the source to desktop. A capture card/webcam: format dshow, source video=<device name>."), nil, src),
		container.NewBorder(nil, nil, labelWithHelp("Input format",
			"ffmpeg demuxer for the source (-f). Leave empty for files/URLs (auto-detected). gdigrab = Windows screen capture, dshow = DirectShow device."), nil, inFmt),
		container.NewHBox(passthrough, helpIcon("If the source already delivers H.264 (many IP cams, an OBS RTMP feed), copy it through untouched - zero quality loss and near-zero CPU. Leave off for screen capture or non-H.264 sources; they are encoded with zero-latency x264.")),

		widget.NewSeparator(),
		container.NewBorder(nil, nil, labelWithHelp("Listen address",
			"Where the RTSP server accepts players. Default binds every interface on TCP 8554 (the conventional RTSP port). Enter ip:port to bind one interface. Toggle off/on to apply."), nil, listen),
		container.NewBorder(nil, nil, labelWithHelp("Stream path",
			"The path part of the stream URL players open, e.g. /live → rtspt://<ip>:8554/live."), nil, path),
		container.NewBorder(nil, nil, labelWithHelp("Frame rate",
			"Encode + timestamp rate (1–120, default 30). Keyframes are sent once per second so joining players get a picture fast."), nil, fps),
		container.NewBorder(nil, nil, labelWithHelp("Bitrate (kbps)",
			"H.264 bitrate when re-encoding (default 6000). On a LAN you can go high; raise it if the picture smears on motion. Ignored in pass-through mode."), nil, bitrate),

		widget.NewSeparator(),
		detail,
	)
	return featureCard("Local RTSP stream", "Low-latency LAN video into VRChat (AVPro rtspt://) - screen, camera or any source, no relay chain.", toggle, st, body)
}
