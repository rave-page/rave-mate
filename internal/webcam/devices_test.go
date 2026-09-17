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
	// Deduped by size (MJPEG preferred over raw at any fps, else fastest), sorted largest area
	// first, each carrying the -input_format that actually reaches its fps. 1920x1080 = mjpeg@30
	// (NOT the raw yuyv422@5 on the same size); the raw-only nv12 720p keeps its raw token.
	want := []Mode{{1920, 1080, 30, "mjpeg"}, {1280, 720, 60.0002, "nv12"}, {640, 480, 30, "mjpeg"}}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mode %d: got %v want %v", i, got[i], want[i])
		}
	}
}

// A size offering BOTH raw and MJPEG: MJPEG wins even when raw advertises a higher (bus-
// unsustainable) fps - the C920/BRIO 720p case (nv12@60 vs mjpeg@30 over USB-2).
const fixtureOptions720Both = `[dshow @ x]   pixel_format=nv12  min s=1280x720 fps=10 max s=1280x720 fps=60
[dshow @ x]   vcodec=mjpeg  min s=1280x720 fps=5 max s=1280x720 fps=30
`

func TestParseDshowOptionsPrefersMJPEGOverFasterRaw(t *testing.T) {
	got := parseDshowOptions(fixtureOptions720Both)
	want := []Mode{{1280, 720, 30, "mjpeg"}} // mjpeg@30 beats nv12@60
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %v want %v", got, want)
	}
}

// Older ffmpeg omits the vcodec=/pixel_format= token: modes still parse, InputFormat stays empty
// (ffmpeg negotiates the device default - the pre-fix behaviour).
const fixtureOptionsNoCodec = `[dshow @ 0000023e]  Pin "Capture"
[dshow @ 0000023e]   min s=1280x720 fps=5 max s=1280x720 fps=30
[dshow @ 0000023e]   min s=640x480 fps=5 max s=640x480 fps=30
`

func TestParseDshowOptionsNoCodec(t *testing.T) {
	got := parseDshowOptions(fixtureOptionsNoCodec)
	want := []Mode{{1280, 720, 30, ""}, {640, 480, 30, ""}}
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
