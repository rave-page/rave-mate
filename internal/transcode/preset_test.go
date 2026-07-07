package transcode

import (
	"strings"
	"testing"
)

func args(p Preset) string {
	return strings.Join(Job{Input: "in.x", Output: "out" + p.Ext(), Preset: p}.Args(), " ")
}

func TestArgsRemux(t *testing.T) {
	p, _ := Find("remux")
	j := Job{Input: "in.mkv", Output: "out.mp4", Preset: p}
	got := strings.Join(j.Args(), " ")
	for _, want := range []string{"-i in.mkv", "-c:v copy", "-c:a copy", "-movflags +faststart", "out.mp4"} {
		if !strings.Contains(got, want) {
			t.Errorf("remux args missing %q in: %s", want, got)
		}
	}
}

func TestArgsWeb1080WithTrim(t *testing.T) {
	p, _ := Find("web1080")
	j := Job{Input: "in.mov", Output: "out.mp4", Preset: p, TrimStart: 5, TrimEnd: 65}
	got := strings.Join(j.Args(), " ")
	for _, want := range []string{"-ss 5.000", "-t 60.000", "-c:v libx264", "-crf 21", "-pix_fmt yuv420p", "scale=-2:'min(1080,ih)'", "-c:a aac", "-b:a 160k"} {
		if !strings.Contains(got, want) {
			t.Errorf("web1080 args missing %q in: %s", want, got)
		}
	}
}

func TestArgsAudioOnly(t *testing.T) {
	p, _ := Find("audioOpus")
	got := args(p)
	if !strings.Contains(got, "-vn") || !strings.Contains(got, "-c:a libopus") || !strings.Contains(got, "-b:a 160k") {
		t.Errorf("audioOpus args wrong: %s", got)
	}
	if p.Ext() != ".opus" || !p.IsAudioOnly() {
		t.Errorf("ext/audioOnly: %q %v", p.Ext(), p.IsAudioOnly())
	}
}

func TestFindUnknown(t *testing.T) {
	if _, ok := Find("nope"); ok {
		t.Error("unknown preset should not resolve")
	}
}

func TestArgsMP3(t *testing.T) {
	cbr, _ := Find("mp3-320")
	if g := args(cbr); !strings.Contains(g, "-c:a libmp3lame") || !strings.Contains(g, "-b:a 320k") || cbr.Ext() != ".mp3" {
		t.Errorf("mp3-320: %s", g)
	}
	vbr, _ := Find("mp3-v0")
	if g := args(vbr); !strings.Contains(g, "-q:a 0") || strings.Contains(g, "-b:a") {
		t.Errorf("mp3-v0 should be VBR: %s", g)
	}
}

func TestArgsOggVorbis(t *testing.T) {
	p, _ := Find("ogg-vorbis")
	g := args(p)
	if !strings.Contains(g, "-vn") || !strings.Contains(g, "-c:a libvorbis") || !strings.Contains(g, "-b:a 320k") || p.Ext() != ".ogg" {
		t.Errorf("ogg-vorbis: %s ext=%s", g, p.Ext())
	}
}

func TestArgsLossless(t *testing.T) {
	fl, _ := Find("flac")
	if g := args(fl); !strings.Contains(g, "-c:a flac") || !strings.Contains(g, "-compression_level 8") {
		t.Errorf("flac: %s", g)
	}
	af, _ := Find("aiff")
	if g := args(af); !strings.Contains(g, "-c:a pcm_s16be") {
		t.Errorf("aiff: %s", g)
	}
	wv, _ := Find("wav")
	if g := args(wv); !strings.Contains(g, "-c:a pcm_s16le") {
		t.Errorf("wav: %s", g)
	}
}

func TestNormalizePresetCompatibility(t *testing.T) {
	p := NormalizePreset(Preset{Container: "ogg", VideoCodec: "h264", AudioCodec: "aac", AudioBitrateK: 999})
	if p.VideoCodec != "none" || p.AudioCodec != "vorbis" || p.AudioBitrateK != 500 {
		t.Errorf("ogg normalization: %+v", p)
	}

	p = NormalizePreset(Preset{Container: "mp4", VideoCodec: "vp9", AudioCodec: "opus", AudioBitrateK: 999})
	if p.VideoCodec != "h264" || p.AudioCodec != "aac" || p.AudioBitrateK != 320 {
		t.Errorf("mp4 normalization: %+v", p)
	}

	p = NormalizePreset(Preset{Container: "flac", VideoCodec: "h264", AudioCodec: "mp3", AudioBitrateK: 320, AudioVBR: true})
	if p.VideoCodec != "none" || p.AudioCodec != "flac" || p.AudioBitrateK != 0 || p.AudioVBR {
		t.Errorf("flac normalization: %+v", p)
	}
}

func TestContainerCodecOptions(t *testing.T) {
	if got := VideoCodecsForContainer("webm", true); !containsTest(got, "vp9") || containsTest(got, "h264") {
		t.Errorf("webm video options: %+v", got)
	}
	if got := AudioCodecsForContainer("ogg"); !containsTest(got, "vorbis") || containsTest(got, "aac") {
		t.Errorf("ogg audio options: %+v", got)
	}
}

func TestArgsVP9(t *testing.T) {
	p, _ := Find("webmVp9")
	g := args(p)
	for _, want := range []string{"-c:v libvpx-vp9", "-crf 31", "-b:v 0", "-c:a libopus"} {
		if !strings.Contains(g, want) {
			t.Errorf("vp9 missing %q: %s", want, g)
		}
	}
	if p.Ext() != ".webm" {
		t.Errorf("ext = %q", p.Ext())
	}
}

func TestArgsBitrateMode(t *testing.T) {
	p := Preset{Container: "mp4", VideoCodec: "h264", RateMode: "bitrate", BitrateK: 12000, AudioCodec: "aac", AudioBitrateK: 192}
	g := args(p)
	if !strings.Contains(g, "-b:v 12000k") || strings.Contains(g, "-crf") {
		t.Errorf("bitrate mode: %s", g)
	}
}

func TestArgsGainDeinterlaceFps(t *testing.T) {
	p := Preset{Container: "mp4", VideoCodec: "h264", CRF: 20, Deinterlace: true, FPS: 60, Width: 1920, Height: 1080,
		AudioCodec: "aac", AudioBitrateK: 160, SampleRate: 48000, Channels: 2}
	gain := -4.25
	g := strings.Join(Job{Input: "in.x", Output: "out.mp4", Preset: p, GainDB: &gain}.Args(), " ")
	for _, want := range []string{"bwdif", "scale=1920:1080", "-r 60.000", "volume=-4.25dB", "-ar 48000", "-ac 2"} {
		if !strings.Contains(g, want) {
			t.Errorf("missing %q: %s", want, g)
		}
	}
	if strings.Contains(g, "loudnorm") {
		t.Errorf("dynamic loudnorm must never be emitted: %s", g)
	}
	// No plan → no audio filter at all.
	if g := args(p); strings.Contains(g, "-af") {
		t.Errorf("no gain → no -af: %s", g)
	}
}

func TestArgsEncoderOverride(t *testing.T) {
	p := Preset{Container: "mp4", VideoCodec: "h264", EncoderOverride: "h264_nvenc", CRF: 23, AudioCodec: "aac", AudioBitrateK: 160}
	g := args(p)
	for _, want := range []string{"-c:v h264_nvenc", "-cq 23", "-preset p5"} {
		if !strings.Contains(g, want) {
			t.Errorf("nvenc override missing %q: %s", want, g)
		}
	}
	if strings.Contains(g, "-sc_threshold") {
		t.Errorf("HW encoder must not get -sc_threshold: %s", g)
	}
}

func TestAllPresetsOverride(t *testing.T) {
	custom := []Preset{{ID: "web1080", Label: "My 1080"}, {ID: "mine", Label: "Mine"}}
	all := AllPresets(custom)
	var got1080, gotMine, count int
	for _, p := range all {
		switch p.ID {
		case "web1080":
			got1080++
			if p.Label != "My 1080" {
				t.Errorf("override not applied: %q", p.Label)
			}
		case "mine":
			gotMine++
		}
		count++
	}
	if got1080 != 1 || gotMine != 1 {
		t.Errorf("dup/missing: web1080=%d mine=%d", got1080, gotMine)
	}
	if count != len(Builtins)+1 { // +1 custom (web1080 replaced, mine added)
		t.Errorf("count = %d, want %d", count, len(Builtins)+1)
	}
}

func TestResolveEncoder(t *testing.T) {
	working := map[string]bool{"h264_nvenc": true, "libx264": true, "h264_qsv": false}
	if enc, ok := ResolveEncoder("h264", "auto", working); !ok || enc != "h264_nvenc" {
		t.Errorf("auto should pick nvenc: %q %v", enc, ok)
	}
	if enc, ok := ResolveEncoder("h264", "qsv", working); !ok || enc != "libx264" {
		t.Errorf("qsv not working → software fallback: %q %v", enc, ok)
	}
	if enc, ok := ResolveEncoder("h264", "software", working); !ok || enc != "libx264" {
		t.Errorf("software: %q %v", enc, ok)
	}
	if _, ok := ResolveEncoder("copy", "auto", working); ok {
		t.Error("copy should not resolve an encoder")
	}
	// No detection → software only.
	if enc, _ := ResolveEncoder("h264", "auto", nil); enc != "libx264" {
		t.Errorf("nil working → software: %q", enc)
	}
}

func TestApplyProfile(t *testing.T) {
	p := Preset{VideoCodec: "h264", Height: 1080}
	ApplyProfile(&p, "streaming")
	if p.RateMode != "bitrate" || p.BitrateK <= 0 {
		t.Errorf("streaming: %+v", p)
	}
	p2 := Preset{VideoCodec: "h264"}
	ApplyProfile(&p2, "master")
	if p2.RateMode != "crf" || p2.CRF != 16 {
		t.Errorf("master: %+v", p2)
	}
}

func containsTest(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
