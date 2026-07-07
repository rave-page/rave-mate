package stt

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

// We reuse the app's ffmpeg (the same dshow capture path internal/audiorec uses) to record the
// mic - no new audio dependency. ffmpeg captures the selected dshow input and pipes raw 16kHz mono
// s16le PCM to stdout, which we read for VAD + buffering.

var dshowAudioRe = regexp.MustCompile(`"([^"]+)"\s*\(audio\)`)

// InputDevices lists Windows dshow audio input device names (ffmpeg prints them to stderr).
func InputDevices() ([]string, error) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, fmt.Errorf("ffmpeg not found (install it in Settings)")
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	sysexec.Hide(cmd)
	out, _ := cmd.CombinedOutput() // ffmpeg exits non-zero after listing; output is what we want
	seen := map[string]bool{}
	var names []string
	for _, m := range dshowAudioRe.FindAllStringSubmatch(string(out), -1) {
		if n := m[1]; n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Capture is an in-flight ffmpeg mic capture streaming s16le PCM from Read.
type Capture struct {
	cmd  *exec.Cmd
	pipe io.ReadCloser
	r    *bufio.Reader
}

// StartCapture begins capturing the named dshow input as 16kHz mono s16le PCM. Empty device =
// ffmpeg's default. Read the PCM via the returned Capture; call Stop when done.
func StartCapture(ctx context.Context, device string) (*Capture, error) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, fmt.Errorf("ffmpeg not found (install it in Settings)")
	}
	dev := "audio=" + device
	if device == "" {
		dev = "audio=default"
	}
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "dshow", "-i", dev,
		"-ar", fmt.Sprintf("%d", SampleRate), "-ac", "1",
		"-f", "s16le", "-") // raw PCM to stdout
	sysexec.Hide(cmd)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg capture: %w", err)
	}
	return &Capture{cmd: cmd, pipe: pipe, r: bufio.NewReaderSize(pipe, 64*1024)}, nil
}

// Read returns raw s16le PCM bytes.
func (c *Capture) Read(p []byte) (int, error) { return c.r.Read(p) }

// Stop ends the capture (kills ffmpeg + its tree) and waits for it to exit.
func (c *Capture) Stop() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	sysexec.KillTree(c.cmd.Process)
	_ = c.pipe.Close()
	_ = c.cmd.Wait()
	return nil
}
