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
		JitterNs: 420_000, LatencyP50Ns: 2_100_000, LatencyP95Ns: 4_300_000, LatencySamples: 64}
	title, detail := fmtRouteStat(recv, names)
	if title != "◂ receiving from Stage-Left - stream 3 · 1200 frames · 4.0 MB" {
		t.Errorf("recv title: %q", title)
	}
	if detail != "loss 2 · recovered 5 · jitter 0.42 ms · latency 2.10 ms/4.30 ms p50/p95 · nack 4" {
		t.Errorf("recv detail: %q", detail)
	}

	// No plausible sample = no duration. The percentiles are still SET (a foreign PTS domain
	// leaves whatever the window last held); rendering them printed an epoch as a latency.
	off := recv
	off.LatencySamples, off.LatUnsynced = 0, 900
	off.LatencyP50Ns, off.LatencyP95Ns = 1_785_118_072_019_600_000, 1_785_118_072_016_000_000
	if _, d := fmtRouteStat(off, names); !strings.Contains(d, "latency off-clock/off-clock") {
		t.Errorf("off-clock PTS rendered as a duration: %q", d)
	}
	off.LatUnsynced = 0 // nothing received yet
	if _, d := fmtRouteStat(off, names); !strings.Contains(d, "latency n/a/n/a") {
		t.Errorf("unmeasured latency rendered as a duration: %q", d)
	}

	// percentiles() sorts ascending, so p50 ≤ p95 ALWAYS holds on the values. Printing both
	// through an abs() showed "29.0 ms/26.1 ms p50/p95" - a median above the 95th percentile -
	// whenever the median transit was negative (unaligned media clocks). The sign must survive.
	neg := recv
	neg.LatencyP50Ns, neg.LatencyP95Ns = -29_000_000, 26_100_000
	_, d := fmtRouteStat(neg, names)
	if !strings.Contains(d, "latency −29.0 ms/26.1 ms p50/p95") {
		t.Errorf("negative p50 lost its sign (p50 > p95 on screen): %q", d)
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
	s := medialink.RouteStat{Encoder: "hevc_nvenc", Tier: 2, RateBps: 14_200_000, WireFPS: 59.8,
		Keyframes: 30,
		JB:        &medialink.JitterStats{Depth: 2, LateRate: 0.013, PolicyDrops: 4},
		Pipe: &medialink.PipelineStats{OutFPS: 59.8, HWAccel: "cuda", Restarts: 1,
			PubFrames: 3591, PubBytes: 3591 * 1920 * 1080 * 4}}
	got := fmtPipeLine(s)
	want := "hevc_nvenc tier 2 · 14.2 Mbps · wire 59.8 fps · kf 30 · buffer 2f · late 1.3% · drops 4 · out 59.8 fps · cuda · restarts 1 · published 3591"
	if got != want {
		t.Errorf("pipe line:\n got %q\nwant %q", got, want)
	}
	// Deliberate fps-cap throttling and real loss are SEPARATE segments: a route capped from 60 to
	// 40 fps must not read "dropped 41902" (the whole point of RateCapped).
	capped := s
	capped.Pipe = &medialink.PipelineStats{OutFPS: 40, Dropped: 41902, RateCapped: 41902}
	if line := fmtPipeLine(capped); !strings.Contains(line, "rate-capped 41902") || strings.Contains(line, "dropped") {
		t.Errorf("a purely rate-capped route must not report drops: %q", line)
	}
	mixed := s
	mixed.Pipe = &medialink.PipelineStats{OutFPS: 40, Dropped: 41905, RateCapped: 41902}
	if line := fmtPipeLine(mixed); !strings.Contains(line, "rate-capped 41902") || !strings.Contains(line, "dropped 3") {
		t.Errorf("real loss must survive the split: %q", line)
	}
	// §3.2 software tier carries the CPU warning.
	sw := fmtPipeLine(medialink.RouteStat{Encoder: "libx264", Tier: 4, Software: true})
	if !strings.Contains(sw, "software encode (high CPU)") {
		t.Errorf("sw warning missing: %q", sw)
	}
}

// TestFmtContentLineRendersTheOracle is the gate for zigmedia inc-5 promotion gate 2: the numbers
// that separate a live picture from a black one, and the reason a route is off its best path, must
// REACH THE PANEL. Every field asserted here was collected since inc 1 and rendered nowhere, which
// is how a black route kept healthy-looking counters for 12 minutes.
func TestFmtContentLineRendersTheOracle(t *testing.T) {
	// The field's black 4K30 route: 255 B/frame on a budget where real content is ~83 kB.
	black := medialink.PipelineStats{OutFPS: 30, AUCount: 900, AUBytesPerFrame: 255}
	line := fmtContentLine(black)
	for _, want := range []string{"255 B/frame", "no picture content"} {
		if !strings.Contains(line, want) {
			t.Errorf("a 255 B/frame route does not render %q: %q", want, line)
		}
	}
	// Real 4K content must NOT be accused of being black, and must read in kB.
	live := medialink.PipelineStats{OutFPS: 30, AUCount: 900, AUBytesPerFrame: 83000}
	line = fmtContentLine(live)
	if !strings.Contains(line, "81.1 kB/frame") {
		t.Errorf("live content renders %q", line)
	}
	if strings.Contains(line, "no picture content") {
		t.Errorf("a live 83 kB/frame route was accused of carrying nothing: %q", line)
	}
	// A genuinely static sender sits at the noise floor too - the wording must not claim "black".
	if !strings.Contains(fmtContentLine(black), "static") {
		t.Error("the noise-floor wording must name the static case, not only black")
	}
	// Degrade visibility: the one field whose EMPTY value is the only healthy one.
	deg := medialink.PipelineStats{OutFPS: 30, DegradeReason: "hardware MFT poisoned",
		SoftwareEncode: true, Poisoned: true, BusyDrops: 12, EncFails: 3}
	line = fmtContentLine(deg)
	for _, want := range []string{"degraded: hardware MFT poisoned", "software encode",
		"hardware poisoned", "encoder saturated 12", "encode failures 3"} {
		if !strings.Contains(line, want) {
			t.Errorf("degrade block missing %q: %q", want, line)
		}
	}
	if fmtContentLine(medialink.PipelineStats{}) != "" {
		t.Error("a stats block with nothing to say must render nothing")
	}
	// WIRING, not just formatting: the block must reach the line the panel actually shows.
	wired := fmtPipeLine(medialink.RouteStat{Encoder: "h264_nvenc", Tier: 1, Pipe: &black})
	if !strings.Contains(wired, "255 B/frame") || !strings.Contains(wired, "no picture content") {
		t.Errorf("the content oracle does not reach the rendered pipe line: %q", wired)
	}
	// "Route up, frames received, nothing published" - the failure spoutSink.Write answers nil to.
	stuck := medialink.PipelineStats{OutFPS: 59.8, HWAccel: "cuda"}
	if !strings.Contains(fmtContentLine(stuck), "published 0") {
		t.Errorf("a decode route publishing nothing renders %q", fmtContentLine(stuck))
	}
	// ...but a route that has not started yet must stay quiet.
	if strings.Contains(fmtContentLine(medialink.PipelineStats{HWAccel: "cuda"}), "published") {
		t.Error("a cold receive route was accused of publishing nothing")
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
