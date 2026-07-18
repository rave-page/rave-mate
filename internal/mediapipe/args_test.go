package mediapipe

import (
	"strings"
	"testing"

	"rave.page/mate/internal/medialink"
)

// Downscale ceiling: 4K input + MaxHeight 1080 → scale filter + bitrate from the OUTPUT
// pixel rate (1080p60 = the 20 Mbps reference, not the 80 Mbps 4K clamp).
func TestEncodeArgsDownscale(t *testing.T) {
	args := strings.Join(encodeArgs(medialink.EncodeSpec{Encoder: "libx264", Codec: medialink.CodecH264,
		Width: 3840, Height: 2160, FPS: 60, MaxHeight: 1080}), " ")
	if !strings.Contains(args, "-vf scale=-2:1080") {
		t.Fatalf("missing scale filter: %s", args)
	}
	if !strings.Contains(args, "-b:v 20000k") {
		t.Fatalf("bitrate not derived from scaled output: %s", args)
	}
	// input at/below the ceiling = no filter, native bitrate math
	native := strings.Join(encodeArgs(medialink.EncodeSpec{Encoder: "libx264", Codec: medialink.CodecH264,
		Width: 1280, Height: 720, FPS: 30, MaxHeight: 1080}), " ")
	if strings.Contains(native, "scale=") {
		t.Fatalf("unexpected scale on native-size input: %s", native)
	}
}
