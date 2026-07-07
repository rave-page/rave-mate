package audiorec

import (
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/session/sinks/recorder"
)

func TestCaptureArgs(t *testing.T) {
	const dev = "Line (Focusrite USB)"
	cases := []struct {
		name       string
		format     string
		bitrate    int
		sampleRate int
		wantCodec  []string // sequence that must appear in order
		wantExt    string
		wantAr     bool
	}{
		{"flac no rate", "flac", 320, 0, []string{"-c:a", "flac", "-compression_level", "5"}, ".flac", false},
		{"wav", "wav", 0, 48000, []string{"-c:a", "pcm_s24le"}, ".wav", true},
		{"mp3", "mp3", 320, 44100, []string{"-c:a", "libmp3lame", "-b:a", "320k"}, ".mp3", true},
		{"aac", "aac", 256, 0, []string{"-c:a", "aac", "-b:a", "256k"}, ".m4a", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := "/tmp/x" + c.wantExt
			args := captureArgs(dev, out, c.format, c.bitrate, c.sampleRate)
			joined := strings.Join(args, " ")
			// device passed as single audio=<name> argv element
			if got := argAfter(args, "-i"); got != "audio="+dev {
				t.Fatalf("device arg = %q, want audio=%s", got, dev)
			}
			if !containsSeq(args, c.wantCodec) {
				t.Fatalf("codec seq %v not found in %v", c.wantCodec, args)
			}
			hasAr := argAfter(args, "-ar") != ""
			if hasAr != c.wantAr {
				t.Fatalf("-ar present=%v, want %v (args=%v)", hasAr, c.wantAr, args)
			}
			if c.wantAr && argAfter(args, "-ar") == "" {
				t.Fatalf("-ar value missing")
			}
			if !strings.HasSuffix(joined, "-y "+out) {
				t.Fatalf("output not last: %q", joined)
			}
			if got := extFor(c.format); got != c.wantExt {
				t.Fatalf("extFor(%s)=%s want %s", c.format, got, c.wantExt)
			}
		})
	}
}

func TestExtForDefault(t *testing.T) {
	if got := extFor("opus"); got != ".flac" {
		t.Fatalf("unknown format should default to .flac, got %s", got)
	}
}

func TestRecordingName(t *testing.T) {
	tm := time.Date(2026, 6, 27, 21, 5, 9, 0, time.UTC)
	if got := recordingName(tm, "flac"); got != "2026-06-27 21-05-09.flac" {
		t.Fatalf("recordingName = %q", got)
	}
	if got := recordingName(tm, "mp3"); got != "2026-06-27 21-05-09.mp3" {
		t.Fatalf("recordingName mp3 = %q", got)
	}
}

func TestCueSheet(t *testing.T) {
	start := time.Date(2026, 6, 27, 20, 0, 0, 0, time.UTC)
	tracks := []recorder.Track{
		{Title: `Intro "Live"`, Artist: "Stonx", StartedAt: start},
		{Title: "Second", Artist: "Various Artists", StartedAt: start.Add(3*time.Minute + 30*time.Second)},
		{Title: "Third", Artist: "DJ X", StartedAt: start.Add(65 * time.Minute)},
	}
	cue := cueSheet("My Set", "Stonx", "set.flac", start, tracks)

	if !strings.Contains(cue, `TITLE "My Set"`) {
		t.Fatalf("missing set title:\n%s", cue)
	}
	if !strings.Contains(cue, `FILE "set.flac" WAVE`) {
		t.Fatalf("missing FILE line:\n%s", cue)
	}
	// quotes in a track title are escaped (no raw embedded double-quote in the value)
	if !strings.Contains(cue, `TITLE "Intro 'Live'"`) {
		t.Fatalf("track quotes not escaped:\n%s", cue)
	}
	// INDEX offsets relative to capture start
	if !strings.Contains(cue, "INDEX 01 00:00:00") {
		t.Fatalf("first index wrong:\n%s", cue)
	}
	if !strings.Contains(cue, "INDEX 01 03:30:00") {
		t.Fatalf("second index wrong:\n%s", cue)
	}
	if !strings.Contains(cue, "INDEX 01 65:00:00") {
		t.Fatalf("third index (>59min) wrong:\n%s", cue)
	}
	if !strings.Contains(cue, "TRACK 01 AUDIO") || !strings.Contains(cue, "TRACK 03 AUDIO") {
		t.Fatalf("track numbering wrong:\n%s", cue)
	}
}

func TestCommentBody(t *testing.T) {
	start := time.Date(2026, 6, 27, 20, 0, 0, 0, time.UTC)
	tracks := []recorder.Track{
		{Title: "A", Artist: "Stonx", StartedAt: start},
		{Title: "B", Artist: "DJ X", StartedAt: start.Add(4*time.Minute + 5*time.Second)},
	}
	got := commentBody(start, tracks)
	want := "2 tracks\n00:00 Stonx - A\n04:05 DJ X - B"
	if got != want {
		t.Fatalf("commentBody:\n got=%q\nwant=%q", got, want)
	}
}

func TestArtistField(t *testing.T) {
	if got := artistField(nil); got != "Various" {
		t.Fatalf("empty => %q, want Various", got)
	}
	tracks := []recorder.Track{
		{Artist: "Stonx"}, {Artist: "Stonx"}, {Artist: "DJ X"}, {Artist: ""},
	}
	if got := artistField(tracks); got != "Stonx, DJ X" {
		t.Fatalf("distinct artists => %q", got)
	}
}

func TestParseDshowAudioDevices(t *testing.T) {
	stderr := `[dshow @ 0000] "Microphone (Realtek)" (audio)
[dshow @ 0000]   Alternative name "@device_cm_{...}"
[dshow @ 0000] "Line (Focusrite USB)" (audio)
[dshow @ 0000]   Alternative name "@device_cm_{...}"
[dshow @ 0000] "Some Camera" (video)
[dshow @ 0000] "Microphone (Realtek)" (audio)
`
	got := parseDshowAudioDevices(stderr)
	want := []string{"Microphone (Realtek)", "Line (Focusrite USB)"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

// argAfter returns the argv element following flag, or "".
func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// containsSeq reports whether seq appears contiguously in args.
func containsSeq(args, seq []string) bool {
	if len(seq) == 0 {
		return true
	}
	for i := 0; i+len(seq) <= len(args); i++ {
		ok := true
		for j := range seq {
			if args[i+j] != seq[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
