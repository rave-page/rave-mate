//go:build spout

package videoshare

import (
	"fmt"
	"image"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

// sendcost_spout_test.go - what does the produce path's per-frame GL upload actually cost?
//
// Increment 4's stated target is "render/receive straight into a D3D11 texture and publish it, which
// deletes both the GL upload and spout_shim.cpp's per-frame malloc". The malloc is long gone and the
// transpose is now row-wise, so the GL upload is the ONLY cost left that a D3D11 publish path would
// remove - and the replacement (host->SHM copy + UpdateSubresource + VideoProcessorBlt) is not
// obviously cheaper. Measure before building a third protocol direction.
//
//	go test -tags spout ./internal/videoshare -run TestSendCost -v
func TestSendCost(t *testing.T) {
	for _, g := range [][2]int{{1280, 720}, {1920, 1080}, {3840, 2160}} {
		w, h := g[0], g[1]
		name := fmt.Sprintf("rave-mate sendcost %dx%d", w, h)
		fs, err := NewFrameSender(logbus.New(32), name)
		if err != nil {
			t.Skipf("no sender: %v", err)
		}
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for i := range img.Pix {
			img.Pix[i] = byte(i)
		}
		// Warm up: the first send creates the sender + its shared texture + the GL interop object.
		start := time.Now()
		_ = fs.Send(img)
		first := time.Since(start)

		const n = 60
		t0 := time.Now()
		for i := 0; i < n; i++ {
			_ = fs.Send(img)
		}
		per := time.Since(t0) / n
		mb := float64(w*h*4) / (1 << 20)
		t.Logf("%dx%d (%.1f MB/frame): first send %v, steady %v/frame = %.0f MB/s, %.1f%% of a 60 fps budget",
			w, h, mb, first, per, mb/per.Seconds(), float64(per)/float64(time.Second/60)*100)
		fs.Close()
	}
	t.Log("VERDICT input: a D3D11 publish path replaces this GL upload with a host->SHM copy plus " +
		"UpdateSubresource + Blt. It is only worth a new protocol direction if the number above is a " +
		"material share of the frame budget AND the replacement is materially cheaper.")
}
