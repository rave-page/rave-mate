// Package flipbook generates VRChat animated-emoji sprite sheets from a video or GIF.
//
// Output spec (wiki.vrchat.com/wiki/Emojis): a 1024×1024 PNG of square frames in a uniform
// grid, ordered left→right then top→bottom. Frame count picks the tier: 2×2 = 4 frames @512px,
// 4×4 = 16 @256px, 8×8 = 64 @128px (max 64). VRChat reads default frame-count + FPS from the
// filename - so the sheet is named `<name>_<N>frames_<fps>fps.png`. Upload is website-only
// ("Enable Sprite Sheet Mode"); custom emoji need VRC+.
//
// ffmpeg samples + crops + scales + pads the source to exact square tier frames in one pass
// (fps/crop/scale/pad filters); the 1024² grid is assembled in Go (image/draw) so ordering,
// ping-pong, and exact tier sizing are deterministic + unit-testable without ffmpeg.
package flipbook

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/sysexec"
)

// SheetSize is the fixed sprite-sheet edge length in px.
const SheetSize = 1024

// MaxFrames is VRChat's per-emoji frame cap.
const MaxFrames = 64

// EmojiUploadURL is the VRChat website gallery where sprite sheets are uploaded (website-only;
// VRC+ required). There is no public API for emoji upload (verified against the wiki + API spec).
const EmojiUploadURL = "https://vrchat.com/home/gallery"

// Tier maps a sprite-sheet frame capacity to its grid + per-frame resolution.
type Tier struct {
	Frames   int // total frames the sheet holds: 4 | 16 | 64
	Grid     int // columns == rows: 2 | 4 | 8
	FrameRes int // per-frame px: 512 | 256 | 128
}

var tiers = []Tier{
	{Frames: 4, Grid: 2, FrameRes: 512},
	{Frames: 16, Grid: 4, FrameRes: 256},
	{Frames: 64, Grid: 8, FrameRes: 128},
}

// Tiers returns the supported sprite-sheet tiers (for the UI's frame-count picker).
func Tiers() []Tier {
	out := make([]Tier, len(tiers))
	copy(out, tiers)
	return out
}

// TierFor returns the tier whose frame capacity exactly equals n.
func TierFor(n int) (Tier, error) {
	for _, t := range tiers {
		if t.Frames == n {
			return t, nil
		}
	}
	return Tier{}, fmt.Errorf("flipbook: %d frames is not a valid tier (want 4, 16, or 64)", n)
}

// Rect is a pixel crop region applied before scaling.
type Rect struct{ X, Y, W, H int }

// Options configures one sprite-sheet generation.
type Options struct {
	Input     string  // source video/GIF path
	OutName   string  // emoji name (drives the filename); sanitized
	Frames    int     // total sheet frames (must be a tier: 4|16|64)
	FPS       float64 // playback FPS (encoded in the filename; also the sample rate)
	TrimStart float64 // seconds into the source to start (0 = beginning)
	TrimEnd   float64 // seconds into the source to stop (<=0 = bounded by frame count)
	Crop      *Rect   // optional pre-scale crop
	PingPong  bool    // append reversed frames so the loop plays forward then back
	OutDir    string  // directory the sheet is written to
}

// Validate checks the options independently of ffmpeg (unit-tested).
func (o Options) Validate() error {
	if strings.TrimSpace(o.Input) == "" {
		return errors.New("flipbook: no input file")
	}
	if _, err := TierFor(o.Frames); err != nil {
		return err
	}
	if o.FPS <= 0 || o.FPS > 120 {
		return fmt.Errorf("flipbook: fps %g out of range (0 < fps <= 120)", o.FPS)
	}
	if o.TrimStart < 0 {
		return errors.New("flipbook: negative trim start")
	}
	if o.TrimEnd > 0 && o.TrimEnd <= o.TrimStart {
		return errors.New("flipbook: trim end must exceed trim start")
	}
	if o.Crop != nil && (o.Crop.W <= 0 || o.Crop.H <= 0) {
		return errors.New("flipbook: crop width/height must be positive")
	}
	if strings.TrimSpace(o.OutDir) == "" {
		return errors.New("flipbook: no output dir")
	}
	return nil
}

// OutFileName formats the VRChat-convention filename: `<name>_<N>frames_<fps>fps.png`.
func OutFileName(name string, frames int, fps float64) string {
	return fmt.Sprintf("%s_%dframes_%sfps.png", sanitizeName(name), frames, formatNum(fps))
}

// Generate produces the sprite sheet and returns its absolute path. ffmpegPath is the resolved
// ffmpeg binary (caller resolves via mediatools). One ffmpeg pass extracts the square tier
// frames; Go tiles them into the 1024² sheet.
func Generate(ffmpegPath string, o Options) (string, error) {
	if err := o.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(ffmpegPath) == "" {
		return "", errors.New("flipbook: ffmpeg path is empty")
	}
	tier, _ := TierFor(o.Frames)
	extract := framesToExtract(o.Frames, o.PingPong)

	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return "", fmt.Errorf("flipbook: create out dir: %w", err)
	}
	tmp, err := os.MkdirTemp("", "ravemate-flipbook-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	pattern := filepath.Join(tmp, "f_%05d.png")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath, ffmpegArgs(o, tier.FrameRes, extract, pattern)...)
	sysexec.Hide(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("flipbook: ffmpeg failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	frames, err := loadFrames(tmp)
	if err != nil {
		return "", err
	}
	if len(frames) == 0 {
		return "", errors.New("flipbook: ffmpeg produced no frames (clip too short or trim out of range?)")
	}

	sheet := assemble(frames, tier, frameSequence(o.Frames, o.PingPong))
	outPath := filepath.Join(o.OutDir, OutFileName(o.OutName, o.Frames, o.FPS))
	if err := writePNG(outPath, sheet); err != nil {
		return "", err
	}
	return outPath, nil
}

// ── pure helpers (unit-tested) ───────────────────────────────────────────────

// framesToExtract is the count of distinct source frames sampled for an n-cell sheet. Ping-pong
// reuses the forward frames in reverse, so only n/2+1 distinct frames are needed.
func framesToExtract(n int, pingPong bool) int {
	if pingPong {
		return n/2 + 1
	}
	return n
}

// frameSequence maps each output cell to a source-frame index (L→R, T→B). Normal: 0..n-1.
// Ping-pong: forward then back without repeating the endpoints, total length n (n is even for
// every tier). Cell i pulls source frame seq[i].
func frameSequence(n int, pingPong bool) []int {
	if !pingPong {
		seq := make([]int, n)
		for i := range seq {
			seq[i] = i
		}
		return seq
	}
	base := n/2 + 1
	seq := make([]int, 0, n)
	for i := 0; i < base; i++ {
		seq = append(seq, i)
	}
	for i := base - 2; i >= 1; i-- {
		seq = append(seq, i)
	}
	return seq
}

// videoFilters builds the ffmpeg -vf chain: resample to fps, optional crop, scale to fit the
// tier square (lanczos), then pad with transparency to the exact square. crop precedes scale.
func videoFilters(fps float64, res int, crop *Rect) string {
	parts := []string{"fps=" + formatNum(fps)}
	if crop != nil {
		parts = append(parts, fmt.Sprintf("crop=%d:%d:%d:%d", crop.W, crop.H, crop.X, crop.Y))
	}
	parts = append(parts,
		fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:flags=lanczos", res, res),
		"format=rgba",
		fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black@0.0", res, res),
	)
	return strings.Join(parts, ",")
}

// ffmpegArgs builds the argv for a single extract pass writing numbered square PNGs.
func ffmpegArgs(o Options, res, extract int, framePattern string) []string {
	args := []string{"-y", "-hide_banner", "-loglevel", "error"}
	if o.TrimStart > 0 {
		args = append(args, "-ss", formatSeconds(o.TrimStart))
	}
	if d := readDuration(o, extract); d > 0 {
		args = append(args, "-t", formatSeconds(d)) // bound input read on long sources
	}
	return append(args, "-i", o.Input,
		"-vf", videoFilters(o.FPS, res, o.Crop),
		"-frames:v", strconv.Itoa(extract),
		"-start_number", "0",
		framePattern)
}

// readDuration bounds how much source to read: enough for `extract` frames at FPS (+epsilon),
// clamped to the trim span when one is set.
func readDuration(o Options, extract int) float64 {
	d := float64(extract)/o.FPS + 0.5
	if o.TrimEnd > o.TrimStart {
		if span := o.TrimEnd - o.TrimStart; span < d {
			return span
		}
	}
	return d
}

// assemble blits square source frames into the 1024² sheet per seq (L→R, T→B). A seq index past
// the available frames reuses the last frame (short clip) so no cell is left blank.
func assemble(frames []image.Image, t Tier, seq []int) *image.RGBA {
	sheet := image.NewRGBA(image.Rect(0, 0, SheetSize, SheetSize))
	for cell, src := range seq {
		if cell >= t.Frames {
			break
		}
		idx := src
		if idx >= len(frames) {
			idx = len(frames) - 1
		}
		col, row := cell%t.Grid, cell/t.Grid
		dst := image.Rect(col*t.FrameRes, row*t.FrameRes, (col+1)*t.FrameRes, (row+1)*t.FrameRes)
		draw.Draw(sheet, dst, frames[idx], frames[idx].Bounds().Min, draw.Src)
	}
	return sheet
}

// loadFrames reads f_*.png from dir in lexical (== capture) order.
func loadFrames(dir string) ([]image.Image, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "f_*.png"))
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	out := make([]image.Image, 0, len(entries))
	for _, p := range entries {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		img, err := png.Decode(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("flipbook: decode %s: %w", filepath.Base(p), err)
		}
		out = append(out, img)
	}
	return out, nil
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// formatNum renders a float with no trailing zeros (20.0 → "20", 12.5 → "12.5").
func formatNum(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// formatSeconds renders a seek/duration value with millisecond precision for ffmpeg.
func formatSeconds(s float64) string { return strconv.FormatFloat(s, 'f', 3, 64) }

// sanitizeName trims + replaces filename-illegal chars so the emoji name is filesystem-safe.
func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "emoji"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		}
		return r
	}, name)
}
