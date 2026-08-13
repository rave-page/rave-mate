package worker

// Surface path of the realtime editor preview (SDL_WEBVIEW_SURFACE_DESIGN P4). Two jobs, because
// the two halves have opposite lifetimes:
//
//	vfx.surface → decode | rave-mate-vfx --pipe | (fit overlay) → SHARED TEXTURE
//	vfx.audio   → audio-only fragmented MP4 for the /ms/ tail, i.e. the preview's CLOCK
//
// No x264, no fMP4, no MSE for the picture: that whole leg is what the surface deletes. The audio
// stream stays because the <audio-only> element is the master clock the surface presents against -
// the decision recorded in §6 P4. Splitting the jobs is what keeps the element's make-before-break
// handover (strPend, stall, seek) byte-identical while the VIDEO half is break-before-make: two
// producers cannot share one surface id (both would create `Local\rave-surface-<id>-ctl` and fight
// over the ring generation), so the old picture is retired before the new one is built.
//
// GEOMETRY IS THE CONSUMER'S. The shell child writes the surface's full rect into the control block;
// this job sizes ffmpeg's scale to exactly that, so the present is a 1:1 copy with no scaler on
// either side. A settled rect change ends the job with {"resize":true} - the daemon respawns, which
// is the only way to re-size a chain whose FBOs and plugin instances are built at load.

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/surfacepub"
	"rave.page/mate/internal/sysexec"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/vfx"
)

type vfxSurfaceIn struct {
	Input      string    `json:"input"`
	Surface    string    `json:"surface"` // [data-surface] id the shell child declared
	T          float64   `json:"t"`       // source-time start (seek)
	VF         string    `json:"vf,omitempty"`
	DecodePost string    `json:"decodePost,omitempty"`
	Fit        bool      `json:"fit,omitempty"`
	Chain      vfx.Chain `json:"chain"` // W/H are the FALLBACK; the negotiated rect wins
}

type vfxAudioIn struct {
	Input  string  `json:"input"`
	Output string  `json:"output"` // growing fragmented-MP4 target (/ms/ tails it)
	T      float64 `json:"t"`
	D      float64 `json:"d,omitempty"`
}

const (
	// surfWantWait bounds the wait for the shell child to attach and report its rect. Past it the
	// surface is not there (windowed hosting, headless mirror, visual-hosting fallback) and the
	// daemon must go back to MSE - §5's automatic fallback, which is why this is an EVENT and not a
	// silent default.
	surfWantWait = 12 * time.Second
	// surfSizeSettle: a rect must hold still this long before it counts as a resize. A drag must not
	// end the job once per frame.
	surfSizeSettle = 700 * time.Millisecond
	// surfSizeSlack: sub-pixel rect jitter (dpr rounding) is not a resize.
	surfSizeSlack = 2
)

// vfxSurface runs decode → chain → (fit overlay) → shared texture until cancelled.
func vfxSurface(params json.RawMessage, emit EmitFunc) (json.RawMessage, error) {
	var in vfxSurfaceIn
	if err := json.Unmarshal(params, &in); err != nil || in.Input == "" || in.Surface == "" {
		return nil, fmt.Errorf("missing input/surface")
	}
	if in.Chain.W <= 0 || in.Chain.H <= 0 {
		return nil, fmt.Errorf("missing chain dims")
	}
	if in.Chain.FPS <= 0 {
		in.Chain.FPS = 30
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	exe, err := vfx.ExePath()
	if err != nil {
		return nil, err
	}

	pub, err := surfacepub.Open(in.Surface)
	if err != nil {
		return nil, err
	}
	defer pub.Close()

	// Wait for the consumer's rect. No rect = no surface: say so and let the daemon fall back.
	w, h := 0, 0
	for deadline := time.Now().Add(surfWantWait); time.Now().Before(deadline); {
		if ww, hh := pub.Want(); ww > 0 && hh > 0 {
			w, h = surfacepub.ClampGeometry(ww, hh)
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if w == 0 {
		emit("nosurface", map[string]any{"id": in.Surface})
		return nil, fmt.Errorf("surface %q: no consumer attached in %s", in.Surface, surfWantWait)
	}
	w, h = w&^1, h&^1 // even dims: libx264 is gone but swscale still prefers them
	if err := surfacepub.ValidGeometry(w, h); err != nil {
		return nil, err
	}
	if err := pub.SetGeometry(w, h); err != nil {
		return nil, err
	}
	// The chain keeps the daemon's render-quality size (Chain.W/H, already capped by the preview
	// quality selector) whenever the surface is bigger; the present stage scales it up. Rendering the
	// chain at the element's own pixels instead would silently retire that selector - and cost 1.85x
	// on this rig, enough to drop the picture behind the audio clock.
	cw, ch := in.Chain.W, in.Chain.H
	if ch >= h {
		cw, ch = w, h
	}
	cw, ch = cw&^1, ch&^1
	in.Chain.W, in.Chain.H = cw, ch
	scaling := cw != w || ch != h

	tmp, err := os.MkdirTemp("", "rvfx-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	chainPath, err := in.Chain.WriteFile(tmp)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()

	job := transcode.Job{Input: in.Input, TrimStart: in.T, VF: in.VF}
	// -re always on this path: the picture is paced by the CLOCK (the shell holds a frame whose PTS
	// is ahead of the audio element), so a decoder racing ahead would only fill the 2-slot ring and
	// then block anyway - at the cost of a burst of wasted decode.
	dec := exec.CommandContext(ctx, bin, job.DecodeRawArgsRT(cw, ch, in.Chain.FPS, in.DecodePost)...)
	fx := exec.CommandContext(ctx, exe, "--pipe", chainPath)
	cmds := []*exec.Cmd{dec, fx}
	var ovl *exec.Cmd
	if in.Fit || scaling {
		ovl = exec.CommandContext(ctx, bin, job.PresentRawArgs(cw, ch, w, h, in.Chain.FPS, in.Fit)...)
		cmds = append(cmds, ovl)
	}
	for _, c := range cmds {
		sysexec.Hide(c) // children inherit this worker's kill-on-close job object
	}
	decErr := tailBuf(&dec.Stderr)
	fxErr := tailBuf(&fx.Stderr)
	var ovlErr func() string
	if ovl != nil {
		ovlErr = tailBuf(&ovl.Stderr)
	}

	if fx.Stdin, err = dec.StdoutPipe(); err != nil {
		return nil, err
	}
	frames, err := fx.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if ovl != nil {
		ovl.Stdin = frames
		if frames, err = ovl.StdoutPipe(); err != nil {
			return nil, err
		}
	}
	for i := len(cmds) - 1; i >= 0; i-- { // downstream first: nobody writes into a dead pipe
		if err := cmds[i].Start(); err != nil {
			for _, c := range cmds[i+1:] {
				_ = c.Process.Kill()
				_, _ = c.Process.Wait()
			}
			return nil, fmt.Errorf("start %s: %w", []string{"decode", "vfx", "overlay"}[i], err)
		}
	}

	// Publish loop. One reusable frame - the ring IS the only buffer in this path, and it is bounded
	// (surfacepub.Slots frames / w*h*4*Slots bytes, newest-wins on the producer side).
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	resize := make(chan [2]int, 1)
	stop := make(chan struct{})
	go watchWant(pub, w, h, resize, stop)
	defer close(stop)

	var idx int64
	var pumpErr error
	shown := false
	lastEmit := time.Now()
	fpsN := in.Chain.FPS
	for {
		select {
		case wh := <-resize:
			_ = frames.Close()
			raw, _ := json.Marshal(map[string]any{"resize": true, "w": wh[0], "h": wh[1]})
			return raw, nil
		default:
		}
		if _, rerr := io.ReadFull(frames, img.Pix); rerr != nil {
			if rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
				pumpErr = rerr
			}
			break
		}
		// PTS is FEED time (seconds since the feed's first frame), which is exactly what the audio
		// element's currentTime counts. Wall time would drift the moment a heavy chain falls behind.
		if err := pub.Send(img, int64(float64(idx)/fpsN*1e9)); err != nil {
			pumpErr = err
			break
		}
		idx++
		// The FIRST presented frame is reported the moment it happens, not on the 2 s cadence: it is
		// the click→pixel measurement, and a 2 s sampling window cannot measure a sub-second interval.
		if st := pub.Stats(); time.Since(lastEmit) >= 2*time.Second || (!shown && st.PresentSeq > 0) {
			lastEmit = time.Now()
			shown = shown || st.PresentSeq > 0
			emit("stats", map[string]any{"pub": st, "sec": float64(idx) / fpsN})
		}
	}

	decWait := dec.Wait()
	fxWait := fx.Wait()
	var ovlWait error
	if ovl != nil {
		ovlWait = ovl.Wait()
	}
	if ctx.Err() != nil {
		return json.RawMessage(`{"stopped":true}`), nil
	}
	switch {
	case fxWait != nil:
		return nil, fmt.Errorf("vfx: %v (%s)", fxWait, fxErr())
	case ovlWait != nil:
		return nil, fmt.Errorf("overlay: %v (%s)", ovlWait, ovlErr())
	case decWait != nil:
		return nil, fmt.Errorf("decode: %v (%s)", decWait, decErr())
	case pumpErr != nil:
		return nil, pumpErr
	}
	raw, err := json.Marshal(map[string]any{"frames": idx, "sec": float64(idx) / fpsN})
	return raw, err
}

// watchWant reports a SETTLED rect change. The chain's FBOs and plugin instances are sized at load,
// so a resize cannot be applied in place - it ends the job and the daemon respawns.
func watchWant(pub *surfacepub.Pub, w, h int, out chan<- [2]int, stop <-chan struct{}) {
	pendW, pendH := w, h
	since := time.Time{}
	for {
		select {
		case <-stop:
			return
		case <-time.After(150 * time.Millisecond):
		}
		ww, hh := pub.Want()
		if ww <= 0 || hh <= 0 {
			continue
		}
		ww, hh = surfacepub.ClampGeometry(ww, hh)
		ww, hh = ww&^1, hh&^1
		if abs(ww-w) <= surfSizeSlack && abs(hh-h) <= surfSizeSlack {
			since = time.Time{}
			continue
		}
		if ww != pendW || hh != pendH {
			pendW, pendH, since = ww, hh, time.Now()
			continue
		}
		if !since.IsZero() && time.Since(since) >= surfSizeSettle {
			select {
			case out <- [2]int{ww, hh}:
			default:
			}
			return
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// vfxAudio produces the surface path's clock: an audio-only growing fragmented MP4 the existing
// /ms/ tail + __mst runtime feed into the same <video> element, whose transport (play/pause/seek/
// stall/loop) therefore needs no change at all.
func vfxAudio(params json.RawMessage, emit EmitFunc) (json.RawMessage, error) {
	var in vfxAudioIn
	if err := json.Unmarshal(params, &in); err != nil || in.Input == "" || in.Output == "" {
		return nil, fmt.Errorf("missing input/output")
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(in.Output), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir output: %w", err)
	}
	job := transcode.Job{Input: in.Input, Output: in.Output, TrimStart: in.T}
	if in.D > 0 {
		job.TrimEnd = in.T + in.D
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, job.EncodeAudioStreamArgs(!hasAudioStream(in.Input))...)
	sysexec.Hide(cmd)
	tail := tailBuf(&cmd.Stderr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start audio: %w", err)
	}
	emit("stage", map[string]any{"name": "audio-clock"})
	if werr := cmd.Wait(); werr != nil && ctx.Err() == nil {
		return nil, fmt.Errorf("audio: %v (%s)", werr, tail())
	}
	return json.RawMessage(`{"ok":true}`), nil
}

// hasAudioStream reports whether the source carries audio. A miss is not fatal - it only picks the
// silent-clock variant, and being wrong the other way would produce a zero-stream MP4.
func hasAudioStream(input string) bool {
	out, err := ffprobe("-select_streams", "a:0", "-show_entries", "stream=index", "-of", "csv=p=0", input)
	return err == nil && strings.TrimSpace(out) != ""
}
