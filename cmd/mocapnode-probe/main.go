// Command mocapnode-probe is a DEV-ONLY CLI harness for internal/mocapnode (the rave-mate GUI
// stays the single shipped binary): it wires a capture Source to the Node and prints health
// lines - bring-up + diagnostics for capture-node operators. Ctrl-C stops it.
//
//	mocapnode-probe -source desktop [-monitor 0] [-size 1920x1080 | -crop X,Y,WxH]
//	                [-grabber ddagrab|gdigrab] [-fps 30]
//	mocapnode-probe -source dshow -device "OBS Virtual Camera" [-size 1920x1080] [-fps 30]
//	mocapnode-probe -source file -in capture.png [-size WxH (raw .rgb)] [-fps 30]
//
// Common: -dump packets.jsonl (JSON-lines packet dump), -stats (per-second health line).
// The dshow path is the Spout chain: VRChat Stream Camera -> Spout2 -> OBS Spout source ->
// OBS Virtual Camera -> dshow.
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"os"
	"os/signal"
	"time"

	"rave.page/mate/internal/mocapnode"
)

func main() {
	var (
		source  = flag.String("source", "", "capture source: desktop|dshow|file")
		device  = flag.String("device", "OBS Virtual Camera", "dshow device name")
		in      = flag.String("in", "", "fixture path for -source file (.png or raw .rgb)")
		dump    = flag.String("dump", "", "JSON-lines packet dump path")
		stats   = flag.Bool("stats", false, "print a health line every second")
		size    = flag.String("size", "", "frame geometry WxH (desktop full-frame, dshow, raw .rgb)")
		crop    = flag.String("crop", "", "desktop capture sub-rect X,Y,WxH")
		monitor = flag.Int("monitor", 0, "desktop monitor index (ddagrab output_idx)")
		grabber = flag.String("grabber", "ddagrab", "desktop grabber: ddagrab|gdigrab")
		fps     = flag.Int("fps", 30, "target capture fps")
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
	node := mocapnode.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *stats {
		go printStats(ctx, node)
	}
	if err := node.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "mocapnode-probe:", err)
		os.Exit(1)
	}
	printHealth(node.Health())
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
	case "dshow":
		return &mocapnode.FFmpegDShowSource{Device: device, W: w, H: h, FPS: fps}, nil
	case "file":
		if in == "" {
			return nil, fmt.Errorf("-source file needs -in")
		}
		return &mocapnode.FileSource{Path: in, W: w, H: h, FPS: fps}, nil
	default:
		return nil, fmt.Errorf("unknown -source %q (want desktop|dshow|file)", source)
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
