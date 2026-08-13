package app

// ctl surface-test: drive the window child's native render surface (SDL_WEBVIEW_SURFACE_DESIGN).
//
//	on|off       the hole itself - the child injects/removes a [data-surface] element (P2)
//	card [spec]  start the P3 PRODUCER: internal/testcard into a shared D3D11 texture ring
//	stats        sample what is ON SCREEN and judge it with the content oracle
//	reset        clear the sampler's tallies for a fresh experiment
//
// The daemon's whole role here is process lifecycle and counters. It holds no surface, learns no
// rect, and never carries a frame or a handle: the producer publishes its ring under a name and the
// child finds it (§4.5). `stats` is the one place pixels come back, and even then the CHILD picks
// the crop - the daemon only supplies a path.

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/framedebug"
	"rave.page/mate/internal/surfacepub"
	"rave.page/mate/internal/testcard"
	"rave.page/mate/internal/worker"
)

// surfaceStage is the oracle's name for "what the compositor actually put on screen". Deliberately
// distinct from any producer-side stage: the producer proving its own frames change proves nothing
// about the transport, and that confusion is exactly what let a 4K route republish one frame for
// 48 minutes with every counter green (#58).
const surfaceStage = "surface-present"

// surfaceTestID is the id of the child's own test hole (surfaces.js __sfTest). The producer names
// its shared objects after it, which is the entire bind.
const surfaceTestID = "test-hole"

// surfaceProducer tracks the supervised producer child. One at a time: this is a diagnostic.
type surfaceProducer struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	started time.Time
	spec    string
	last    worker.SurfaceCardStats
	lastErr string
}

func (c *appControl) SurfaceTest(args string) string {
	f := strings.Fields(strings.TrimSpace(args))
	op := "stats"
	if len(f) > 0 {
		op = strings.ToLower(f[0])
	}
	switch op {
	case "on":
		return c.surfaceHole(true)
	case "off":
		c.stopSurfaceProducer()
		return c.surfaceHole(false)
	case "card":
		spec := ""
		if len(f) > 1 {
			spec = f[1]
		}
		return c.startSurfaceProducer(spec)
	case "stats":
		return c.surfaceStats()
	case "reset":
		testcard.VerifyReset()
		return "surface sampler tallies cleared (the producer and the framedebug stage keep running)"
	}
	return fmt.Sprintf("error: unknown op %q (on|off|card [WxH@fps]|stats|reset)", op)
}

// surfaceHole toggles the child's test element. Pure command plumbing - one bool crosses.
func (c *appControl) surfaceHole(on bool) string {
	s, ok := c.ui.(interface{ SurfaceTest(on bool) string })
	if !ok {
		return "unsupported (webview renderer only)"
	}
	return s.SurfaceTest(on)
}

// startSurfaceProducer opens the hole and starts the supervised producer child. The producer does
// NOT get told a size: the shell child publishes the surface's full rect into the shared control
// block and the producer renders to that, which is what keeps the present a 1:1 crop.
func (c *appControl) startSurfaceProducer(spec string) string {
	if c.workers == nil {
		return "error: worker supervisor unavailable"
	}
	w, h, fps := 0, 0, 0
	if spec != "" {
		if n, err := fmt.Sscanf(spec, "%dx%d@%d", &w, &h, &fps); err != nil || n != 3 {
			return fmt.Sprintf("error: bad spec %q (want WxH@fps, e.g. 1280x720@30)", spec)
		}
	}
	if hole := c.surfaceHole(true); strings.HasPrefix(hole, "unsupported") || strings.HasPrefix(hole, "failed") {
		return "error: " + hole
	}
	c.stopSurfaceProducer()

	ctx, cancel := context.WithCancel(context.Background())
	c.surfProd.mu.Lock()
	c.surfProd.cancel = cancel
	c.surfProd.running = true
	c.surfProd.started = time.Now()
	c.surfProd.spec = spec
	c.surfProd.lastErr = ""
	c.surfProd.last = worker.SurfaceCardStats{}
	c.surfProd.mu.Unlock()

	params := map[string]any{"id": surfaceTestID, "w": w, "h": h, "fps": fps}
	go func() {
		_, err := c.workers.RunStream(ctx, "surface", "surface.card", params, func(ev string, data json.RawMessage) {
			if ev != "stats" {
				return
			}
			var st worker.SurfaceCardStats
			if json.Unmarshal(data, &st) != nil {
				return
			}
			c.surfProd.mu.Lock()
			c.surfProd.last = st
			c.surfProd.mu.Unlock()
		})
		c.surfProd.mu.Lock()
		c.surfProd.running = false
		if err != nil && ctx.Err() == nil {
			c.surfProd.lastErr = err.Error()
		}
		c.surfProd.mu.Unlock()
	}()
	return "surface producer starting (testcard → shared D3D11 texture, ring " +
		fmt.Sprint(surfacepub.Slots) + " frames); `ctl surface-test stats` to sample the screen, `off` to stop"
}

func (c *appControl) stopSurfaceProducer() {
	c.surfProd.mu.Lock()
	cancel := c.surfProd.cancel
	c.surfProd.cancel = nil
	c.surfProd.mu.Unlock()
	if cancel != nil {
		cancel() // the supervisor discards + kills the child; its job object reaps anything under it
	}
}

// surfaceStats samples the composited surface and judges it. This is the honest end-to-end oracle:
// the CHILD crops its own window to the surface, the PNG comes back, the testcard's in-picture
// sequence says which frame is on screen and framedebug says how much of the picture moved. A rate
// counter cannot answer either question.
func (c *appControl) surfaceStats() string {
	var b strings.Builder
	c.surfProd.mu.Lock()
	running, started, last, lerr := c.surfProd.running, c.surfProd.started, c.surfProd.last, c.surfProd.lastErr
	c.surfProd.mu.Unlock()

	if running {
		g, p := last.Gen, last.Pub
		fmt.Fprintf(&b, "producer: up %s, %dx%d@%d session %d, %d frames rendered, %d skips, worst send %dms%s\n",
			time.Since(started).Round(time.Second), g.W, g.H, g.FPS, g.Session, g.Frames, g.Skips, g.SendMaxMs,
			map[bool]string{true: " [" + last.Note + "]", false: ""}[last.Note != ""])
		fmt.Fprintf(&b, "transport: gen %d %dx%d, published %d, producer-dropped %d (no free slot), "+
			"consumer presented seq %d, consumer-dropped %d, consumer last seen %dms ago\n",
			p.Gen, p.W, p.H, p.Published, p.Dropped, p.PresentSeq, p.ConsumerDrops, p.ConsumerAgeMs)
		if p.WantW > 0 {
			fmt.Fprintf(&b, "geometry: surface asked for %dx%d, ring is %dx%d (equal = the present is a 1:1 crop, never a scale)\n",
				p.WantW, p.WantH, p.W, p.H)
		}
	} else {
		fmt.Fprintln(&b, "producer: not running (`ctl surface-test card` to start it)")
		if lerr != "" {
			fmt.Fprintln(&b, "last producer error:", lerr)
		}
	}

	img, err := c.sampleSurface()
	if err != nil {
		fmt.Fprintln(&b, "screen sample: "+err.Error())
		return b.String()
	}
	now := time.Now()
	framedebug.For(surfaceStage).Frame(img)
	testcard.ObserveAt(surfaceStage, img, now)

	p, derr := testcard.Decode(img)
	fmt.Fprintf(&b, "screen sample: %dx%d, decode %s", img.Rect.Dx(), img.Rect.Dy(), derr)
	if derr == testcard.DecodeOK {
		fmt.Fprintf(&b, ", seq %d, age %dms", p.Seq, p.DeltaMs(now))
	}
	fmt.Fprintln(&b)

	fd := framedebug.Snapshot()[surfaceStage]
	fmt.Fprintf(&b, "content oracle: samples %d, CHANGED %d, lastChange %dms ago, moved %.1f%% (peak %.1f%%)%s\n",
		fd.Frames, fd.Changes, fd.StalledMs, fd.MovedFrac*100, fd.PeakFrac*100,
		map[bool]string{true: "  ** STATIC: the picture changes but does not move **", false: ""}[fd.Static()])

	if v, ok := testcard.VerifySnapshot()[surfaceStage]; ok {
		fmt.Fprintf(&b, "card oracle: decoded %d/%d, unique %d, dups %d (worst freeze %d samples), "+
			"gaps %d, reorders %d, crcFail %d, restarts %d\n",
			v.Decoded, v.Frames, v.Unique, v.Dups, v.MaxDupRun, v.Gaps, v.Reorders, v.CRCFail, v.Restarts)
		fmt.Fprintf(&b, "card latency: last %dms, min %dms, max %dms, drift %+dms (drift climbing = falling behind)\n",
			v.LastDeltaMs, v.MinDeltaMs, v.MaxDeltaMs, v.DriftMs())
		fmt.Fprintln(&b, "note: samples are ctl calls, not frames. `dups` = the SCREEN did not advance between two "+
			"samples (the freeze question a wire fps cannot answer); `gaps` = the seqs that went by BETWEEN samples, "+
			"which is the sampling interval, not loss - the transport's own loss is published minus presentSeq.")
	}
	return b.String()
}

// sampleSurface has the child crop its own window to the presenting surface and reads the PNG back.
func (c *appControl) sampleSurface() (*image.NRGBA, error) {
	s, ok := c.ui.(interface{ SurfaceShot(path string) error })
	if !ok {
		return nil, fmt.Errorf("unsupported (needs the zig window child)")
	}
	path := filepath.Join(os.TempDir(), "rave-mate-surface-sample.png")
	if err := s.SurfaceShot(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	if n, ok := src.(*image.NRGBA); ok {
		return n, nil
	}
	bnd := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, bnd.Dx(), bnd.Dy()))
	for y := 0; y < bnd.Dy(); y++ {
		for x := 0; x < bnd.Dx(); x++ {
			r, g, bl, a := src.At(bnd.Min.X+x, bnd.Min.Y+y).RGBA()
			o := y*out.Stride + x*4
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8(r>>8), uint8(g>>8), uint8(bl>>8), uint8(a>>8)
		}
	}
	return out, nil
}
