package studio

import (
	"encoding/json"
	"testing"

	"rave.page/mate/internal/transcode"
)

// decodeJob parses a JSON TranscodeJob the same way the wire does (UseNumber), so the test
// exercises the json.Number coercion path in mapTranscodeJob.
func decodeJob(t *testing.T, s string) map[string]any {
	t.Helper()
	m, err := parseMapNum([]byte(s))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func TestMapTranscodeJobVideo(t *testing.T) {
	job := decodeJob(t, `{
		"inputPath": "C:/in.mov", "outputPath": "C:/out.mp4", "container": "mp4",
		"video": {"enabled": true, "codec": "h264", "encoder": "h264_nvenc", "hwaccel": "nvenc",
		          "crf": 21, "width": 1920, "height": 1080, "fps": 30, "keyintSeconds": 2,
		          "tune": "none", "speedPreset": "p5"},
		"audio": {"enabled": true, "codec": "aac", "bitrateK": 160, "channels": 2, "sampleRate": 48000,
		          "loudnessProfile": "streaming"},
		"deinterlace": true,
		"trim": {"mode": "fast", "start": 5, "end": 65}
	}`)
	out, err := mapTranscodeJob(job)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	if out["input"] != "C:/in.mov" || out["output"] != "C:/out.mp4" {
		t.Fatalf("input/output wrong: %v", out)
	}
	if out["trimStart"] != float64(5) || out["trimEnd"] != float64(65) {
		t.Fatalf("trim wrong: %v %v", out["trimStart"], out["trimEnd"])
	}
	p := out["preset"].(transcode.Preset)
	if p.VideoCodec != "h264" || p.EncoderOverride != "h264_nvenc" || p.Accel != "nvenc" {
		t.Fatalf("video codec/encoder/accel: %+v", p)
	}
	if p.CRF != 21 || p.Width != 1920 || p.Height != 1080 || p.FPS != 30 || p.GOPSeconds != 2 {
		t.Fatalf("video numeric fields: %+v", p)
	}
	if p.Tune != "" { // "none" must drop
		t.Fatalf("tune should be empty, got %q", p.Tune)
	}
	if p.SpeedPreset != "p5" || !p.Deinterlace {
		t.Fatalf("speedPreset/deinterlace: %+v", p)
	}
	if p.AudioCodec != "aac" || p.AudioBitrateK != 160 || p.Channels != 2 || p.SampleRate != 48000 {
		t.Fatalf("audio fields: %+v", p)
	}
	if p.Loudness != "music-stream" {
		t.Fatalf("loudness map: %q", p.Loudness)
	}

	// The produced params must marshal to the worker tcRunIn JSON shape.
	raw, _ := json.Marshal(out)
	var probe struct {
		Input  string `json:"input"`
		Output string `json:"output"`
		Preset struct {
			VideoCodec      string `json:"videoCodec"`
			EncoderOverride string `json:"encoderOverride"`
		} `json:"preset"`
		TrimStart float64 `json:"trimStart"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if probe.Preset.EncoderOverride != "h264_nvenc" || probe.Input != "C:/in.mov" || probe.TrimStart != 5 {
		t.Fatalf("worker shape mismatch: %+v", probe)
	}
}

func TestMapTranscodeJobDisabledStreamsAndPassthrough(t *testing.T) {
	job := decodeJob(t, `{
		"inputPath": "i.mkv", "outputPath": "o.opus", "container": "opus",
		"video": {"enabled": false},
		"audio": {"enabled": true, "passthrough": true},
		"trim": {"mode": "none"}
	}`)
	out, err := mapTranscodeJob(job)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	p := out["preset"].(transcode.Preset)
	if p.VideoCodec != "none" {
		t.Fatalf("disabled video should be none, got %q", p.VideoCodec)
	}
	if p.AudioCodec != "copy" {
		t.Fatalf("passthrough audio should be copy, got %q", p.AudioCodec)
	}
	if _, ok := out["trimStart"]; ok {
		t.Fatalf("trim mode none must not set trimStart")
	}
}

func TestMapTranscodeJobForceBitrate(t *testing.T) {
	job := decodeJob(t, `{
		"inputPath": "i.mp4", "outputPath": "o.mp4",
		"video": {"enabled": true, "codec": "h264", "forceBitrate": true, "bitrateK": 8000},
		"audio": {"enabled": false}
	}`)
	out, _ := mapTranscodeJob(job)
	p := out["preset"].(transcode.Preset)
	if p.RateMode != "bitrate" || p.BitrateK != 8000 {
		t.Fatalf("force bitrate: %+v", p)
	}
	if p.AudioCodec != "none" {
		t.Fatalf("disabled audio should be none, got %q", p.AudioCodec)
	}
}

func TestMapTranscodeJobMissingPaths(t *testing.T) {
	if _, err := mapTranscodeJob(map[string]any{}); err == nil {
		t.Fatal("expected error on missing input/output")
	}
}

func TestBuildCatalog(t *testing.T) {
	// Mixed build: NVENC h264 works, QSV h264 is in-build but non-functional, libx264 (sw)
	// works. recommended[h264] must prefer the WORKING hardware encoder.
	encs := []detectedEnc{
		{Name: "libx264", Codec: "h264", Kind: "sw", Working: true},
		{Name: "h264_nvenc", Codec: "h264", Kind: "hw", Vendor: "nvidia", Working: true},
		{Name: "h264_qsv", Codec: "h264", Kind: "hw", Vendor: "intel", Working: false},
		{Name: "libx265", Codec: "hevc", Kind: "sw", Working: true},
		{Name: "aac", Codec: "aac", Kind: "sw", Audio: true, Working: true},
		{Name: "libopus", Codec: "opus", Kind: "sw", Audio: true, Working: true},
	}
	cat := buildCatalog(encs)

	video := cat["video"].([]map[string]any)
	if len(video) != 4 {
		t.Fatalf("video count = %d", len(video))
	}
	// h264_nvenc → family h264, kind hardware, vendor nvidia, available true.
	var nvenc map[string]any
	for _, v := range video {
		if v["name"] == "h264_nvenc" {
			nvenc = v
		}
	}
	if nvenc["family"] != "h264" || nvenc["kind"] != "hardware" || nvenc["vendor"] != "nvidia" || nvenc["available"] != true {
		t.Fatalf("nvenc mapping wrong: %+v", nvenc)
	}

	rec := cat["recommended"].(map[string]any)
	if rec["h264"] != "h264_nvenc" { // working HW beats working SW
		t.Fatalf("recommended h264 = %v, want h264_nvenc", rec["h264"])
	}
	if rec["h265"] != "libx265" { // only SW works for hevc
		t.Fatalf("recommended h265 = %v, want libx265", rec["h265"])
	}

	audio := cat["audio"].([]map[string]any)
	if len(audio) != 2 {
		t.Fatalf("audio count = %d", len(audio))
	}

	// libsvtav1 absent → no recommended av1.
	if _, ok := rec["av1"]; ok {
		t.Fatalf("av1 should be unrecommended, got %v", rec["av1"])
	}
}

func TestBuildCatalogNonWorkingHWNotRecommended(t *testing.T) {
	// Only a non-functional HW encoder for h264 → not recommended at all (Electron's bug
	// was recommending it because it parsed as "available").
	cat := buildCatalog([]detectedEnc{
		{Name: "h264_qsv", Codec: "h264", Kind: "hw", Vendor: "intel", Working: false},
	})
	rec := cat["recommended"].(map[string]any)
	if _, ok := rec["h264"]; ok {
		t.Fatalf("non-working HW must not be recommended, got %v", rec["h264"])
	}
}
