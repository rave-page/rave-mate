package worker

import (
	"sync"

	"rave.page/mate/internal/transcode"
)

// Worker-side encoder resolution. The UI pre-resolves the concrete HW/SW encoder before
// dispatch (it owns detection), but headless callers - the automation engine, peer
// remote-control, the trim transcode - don't. Without resolution those paths silently
// fall back to software even when "auto" should pick the GPU. So when a job arrives with
// no EncoderOverride we resolve it here against THIS machine's real test-encode results,
// caching each probe so repeated jobs stay instant. Resolving on the executing machine is
// also why peer remote-control transcodes can use the controlled box's hardware.

var (
	encMu       sync.Mutex
	encCache    = map[string]bool{} // encoder name → working (test-encode succeeded)
	inBuildOnce sync.Once
	inBuildSet  map[string]bool
)

// encoderWorks reports (and memoizes) whether a video encoder is build-present AND passes a
// real test encode on this machine.
func encoderWorks(name string) bool {
	bin, err := ffmpegBin()
	if err != nil {
		return false
	}
	inBuildOnce.Do(func() { inBuildSet = buildEncoderSet(bin) })
	if !inBuildSet[name] {
		return false
	}
	encMu.Lock()
	if v, ok := encCache[name]; ok {
		encMu.Unlock()
		return v
	}
	encMu.Unlock()
	ok := testEncode(bin, name, false)
	encMu.Lock()
	encCache[name] = ok
	encMu.Unlock()
	return ok
}

// hwCodecKey maps a logical preset codec to the candidate-table Codec value used by HW
// encoders (transcode uses h265; the candidate table uses hevc).
var hwCodecKey = map[string]string{"h264": "h264", "h265": "hevc", "av1": "av1"}

// resolveWorkerEncoder picks the concrete ffmpeg encoder for codec×accel, probing only the
// candidates for this codec (bounded, cheap) so a fresh worker pays at most a few test
// encodes. Returns "" for copy/none (no encoder needed).
func resolveWorkerEncoder(codec, accel string) string {
	switch codec {
	case "", "none", "copy":
		return ""
	}
	working := map[string]bool{}
	if key := hwCodecKey[codec]; key != "" {
		for _, e := range candidateVideo {
			if e.Kind == "hw" && e.Codec == key {
				working[e.Name] = encoderWorks(e.Name)
			}
		}
	}
	enc, _ := transcode.ResolveEncoder(codec, accel, working)
	return enc
}
