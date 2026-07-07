package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"rave.page/mate/internal/mediatools"
)

// fingerprintHandlers serves the "fingerprint" worker type: a process-isolated
// Chromaprint/fpcalc fingerprinter. ping proves the pipeline; fingerprint.compute
// shells out to fpcalc so a hang or crash can't take down the daemon. fingerprint.segment
// fingerprints a time-slice of a longer file (a per-track span of a captured set).
func fingerprintHandlers() map[string]Handler {
	return map[string]Handler{
		"fingerprint.ping":    fpPingHandler,
		"fingerprint.compute": fpComputeHandler,
		"fingerprint.segment": fpSegmentHandler,
	}
}

func fpPingHandler(_ json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"pong": true, "pid": os.Getpid()})
}

type fpComputeResult struct {
	Fingerprint     string  `json:"fingerprint"`
	DurationSeconds float64 `json:"durationSeconds"`
}

func fpComputeHandler(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var p pathParams
	if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
		return nil, fmt.Errorf("missing path")
	}
	fp, dur, err := runFpcalc(p.Path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fpComputeResult{Fingerprint: fp, DurationSeconds: dur})
}

type fpSegmentParams struct {
	Path          string  `json:"path"`
	OffsetSeconds float64 `json:"offsetSeconds"`
	LengthSeconds float64 `json:"lengthSeconds"` // <=0 => to end of file
}

// fpSegmentHandler fingerprints a time-slice of a file: ffmpeg decodes [offset, offset+len)
// to a temp mono WAV (Chromaprint-friendly 11025 Hz), fpcalc fingerprints it, temp is
// removed. Used to fingerprint each track of a captured set via its offset into the audio.
func fpSegmentHandler(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var p fpSegmentParams
	if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
		return nil, fmt.Errorf("missing path")
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("ravemate-fpseg-%d-%d.wav", os.Getpid(), time.Now().UnixNano()))
	defer func() { _ = os.Remove(tmp) }()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// -ss before -i = fast input seek; -ac 1 -ar 11025 = Chromaprint's working format.
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if p.OffsetSeconds > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", p.OffsetSeconds))
	}
	args = append(args, "-i", p.Path)
	if p.LengthSeconds > 0 {
		args = append(args, "-t", fmt.Sprintf("%.3f", p.LengthSeconds))
	}
	args = append(args, "-ac", "1", "-ar", "11025", "-f", "wav", tmp)
	cmd := exec.CommandContext(ctx, bin, args...)
	prepareCmd(cmd)
	if out, eerr := cmd.CombinedOutput(); eerr != nil {
		return nil, fmt.Errorf("ffmpeg segment: %w: %s", eerr, trimLine(string(out)))
	}
	fp, dur, err := runFpcalc(tmp)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fpComputeResult{Fingerprint: fp, DurationSeconds: dur})
}

// runFpcalc shells out to fpcalc with a 60 s timeout. Returns fingerprint + duration.
func runFpcalc(path string) (fingerprint string, dur float64, err error) {
	bin, ok := mediatools.Resolve("fpcalc")
	if !ok {
		return "", 0, fmt.Errorf("fpcalc not found (install Chromaprint from Settings → Fingerprinting, or add it to PATH)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-json", "-length", "120", path)
	prepareCmd(cmd)
	out, eerr := cmd.Output()
	if eerr != nil {
		return "", 0, fmt.Errorf("fpcalc: %w", eerr)
	}
	return parseFpcalc(out)
}

// parseFpcalc parses fpcalc -json output. Kept separate so tests avoid the binary.
func parseFpcalc(out []byte) (fingerprint string, dur float64, err error) {
	var v struct {
		Duration    float64 `json:"duration"`
		Fingerprint string  `json:"fingerprint"`
	}
	if err = json.Unmarshal(out, &v); err != nil {
		return "", 0, fmt.Errorf("fpcalc: unexpected output: %w", err)
	}
	if v.Fingerprint == "" {
		return "", 0, fmt.Errorf("fpcalc: empty fingerprint in output")
	}
	return v.Fingerprint, v.Duration, nil
}
