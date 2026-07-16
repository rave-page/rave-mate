// Command mocapnode-probe is a DEV-ONLY CLI harness for internal/mocapnode (the rave-mate GUI
// stays the single shipped binary): it wires a capture Source to the Node and prints health
// lines - bring-up + diagnostics for capture-node operators. Ctrl-C stops it.
//
//	mocapnode-probe -source desktop [-monitor 0] [-size 1920x1080 | -crop X,Y,WxH]
//	                [-grabber ddagrab|gdigrab] [-fps 30]
//	mocapnode-probe -source spout -device "VRChat-StreamCamera"
//	mocapnode-probe -source dshow -device "OBS Virtual Camera" [-size 1920x1080] [-fps 30]
//	mocapnode-probe -source file -in capture.png [-size WxH (raw .rgb)] [-fps 30]
//
// Common: -dump packets.jsonl (JSON-lines packet dump), -stats (per-second health line).
// spout = the DIRECT camera-node path (VRChat Stream Camera -> Spout2 -> in-process
// videoshare receiver; needs a SPOUT=1 build, -device "" lists senders). dshow = the
// no-Spout-build fallback chain (Spout -> OBS Spout source -> OBS Virtual Camera -> dshow).
//
// Master mode: -master routes packets through a mocapmaster.Master (pose store + region
// renderer) built from -bone-slots/-stage-min/-stage-size; -region-out FILE.png additionally
// renders the composite mocap region into a black 1920x1080 canvas once per second and
// overwrites the PNG - a live conformance fixture for world-side decoders.
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/signal"
	"time"

	"rave.page/mate/internal/mocapmaster"
	"rave.page/mate/internal/mocapnode"
)

func main() {
	var (
		source  = flag.String("source", "", "capture source: desktop|spout|dshow|file")
		device  = flag.String("device", "OBS Virtual Camera", "dshow device / spout sender name")
		in      = flag.String("in", "", "fixture path for -source file (.png or raw .rgb)")
		dump    = flag.String("dump", "", "JSON-lines packet dump path")
		stats   = flag.Bool("stats", false, "print a health line every second")
		size    = flag.String("size", "", "frame geometry WxH (desktop full-frame, dshow, raw .rgb)")
		crop    = flag.String("crop", "", "desktop capture sub-rect X,Y,WxH")
		monitor = flag.Int("monitor", 0, "desktop monitor index (ddagrab output_idx)")
		grabber = flag.String("grabber", "ddagrab", "desktop grabber: ddagrab|gdigrab")
		fps     = flag.Int("fps", 30, "target capture fps")

		master    = flag.Bool("master", false, "route packets through a mocapmaster.Master (pose store + region renderer)")
		regionOut = flag.String("region-out", "", "with -master: overwrite this PNG with the rendered 1920x1080 region frame once per second")
		boneSlots = flag.Int("bone-slots", 22, "master bone slots S (1..32; region stride = 8+2*S)")
		stageMin  = flag.String("stage-min", "-8,0,-6", "master stage bounds min x,y,z (m)")
		stageSize = flag.String("stage-size", "16,4,12", "master stage bounds size x,y,z (m; all > 0)")
	)
	flag.Parse()

	src, err := buildSource(*source, *device, *in, *size, *crop, *monitor, *grabber, *fps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mocapnode-probe:", err)
		flag.Usage()
		os.Exit(2)
	}

	cfg := mocapnode.Config{
		Source: src,
		Logf:   func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}
	if *dump != "" {
		f, err := os.Create(*dump)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mocapnode-probe:", err)
			os.Exit(1)
		}
		defer func() { _ = f.Close() }()
		cfg.Dump = f
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *regionOut != "" && !*master {
		fmt.Fprintln(os.Stderr, "mocapnode-probe: -region-out needs -master")
		os.Exit(2)
	}
	if *master {
		m, err := buildMaster(*boneSlots, *stageMin, *stageSize)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mocapnode-probe:", err)
			os.Exit(2)
		}
		cfg.OnPacket = m.OnPacket
		if *regionOut != "" {
			go writeRegionPNGs(ctx, m, *regionOut)
		}
	}
	node := mocapnode.New(cfg)

	if *stats {
		go printStats(ctx, node)
	}
	if err := node.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "mocapnode-probe:", err)
		os.Exit(1)
	}
	printHealth(node.Health())
}

// buildMaster wires a Master from the probe flags.
func buildMaster(boneSlots int, minS, sizeS string) (*mocapmaster.Master, error) {
	min, err := parseVec3(minS)
	if err != nil {
		return nil, fmt.Errorf("bad -stage-min: %w", err)
	}
	size, err := parseVec3(sizeS)
	if err != nil {
		return nil, fmt.Errorf("bad -stage-size: %w", err)
	}
	return mocapmaster.New(mocapmaster.Config{
		BoneSlots: boneSlots, StageMin: min, StageSize: size,
		Logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
}

// writeRegionPNGs renders the master's region into a black 1920x1080 canvas once per second
// and overwrites path (write temp + rename so a reader never sees a torn file).
func writeRegionPNGs(ctx context.Context, m *mocapmaster.Master, path string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	canvas := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{0, 0, 0, 255}), image.Point{}, draw.Src)
			m.RenderInto(canvas)
			if err := writePNG(path, canvas); err != nil {
				fmt.Fprintln(os.Stderr, "mocapnode-probe: region-out:", err)
			}
		}
	}
}

func writePNG(path string, img image.Image) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// parseVec3 parses "x,y,z" into metres.
func parseVec3(s string) ([3]float64, error) {
	var v [3]float64
	if _, err := fmt.Sscanf(s, "%g,%g,%g", &v[0], &v[1], &v[2]); err != nil {
		return v, fmt.Errorf("%q (want x,y,z)", s)
	}
	return v, nil
}

// buildSource maps the flag set onto a mocapnode Source.
func buildSource(source, device, in, size, crop string, monitor int, grabber string, fps int) (mocapnode.Source, error) {
	w, h, err := parseSize(size)
	if err != nil {
		return nil, err
	}
	switch source {
	case "desktop":
		s := &mocapnode.FFmpegDesktopSource{Monitor: monitor, Grabber: grabber, FPS: fps, W: w, H: h}
		if crop != "" {
			if s.Crop, err = parseCrop(crop); err != nil {
				return nil, err
			}
		}
		return s, nil
	case "spout":
		return &mocapnode.SpoutSource{Sender: device}, nil
	case "dshow":
		return &mocapnode.FFmpegDShowSource{Device: device, W: w, H: h, FPS: fps}, nil
	case "file":
		if in == "" {
			return nil, fmt.Errorf("-source file needs -in")
		}
		return &mocapnode.FileSource{Path: in, W: w, H: h, FPS: fps}, nil
	default:
		return nil, fmt.Errorf("unknown -source %q (want desktop|spout|dshow|file)", source)
	}
}

func parseSize(s string) (w, h int, err error) {
	if s == "" {
		return 0, 0, nil
	}
	if _, err := fmt.Sscanf(s, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("bad -size %q (want WxH)", s)
	}
	return w, h, nil
}

func parseCrop(s string) (image.Rectangle, error) {
	var x, y, w, h int
	if _, err := fmt.Sscanf(s, "%d,%d,%dx%d", &x, &y, &w, &h); err != nil || w <= 0 || h <= 0 {
		return image.Rectangle{}, fmt.Errorf("bad -crop %q (want X,Y,WxH)", s)
	}
	return image.Rect(x, y, x+w, y+h), nil
}

// printStats emits one health line per second.
func printStats(ctx context.Context, node *mocapnode.Node) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			printHealth(node.Health())
		}
	}
}

func printHealth(h mocapnode.Health) {
	fmt.Printf("fps=%.1f locked=%v identity=%v live=%v frames=%d decoded=%d failed=%d ok=%.0f%% counter=%d err=%q\n",
		h.FPS, h.Locked, h.Identity, h.Live, h.Frames, h.Decoded, h.Failed, h.SuccessRate*100, h.LastCounter, h.LastErr)
}
