package ui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediaroute"
)

func nameFixed(m map[string]string) func(string) string {
	return func(id string) string {
		if n, ok := m[id]; ok {
			return n
		}
		return id
	}
}

func TestFmtClockLine(t *testing.T) {
	got := fmtClockLine(medialink.ClockQuality{Tier: medialink.TierSoftware, Locked: true, OffsetNs: 310_000})
	if got != "clock software · locked · offset +0.31 ms" {
		t.Errorf("locked: %q", got)
	}
	got = fmtClockLine(medialink.ClockQuality{Tier: medialink.TierMonotonic, Locked: true})
	if got != "clock monotonic · locked" {
		t.Errorf("monotonic (zero offset omitted): %q", got)
	}
	got = fmtClockLine(medialink.ClockQuality{Tier: medialink.TierSoftware, OffsetNs: -1_500_000})
	if got != "clock software · acquiring · offset −1.50 ms" {
		t.Errorf("acquiring negative: %q", got)
	}
}

func TestFmtSyncLine(t *testing.T) {
	s := medialink.SyncStat{Peer: "n1", SyncEstimate: medialink.SyncEstimate{
		OffsetNs: 420_000, RTTNs: 850_000, Locked: true}}
	got := fmtSyncLine(s, "Stage-Left")
	if got != "sync Stage-Left: offset +0.42 ms · rtt 0.85 ms · locked" {
		t.Errorf("sync line: %q", got)
	}
}

func TestFmtTCLine(t *testing.T) {
	names := nameFixed(map[string]string{"peer1": "Stage-Left"})
	tc := medialink.Timecode{H: 1, M: 2, S: 3, F: 4, Rate: medialink.FPS30}
	got := fmtTCLine(medialink.TCStatus{Role: medialink.TCRoleMaster, Master: "self",
		Running: true, TC: tc, Rate: medialink.FPS30}, names)
	if got != "TC master: this instance · 01:02:03:04 @30" {
		t.Errorf("master: %q", got)
	}
	got = fmtTCLine(medialink.TCStatus{Role: medialink.TCRoleSlave, Master: "peer1",
		TC: tc, Rate: medialink.FPS30, Holdover: true}, names)
	if got != "TC master: Stage-Left · 01:02:03:04 @30 · stopped · HOLDOVER" {
		t.Errorf("slave holdover: %q", got)
	}
	// Drop-frame rate token + no rate → no TC segment.
	if r := fmtRate(medialink.FPS2997); r != "29.97df" {
		t.Errorf("fmtRate df: %q", r)
	}
	got = fmtTCLine(medialink.TCStatus{Role: medialink.TCRoleSlave, Master: "peer1"}, names)
	if got != "TC master: Stage-Left" {
		t.Errorf("no announce yet: %q", got)
	}
}

func TestFmtRouteStat(t *testing.T) {
	names := nameFixed(map[string]string{"p": "Stage-Left"})
	recv := medialink.RouteStat{Session: "s", Peer: "p", Stream: 3, Direction: "recv",
		Frames: 1200, Bytes: 4 << 20, LostEst: 2, Recovered: 5, NACKsSent: 4,
		JitterNs: 420_000, LatencyP50Ns: 2_100_000, LatencyP95Ns: 4_300_000}
	title, detail := fmtRouteStat(recv, names)
	if title != "◂ receiving from Stage-Left - stream 3 · 1200 frames · 4.0 MB" {
		t.Errorf("recv title: %q", title)
	}
	if detail != "loss 2 · recovered 5 · jitter 0.42 ms · latency 2.10 ms/4.30 ms p50/p95 · nack 4" {
		t.Errorf("recv detail: %q", detail)
	}

	send := medialink.RouteStat{Session: "s2", Peer: "p", Stream: 3, Direction: "send",
		Frames: 100, Bytes: 1024, Retransmits: 7, PLIRequests: 1,
		Remote: &medialink.Report{Lost: 3, FractionLost: 0.0031, Jitter: 900_000}}
	title, detail = fmtRouteStat(send, names)
	if !strings.HasPrefix(title, "▸ sending to Stage-Left - stream 3") {
		t.Errorf("send title: %q", title)
	}
	if detail != "peer reports: loss 3 (0.31%) · jitter 0.90 ms · retx 7 · pli 1" {
		t.Errorf("send detail: %q", detail)
	}

	// No remote report yet: generic copy, never a setup-specific name.
	send.Remote = nil
	_, detail = fmtRouteStat(send, names)
	if !strings.Contains(detail, "paired instance") {
		t.Errorf("no-report detail: %q", detail)
	}
}

func TestFmtPipeLine(t *testing.T) {
	// Raw/audio route: no pipeline line.
	if got := fmtPipeLine(medialink.RouteStat{}); got != "" {
		t.Errorf("empty: %q", got)
	}
	s := medialink.RouteStat{Encoder: "hevc_nvenc", Tier: 2, RateBps: 14_200_000, Keyframes: 30,
		JB:   &medialink.JitterStats{Depth: 2, LateRate: 0.013, PolicyDrops: 4},
		Pipe: &medialink.PipelineStats{OutFPS: 59.8, HWAccel: "cuda", Restarts: 1}}
	got := fmtPipeLine(s)
	want := "hevc_nvenc tier 2 · 14.2 Mbps · kf 30 · buffer 2f · late 1.3% · drops 4 · out 59.8 fps · cuda · restarts 1"
	if got != want {
		t.Errorf("pipe line:\n got %q\nwant %q", got, want)
	}
	// §3.2 software tier carries the CPU warning.
	sw := fmtPipeLine(medialink.RouteStat{Encoder: "libx264", Tier: 4, Software: true})
	if !strings.Contains(sw, "software encode (high CPU)") {
		t.Errorf("sw warning missing: %q", sw)
	}
}

func TestFmtRemoteSource(t *testing.T) {
	names := nameFixed(map[string]string{"p": "Stage-Left"})
	src := mediaroute.RemoteSource{Peer: "p", Desc: medialink.SourceDesc{
		Name: "OBS Spout", Width: 1920, Height: 1080, FPS: 60}}
	if got := fmtRemoteSource(src, names); got != "OBS Spout @ Stage-Left · 1920x1080@60" {
		t.Errorf("remote source: %q", got)
	}
	bare := mediaroute.RemoteSource{Peer: "p", Desc: medialink.SourceDesc{Name: "Webcam"}}
	if got := fmtRemoteSource(bare, names); got != "Webcam @ Stage-Left" {
		t.Errorf("bare source: %q", got)
	}
}
