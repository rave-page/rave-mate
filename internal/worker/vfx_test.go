package worker

// vfx worker integration tests. Need the built child (native/zigvfx/zig-out) with its
// test plugin, plus ffmpeg for preview/run - skip cleanly when either is absent.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/vfx"
)

func testPluginPath(t *testing.T) string {
	t.Helper()
	exe, err := vfx.ExePath()
	if err != nil {
		t.Skip("rave-mate-vfx not built: ", err)
	}
	dll := filepath.Join(filepath.Dir(exe), "f0r_test_invert.dll")
	if _, err := os.Stat(dll); err != nil {
		dll = filepath.Join(filepath.Dir(exe), "libf0r_test_invert.so")
		if _, err := os.Stat(dll); err != nil {
			t.Skip("test plugin not built beside rave-mate-vfx")
		}
	}
	return dll
}

func TestVfxList(t *testing.T) {
	dll := testPluginPath(t)
	raw, err := vfxList(mustJSON(t, vfxListIn{Dirs: []string{filepath.Dir(dll)}}), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Plugins []vfx.Plugin `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	var found *vfx.Plugin
	for i := range out.Plugins {
		if out.Plugins[i].Name == "Invert Test" {
			found = &out.Plugins[i]
		}
	}
	if found == nil {
		t.Fatalf("Invert Test not discovered in %v", out.Plugins)
	}
	if found.Kind != "frei0r" || len(found.Params) != 2 {
		t.Errorf("unexpected plugin shape: %+v", found)
	}
	if found.Params[0].Name != "amount" || found.Params[0].Type != "double" || found.Params[0].Def[0] != 1 {
		t.Errorf("amount param: %+v", found.Params[0])
	}
}

func TestVfxPreviewAndRun(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dll := testPluginPath(t)
	bin, err := ffmpegBin()
	if err != nil {
		t.Skip("ffmpeg unavailable: ", err)
	}
	dir := t.TempDir()

	// 2s 64x48 red test clip with silent audio
	src := filepath.Join(dir, "src.mp4")
	if err := runQuiet(t.Context(), bin, "-hide_banner", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=64x48:r=10:d=2",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono",
		"-t", "2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", src); err != nil {
		t.Fatal("gen clip: ", err)
	}

	chain := vfx.Chain{W: 32, H: 24, FPS: 10, Fx: []vfx.Fx{{Kind: "frei0r", Ref: dll}}}

	png := filepath.Join(dir, "prev.png")
	if _, err := vfxPreview(mustJSON(t, vfxPreviewIn{
		Input: src, T: 0.5, Chain: chain, Output: png,
	}), nil); err != nil {
		t.Fatal("preview: ", err)
	}
	if st, err := os.Stat(png); err != nil || st.Size() == 0 {
		t.Fatal("preview png missing/empty")
	}

	out := filepath.Join(dir, "out.mp4")
	var events []string
	emit := func(event string, data any) {
		events = append(events, event)
	}
	// explicit software encoder: HW encoders (NVENC etc.) reject tiny frames
	preset := transcode.Preset{Container: "mp4", VideoCodec: "h264",
		EncoderOverride: "libx264", CRF: 20, SpeedPreset: "ultrafast",
		AudioCodec: "aac", AudioBitrateK: 128}
	raw, err := vfxRun(mustJSON(t, vfxRunIn{
		Input: src, Output: out, Preset: &preset,
		TrimStart: 0.5, TrimEnd: 1.5, Chain: chain,
	}), emit)
	if err != nil {
		t.Fatal("run: ", err)
	}
	var res struct {
		Frames int64 `json:"frames"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	// 1s @ 10fps ≈ 10 frames (container rounding tolerated)
	if res.Frames < 8 || res.Frames > 12 {
		t.Errorf("frames = %d, want ~10", res.Frames)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatal("output missing/empty")
	}
	if !strings.Contains(strings.Join(events, ","), "progress") {
		t.Errorf("no progress events: %v", events)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
