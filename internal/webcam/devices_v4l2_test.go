package webcam

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Captured from a Logitech C920 on Arch (v4l-utils), trimmed.
const v4l2CtlC920 = `ioctl: VIDIOC_ENUM_FMT
	Type: Video Capture

	[0]: 'YUYV' (YUYV 4:2:2)
		Size: Discrete 640x480
			Interval: Discrete 0.033s (30.000 fps)
			Interval: Discrete 0.042s (24.000 fps)
		Size: Discrete 1280x720
			Interval: Discrete 0.100s (10.000 fps)
			Interval: Discrete 0.200s (5.000 fps)
		Size: Discrete 1920x1080
			Interval: Discrete 0.200s (5.000 fps)
	[1]: 'H264' (H.264, compressed)
		Size: Discrete 1920x1080
			Interval: Discrete 0.033s (30.000 fps)
	[2]: 'MJPG' (Motion-JPEG, compressed)
		Size: Discrete 1280x720
			Interval: Discrete 0.033s (30.000 fps)
			Interval: Discrete 0.042s (24.000 fps)
		Size: Discrete 1920x1080
			Interval: Discrete 0.033s (30.000 fps)
	[3]: 'ZZZZ' (unknown)
		Size: Discrete 4000x3000
			Interval: Discrete 0.033s (30.000 fps)
`

func TestParseV4L2CtlFormats(t *testing.T) {
	got := parseV4L2CtlFormats(v4l2CtlC920)
	want := []Mode{
		{W: 1920, H: 1080, FPS: 30, InputFormat: "mjpeg"}, // mjpeg beats h264@30 and yuyv@5
		{W: 1280, H: 720, FPS: 30, InputFormat: "mjpeg"},
		{W: 640, H: 480, FPS: 30, InputFormat: "yuyv422"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("modes = %+v, want %+v", got, want)
	}
	if m := parseV4L2CtlFormats("ioctl: VIDIOC_ENUM_FMT\n\tType: Video Capture\n\n"); len(m) != 0 {
		t.Fatalf("metadata node yielded modes: %+v", m)
	}
}

func TestParseV4L2FFmpegFormats(t *testing.T) {
	in := "[in#0 @ 0x55de] Raw       :     yuyv422 :           YUYV 4:2:2 : 640x480 1280x720 2304x1536\n" +
		"[in#0 @ 0x55de] Compressed:       mjpeg :          Motion-JPEG : 640x480 1280x720\r\n" +
		"Error opening input file /dev/video2.\n"
	got := parseV4L2FFmpegFormats(in)
	want := []Mode{
		{W: 2304, H: 1536, FPS: defaultFPS, InputFormat: "yuyv422"},
		{W: 1280, H: 720, FPS: defaultFPS, InputFormat: "mjpeg"},
		{W: 640, H: 480, FPS: defaultFPS, InputFormat: "mjpeg"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("modes = %+v, want %+v", got, want)
	}
}

func TestV4L2Nodes(t *testing.T) {
	dir := t.TempDir()
	mk := func(node, index, name string) {
		p := filepath.Join(dir, node)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, v := range map[string]string{"index": index, "name": name} {
			if err := os.WriteFile(filepath.Join(p, f), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("video10", "0", "HD Pro Webcam C920") // second C920, sorts numerically after video2
	mk("video2", "0", "HD Pro Webcam C920")
	mk("video3", "1", "HD Pro Webcam C920") // metadata node
	mk("video0", "0", "3D Camera: eTronVideo")
	mk("v4l-subdev0", "0", "x")
	got := v4l2Nodes(dir)
	want := []v4l2Node{
		{Name: "3D Camera: eTronVideo", Path: "/dev/video0"},
		{Name: "HD Pro Webcam C920", Path: "/dev/video2"},
		{Name: "HD Pro Webcam C920 #2", Path: "/dev/video10"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nodes = %+v, want %+v", got, want)
	}
}

func TestCaptureArgsLinux(t *testing.T) {
	d := capDesc{Device: "HD Pro Webcam C920", W: 1280, H: 720, FPS: 30, InputFormat: "mjpeg"}
	got := strings.Join(captureArgsFor("linux", d, "/dev/video2"), " ")
	want := "-hide_banner -loglevel error -fflags nobuffer -f v4l2 -video_size 1280x720 -framerate 30 " +
		"-input_format mjpeg -i /dev/video2 -an -pix_fmt rgba -fps_mode passthrough -f rawvideo -"
	if got != want {
		t.Fatalf("argv = %q\nwant  %q", got, want)
	}
}
