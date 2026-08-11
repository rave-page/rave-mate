package worker

// vfx worker: thin orchestrator for the rave-mate-vfx effects child (native/zigvfx).
// Own worker type so a GL/plugin fault can't take a transcode slot down with it.
//   vfx.list    → plugin discovery over the user's vfx dirs
//   vfx.preview → one source frame → chain → PNG (reframe/effect preview)
//   vfx.run     → full export: ffmpeg decode | rave-mate-vfx --pipe | ffmpeg encode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/sysexec"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/vfx"
)

func vfxHandlers() map[string]Handler {
	return map[string]Handler{
		"vfx.ping":    func(json.RawMessage, EmitFunc) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		"vfx.list":    vfxList,
		"vfx.preview": vfxPreview,
		"vfx.run":     vfxRun,
	}
}

type vfxListIn struct {
	Dirs []string `json:"dirs"`
}

func vfxList(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var in vfxListIn
	if err := json.Unmarshal(params, &in); err != nil || len(in.Dirs) == 0 {
		return nil, fmt.Errorf("missing dirs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	plugins, err := vfx.List(ctx, in.Dirs)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"plugins": plugins})
	return raw, err
}

type vfxPreviewIn struct {
	Input  string    `json:"input"`
	T      float64   `json:"t"`   // source-time seconds of the frame
	FxT    float64   `json:"fxT"` // effect-time seconds (clip time, trim already subtracted)
	VF     string    `json:"vf,omitempty"`
	Chain  vfx.Chain `json:"chain"`
	Output string    `json:"output"` // .png target
}

// vfxPreview extracts one frame at T, runs it through the chain, encodes a PNG.
func vfxPreview(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var in vfxPreviewIn
	if err := json.Unmarshal(params, &in); err != nil || in.Input == "" || in.Output == "" {
		return nil, fmt.Errorf("missing input/output")
	}
	if in.Chain.W <= 0 || in.Chain.H <= 0 {
		return nil, fmt.Errorf("missing chain dims")
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	exe, err := vfx.ExePath()
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "rvfx-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	chainPath, err := in.Chain.WriteFile(tmp)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(in.Output), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir output: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	vf := fmt.Sprintf("scale=%d:%d", in.Chain.W, in.Chain.H)
	if in.VF != "" {
		vf = in.VF + "," + vf
	}
	rawIn := filepath.Join(tmp, "in.raw")
	a := []string{"-hide_banner", "-nostats", "-y"}
	if in.T > 0 {
		a = append(a, "-ss", fmt.Sprintf("%.3f", in.T))
	}
	a = append(a, "-i", in.Input, "-frames:v", "1", "-vf", vf,
		"-f", "rawvideo", "-pix_fmt", "rgba", rawIn)
	if err := runQuiet(ctx, bin, a...); err != nil {
		return nil, fmt.Errorf("frame extract: %w", err)
	}

	rawOut := filepath.Join(tmp, "out.raw")
	if err := runQuiet(ctx, exe, "--frame", chainPath, rawIn, rawOut,
		fmt.Sprintf("%.3f", max(in.FxT, 0))); err != nil {
		return nil, fmt.Errorf("vfx chain: %w", err)
	}

	if err := runQuiet(ctx, bin, "-hide_banner", "-nostats", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", in.Chain.W, in.Chain.H),
		"-i", rawOut, "-frames:v", "1", in.Output); err != nil {
		return nil, fmt.Errorf("png encode: %w", err)
	}
	return json.RawMessage(`{"ok":true}`), nil
}

type vfxRunIn struct {
	Input     string            `json:"input"`
	Output    string            `json:"output"`
	Preset    *transcode.Preset `json:"preset,omitempty"`
	PresetID  string            `json:"presetId,omitempty"`
	TrimStart float64           `json:"trimStart,omitempty"`
	TrimEnd   float64           `json:"trimEnd,omitempty"`
	VF        string            `json:"vf,omitempty"`
	Chain     vfx.Chain         `json:"chain"`
}

// vfxRun exports through the effect chain: decode | chain | encode, three
// supervised children, one frame in flight in the middle. Progress = frames
// through the chain vs the clip's expected frame count.
func vfxRun(params json.RawMessage, emit EmitFunc) (json.RawMessage, error) {
	var in vfxRunIn
	if err := json.Unmarshal(params, &in); err != nil || in.Input == "" || in.Output == "" {
		return nil, fmt.Errorf("missing input/output")
	}
	if in.Chain.W <= 0 || in.Chain.H <= 0 {
		return nil, fmt.Errorf("missing chain dims")
	}
	if in.Chain.FPS <= 0 {
		in.Chain.FPS = 30
	}
	var preset transcode.Preset
	switch {
	case in.Preset != nil:
		preset = *in.Preset
	case in.PresetID != "":
		p, ok := transcode.Find(in.PresetID)
		if !ok {
			return nil, fmt.Errorf("unknown preset %q", in.PresetID)
		}
		preset = p
	default:
		return nil, fmt.Errorf("missing preset/presetId")
	}
	preset = transcode.NormalizePreset(preset)
	if preset.EncoderOverride == "" {
		if enc := resolveWorkerEncoder(preset.VideoCodec, preset.Accel); enc != "" {
			preset.EncoderOverride = enc
		}
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	exe, err := vfx.ExePath()
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "rvfx-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	chainPath, err := in.Chain.WriteFile(tmp)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(in.Output), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir output: %w", err)
	}

	// expected frame total for progress (header duration may be absent on
	// unfinalized captures - probe fallback like tcRun)
	clipDur := in.TrimEnd - in.TrimStart
	if clipDur <= 0 {
		if d := probeDurationSec(in.Input); d > 0 {
			clipDur = max(d-in.TrimStart, 0)
		}
	}
	totalFrames := clipDur * in.Chain.FPS

	job := transcode.Job{Input: in.Input, Output: in.Output, Preset: preset,
		TrimStart: in.TrimStart, TrimEnd: in.TrimEnd, VF: in.VF}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	dec := exec.CommandContext(ctx, bin, job.DecodeRawArgs(in.Chain.W, in.Chain.H, in.Chain.FPS)...)
	fx := exec.CommandContext(ctx, exe, "--pipe", chainPath)
	enc := exec.CommandContext(ctx, bin, job.EncodeRawArgs(in.Chain.W, in.Chain.H, in.Chain.FPS)...)
	for _, c := range []*exec.Cmd{dec, fx, enc} {
		sysexec.Hide(c) // children inherit this worker's kill-on-close job object
	}

	decErr := tailBuf(&dec.Stderr)
	fxErr := tailBuf(&fx.Stderr)
	encErr := tailBuf(&enc.Stderr)

	fx.Stdin, err = dec.StdoutPipe()
	if err != nil {
		return nil, err
	}
	fxOut, err := fx.StdoutPipe()
	if err != nil {
		return nil, err
	}
	encIn, err := enc.StdinPipe()
	if err != nil {
		return nil, err
	}

	emit("stage", map[string]any{"name": "fx-encode"})
	if err := enc.Start(); err != nil {
		return nil, fmt.Errorf("start encoder: %w", err)
	}
	if err := fx.Start(); err != nil {
		_ = enc.Process.Kill()
		_, _ = enc.Process.Wait()
		return nil, fmt.Errorf("start vfx: %w", err)
	}
	if err := dec.Start(); err != nil {
		_ = fx.Process.Kill()
		_ = enc.Process.Kill()
		_, _ = fx.Process.Wait()
		_, _ = enc.Process.Wait()
		return nil, fmt.Errorf("start decoder: %w", err)
	}

	// pump chain→encoder, counting frames for progress
	frameBytes := int64(in.Chain.W) * int64(in.Chain.H) * 4
	var pumped int64
	lastPct := -1.0
	buf := make([]byte, 1<<20)
	var pumpErr error
	for {
		n, rerr := fxOut.Read(buf)
		if n > 0 {
			if _, werr := encIn.Write(buf[:n]); werr != nil {
				pumpErr = fmt.Errorf("encoder pipe: %w", werr)
				break
			}
			pumped += int64(n)
			if totalFrames > 0 {
				pct := min(float64(pumped/frameBytes)/totalFrames, 1) * 100
				if pct-lastPct >= 1 {
					lastPct = pct
					emit("progress", map[string]any{"percent": pct})
				}
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				pumpErr = rerr
			}
			break
		}
	}
	_ = encIn.Close()

	decWait := dec.Wait()
	fxWait := fx.Wait()
	encWait := enc.Wait()
	if encWait != nil || fxWait != nil || decWait != nil || pumpErr != nil {
		_ = os.Remove(in.Output)
		switch {
		case encWait != nil:
			return nil, fmt.Errorf("encode: %v (%s)", encWait, encErr())
		case fxWait != nil:
			return nil, fmt.Errorf("vfx: %v (%s)", fxWait, fxErr())
		case decWait != nil:
			return nil, fmt.Errorf("decode: %v (%s)", decWait, decErr())
		default:
			return nil, pumpErr
		}
	}
	emit("progress", map[string]any{"percent": 100.0})
	frames := int64(0)
	if frameBytes > 0 {
		frames = pumped / frameBytes
	}
	raw, err := json.Marshal(map[string]any{"output": in.Output, "frames": frames})
	return raw, err
}

// runQuiet runs cmd hidden, returning stderr's tail on failure.
func runQuiet(ctx context.Context, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	sysexec.Hide(cmd)
	tail := tailBuf(&cmd.Stderr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w (%s)", err, tail())
	}
	return nil
}

// tailBuf wires a bounded stderr capture into dst; the returned func yields the tail.
func tailBuf(dst *io.Writer) func() string {
	b := &boundedTail{}
	*dst = b
	return b.String
}

// boundedTail keeps the last <=2KiB written (newest-wins; error text lives at the end).
type boundedTail struct{ tail []byte }

func (b *boundedTail) Write(p []byte) (int, error) {
	const keep = 2048
	b.tail = append(b.tail, p...)
	if len(b.tail) > keep {
		b.tail = b.tail[len(b.tail)-keep:]
	}
	return len(p), nil
}

func (b *boundedTail) String() string {
	s := strings.TrimSpace(string(b.tail))
	if len(s) > 400 {
		s = s[len(s)-400:]
	}
	return strings.ReplaceAll(s, "\n", " | ")
}
