package transcode

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecodeRawArgs(t *testing.T) {
	j := Job{Input: "in.mp4", TrimStart: 3, TrimEnd: 10, VF: "crop=606:1080:200:0"}
	got := j.DecodeRawArgs(1080, 1920, 30)
	want := []string{"-hide_banner", "-nostats", "-y", "-ss", "3.000", "-i", "in.mp4",
		"-t", "7.000", "-vf", "crop=606:1080:200:0,scale=1080:1920", "-r", "30.000",
		"-an", "-f", "rawvideo", "-pix_fmt", "rgba", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DecodeRawArgs\n got %q\nwant %q", got, want)
	}

	// no trim, no crop
	j2 := Job{Input: "a.mkv"}
	got2 := j2.DecodeRawArgs(640, 360, 25)
	want2 := []string{"-hide_banner", "-nostats", "-y", "-i", "a.mkv",
		"-vf", "scale=640:360", "-r", "25.000", "-an", "-f", "rawvideo", "-pix_fmt", "rgba", "-"}
	if !reflect.DeepEqual(got2, want2) {
		t.Errorf("DecodeRawArgs plain\n got %q\nwant %q", got2, want2)
	}
}

func TestEncodeRawArgs(t *testing.T) {
	p := Preset{Container: "mp4", VideoCodec: "h264", AudioCodec: "aac",
		AudioBitrateK: 192, CRF: 18, SpeedPreset: "medium", GOPSeconds: 2,
		Width: 1080, Height: 1920} // geometry must be suppressed encode-side
	j := Job{Input: "in.mp4", Output: "out.mp4", Preset: p, TrimStart: 3, TrimEnd: 10,
		VF: "crop=606:1080:200:0"}
	got := j.EncodeRawArgs(1080, 1920, 30)

	head := []string{"-hide_banner", "-nostats", "-y", "-f", "rawvideo", "-pix_fmt", "rgba",
		"-video_size", "1080x1920", "-framerate", "30.000", "-i", "pipe:0",
		"-ss", "3.000", "-i", "in.mp4", "-t", "7.000", "-map", "0:v:0", "-map", "1:a:0?"}
	if !reflect.DeepEqual(got[:len(head)], head) {
		t.Errorf("head\n got %q\nwant %q", got[:len(head)], head)
	}
	if got[len(got)-1] != "out.mp4" {
		t.Errorf("last arg = %q, want out.mp4", got[len(got)-1])
	}
	joined := "\x00" + strings.Join(got, "\x00") + "\x00"
	for _, frag := range []string{"-c:v\x00libx264", "-c:a\x00aac", "-b:a\x00192k",
		"-movflags\x00+faststart", "-crf\x0018"} {
		if !strings.Contains(joined, frag) {
			t.Errorf("missing %q in %q", frag, got)
		}
	}
	for _, banned := range []string{"-vf", "scale="} {
		if strings.Contains(joined, banned) {
			t.Errorf("encode side must not carry %q: %q", banned, got)
		}
	}
}
