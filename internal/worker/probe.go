package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"rave.page/mate/internal/mediatools"
)

// probeHandlers serves the "probe" worker type: a process-isolated media inspector. ping
// proves the subprocess pipeline without external tools; the probe.* methods shell out to
// ffprobe (running ffprobe in-process would block the daemon and risk a hang/crash).
func probeHandlers() map[string]Handler {
	return map[string]Handler{
		"ping":           pingHandler,
		"probe.duration": durationHandler,
		"probe.streams":  streamsHandler,
		"probe.tags":     tagsHandler,
		"probe.artwork":  artworkHandler,
		"probe.waveform": waveformHandler,
		"probe.peaks":    peaksHandler,
		"probe.envelope": envelopeHandler,
	}
}

func pingHandler(_ json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"pong": true, "pid": os.Getpid()})
}

type pathParams struct {
	Path string `json:"path"`
}

func durationHandler(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var p pathParams
	if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
		return nil, fmt.Errorf("missing path")
	}
	out, err := ffprobe("-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", p.Path)
	if err != nil {
		return nil, err
	}
	secs, perr := strconv.ParseFloat(trimLine(out), 64)
	if perr != nil {
		return json.Marshal(map[string]any{"durationSeconds": nil})
	}
	return json.Marshal(map[string]any{"durationSeconds": secs})
}

// waveformHandler renders an audio waveform PNG via ffmpeg showwavespic (mono mixdown) and
// returns it base64-encoded. Read-only; the source file is never modified.
func waveformHandler(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var p struct {
		Path   string `json:"path"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Color  string `json:"color"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
		return nil, fmt.Errorf("missing path")
	}
	if p.Width <= 0 {
		p.Width = 800
	}
	if p.Height <= 0 {
		p.Height = 120
	}
	if p.Color == "" {
		p.Color = "#F70864"
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("ravemate-wave-%d-%d.png", os.Getpid(), time.Now().UnixNano()))
	defer func() { _ = os.Remove(tmp) }()
	filter := fmt.Sprintf("aformat=channel_layouts=mono,showwavespic=s=%dx%d:colors=%s", p.Width, p.Height, p.Color)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-loglevel", "error", "-y", "-i", p.Path, "-filter_complex", filter, "-frames:v", "1", tmp)
	prepareCmd(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("waveform: %s", trimLine(string(out)))
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"png": base64.StdEncoding.EncodeToString(data)})
}

// peaksRate is the decode sample rate for waveform peaks - low enough to keep the
// ffmpeg pass fast on multi-hour files, high enough for per-pixel zoom detail.
const peaksRate = 8000

// peaksHandler decodes the audio to mono s16le PCM via ffmpeg and reduces it to N
// uint8 peak buckets (max |sample| per bucket, scaled to 0-255) - the raw material
// for the interactive waveform widget (zoom/scroll re-renders client-side without
// touching ffmpeg again). Returns base64 buckets + the exact decoded duration.
func peaksHandler(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var p struct {
		Path    string `json:"path"`
		Buckets int    `json:"buckets"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
		return nil, fmt.Errorf("missing path")
	}
	if p.Buckets <= 0 {
		p.Buckets = 8192
	}
	if p.Buckets > 1<<16 {
		p.Buckets = 1 << 16
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-loglevel", "error",
		"-i", p.Path, "-map", "a:0", "-ac", "1", "-ar", strconv.Itoa(peaksRate), "-f", "s16le", "-")
	prepareCmd(cmd)
	pcm, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("peaks decode: %w", err)
	}
	samples := len(pcm) / 2
	if samples == 0 {
		return nil, fmt.Errorf("no audio decoded")
	}
	peaks := bucketPeaks(pcm, p.Buckets)
	return json.Marshal(map[string]any{
		"peaks":           base64.StdEncoding.EncodeToString(peaks),
		"durationSeconds": float64(samples) / peaksRate,
	})
}

// bucketPeaks folds little-endian s16 PCM into n uint8 max-abs buckets.
func bucketPeaks(pcm []byte, n int) []byte {
	samples := len(pcm) / 2
	if samples < n {
		n = samples
	}
	out := make([]byte, n)
	for b := 0; b < n; b++ {
		lo, hi := b*samples/n, (b+1)*samples/n
		peak := 0
		for i := lo; i < hi; i++ {
			v := int(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
		if peak > 32767 { // |-32768|
			peak = 32767
		}
		out[b] = byte(peak >> 7) // 0-32767 → 0-255
	}
	return out
}

// envDecodeRate is the PCM decode rate for RMS envelopes - enough for ~10 ms
// alignment precision while keeping a multi-hour decode cheap.
const envDecodeRate = 4000

// envelopeHandler decodes the first audio stream (works for video files too) to mono s16le PCM
// and folds it into fixed-rate RMS buckets - the raw material for cross-recording time alignment
// (setalign). Streamed: only the per-bucket accumulator + the growing envelope are held (a 2 h
// set at 50 Hz ≈ 360k float32). Returns little-endian float32 buckets, base64.
func envelopeHandler(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var p struct {
		Path   string  `json:"path"`
		RateHz float64 `json:"rateHz"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
		return nil, fmt.Errorf("missing path")
	}
	if p.RateHz <= 0 {
		p.RateHz = 50
	}
	if p.RateHz > 200 {
		p.RateHz = 200
	}
	bucketN := int(float64(envDecodeRate) / p.RateHz)
	if bucketN < 1 {
		bucketN = 1
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-loglevel", "error",
		"-i", p.Path, "-map", "a:0", "-ac", "1", "-ar", strconv.Itoa(envDecodeRate), "-f", "s16le", "-")
	prepareCmd(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var (
		env     []float32
		sumsq   float64
		inBkt   int
		samples int64
		buf     = make([]byte, 64<<10)
		carry   byte
		hasCar  bool
	)
	for {
		n, rerr := stdout.Read(buf)
		chunk := buf[:n]
		if hasCar && n > 0 { // re-join a sample split across reads
			v := float64(int16(uint16(carry)|uint16(chunk[0])<<8)) / 32768.0
			sumsq += v * v
			inBkt++
			samples++
			if inBkt == bucketN {
				env = append(env, float32(math.Sqrt(sumsq/float64(bucketN))))
				sumsq, inBkt = 0, 0
			}
			chunk = chunk[1:]
			hasCar = false
		}
		for len(chunk) >= 2 {
			v := float64(int16(uint16(chunk[0])|uint16(chunk[1])<<8)) / 32768.0
			sumsq += v * v
			inBkt++
			samples++
			if inBkt == bucketN {
				env = append(env, float32(math.Sqrt(sumsq/float64(bucketN))))
				sumsq, inBkt = 0, 0
			}
			chunk = chunk[2:]
		}
		if len(chunk) == 1 {
			carry, hasCar = chunk[0], true
		}
		if rerr != nil {
			break
		}
	}
	if werr := cmd.Wait(); werr != nil && samples == 0 {
		return nil, fmt.Errorf("envelope decode: %w", werr)
	}
	if samples == 0 {
		return nil, fmt.Errorf("no audio decoded")
	}
	raw := make([]byte, 4*len(env))
	for i, v := range env {
		bits := math.Float32bits(v)
		raw[4*i], raw[4*i+1], raw[4*i+2], raw[4*i+3] = byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24)
	}
	return json.Marshal(map[string]any{
		"env":             base64.StdEncoding.EncodeToString(raw),
		"rateHz":          float64(envDecodeRate) / float64(bucketN),
		"durationSeconds": float64(samples) / envDecodeRate,
	})
}

func streamsHandler(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var p pathParams
	if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
		return nil, fmt.Errorf("missing path")
	}
	out, err := ffprobe("-show_streams", "-show_format", "-of", "json", p.Path)
	if err != nil {
		return nil, err
	}
	// Pass ffprobe's JSON through as the result (already structured).
	var probed map[string]any
	if json.Unmarshal([]byte(out), &probed) != nil {
		return nil, fmt.Errorf("ffprobe returned non-JSON")
	}
	return json.Marshal(probed)
}

// ffprobe runs the system ffprobe with a timeout. Returns stdout or a clear error when
// ffprobe is not installed (so the caller can surface "install ffmpeg" rather than hang).
func ffprobe(args ...string) (string, error) {
	bin, ok := mediatools.Resolve("ffprobe")
	if !ok {
		return "", fmt.Errorf("ffprobe not found (install FFmpeg from Settings → Transcode, or add it to PATH)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	full := append([]string{"-v", "error"}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	prepareCmd(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ffprobe: %w", err)
	}
	return string(out), nil
}

func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
