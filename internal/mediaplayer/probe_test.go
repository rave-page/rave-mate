package mediaplayer

import "testing"

func TestParseProbeVideo(t *testing.T) {
	j := `{
	  "streams": [
	    {"codec_type":"video","width":1920,"height":1080,"r_frame_rate":"30000/1001","avg_frame_rate":"30000/1001"},
	    {"codec_type":"audio","r_frame_rate":"0/0"}
	  ],
	  "format": {"duration":"3725.400000"}
	}`
	in, err := parseProbe([]byte(j))
	if err != nil {
		t.Fatal(err)
	}
	if !in.HasVideo || !in.HasAudio {
		t.Fatalf("want video+audio, got %+v", in)
	}
	if in.Width != 1920 || in.Height != 1080 {
		t.Fatalf("size = %dx%d, want 1920x1080", in.Width, in.Height)
	}
	if in.FPS < 29.9 || in.FPS > 30.0 {
		t.Fatalf("fps = %.3f, want ~29.97", in.FPS)
	}
	if in.Duration < 3725 || in.Duration > 3726 {
		t.Fatalf("duration = %.1f, want ~3725.4", in.Duration)
	}
}

// An audio file with embedded cover art exposes a 0/0-fps "video" stream - must NOT be treated
// as a video track (else the player would try to render an attached picture as video).
func TestParseProbeCoverArtNotVideo(t *testing.T) {
	j := `{
	  "streams": [
	    {"codec_type":"audio","r_frame_rate":"0/0"},
	    {"codec_type":"video","width":600,"height":600,"r_frame_rate":"0/0","avg_frame_rate":"0/0"}
	  ],
	  "format": {"duration":"210.5"}
	}`
	in, err := parseProbe([]byte(j))
	if err != nil {
		t.Fatal(err)
	}
	if in.HasVideo {
		t.Fatalf("cover-art stream treated as video: %+v", in)
	}
	if !in.HasAudio || in.Duration < 210 {
		t.Fatalf("audio/duration wrong: %+v", in)
	}
}

func TestRatio(t *testing.T) {
	cases := map[string]float64{"30000/1001": 29.97, "25/1": 25, "0/0": 0, "60": 60, "bad": 0}
	for in, want := range cases {
		got := ratio(in)
		if want == 0 {
			if got != 0 {
				t.Errorf("ratio(%q)=%v want 0", in, got)
			}
			continue
		}
		if got < want-0.1 || got > want+0.1 {
			t.Errorf("ratio(%q)=%v want ~%v", in, got, want)
		}
	}
}
