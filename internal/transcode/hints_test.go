package transcode

import "testing"

func TestRecommendVideoBitrateK(t *testing.T) {
	// 1080p H.264 youtube-hq → ~8000k base.
	if got := RecommendVideoBitrateK("youtube-hq", "h264", 1080, 30, 0); got != 8000 {
		t.Errorf("1080p h264 yt-hq = %d, want 8000", got)
	}
	// H.265 is 0.6× H.264.
	if got := RecommendVideoBitrateK("youtube-hq", "h265", 1080, 30, 0); got != 4800 {
		t.Errorf("1080p h265 yt-hq = %d, want 4800", got)
	}
	// >30fps adds 1.5×.
	if got := RecommendVideoBitrateK("youtube-hq", "h264", 1080, 60, 0); got != 12000 {
		t.Errorf("1080p60 h264 = %d, want 12000", got)
	}
	// streaming is 0.8×.
	if got := RecommendVideoBitrateK("streaming", "h264", 1080, 30, 0); got != 6400 {
		t.Errorf("streaming = %d, want 6400", got)
	}
	// Source cap: never exceed src*1.05 (except master).
	if got := RecommendVideoBitrateK("youtube-hq", "h264", 1080, 30, 3000); got != 3150 {
		t.Errorf("capped = %d, want 3150", got)
	}
	// match-source returns the source bitrate verbatim.
	if got := RecommendVideoBitrateK("match-source", "h264", 1080, 30, 5500); got != 5500 {
		t.Errorf("match-source = %d, want 5500", got)
	}
	// copy → 0.
	if got := RecommendVideoBitrateK("youtube-hq", "copy", 1080, 30, 5000); got != 0 {
		t.Errorf("copy = %d, want 0", got)
	}
}

func TestRecommendAudioBitrateK(t *testing.T) {
	if got := RecommendAudioBitrateK("aac", 0); got != 192 {
		t.Errorf("aac default = %d, want 192", got)
	}
	if got := RecommendAudioBitrateK("opus", 0); got != 256 {
		t.Errorf("opus default = %d, want 256", got)
	}
	if got := RecommendAudioBitrateK("vorbis", 0); got != 256 {
		t.Errorf("vorbis default = %d, want 256", got)
	}
	// Closest ladder entry to source, capped at codec max.
	if got := RecommendAudioBitrateK("aac", 200); got != 192 {
		t.Errorf("aac src200 = %d, want 192", got)
	}
	if got := RecommendAudioBitrateK("aac", 600); got != 320 {
		t.Errorf("aac src600 capped = %d, want 320", got)
	}
	if got := RecommendAudioBitrateK("vorbis", 600); got != 500 {
		t.Errorf("vorbis src600 capped = %d, want 500", got)
	}
	if got := RecommendAudioBitrateK("flac", 1000); got != 0 {
		t.Errorf("flac (lossless) = %d, want 0", got)
	}
}

func TestCompareQuality(t *testing.T) {
	src := SourceInfo{HasVideo: true, VideoCodec: "h264", Width: 1280, Height: 720, VideoKbps: 4000,
		HasAudio: true, AudioCodec: "aac", AudioKbps: 192, SampleRate: 44100}

	// Upscale 720p → 1080p warns.
	up := CompareQuality(Preset{VideoCodec: "h265", Width: 1920, Height: 1080, AudioCodec: "aac"}, src)
	if !hasSeverity(up, "warn") {
		t.Errorf("upscale should warn: %+v", up)
	}
	// Same codec → info remux suggestion.
	same := CompareQuality(Preset{VideoCodec: "h264", AudioCodec: "aac"}, src)
	if !hasSeverity(same, "info") {
		t.Errorf("same codec should info: %+v", same)
	}
	// Bitrate up warns.
	br := CompareQuality(Preset{VideoCodec: "h265", RateMode: "bitrate", BitrateK: 9000, AudioCodec: "aac"}, src)
	if !hasSeverity(br, "warn") {
		t.Errorf("bitrate-up should warn: %+v", br)
	}
	// Upsample audio warns.
	au := CompareQuality(Preset{VideoCodec: "none", AudioCodec: "aac", SampleRate: 48000}, src)
	if !hasSeverity(au, "warn") {
		t.Errorf("upsample should warn: %+v", au)
	}
}

func TestParseProbe(t *testing.T) {
	raw := []byte(`{
		"streams":[
			{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"bit_rate":"8000000","r_frame_rate":"30000/1001"},
			{"codec_type":"audio","codec_name":"aac","bit_rate":"256000","sample_rate":"48000","channels":2}
		],
		"format":{"bit_rate":"8300000","duration":"212.5"}
	}`)
	si, ok := ParseProbe(raw)
	if !ok || !si.HasVideo || !si.HasAudio {
		t.Fatalf("parse failed: %+v ok=%v", si, ok)
	}
	if si.VideoCodec != "h264" || si.Width != 1920 || si.Height != 1080 || si.VideoKbps != 8000 {
		t.Errorf("video: %+v", si)
	}
	if si.FPS < 29.9 || si.FPS > 30.0 {
		t.Errorf("fps = %v, want ~29.97", si.FPS)
	}
	if si.AudioCodec != "aac" || si.AudioKbps != 256 || si.SampleRate != 48000 || si.Channels != 2 {
		t.Errorf("audio: %+v", si)
	}
}

func TestParseProbeVideoBitrateFallback(t *testing.T) {
	// No per-stream video bit_rate → derive from container minus audio.
	raw := []byte(`{
		"streams":[
			{"codec_type":"video","codec_name":"hevc","width":3840,"height":2160,"r_frame_rate":"24/1"},
			{"codec_type":"audio","codec_name":"aac","bit_rate":"192000"}
		],
		"format":{"bit_rate":"20192000"}
	}`)
	si, ok := ParseProbe(raw)
	if !ok || si.VideoKbps != 20000 {
		t.Errorf("fallback video kbps = %d, want 20000 (%+v)", si.VideoKbps, si)
	}
}

func TestApplyProfileSrc(t *testing.T) {
	src := &SourceInfo{HasVideo: true, Width: 1920, Height: 1080, FPS: 30, VideoKbps: 5000}
	p := Preset{VideoCodec: "h264"}
	ApplyProfileSrc(&p, "youtube-hq", src)
	// Capped at source*1.05 = 5250.
	if p.RateMode != "bitrate" || p.BitrateK != 5250 {
		t.Errorf("yt-hq src-capped: %+v", p)
	}
	// master ignores source, sets CRF.
	p2 := Preset{VideoCodec: "h264"}
	ApplyProfileSrc(&p2, "master", src)
	if p2.RateMode != "crf" || p2.CRF != 16 {
		t.Errorf("master: %+v", p2)
	}
	// nil source falls back to resolution-only ApplyProfile.
	p3 := Preset{VideoCodec: "h264", Height: 1080}
	ApplyProfileSrc(&p3, "streaming", nil)
	if p3.RateMode != "bitrate" || p3.BitrateK <= 0 {
		t.Errorf("nil-src fallback: %+v", p3)
	}
}

func hasSeverity(ws []Warning, sev string) bool {
	for _, w := range ws {
		if w.Severity == sev {
			return true
		}
	}
	return false
}
