// Package audiorec captures a chosen audio input device to a lossless (default FLAC) file via
// an ffmpeg dshow capture. One capture is active at a time. It can follow OBS recording
// (auto start/stop) or be driven manually, and on finalize it writes a .cue sidecar + embeds
// the played tracklist (from the session recorder) into the file tags, then registers the
// capture in libdb as a native set recording. All collaborators (recorder, libdb, OBS probe)
// are optional - a nil one just disables its slice of behaviour.
package audiorec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/sysexec"
	"rave.page/mate/internal/tagwrite"
)

const source = "audiorec"

// Recorder owns the single active capture + its lifecycle.
type Recorder struct {
	log          *logbus.Bus
	cfg          func() config.AudioRecordFeature
	rec          *recorder.Recorder // tracklist source (may be nil)
	lib          *libdb.DB          // set-recording store (may be nil)
	obsRecording func() bool        // OBS recording probe for FollowOBS (may be nil)

	mu     sync.Mutex
	active *capture
}

// capture is the in-flight ffmpeg process + its metadata.
type capture struct {
	path      string
	device    string
	format    string
	startedAt time.Time
	auto      bool

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *ringWriter
	cancel context.CancelFunc
	done   chan struct{}

	stopping bool // set by stop(): the watcher must not treat the exit as unexpected
}

// Status is the pollable capture state for the UI.
type Status struct {
	Recording bool
	Auto      bool
	Device    string
	Path      string
	StartedAt time.Time
}

// New builds a Recorder. rec/lib/obsRecording may be nil (graceful degrade).
func New(log *logbus.Bus, cfg func() config.AudioRecordFeature, rec *recorder.Recorder, lib *libdb.DB, obsRecording func() bool) *Recorder {
	return &Recorder{log: log, cfg: cfg, rec: rec, lib: lib, obsRecording: obsRecording}
}

// Run is the module lifecycle: a 1s loop that mirrors OBS recording state onto the capture
// (when FollowOBS) and stops any active capture on ctx cancel. Manual start/stop works
// regardless. Returns nil on ctx.Done.
func (r *Recorder) Run(ctx context.Context) error {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	prevOBS := false
	for {
		select {
		case <-ctx.Done():
			_ = r.stop()
			return nil
		case <-t.C:
			c := r.cfg()
			if !c.FollowOBS || r.obsRecording == nil {
				prevOBS = false
				continue
			}
			now := r.obsRecording()
			switch {
			case now && !prevOBS:
				if err := r.start(true); err != nil {
					r.log.Warn(source, "obs-follow start failed", map[string]any{"error": err.Error()})
				}
			case !now && prevOBS:
				if err := r.stop(); err != nil {
					r.log.Warn(source, "obs-follow stop failed", map[string]any{"error": err.Error()})
				}
			}
			prevOBS = now
		}
	}
}

// StartManual starts a capture (auto=false). Errors if already recording or no device set.
func (r *Recorder) StartManual() error {
	r.mu.Lock()
	running := r.active != nil
	r.mu.Unlock()
	if running {
		return fmt.Errorf("audiorec: already recording")
	}
	if strings.TrimSpace(r.cfg().Device) == "" {
		return fmt.Errorf("audiorec: no device configured")
	}
	return r.start(false)
}

// StopManual stops the active capture. Errors if not recording.
func (r *Recorder) StopManual() error {
	r.mu.Lock()
	running := r.active != nil
	r.mu.Unlock()
	if !running {
		return fmt.Errorf("audiorec: not recording")
	}
	return r.stop()
}

// Status reports the current capture state.
func (r *Recorder) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return Status{}
	}
	a := r.active
	return Status{Recording: true, Auto: a.auto, Device: a.device, Path: a.path, StartedAt: a.startedAt}
}

// start launches a capture. No-op (nil) if one is already running.
func (r *Recorder) start(auto bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		return nil
	}
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return fmt.Errorf("audiorec: ffmpeg not found (install it or add to PATH)")
	}
	c := r.cfg()
	device := strings.TrimSpace(c.Device)
	if device == "" {
		return fmt.Errorf("audiorec: no device configured")
	}
	format := strings.ToLower(c.ResolvedFormat())
	dir := c.ResolvedDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("audiorec: create recordings dir: %w", err)
	}
	started := time.Now()
	outPath := filepath.Join(dir, recordingName(started, format))
	args := captureArgs(device, outPath, format, c.ResolvedBitrate(), c.SampleRate)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("audiorec: stdin pipe: %w", err)
	}
	ring := newRingWriter(8 << 10)
	cmd.Stderr = ring
	sysexec.Hide(cmd)
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("audiorec: start ffmpeg: %w", err)
	}
	a := &capture{
		path: outPath, device: device, format: format, startedAt: started, auto: auto,
		cmd: cmd, stdin: stdin, stderr: ring, cancel: cancel, done: make(chan struct{}),
	}
	r.active = a
	go r.watch(a)
	r.log.Info(source, "capture started", map[string]any{"path": outPath, "device": device, "format": format, "auto": auto})
	return nil
}

// watch waits for the process to exit; an exit not triggered by stop() is unexpected.
func (r *Recorder) watch(a *capture) {
	_ = a.cmd.Wait()
	close(a.done)
	r.mu.Lock()
	stopping := a.stopping
	current := r.active == a
	r.mu.Unlock()
	if stopping {
		return // stop() owns finalize + clear
	}
	r.log.Warn(source, "ffmpeg exited unexpectedly", map[string]any{"tail": a.stderr.String()})
	if current {
		r.finalize(a)
		r.mu.Lock()
		if r.active == a {
			r.active = nil
		}
		r.mu.Unlock()
	}
}

// stop finalizes the active capture: graceful "q" → 6s wait → KillTree fallback. No-op if idle.
func (r *Recorder) stop() error {
	r.mu.Lock()
	a := r.active
	if a == nil {
		r.mu.Unlock()
		return nil
	}
	a.stopping = true
	r.mu.Unlock()

	_, _ = io.WriteString(a.stdin, "q\n") // graceful container finalize (FLAC/MP4 trailer)
	_ = a.stdin.Close()

	select {
	case <-a.done:
	case <-time.After(6 * time.Second):
		sysexec.KillTree(a.cmd.Process)
		<-a.done
	}
	a.cancel()

	r.finalize(a)

	r.mu.Lock()
	if r.active == a {
		r.active = nil
	}
	r.mu.Unlock()
	return nil
}

// finalize stats the file, writes a .cue + tags from the tracklist, and registers the capture
// in libdb. All steps are best-effort (logged, never fatal).
func (r *Recorder) finalize(a *capture) {
	ended := time.Now()
	var bytes int64
	if fi, err := os.Stat(a.path); err == nil {
		bytes = fi.Size()
	}

	var recID string
	var tracks []recorder.Track
	setName := strings.TrimSuffix(filepath.Base(a.path), filepath.Ext(a.path))
	if r.rec != nil {
		if found, ok := r.rec.FindByWindow(a.startedAt, ended); ok {
			recID = found.ID
			tracks = found.Tracks
			if found.Name != "" {
				setName = found.Name
			}
		} else if act := r.rec.Active(); act != nil {
			recID = act.ID
			tracks = act.Tracks
			if act.Name != "" {
				setName = act.Name
			}
		}
	}

	if len(tracks) > 0 {
		artist := artistField(tracks)
		cuePath := strings.TrimSuffix(a.path, filepath.Ext(a.path)) + ".cue"
		cue := cueSheet(setName, artist, filepath.Base(a.path), a.startedAt, tracks)
		if err := os.WriteFile(cuePath, []byte(cue), 0o644); err != nil {
			r.log.Warn(source, "write cue failed", map[string]any{"error": err.Error()})
		}
		if r.cfg().WriteTags && (a.format == "flac" || a.format == "mp3") {
			err := tagwrite.Write(a.path, tagwrite.Tags{
				tagwrite.FieldArtist:  artist,
				tagwrite.FieldTitle:   setName,
				tagwrite.FieldAlbum:   "rave.page set",
				tagwrite.FieldComment: commentBody(a.startedAt, tracks),
			})
			if err != nil {
				r.log.Warn(source, "write tags failed", map[string]any{"error": err.Error()})
			}
		}
	}

	if r.lib != nil {
		err := r.lib.SaveSetRecording(libdb.SetRecording{
			ID:          "native-" + randHex(8),
			RecordingID: recID,
			Path:        a.path,
			Format:      a.format,
			Kind:        libdb.SetKindNative,
			StartedAt:   a.startedAt,
			EndedAt:     ended,
			Bytes:       bytes,
		})
		if err != nil {
			r.log.Warn(source, "save set recording failed", map[string]any{"error": err.Error()})
		}
	}
	r.log.Info(source, "capture finalized", map[string]any{"path": a.path, "bytes": bytes, "tracks": len(tracks)})
}

// Devices enumerates Windows dshow audio inputs. ffmpeg prints the list to stderr and exits
// non-zero - that's expected; we parse stderr regardless.
func (r *Recorder) Devices() ([]string, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("audiorec: device enumeration is Windows-only")
	}
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, fmt.Errorf("audiorec: ffmpeg not found")
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	sysexec.Hide(cmd)
	var buf strings.Builder
	cmd.Stderr = &buf
	_ = cmd.Run() // non-zero exit is expected
	return parseDshowAudioDevices(buf.String()), nil
}

// --- pure helpers (unit-tested) ---

// extFor maps a logical format to its container extension.
func extFor(format string) string {
	switch strings.ToLower(format) {
	case "wav":
		return ".wav"
	case "mp3":
		return ".mp3"
	case "aac":
		return ".m4a"
	default: // flac
		return ".flac"
	}
}

// recordingName is the OBS-style timestamped output filename.
func recordingName(t time.Time, format string) string {
	return t.Format("2006-01-02 15-04-05") + extFor(format)
}

// captureArgs builds the ffmpeg argv for a dshow capture to outPath.
func captureArgs(device, outPath, format string, bitrate, sampleRate int) []string {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "dshow", "-i", "audio=" + device,
		"-ac", "2",
	}
	if sampleRate > 0 {
		args = append(args, "-ar", fmt.Sprintf("%d", sampleRate))
	}
	switch strings.ToLower(format) {
	case "wav":
		args = append(args, "-c:a", "pcm_s24le")
	case "mp3":
		args = append(args, "-c:a", "libmp3lame", "-b:a", fmt.Sprintf("%dk", bitrate))
	case "aac":
		args = append(args, "-c:a", "aac", "-b:a", fmt.Sprintf("%dk", bitrate))
	default: // flac
		args = append(args, "-c:a", "flac", "-compression_level", "5")
	}
	return append(args, "-y", outPath)
}

// artistField returns the distinct track artists joined, or "Various" when none.
func artistField(tracks []recorder.Track) string {
	var seen = map[string]bool{}
	var out []string
	for _, t := range tracks {
		a := strings.TrimSpace(t.Artist)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	if len(out) == 0 {
		return "Various"
	}
	return strings.Join(out, ", ")
}

// commentBody is the "N tracks" header + one "mm:ss <artist> - <title>" line per track.
func commentBody(start time.Time, tracks []recorder.Track) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d tracks\n", len(tracks))
	for _, t := range tracks {
		off := t.StartedAt.Sub(start)
		artist := strings.TrimSpace(t.Artist)
		title := strings.TrimSpace(t.Title)
		if artist != "" {
			fmt.Fprintf(&b, "%s %s - %s\n", clockMMSS(off), artist, title)
		} else {
			fmt.Fprintf(&b, "%s %s\n", clockMMSS(off), title)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// cueSheet renders a CUE sidecar for the capture + its tracklist.
func cueSheet(setName, performer, audioBasename string, start time.Time, tracks []recorder.Track) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PERFORMER %q\n", cueField(performer))
	fmt.Fprintf(&b, "TITLE %q\n", cueField(setName))
	fmt.Fprintf(&b, "FILE %q WAVE\n", audioBasename)
	for i, t := range tracks {
		fmt.Fprintf(&b, "  TRACK %02d AUDIO\n", i+1)
		fmt.Fprintf(&b, "    TITLE %q\n", cueField(t.Title))
		fmt.Fprintf(&b, "    PERFORMER %q\n", cueField(t.Artist))
		fmt.Fprintf(&b, "    INDEX 01 %s\n", cueIndex(t.StartedAt.Sub(start)))
	}
	return b.String()
}

// cueField sanitizes a value for a CUE quoted token (no embedded double-quotes in the format).
func cueField(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), `"`, `'`)
}

// cueIndex formats a duration as CUE mm:ss:ff (75 frames/sec). Negative clamps to zero.
func cueIndex(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	frames := int((d % time.Second) / (time.Second / 75))
	return fmt.Sprintf("%02d:%02d:%02d", mins, secs, frames)
}

// clockMMSS formats a duration as mm:ss (minutes may exceed 59). Negative clamps to zero.
func clockMMSS(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%02d:%02d", mins, secs)
}

var dshowAudioRe = regexp.MustCompile(`"([^"]+)"\s*\(audio\)`)

// parseDshowAudioDevices extracts deduped human device names from ffmpeg -list_devices stderr.
func parseDshowAudioDevices(stderr string) []string {
	var seen = map[string]bool{}
	var out []string
	for _, m := range dshowAudioRe.FindAllStringSubmatch(stderr, -1) {
		name := m[1]
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// randHex returns n random bytes hex-encoded (2n chars).
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ringWriter keeps the last cap bytes written (for tailing ffmpeg stderr on a crash).
type ringWriter struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newRingWriter(capacity int) *ringWriter { return &ringWriter{cap: capacity} }

func (w *ringWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.cap {
		w.buf = w.buf[len(w.buf)-w.cap:]
	}
	return len(p), nil
}

func (w *ringWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}
