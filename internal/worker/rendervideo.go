package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/motionrender"
	"rave.page/mate/internal/mp4meta"
	"rave.page/mate/internal/vrm"
	"rave.page/mate/internal/vrmdyn"
	"rave.page/mate/internal/vrmik"
	"rave.page/mate/internal/vrmotion"
)

// renderHandlers serves the "render" worker type: offline motion→video renders (pure-CPU
// raster frames piped into ffmpeg). Process-isolated like transcode - a runaway encode
// can't take down the daemon.
func renderHandlers() map[string]Handler {
	return map[string]Handler{
		"render.ping":        tcPing,
		"render.motionvideo": rvMotionVideo,
	}
}

type rvIn struct {
	Recording          string  `json:"recording"`          // take .json path (required)
	Avatar             string  `json:"avatar"`             // VRM path; "" = stick figure
	Mode               string  `json:"mode"`               // "orbit" (default) | "equirect"
	Width              int     `json:"width"`              // default 1280 / 2048 (equirect)
	Height             int     `json:"height"`             // default 720 / 1024 (equirect)
	FPS                int     `json:"fps"`                // default 30
	Out                string  `json:"out"`                // mp4 path (required)
	OrbitSecondsPerRev float64 `json:"orbitSecondsPerRev"` // default 12
	Physics            *bool   `json:"physics"`            // secondary-motion sim; nil/true = on
}

// rvTrailMax bounds the head-trail polyline per frame (decimated, not accumulated).
const rvTrailMax = 2048

// rvMotionVideo renders a recorded VR motion take to an MP4: orbit (perspective camera
// circling the scene) or equirect (true 360° equirectangular). Frames stream straight
// into ffmpeg stdin (one frame in flight - no buffering).
func rvMotionVideo(params json.RawMessage, emit EmitFunc) (json.RawMessage, error) {
	var in rvIn
	if err := json.Unmarshal(params, &in); err != nil || in.Recording == "" || in.Out == "" {
		return nil, fmt.Errorf("missing recording/out")
	}
	if in.Mode == "" {
		in.Mode = "orbit"
	}
	if in.Mode != "orbit" && in.Mode != "equirect" {
		return nil, fmt.Errorf("unknown mode %q (want orbit|equirect)", in.Mode)
	}
	if in.FPS <= 0 {
		in.FPS = 30
	}
	if in.Width <= 0 || in.Height <= 0 {
		if in.Mode == "equirect" {
			in.Width, in.Height = 2048, 1024
		} else {
			in.Width, in.Height = 1280, 720
		}
	}
	in.Width, in.Height = in.Width&^1, in.Height&^1 // yuv420p needs even dims
	if in.OrbitSecondsPerRev <= 0 {
		in.OrbitSecondsPerRev = 12
	}

	rec, err := vrmotion.Load(in.Recording)
	if err != nil {
		return nil, fmt.Errorf("load recording: %w", err)
	}
	if len(rec.Frames) == 0 {
		return nil, fmt.Errorf("empty recording")
	}
	var model *vrm.Model
	if in.Avatar != "" {
		if model, err = vrm.Load(in.Avatar); err != nil {
			return nil, fmt.Errorf("load avatar: %w", err)
		}
	}
	var dyn *vrmdyn.State // hair/tail physics: sidecar params if present, else heuristic
	if model != nil && (in.Physics == nil || *in.Physics) {
		dyn = vrmdyn.NewStateFromFile(model, in.Avatar)
	}
	var rt *vrmik.Retarget // take calibration: recenter/scale + role-correct limbs
	if model != nil {
		rt = vrmik.Calibrate(model, rec)
	}
	frameDT := 1 / float64(in.FPS)

	player := vrmotion.NewPlayer(rec)
	dur := player.Duration()
	nFrames := int(dur*float64(in.FPS)) + 1 // t = 0..dur step 1/fps

	lo, hi, trail, headY := rvSceneStats(rec)

	// mode setup
	var cam motionrender.Camera
	var eq *motionrender.EquirectRenderer
	var eqf motionrender.EqFrame
	if in.Mode == "orbit" {
		cam.Pitch = 0.35 // fixed gentle pitch
		if model != nil {
			cam.FrameModel(model)
		} else {
			rvFrameBounds(&cam, lo, hi, 1.4, 1.2) // webui orbitCam.frame math
		}
	} else {
		eq = motionrender.NewEquirectRenderer(in.Width, in.Height)
		eqf = motionrender.EqFrame{
			Eye:    [3]float32{(lo[0] + hi[0]) / 2, headY, (lo[2] + hi[2]) / 2},
			Center: [3]float32{(lo[0] + hi[0]) / 2, 0, (lo[2] + hi[2]) / 2},
			FloorY: lo[1],
			GridR:  float32(math.Max(1, float64((hi[0]-lo[0]+hi[2]-lo[2])/2))),
			Model:  model,
			Name:   rec.Name,
		}
		if model != nil {
			mlo, _ := model.Bounds()
			eqf.FloorY = mlo[1] // avatar-space floor (feet), not tracker min
		}
	}

	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(in.Out), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir output: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"-hide_banner", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", in.Width, in.Height),
		"-r", strconv.Itoa(in.FPS), "-i", "-",
		"-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart",
		in.Out)
	prepareCmd(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}
	lastErr := ""
	errDone := make(chan struct{})
	go func() { // keep only the last non-empty stderr line (bounded)
		defer close(errDone)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		sc.Split(scanFFmpegLines)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				lastErr = t
			}
		}
	}()

	var writeErr error
	lastPct := -1.0
	for i := 0; i < nFrames; i++ {
		t := float64(i) / float64(in.FPS)
		sample := player.Sample(t)
		tr := rvTrailUpTo(trail, t)
		var img []byte
		if in.Mode == "orbit" {
			cam.Yaw = float32(2 * math.Pi * t / in.OrbitSecondsPerRev)
			img = motionrender.Render(motionrender.Frame{
				W: in.Width, H: in.Height, Cam: cam,
				Model: model, Sample: sample, Trail: tr, Name: rec.Name,
				Dyn: dyn, DT: frameDT, RT: rt,
			}).Pix
		} else {
			eqf.Sample, eqf.Trail = sample, tr
			eqf.Dyn, eqf.DT, eqf.RT = dyn, frameDT, rt
			img = eq.Render(eqf).Pix
		}
		if _, werr := stdin.Write(img); werr != nil {
			writeErr = werr // ffmpeg died - surface its stderr below
			break
		}
		if pct := float64(i+1) / float64(nFrames) * 100; pct-lastPct >= 1 || i == nFrames-1 {
			lastPct = pct
			emit("progress", map[string]any{
				"percent": pct, "timeSec": t,
				"frame": i + 1, "frames": nFrames, "phase": "render",
			})
		}
	}
	emit("progress", map[string]any{
		"percent": 100, "frame": nFrames, "frames": nFrames, "phase": "encode",
	})
	_ = stdin.Close()
	<-errDone
	if err := cmd.Wait(); err != nil || writeErr != nil {
		return nil, fmt.Errorf("ffmpeg failed: %s", lastErr)
	}
	spherical, sphErr := rvFinalize(in.Mode, in.Out)
	res := map[string]any{
		"out": in.Out, "frames": nFrames, "duration_s": dur, "mode": in.Mode,
		"spherical_metadata": spherical,
	}
	if sphErr != nil { // encode succeeded; tag failure is a warning, not a render failure
		res["spherical_error"] = sphErr.Error()
	}
	return json.Marshal(res)
}

// rvFinalize post-processes the encoded MP4: equirect renders get the Spherical Video
// V1 uuid box injected (players enable 360° look-around); flat orbit renders untouched.
func rvFinalize(mode, out string) (spherical bool, err error) {
	if mode != "equirect" {
		return false, nil
	}
	if err := mp4meta.InjectSphericalV1(out); err != nil {
		return false, err
	}
	return true, nil
}

// rvSceneStats scans the take once: pose AABB, decimated head trail (≤rvTrailMax points,
// with timestamps for progressive reveal) and mean head height.
func rvSceneStats(rec *vrmotion.Recording) (lo, hi [3]float32, trail []rvTrailPt, headY float32) {
	lo = [3]float32{1e9, 1e9, 1e9}
	hi = [3]float32{-1e9, -1e9, -1e9}
	heads := 0
	for _, fr := range rec.Frames {
		for _, p := range fr.Poses {
			for k := range 3 {
				lo[k] = float32(math.Min(float64(lo[k]), float64(p.Pos[k])))
				hi[k] = float32(math.Max(float64(hi[k]), float64(p.Pos[k])))
			}
		}
		if h, ok := fr.Poses[0]; ok {
			trail = append(trail, rvTrailPt{t: fr.T, pos: h.Pos})
			headY += h.Pos[1]
			heads++
		}
	}
	if heads > 0 {
		headY /= float32(heads)
	} else {
		headY = (lo[1] + hi[1]) / 2
	}
	if stride := len(trail)/rvTrailMax + 1; stride > 1 {
		kept := trail[:0]
		for i := 0; i < len(trail); i += stride {
			kept = append(kept, trail[i])
		}
		trail = kept
	}
	return lo, hi, trail, headY
}

type rvTrailPt struct {
	t   float64
	pos [3]float32
}

// rvTrailUpTo returns the head path revealed up to time t (positions only).
func rvTrailUpTo(trail []rvTrailPt, t float64) [][3]float32 {
	n := 0
	for n < len(trail) && trail[n].t <= t {
		n++
	}
	out := make([][3]float32, n)
	for i := range n {
		out[i] = trail[i].pos
	}
	return out
}

// rvFrameBounds fits the orbit camera on an AABB (webui orbitCam.frame: mul/add tune dist).
func rvFrameBounds(c *motionrender.Camera, lo, hi [3]float32, mul, add float32) {
	c.Center = [3]float32{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	c.FloorY = lo[1]
	dx, dy, dz := hi[0]-lo[0], hi[1]-lo[1], hi[2]-lo[2]
	diag := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
	c.GridR = float32(math.Max(1, float64((dx+dz)/2)))
	c.Dist = diag*mul + add
}
