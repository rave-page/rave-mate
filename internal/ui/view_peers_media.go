package ui

import (
	"fmt"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediaroute"
)

// view_peers_media.go is the Peers tab's "Media plane" block: per-route medialink stats (loss /
// jitter / latency / recovered), clock-sync tier + offset, and the timecode master state
// (MEDIALINK_DESIGN.md §7 telemetry surface).

const helpMediaPlane = "Low-latency encrypted audio/video/timecode routes between paired " +
	"instances (the medialink plane). Shows each live route's delivery quality plus the shared " +
	"media clock and timecode master state."

const helpMediaClock = "Shared media clock all routes stamp frames on. Tier: monotonic = " +
	"local only, software = NTP-style sync to the elected master, ptp = hardware clock. " +
	"Offset = applied slew vs the raw local clock; locked means the sync filter has fresh, " +
	"consistent samples. Sync rows show the measured offset/round-trip per paired instance."

const helpMediaTC = "Timecode master election: one instance generates house timecode, the " +
	"others chase it. Auto-elected (lowest node id) unless pinned. Holdover = the master went " +
	"silent; this instance freewheels on the last position until it recovers or a new master " +
	"is elected."

const helpMediaRoutes = "Per-route delivery stats. loss = frames never recovered (recovered = " +
	"filled by retransmit). jitter = interarrival jitter (RFC 3550). latency = end-to-end " +
	"frame transit p50/p95 - only as accurate as the clock sync above. nack/retx = loss " +
	"recovery round-trips. Video routes add the negotiated encoder tier, live bitrate, " +
	"keyframes, adaptive buffer depth and decode rate."

const helpMediaReceive = "Video sources advertised by paired instances. Receive pulls one over " +
	"an encrypted route; it appears on THIS machine as a Spout sender named " +
	"\"rave-mate link <source>\" for OBS/Resolume/TouchDesigner. Codec + bitrate come from the " +
	"media-link settings; hardware encoders are negotiated automatically - a software fallback " +
	"is flagged (high CPU). Same-PC instances are refused: use the Spout sender directly."

// mediaSection builds the media-plane block (nil when the media plane is unavailable).
func (u *UI) mediaSection(peerName func(string) string) []fyne.CanvasObject {
	m := u.svc.Media
	if m == nil {
		return nil
	}
	objs := []fyne.CanvasObject{
		widget.NewSeparator(),
		container.NewHBox(sectionLabel("Media plane"), helpIcon(helpMediaPlane)),
	}

	// Clock: active tier/lock/offset + per-peer sync estimates.
	clockLine := fmtClockLine(m.ClockQuality())
	syncs := m.SyncStats()
	sort.Slice(syncs, func(i, j int) bool { return syncs[i].Peer < syncs[j].Peer })
	for _, s := range syncs {
		clockLine += "\n" + fmtSyncLine(s, peerName(s.Peer))
	}
	objs = append(objs, container.NewBorder(nil, nil, nil, helpIcon(helpMediaClock), mutedLabel(clockLine)))

	// Timecode master state.
	if p := u.svc.TCPlane; p != nil {
		objs = append(objs, container.NewBorder(nil, nil, nil, helpIcon(helpMediaTC), mutedLabel(fmtTCLine(p.Status(), peerName))))
	}

	// Routes.
	stats := m.Stats()
	if len(stats) == 0 {
		objs = append(objs, mutedLabel("No active media routes."))
	} else {
		sort.Slice(stats, func(i, j int) bool { return stats[i].Session < stats[j].Session })
		objs = append(objs, container.NewBorder(nil, nil, nil, helpIcon(helpMediaRoutes), mutedLabel(fmt.Sprintf("%d route(s)", len(stats)))))
		for _, s := range stats {
			title, detail := fmtRouteStat(s, peerName)
			objs = append(objs, widget.NewLabel(title), mutedLabel("   "+detail))
			if pl := fmtPipeLine(s); pl != "" {
				objs = append(objs, mutedLabel("   "+pl))
			}
		}
	}
	// P4: receivable remote video sources + this instance's receive routes.
	objs = append(objs, u.mediaReceiveRows(peerName)...)
	return objs
}

// mediaReceiveRows builds the "Receive video" block (remote sources + active receives).
func (u *UI) mediaReceiveRows(peerName func(string) string) []fyne.CanvasObject {
	mr := u.svc.MediaRoutes
	if mr == nil {
		return nil
	}
	srcs := mr.RemoteVideoSources()
	recvs := mr.Receives()
	if len(srcs) == 0 && len(recvs) == 0 {
		return nil
	}
	objs := []fyne.CanvasObject{
		container.NewHBox(sectionLabel("Receive video"), helpIcon(helpMediaReceive)),
	}
	receiving := map[string]bool{}
	for _, r := range recvs {
		receiving[r.Peer+"\x00"+r.Name] = true
		sess := r.Session
		stop := widget.NewButton("Stop", func() { mr.StopReceive(sess) })
		objs = append(objs, container.NewBorder(nil, nil, nil, stop,
			mutedLabel(fmt.Sprintf("◂ %s from %s → Spout \"rave-mate link %s\"",
				r.Name, peerName(r.Peer), r.Name))))
	}
	for _, s := range srcs {
		if receiving[s.Peer+"\x00"+s.Desc.Name] {
			continue
		}
		src := s
		btn := widget.NewButton("Receive", func() {
			if _, err := mr.StartReceive(src.Peer, src.Desc.ID); err != nil {
				u.Notify("Media route", err.Error())
			}
		})
		objs = append(objs, container.NewBorder(nil, nil, nil, btn,
			mutedLabel(fmtRemoteSource(src, peerName))))
	}
	return objs
}

// fmtRemoteSource renders one receivable remote source line.
func fmtRemoteSource(s mediaroute.RemoteSource, peerName func(string) string) string {
	d := s.Desc
	line := fmt.Sprintf("%s @ %s", d.Name, peerName(s.Peer))
	if d.Width > 0 {
		line += fmt.Sprintf(" · %dx%d", d.Width, d.Height)
		if d.FPS > 0 {
			line += fmt.Sprintf("@%.0f", d.FPS)
		}
	}
	return line
}

// fmtPipeLine renders the P4 pipeline stats line ("" for non-video/raw routes).
func fmtPipeLine(s medialink.RouteStat) string {
	var parts []string
	if s.Encoder != "" {
		p := fmt.Sprintf("%s tier %d", s.Encoder, s.Tier)
		if s.Software {
			p += " ⚠ software encode (high CPU)"
		}
		parts = append(parts, p)
	}
	if s.RateBps > 0 {
		// Wire fps alongside the bitrate: the encoder's "out N fps" below is a DIFFERENT counter,
		// and a route whose encoder runs at 40 while the wire carries 4 is invisible without both.
		parts = append(parts, fmt.Sprintf("%.1f Mbps · wire %.1f fps", s.RateBps/1e6, s.WireFPS))
	}
	if s.Keyframes > 0 {
		parts = append(parts, fmt.Sprintf("kf %d", s.Keyframes))
	}
	if s.JB != nil {
		parts = append(parts, fmt.Sprintf("buffer %df · late %.1f%% · drops %d",
			s.JB.Depth, s.JB.LateRate*100, s.JB.PolicyDrops))
	}
	if s.Pipe != nil {
		p := fmt.Sprintf("out %.1f fps", s.Pipe.OutFPS)
		if s.Pipe.HWAccel != "" {
			p += " · " + s.Pipe.HWAccel
		}
		if s.Pipe.Restarts > 0 {
			p += fmt.Sprintf(" · restarts %d", s.Pipe.Restarts)
		}
		// Collected since the native engine landed but never rendered until now: submit→AU
		// latency, encoder queue depth and child CPU are THE saturation signals, and a route
		// that ships them nowhere ships blind.
		if s.Pipe.LatP99Ms > 0 {
			p += fmt.Sprintf(" · lat %.1f/%.1f ms p50/p99", s.Pipe.LatP50Ms, s.Pipe.LatP99Ms)
		}
		if s.Pipe.QueueDepth != 0 {
			p += fmt.Sprintf(" · queue %d", s.Pipe.QueueDepth)
		}
		if s.Pipe.ChildCPUPct > 0 {
			p += fmt.Sprintf(" · child cpu %.0f%%", s.Pipe.ChildCPUPct)
		}
		// Every stage counted its own drops and none of them reached a surface: a route that
		// throws most frames away (dims mismatch, no keyframe yet) looked healthy. Deliberate
		// fps-cap throttling is rendered SEPARATELY - summed in, a correctly capped route read
		// "dropped 41902 and climbing", which is indistinguishable from catastrophic loss.
		if s.Pipe.RateCapped > 0 {
			p += fmt.Sprintf(" · rate-capped %d", s.Pipe.RateCapped)
		}
		if lost := s.Pipe.RealDrops(); lost > 0 {
			p += fmt.Sprintf(" · dropped %d", lost)
		}
		parts = append(parts, p)
		if z := fmtCaptureLine(*s.Pipe); z != "" {
			parts = append(parts, z)
		}
	}
	return strings.Join(parts, " · ")
}

// fmtCaptureLine renders the zero-copy capture block ("" on a readback route with no
// downgrades). A rig that always downgrades must be visible here rather than silently slow.
func fmtCaptureLine(p medialink.PipelineStats) string {
	var out []string
	if p.ZeroCopy {
		out = append(out, fmt.Sprintf("zero-copy %.1f fps", p.CapFPS))
		if p.EncBusyMs > 0 {
			out = append(out, fmt.Sprintf("encode %.1f ms/frame", p.EncBusyMs))
		}
		if p.CapSkips > 0 {
			out = append(out, fmt.Sprintf("skips %d", p.CapSkips))
		}
		if p.MtxTimeouts > 0 {
			out = append(out, fmt.Sprintf("mutex timeouts %d", p.MtxTimeouts))
		}
		if p.SrcErrors > 0 {
			out = append(out, fmt.Sprintf("src errors %d", p.SrcErrors))
		}
		if p.CapStaleMs > 0 {
			out = append(out, fmt.Sprintf("stale %.0f ms", p.CapStaleMs))
		}
	}
	if p.ZeroDecode {
		out = append(out, fmt.Sprintf("gpu decode %.1f fps", p.DecFPS))
		if p.DecBusyMs > 0 {
			out = append(out, fmt.Sprintf("decode %.1f ms/frame", p.DecBusyMs))
		}
		if p.InDropped > 0 {
			out = append(out, fmt.Sprintf("ring drops %d", p.InDropped))
		}
		if p.DecErrors > 0 {
			out = append(out, fmt.Sprintf("publish errors %d", p.DecErrors))
		}
		if p.DecMtxTimeo > 0 {
			out = append(out, fmt.Sprintf("dest mutex timeouts %d", p.DecMtxTimeo))
		}
		if p.DecStaleMs > 0 {
			out = append(out, fmt.Sprintf("stale %.0f ms", p.DecStaleMs))
		}
	}
	if p.AdapterMoved {
		out = append(out, "adapter re-placed (sender on another GPU)")
	}
	if p.Downgrades > 0 {
		out = append(out, fmt.Sprintf("downgrades %d", p.Downgrades))
	}
	return strings.Join(out, " · ")
}

// fmtClockLine renders the media-clock tier ("clock software · locked · offset +0.31 ms").
func fmtClockLine(q medialink.ClockQuality) string {
	lock := "acquiring"
	if q.Locked {
		lock = "locked"
	}
	line := fmt.Sprintf("clock %s · %s", q.Tier, lock)
	if q.OffsetNs != 0 {
		line += " · offset " + fmtSignedMs(q.OffsetNs)
	}
	return line
}

// fmtSyncLine renders one peer's pairwise sync estimate.
func fmtSyncLine(s medialink.SyncStat, name string) string {
	lock := "acquiring"
	if s.Locked {
		lock = "locked"
	}
	return fmt.Sprintf("sync %s: offset %s · rtt %s · %s",
		name, fmtSignedMs(s.OffsetNs), fmtMs(float64(s.RTTNs)), lock)
}

// fmtTCLine renders the timecode-master state ("TC master: this instance · 01:02:03:04 @30").
func fmtTCLine(st medialink.TCStatus, peerName func(string) string) string {
	var line string
	if st.Role == medialink.TCRoleMaster {
		line = "TC master: this instance"
	} else {
		line = "TC master: " + peerName(st.Master)
	}
	if st.Rate.Nominal != 0 {
		line += fmt.Sprintf(" · %s @%s", st.TC.String(), fmtRate(st.Rate))
		if !st.Running {
			line += " · stopped"
		}
	}
	if st.Holdover {
		line += " · HOLDOVER"
	}
	return line
}

// fmtRouteStat renders one route as a title + muted detail line.
func fmtRouteStat(s medialink.RouteStat, peerName func(string) string) (title, detail string) {
	dir := "◂ receiving from"
	if s.Direction == "send" {
		dir = "▸ sending to"
	}
	title = fmt.Sprintf("%s %s - stream %d · %d frames · %s",
		dir, peerName(s.Peer), s.Stream, s.Frames, humanBytes(int64(s.Bytes)))
	if s.Direction == "recv" {
		detail = fmt.Sprintf("loss %d · recovered %d · jitter %s · latency %s/%s p50/p95 · nack %d",
			s.LostEst, s.Recovered, fmtMs(s.JitterNs),
			fmtLat(s, s.LatencyP50Ns), fmtLat(s, s.LatencyP95Ns), s.NACKsSent)
		return title, detail
	}
	if r := s.Remote; r != nil { // sender: quality as the receiver reports it (RFC 3550 RR)
		detail = fmt.Sprintf("peer reports: loss %d (%.2f%%) · jitter %s · retx %d · pli %d",
			r.Lost, r.FractionLost*100, fmtMs(r.Jitter), s.Retransmits, s.PLIRequests)
	} else {
		detail = fmt.Sprintf("retx %d · pli %d · no report from the paired instance yet",
			s.Retransmits, s.PLIRequests)
	}
	return title, detail
}

// fmtRate renders a Rate token ("30", "29.97df").
func fmtRate(r medialink.Rate) string {
	if r.Drop {
		return fmt.Sprintf("%.2fdf", float64(r.Nominal)*1000/1001)
	}
	return fmt.Sprintf("%d", r.Nominal)
}

// fmtLat renders one e2e latency percentile, or "n/a" when no PLAUSIBLE transit sample exists.
// arrival−PTS is a duration only while the peer stamps PTS on our media clock; a foreign domain
// used to print the raw epoch as a latency ("1785118072019.6 ms"). "off-clock" names the cause.
func fmtLat(s medialink.RouteStat, ns int64) string {
	if s.LatencySamples > 0 {
		return fmtMs(float64(ns))
	}
	if s.LatUnsynced > 0 {
		return "off-clock"
	}
	return "n/a"
}

// fmtMs renders nanoseconds as adaptive milliseconds ("0.42 ms", "12.3 ms").
func fmtMs(ns float64) string {
	ms := ns / 1e6
	if ms < 0 {
		ms = -ms
	}
	if ms < 10 {
		return fmt.Sprintf("%.2f ms", ms)
	}
	return fmt.Sprintf("%.1f ms", ms)
}

// fmtSignedMs renders a signed ns offset as ms ("+0.31 ms").
func fmtSignedMs(ns int64) string {
	if ns < 0 {
		return "−" + fmtMs(float64(-ns))
	}
	return "+" + fmtMs(float64(ns))
}
