package app

// One log line answering "is the Zig work actually live in THIS build, on THIS machine?".
//
// It exists because every Zig surface is reached through a different mechanism - a build tag
// (zigdsp/zigui/zigvr), an ABI handshake at runtime, an embedded child exe, or a config default -
// so before this line the only way to answer the question was to read the build recipe and then
// grep the log for four unrelated messages. A tag that was compiled in but whose lib failed its ABI
// check looks exactly like a tag that was never enabled, which is precisely the confusion worth
// killing. The shell's RESOLVED host is logged separately by webui (it is decided later, and
// wanting the Zig child is not the same as having its exe).

import (
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/zignative"
	"rave.page/mate/internal/zigui"
)

// logZigPosture reports which native-Zig paths this process can actually take. Values are probed,
// not assumed: Available() is an ABI-version handshake against the linked lib, so a stale artifact
// reports false instead of crashing later.
func logZigPosture(log *logbus.Bus, cfg config.Config) {
	if log == nil {
		return
	}
	// shellWanted/media* are WANTED, not resolved: the shell exe may be missing (webui logs that
	// outcome) and the media flags are re-read per route.
	log.Info("app", "zig posture", map[string]any{
		"core":         zignative.Available(), // native/zigcore: resampler, DSP, PCM decode (zigdsp)
		"ui":           zigui.Available(),     // native/zigui: webui render fast paths (zigui)
		"shellWanted":  shellWantedLabel(cfg),
		"mediaCapture": captureLabel(cfg.Features.MediaLink.ZeroCopyCapture()),
		"mediaDecode":  captureLabel(cfg.Features.MediaLink.ZeroCopyDecode()),
	})
}

// shellWantedLabel names the window host the config asks for.
func shellWantedLabel(cfg config.Config) string {
	if cfg.Features.UI.ZigShell() {
		return "zig"
	}
	return "go"
}

// captureLabel names a media path the way the route telemetry does, so the two are comparable.
func captureLabel(zeroCopy bool) string {
	if zeroCopy {
		return "zerocopy"
	}
	return "readback"
}
