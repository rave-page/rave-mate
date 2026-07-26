package mediapipe

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

// probe.go - §3.2 capability probe: which video encoders WORK on this machine (build listing +
// a tiny test encode - a listed HW encoder can still fail: no GPU / driver / busy), which codecs
// decode, and which hwaccel methods exist. Probed once per launch, cached.

// Caps is the probed capability set fed into medialink Options / SetCodecCaps.
type Caps struct {
	Encoders []string          // working video encoders (§3.2 matrix candidates: build-present AND test-encode passed)
	InBuild  []string          // candidates present in `ffmpeg -encoders` (superset of Encoders; diff = in build but failed to encode)
	Errors   map[string]string // encoder name → last stderr line, for in-build candidates whose test-encode failed
	Decoders []string          // decodable codec capability names (medialink.Decode*)
	HWAccels []string          // decode hwaccel methods, preference-ordered
	// Validated is false for a LISTING-ONLY probe (ProbeListing): Encoders is then the in-build
	// candidate set, not a test-encode-proven set, so an entry may still fail at route time.
	Validated bool
}

// hwEncoderMarkers identify hardware video encoders in ANY vendor's ffmpeg backend - vendor-neutral
// so the probe supports every config incl. custom encoder cards (which register via Media
// Foundation → *_mf). Candidates are DISCOVERED from `ffmpeg -encoders`, not hardcoded, so a
// backend we never named (a new *_vaapi, a card's *_mf) is still found + validated.
var hwEncoderMarkers = []string{"nvenc", "qsv", "amf", "vaapi", "v4l2m2m", "videotoolbox", "mediacodec", "omx", "_mf"}

// swBaselineEncoders are the software encoders we always want as fallback/echo tiers.
var swBaselineEncoders = []string{"libx264", "libx265", "libsvtav1", "mjpeg"}

// probeCodecs limits which codecs we test-encode (skip exotic ones we never route: prores, ffv1…).
var probeCodecs = []string{"h264", "hevc", "av1", "mjpeg"}

// probeDecoders maps ffmpeg decoder names to the §3.2 decode capability they prove.
var probeDecoders = map[string]string{
	"h264":  medialink.DecodeH264,
	"hevc":  medialink.DecodeHEVC,
	"mjpeg": medialink.DecodeJPEG,
}

// hwaccelOrder is the decode-tier preference (§3.2: NVDEC → QSV → generic D3D11 → DXVA2 → sw).
var hwaccelOrder = []string{"cuda", "qsv", "d3d11va", "dxva2"}

var (
	probeMu       sync.Mutex
	probeCached   map[string]Caps // ffmpeg path → validated result (test-encodes ran)
	listingCached map[string]Caps // ffmpeg path → listing-only result (no test-encodes)
)

// Probe returns this machine's cached capability set. ok=false when ffmpeg is unavailable.
// First call blocks for the test encodes (seconds) - call it off the UI path.
func Probe(ctx context.Context, log *logbus.Bus) (Caps, bool) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return Caps{}, false
	}
	probeMu.Lock()
	if c, ok := probeCached[ffmpeg]; ok {
		probeMu.Unlock()
		return c, true
	}
	probeMu.Unlock()

	c := runProbe(ctx, ffmpeg)
	if log != nil {
		log.Info(source, "codec probe", map[string]any{
			"encoders": strings.Join(c.Encoders, ","),
			"decoders": strings.Join(c.Decoders, ","),
			"hwaccels": strings.Join(c.HWAccels, ",")})
	}
	probeMu.Lock()
	if probeCached == nil {
		probeCached = map[string]Caps{}
	}
	probeCached[ffmpeg] = c
	probeMu.Unlock()
	return c, true
}

// Cached returns the codec probe result for the current ffmpeg WITHOUT triggering a probe
// (never blocks / never runs test-encodes). ok=false if ffmpeg is unresolvable or the probe
// hasn't completed yet. For diagnostics (encoder-scan) that must not stall or poison the cache.
func Cached() (Caps, bool) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return Caps{}, false
	}
	probeMu.Lock()
	defer probeMu.Unlock()
	c, ok := probeCached[ffmpeg]
	return c, ok
}

// ProbeListing returns capabilities WITHOUT test-encoding anything: encoder/decoder/hwaccel
// listings only (`ffmpeg -encoders` etc. - text output, no GPU work, no encode session). Caps.
// Encoders is therefore the in-build candidate set with Validated=false.
//
// This is what we advertise while the activity governor forbids background work: a test encode on
// h264_nvenc takes a real NVENC session, and taking one mid-stream can fail OBS's encoder. Skipping
// the advertisement entirely instead would be worse - a sender with no advertised encoders makes the
// far end refuse the route (or fall back to raw video, the very melt we are fixing). ok=false when
// ffmpeg is unavailable. Cached per ffmpeg path; never poisons the validated Probe cache.
func ProbeListing(ctx context.Context, log *logbus.Bus) (Caps, bool) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return Caps{}, false
	}
	probeMu.Lock()
	if c, ok := probeCached[ffmpeg]; ok { // a validated result is strictly better - use it
		probeMu.Unlock()
		return c, true
	}
	if c, ok := listingCached[ffmpeg]; ok {
		probeMu.Unlock()
		return c, true
	}
	probeMu.Unlock()

	c := listProbe(ctx, ffmpeg)
	if log != nil {
		log.Info(source, "codec probe (listing only - no test encodes)", map[string]any{
			"encoders": strings.Join(c.Encoders, ","),
			"decoders": strings.Join(c.Decoders, ","),
			"hwaccels": strings.Join(c.HWAccels, ",")})
	}
	probeMu.Lock()
	if listingCached == nil {
		listingCached = map[string]Caps{}
	}
	listingCached[ffmpeg] = c
	probeMu.Unlock()
	return c, true
}

// listProbe reads the build's encoder/decoder/hwaccel listings - no test encodes.
func listProbe(ctx context.Context, ffmpeg string) Caps {
	cands := discoverVideoEncoders(ffmpegText(ctx, ffmpeg, "-encoders"))
	return Caps{
		Encoders: cands, InBuild: cands,
		Decoders: parseDecoders(ffmpegText(ctx, ffmpeg, "-decoders")),
		HWAccels: parseHWAccels(ffmpegText(ctx, ffmpeg, "-hwaccels")),
	}
}

func runProbe(ctx context.Context, ffmpeg string) Caps {
	c := Caps{Validated: true}
	// Candidates are DISCOVERED from the build's own `-encoders` (all in-build by construction),
	// so any HW backend present - named or not - is validated. Test-encode in parallel (bounded).
	cands := discoverVideoEncoders(ffmpegText(ctx, ffmpeg, "-encoders"))
	c.InBuild = cands
	results := make([]bool, len(cands))
	errs := make([]string, len(cands))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, name := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, enc string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx], errs[idx] = testEncode(ctx, ffmpeg, enc)
		}(i, name)
	}
	wg.Wait()
	for i, name := range cands {
		if results[i] {
			c.Encoders = append(c.Encoders, name)
		} else { // present in build but test-encode failed - keep the reason
			if c.Errors == nil {
				c.Errors = map[string]string{}
			}
			c.Errors[name] = errs[i]
		}
	}
	c.Decoders = parseDecoders(ffmpegText(ctx, ffmpeg, "-decoders"))
	c.HWAccels = parseHWAccels(ffmpegText(ctx, ffmpeg, "-hwaccels"))
	return c
}

// ffmpegText runs `ffmpeg <flag>` and returns stdout ("" on error).
func ffmpegText(ctx context.Context, ffmpeg, flag string) string {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, ffmpeg, "-hide_banner", flag)
	sysexec.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// testEncode runs a tiny throwaway encode; ok=false with the trimmed ffmpeg stderr on failure so
// the caller can report WHY a build-present HW encoder didn't work (device/driver/resolution).
// 1280x720 (not a tiny 160x120) so HW encoders with a minimum-resolution floor aren't false-negatived.
func testEncode(ctx context.Context, ffmpeg, name string) (bool, string) {
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, ffmpeg,
		"-hide_banner", "-loglevel", "warning", // warning: surface the encoder's OWN init failure, not just the muxer's downstream complaint
		"-f", "lavfi", "-i", "color=c=black:s=1280x720:r=10",
		"-frames:v", "3", "-pix_fmt", "yuv420p", "-c:v", name, "-f", "null", "-")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	sysexec.Hide(cmd)
	cmd.Cancel = func() error { sysexec.KillTree(cmd.Process); return nil }
	if err := cmd.Start(); err != nil {
		return false, err.Error()
	}
	sysexec.AssignToJobClass(cmd.Process, sysexec.JobBatch) // deferrable diagnostic: stay CPU-capped
	if err := cmd.Wait(); err != nil {
		return false, bestErrLine(strings.TrimSpace(stderr.String()))
	}
	return true, ""
}

// bestErrLine picks the encoder's real init error from ffmpeg stderr: the FIRST line naming the
// encoder or an init failure, ignoring the generic downstream "Nothing was written…" muxer line.
func bestErrLine(s string) string {
	if s == "" {
		return "no stderr (nonzero exit / timeout)"
	}
	lines := strings.Split(s, "\n")
	cap160 := func(t string) string {
		if len(t) > 160 {
			return t[:160]
		}
		return t
	}
	var last string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		last = t
		lc := strings.ToLower(t)
		// Skip non-fatal noise: the muxer's downstream complaint + pixel-format auto-select warnings.
		if strings.Contains(t, "Nothing was written") ||
			strings.Contains(lc, "auto-selecting") || strings.Contains(lc, "incompatible pixel format") {
			continue
		}
		for _, kw := range []string{"amf", "nvenc", "qsv", "d3d", "device", "error", "fail", "cannot", "unable", "unsupported", "not found", "no capable", "no device"} {
			if strings.Contains(lc, kw) {
				return cap160(t)
			}
		}
	}
	return cap160(last)
}

// discoverVideoEncoders returns the video encoders in a `ffmpeg -encoders` listing worth probing:
// every HW-family encoder (nvenc/qsv/amf/vaapi/v4l2m2m/videotoolbox/mediacodec/omx/*_mf - the last
// covers custom encoder cards registered as Media Foundation MFTs) plus the software baselines,
// restricted to codecs we route (probeCodecs). Vendor-neutral + discovered → supports any config.
func discoverVideoEncoders(out string) []string {
	var vids []string
	for _, line := range strings.Split(out, "\n") {
		if n := videoEncoderName(line); n != "" {
			vids = append(vids, n)
		}
	}
	seen := map[string]bool{}
	var sel []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			sel = append(sel, n)
		}
	}
	relevantCodec := func(lc string) bool {
		for _, cd := range probeCodecs {
			if strings.Contains(lc, cd) {
				return true
			}
		}
		return false
	}
	for _, n := range vids { // HW encoders first (preferred), only for codecs we route
		lc := strings.ToLower(n)
		if !relevantCodec(lc) {
			continue
		}
		for _, m := range hwEncoderMarkers {
			if strings.Contains(lc, m) {
				add(n)
				break
			}
		}
	}
	for _, n := range vids { // then software baselines
		for _, b := range swBaselineEncoders {
			if n == b {
				add(n)
				break
			}
		}
	}
	return sel
}

// videoEncoderName extracts the encoder name from one `ffmpeg -encoders` row (" V....D name  desc"),
// or "" if the row isn't a video encoder. The flags column is 6 chars of [A-Z.] starting with V.
func videoEncoderName(line string) string {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) < 2 || len(f[0]) != 6 || f[0][0] != 'V' {
		return ""
	}
	for _, r := range f[0] { // flags column: [A-Z.] only
		if r != '.' && (r < 'A' || r > 'Z') {
			return ""
		}
	}
	for _, r := range f[1] { // encoder name: identifier chars only (skips legend rows like "= Video")
		if r != '_' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return ""
		}
	}
	return f[1]
}

// parseDecoders maps a `-decoders` listing to decode capability names (stable probe order).
func parseDecoders(out string) []string {
	var caps []string
	for _, name := range []string{"h264", "hevc", "mjpeg"} {
		if containsName(out, name) {
			caps = append(caps, probeDecoders[name])
		}
	}
	return caps
}

// parseHWAccels extracts the usable hwaccel methods from a `-hwaccels` listing, in
// hwaccelOrder preference.
func parseHWAccels(out string) []string {
	have := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var accels []string
	for _, a := range hwaccelOrder {
		if have[a] {
			accels = append(accels, a)
		}
	}
	return accels
}

// containsName reports whether an ffmpeg list output names the codec as its own token
// (" h264_nvenc " column form - never a substring of another name).
func containsName(out, name string) bool {
	return strings.Contains(out, " "+name+" ")
}
