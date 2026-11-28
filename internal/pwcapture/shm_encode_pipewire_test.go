//go:build linux && pipewire

package pwcapture

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRouteSHMInEncodeMode locks the 2026-11-28 defect: a SHM producer (gst pipewiresink, MemFd/MemPtr)
// captured while the route runs the vaapi-native engine was routed to the RGBA readback slot, which the
// AU consumer never reads - 0 AUs, capFps 0, healthy PipeWire link. In encode mode an SHM buffer must go
// to the encoder (CPU upload), at 1080p and 4K; dmabuf routing is unchanged. Pure (no PipeWire/GPU).
func TestRouteSHMInEncodeMode(t *testing.T) {
	const sz4k = 3840 * 2160 * 4
	cases := []struct {
		name       string
		enc        bool
		kind       int
		data       bool
		off, size  uint32
		stride     int32
		maxsize    uint32
		w, h       int
		want       int
		wantStride uint32
	}{
		{"4K BGRx MemFd encode", true, bufMemFd, true, 0, sz4k, 15360, sz4k, 3840, 2160, routeEncSHM, 15360},
		{"4K MemPtr encode", true, bufMemPtr, true, 0, sz4k, 15360, sz4k, 3840, 2160, routeEncSHM, 15360},
		{"1080p MemFd encode, stride 0 = tight", true, bufMemFd, true, 0, 1920 * 1080 * 4, 0, 1920 * 1080 * 4, 1920, 1080, routeEncSHM, 7680},
		{"1080p MemFd readback", false, bufMemFd, true, 0, 1920 * 1080 * 4, 7680, 1920 * 1080 * 4, 1920, 1080, routeStoreSHM, 7680},
		{"dmabuf encode unchanged", true, bufDmaBuf, false, 0, 0, 0, 0, 3840, 2160, routeEncDMA, 0},
		{"dmabuf readback unchanged", false, bufDmaBuf, false, 0, 0, 0, 0, 1920, 1080, routeReadbackDMA, 0},
		{"SHM unmapped", true, bufMemFd, false, 0, sz4k, 15360, sz4k, 3840, 2160, routeDrop, 0},
		{"SHM empty chunk", true, bufMemFd, true, 0, 0, 15360, sz4k, 3840, 2160, routeDrop, 0},
		{"SHM no format yet", true, bufMemFd, true, 0, sz4k, 15360, sz4k, 0, 0, routeDrop, 0},
		{"SHM stride < row", true, bufMemFd, true, 0, sz4k, 7680, sz4k, 3840, 2160, routeDrop, 0},
		{"SHM negative stride", true, bufMemFd, true, 0, sz4k, -15360, sz4k, 3840, 2160, routeDrop, 0},
		{"SHM overruns maxsize", true, bufMemFd, true, 64, sz4k, 15360, sz4k, 3840, 2160, routeDrop, 0},
		{"SHM fits with offset", true, bufMemFd, true, 64, sz4k, 15360, sz4k + 64, 3840, 2160, routeEncSHM, 15360},
	}
	for _, c := range cases {
		got, st := routeBuffer(c.enc, c.kind, c.data, c.off, c.size, c.stride, c.maxsize, c.w, c.h)
		if got != c.want {
			t.Errorf("%s: route = %d, want %d", c.name, got, c.want)
			continue
		}
		if c.wantStride != 0 && st != c.wantStride {
			t.Errorf("%s: stride = %d, want %d", c.name, st, c.wantStride)
		}
	}
}

// TestVAEncodeSHMUpload drives the SHM upload encode (rva_enc_frame_mem) directly with a synthetic BGRx
// corner pattern - no PipeWire producer needed. Asserts every frame yields an AU, the first is an IDR,
// and (when ffmpeg is on PATH) the decoded first frame has the right size and corner colours. Needs a VA
// render node: RPW_EGL_DEVICE or /dev/dri/renderD128. RPW_SHM_ENC_W/H override 3840x2160;
// RPW_ENC_CODEC=h264 selects H.264 (default HEVC, the engine the live route used).
func TestVAEncodeSHMUpload(t *testing.T) {
	node := os.Getenv("RPW_EGL_DEVICE")
	if node == "" {
		node = "/dev/dri/renderD128"
	}
	if _, err := os.Stat(node); err != nil {
		t.Skipf("no VA render node %s", node)
	}
	if !VAEncodeAvailable() {
		t.Skip("libva not available")
	}
	codec, ext := 1, "hevc"
	if os.Getenv("RPW_ENC_CODEC") == "h264" {
		codec, ext = 0, "h264"
	}
	sizes := [][2]int{{3840, 2160}, {1920, 1080}}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		const n = 30
		aus, keys, err := vaEncodeMem(node, codec, w, h, n)
		if err != nil {
			t.Fatalf("%dx%d: %v (RPW_VA_DEBUG=1 shows the failing VA step)", w, h, err)
		}
		if len(aus) != n {
			t.Fatalf("%dx%d: %d AUs for %d frames", w, h, len(aus), n)
		}
		if !keys[0] {
			t.Errorf("%dx%d: first AU is not a keyframe", w, h)
		}
		total := 0
		for _, a := range aus {
			total += len(a)
		}
		t.Logf("SHM-UPLOAD %s %dx%d: %d AUs, %d bytes, firstKey=%v", ext, w, h, len(aus), total, keys[0])
		ff, err := exec.LookPath("ffmpeg")
		if err != nil {
			t.Logf("ffmpeg not on PATH: decode/corner check skipped")
			continue
		}
		dir := t.TempDir()
		in := filepath.Join(dir, "shm."+ext)
		if err := os.WriteFile(in, bytes.Join(aus, nil), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(ff, "-v", "error", "-f", ext, "-i", in, "-frames:v", "1",
			"-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
		if err != nil {
			t.Fatalf("%dx%d: ffmpeg decode: %v", w, h, err)
		}
		if len(out) != w*h*3 {
			t.Fatalf("%dx%d: decoded %d bytes, want %d", w, h, len(out), w*h*3)
		}
		px := func(x, y int) [3]int {
			o := (y*w + x) * 3
			return [3]int{int(out[o]), int(out[o+1]), int(out[o+2])}
		}
		want := map[string][3]int{"TL": {255, 0, 0}, "TR": {0, 255, 0}, "BL": {0, 0, 255}, "BR": {255, 255, 255}}
		got := map[string][3]int{"TL": px(w/8, h/8), "TR": px(w*7/8, h/8), "BL": px(w/8, h*7/8), "BR": px(w*7/8, h*7/8)}
		for k, wv := range want {
			g := got[k]
			for i := 0; i < 3; i++ {
				if d := g[i] - wv[i]; d > 40 || d < -40 {
					t.Errorf("%dx%d: corner %s = %v, want ~%v", w, h, k, g, wv)
					break
				}
			}
		}
	}
}
