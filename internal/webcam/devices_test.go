package webcam

import "testing"

// Modern ffmpeg (≥4.3): per-device "(video)"/"(audio)" tags, no section headers.
const fixtureTagged = `[dshow @ 0000023e5e2bb800] "OBS Virtual Camera" (video)
[dshow @ 0000023e5e2bb800]   Alternative name "@device_sw_{860BB310-5D01-11D0-BD3B-00A0C911CE86}\{A3FCE0F5-3493-419F-958A-ABA1250EC20B}"
[dshow @ 0000023e5e2bb800] "Logitech BRIO" (video)
[dshow @ 0000023e5e2bb800]   Alternative name "@device_pnp_\\?\usb#vid_046d&pid_085e&mi_00#7&2df6&0&0000#{65e8773d-8f56-11d0-a3b9-00a0c9223196}\global"
[dshow @ 0000023e5e2bb800] "Microphone (Yeti Stereo Microphone)" (audio)
[dshow @ 0000023e5e2bb800] "Elgato Wave:3" (audio, video)
dummy: Immediate exit requested
`

// Legacy ffmpeg: sectioned output, bare quoted names.
const fixtureSectioned = `[dshow @ 000001f2] DirectShow video devices (some may be both video and audio devices)
[dshow @ 000001f2]  "Logitech BRIO"
[dshow @ 000001f2]  "OBS-Camera"
[dshow @ 000001f2] DirectShow audio devices
[dshow @ 000001f2]  "Microphone (Realtek Audio)"
`

func TestParseDshowVideoDevicesTagged(t *testing.T) {
	got := parseDshowVideoDevices(fixtureTagged)
	want := []string{"OBS Virtual Camera", "Logitech BRIO", "Elgato Wave:3"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestParseDshowVideoDevicesSectioned(t *testing.T) {
	got := parseDshowVideoDevices(fixtureSectioned)
	want := []string{"Logitech BRIO", "OBS-Camera"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestParseDshowVideoDevicesDedup(t *testing.T) {
	in := "\"Cam\" (video)\n\"Cam\" (video)\n"
	if got := parseDshowVideoDevices(in); len(got) != 1 || got[0] != "Cam" {
		t.Fatalf("dedup failed: %v", got)
	}
}

const fixtureOptions = `[dshow @ 0000023e] DirectShow video device options (from video devices)
[dshow @ 0000023e]  Pin "Capture" (alternative pin name "0")
[dshow @ 0000023e]   vcodec=mjpeg  min s=640x480 fps=5 max s=640x480 fps=30
[dshow @ 0000023e]   vcodec=mjpeg  min s=1920x1080 fps=5 max s=1920x1080 fps=30
[dshow @ 0000023e]   pixel_format=yuyv422  min s=640x480 fps=5 max s=640x480 fps=30
[dshow @ 0000023e]   pixel_format=yuyv422  min s=1920x1080 fps=5 max s=1920x1080 fps=5
[dshow @ 0000023e]   pixel_format=nv12  min s=1280x720 fps=10 max s=1280x720 fps=60.0002
video=Logitech BRIO: Immediate exit requested
`

func TestParseDshowOptions(t *testing.T) {
	got := parseDshowOptions(fixtureOptions)
	// Deduped by size (max fps wins), sorted largest area first.
	want := []Mode{{1920, 1080, 30}, {1280, 720, 60.0002}, {640, 480, 30}}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mode %d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestParseDshowOptionsEmpty(t *testing.T) {
	if got := parseDshowOptions("could not open device\n"); len(got) != 0 {
		t.Fatalf("expected none, got %v", got)
	}
}
