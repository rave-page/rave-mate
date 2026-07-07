package webui

// Motion tab state + acts. Playback streams OSC/VMC from a daemon goroutine at 30fps
// (the real motion path - no UI redraw in the loop); the stick-figure preview animates
// via SMIL (moSkeletonAnim), the avatar-mesh preview via moRunPreview (15fps JPEG
// stream); onLiveTick only updates the clock label. Low-throughput OSC packets +
// bounded preview raster = in-proc carve-out (same as the Fyne dialog it replaces).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/motionrender"
	"rave.page/mate/internal/osc"
	"rave.page/mate/internal/vmc"
	"rave.page/mate/internal/vrccampaths"
	"rave.page/mate/internal/vrm"
	"rave.page/mate/internal/vrmdyn"
	"rave.page/mate/internal/vrmik"
	"rave.page/mate/internal/vrmotion"
)

type moSt struct {
	mu      sync.Mutex
	section string // "campaths" | "studio"

	// camera paths
	cpPaths  []vrccampaths.Path
	cpSel    int
	cpPts    []vrccampaths.Point
	cpCam    orbitCam
	cpDnYaw  float32
	cpDnPit  float32
	cpDnX    float32
	cpDnY    float32
	cpLoaded bool

	// motion studio
	recNames []string
	recName  string
	rec      *vrmotion.Recording
	player   *vrmotion.Player
	t        float64
	playing  bool
	loop     bool
	oscOn    bool
	vmcOn    bool
	model    *vrm.Model // loaded avatar mesh (nil until "Show avatar model")
	modelOn  bool
	dyn      *vrmdyn.State   // spring-chain physics (hair/tail/accessories); nil until model load
	physOn   bool            // avatar physics toggle (default on at model load)
	rt       *vrmik.Retarget // per-take calibration (recenter/scale/roles); recomputed on take/model change
	restPose bool            // render the mesh at rest (A/T reference) instead of the take pose
	marks    bool            // overlay raw tracker points on the mesh (compare take vs pose)
	cam      orbitCam

	// render-video modal + job
	rMode     string // "orbit" | "equirect"
	rHigh     bool
	rFPS      int
	rendering bool
	rPct      float64 // live job progress (worker "progress" events)
	rFrame    int
	rFrames   int
	rPhase    string // "render" | "encode"
	rDone     string // completion/failure note shown after the job ends ("" = none)
	rOK       bool
	dnYaw     float32
	dnPit     float32
	dnX       float32
	dnY       float32

	stop chan struct{} // playback goroutine (nil = not running)
}

var (
	moMu  sync.Mutex
	moMap = map[*UI]*moSt{}
)

func (u *UI) mo() *moSt {
	moMu.Lock()
	defer moMu.Unlock()
	s := moMap[u]
	if s == nil {
		s = &moSt{section: "campaths", cpSel: -1, cpCam: newOrbitCam(), cam: newOrbitCam()}
		moMap[u] = s
	}
	return s
}

func init() {
	onPrefix("mo-section:", func(u *UI, m actMsg) {
		u.moSet(func(s *moSt) { s.section = strings.TrimPrefix(m.Act, "mo-section:") })
	})
	// camera paths
	onExact("mo-cp-refresh", func(u *UI, m actMsg) { u.moCPRefresh(true) })
	onPrefix("mo-cp-sel:", func(u *UI, m actMsg) {
		if i, err := strconv.Atoi(strings.TrimPrefix(m.Act, "mo-cp-sel:")); err == nil {
			u.moCPSelect(i)
		}
	})
	onExact("mo-cp-orbit", func(u *UI, m actMsg) { u.moOrbit(m.Val, true) })
	onExact("mo-cp-zoom", func(u *UI, m actMsg) { u.moZoom(m.Val, true) })
	onExact("mo-cp-load", func(u *UI, m actMsg) { u.moCPLoad() })
	onExact("mo-cp-copy", func(u *UI, m actMsg) { u.moCPCopy() })
	onExact("mo-cp-organize", func(u *UI, m actMsg) {
		if u.svc.VRCTools != nil {
			_, n := u.svc.VRCTools.OrganizeNow()
			u.toast(i18n.Tn("motion.toast.organized", n))
			u.moCPRefresh(true)
		}
	})
	onExact("mo-cp-dj", func(u *UI, m actMsg) {
		if u.svc.VRCTools == nil {
			return
		}
		n, dst, err := u.svc.VRCTools.InstallBuiltinPaths()
		if err != nil {
			u.toast(i18n.T("motion.toast.djPaths") + err.Error())
			return
		}
		u.toast(i18n.Tn("motion.toast.installedPaths", n, i18n.A{"dst": dst}))
		u.moCPRefresh(true)
	})
	// motion studio
	onExact("mo-rec-refresh", func(u *UI, m actMsg) { u.moRecRefresh(true) })
	onPrefix("mo-rec-sel:", func(u *UI, m actMsg) { u.moRecSelect(strings.TrimPrefix(m.Act, "mo-rec-sel:")) })
	onExact("mo-orbit", func(u *UI, m actMsg) { u.moOrbit(m.Val, false) })
	onExact("mo-zoom", func(u *UI, m actMsg) { u.moZoom(m.Val, false) })
	onExact("mo-scrub", func(u *UI, m actMsg) { u.moScrub(m.Val) })
	onExact("mo-play", func(u *UI, m actMsg) { u.moPlayPause() })
	onExact("mo-stop", func(u *UI, m actMsg) { u.moStop() })
	onExact("mo-loop", func(u *UI, m actMsg) { u.moSetQuiet(func(s *moSt) { s.loop = m.Val == "true" }) })
	onExact("mo-osc", func(u *UI, m actMsg) { u.moSetQuiet(func(s *moSt) { s.oscOn = m.Val == "true" }) })
	onExact("mo-vmc", func(u *UI, m actMsg) { u.moSetQuiet(func(s *moSt) { s.vmcOn = m.Val == "true" }) })
	onExact("mo-model", func(u *UI, m actMsg) { u.moModelToggle(m.Val == "true") })
	onExact("mo-phys", func(u *UI, m actMsg) {
		u.moSetQuiet(func(s *moSt) {
			s.physOn = m.Val == "true"
			if s.dyn != nil {
				s.dyn.Reset() // re-seed at the animated pose on toggle
			}
		})
	})
	onExact("mo-rest", func(u *UI, m actMsg) {
		u.moSetQuiet(func(s *moSt) {
			s.restPose = m.Val == "true"
			if s.dyn != nil {
				s.dyn.Reset() // pose jumps to/from rest - re-seed the chains
			}
		})
		u.moPatchBody()
	})
	onExact("mo-marks", func(u *UI, m actMsg) {
		u.moSetQuiet(func(s *moSt) { s.marks = m.Val == "true" })
		u.moPatchBody()
	})
	onExact("mo-render", func(u *UI, m actMsg) { u.moRenderModal() })
	onPrefix("mo-render-mode:", func(u *UI, m actMsg) {
		u.moSetQuiet(func(s *moSt) { s.rMode = m.arg("mo-render-mode:") })
		u.moRenderModal()
	})
	onPrefix("mo-render-q:", func(u *UI, m actMsg) {
		u.moSetQuiet(func(s *moSt) { s.rHigh = m.arg("mo-render-q:") == "high" })
		u.moRenderModal()
	})
	onPrefix("mo-render-fps:", func(u *UI, m actMsg) {
		if v, err := strconv.Atoi(m.arg("mo-render-fps:")); err == nil {
			u.moSetQuiet(func(s *moSt) { s.rFPS = v })
		}
		u.moRenderModal()
	})
	onExact("mo-render-go", func(u *UI, m actMsg) { u.moRenderGo(m.Val) })
	onExact("mo-export", func(u *UI, m actMsg) { u.moExport(m.Val) })
	onExact("mo-avatar-set", func(u *UI, m actMsg) { u.moAvatarSet(m.Val) })
	onExact("mo-avatar-import", func(u *UI, m actMsg) { u.moAvatarImport(m.Val) })
	onExact("mo-avatar-sync", func(u *UI, m actMsg) { u.moAvatarSync() })

	// 1 Hz: time label + scrub thumb only while playing - the skeleton animates via
	// SMIL inside the SVG itself (moSkeletonAnim); re-patching mo-view here would
	// reset its clock. The thumb moves by direct value set (no innerHTML).
	onLiveTick("motion", func(u *UI) {
		s := u.mo()
		s.mu.Lock()
		playing := s.playing && s.player != nil
		t, dur := s.t, 0.0
		if s.player != nil {
			dur = s.player.Duration()
		}
		s.mu.Unlock()
		if !playing {
			return
		}
		u.eval("window.__patch('mo-time'," + jsQuote(htmlEscape(fmt.Sprintf("%.1f / %.1f s", t, dur))) + ");" +
			fmt.Sprintf("var r=document.querySelector('#mo-body [data-label=scrub] input');"+
				"if(r){r.value=%.0f;var b=r.parentNode.querySelector('.slider-val');if(b)b.textContent=r.value;}", scrubVal(t, dur)))
	})
}

func (u *UI) moSet(mut func(*moSt)) {
	s := u.mo()
	s.mu.Lock()
	mut(s)
	s.mu.Unlock()
	u.moEnsure()
	u.patchMain() // full tab re-render (subtab active state lives outside mo-body)
	if js := u.moAnimClockJS(); js != "" {
		u.eval(js)
	}
}

func (u *UI) moSetQuiet(mut func(*moSt)) {
	s := u.mo()
	s.mu.Lock()
	mut(s)
	s.mu.Unlock()
}

// moEnsure lazily hydrates the active section's lists on first render.
func (u *UI) moEnsure() {
	s := u.mo()
	s.mu.Lock()
	sec := s.section
	cpEmpty := !s.cpLoaded
	recEmpty := s.recNames == nil
	s.mu.Unlock()
	if sec == "campaths" && cpEmpty {
		u.moCPRefresh(false)
	}
	if sec == "studio" && recEmpty {
		u.moRecRefresh(false)
	}
}

// ── camera paths ──

func (u *UI) moCPRefresh(patch bool) {
	if u.svc.VRCTools == nil {
		return
	}
	paths := u.svc.VRCTools.CamPaths()
	sort.SliceStable(paths, func(i, j int) bool {
		fi, fj := paths[i].Folder(), paths[j].Folder()
		if fi != fj {
			if fi == vrccampaths.PlayerRelativeFolder {
				return false
			}
			if fj == vrccampaths.PlayerRelativeFolder {
				return true
			}
			return fi < fj
		}
		return paths[i].SavedAt.After(paths[j].SavedAt)
	})
	s := u.mo()
	s.mu.Lock()
	s.cpPaths, s.cpLoaded = paths, true
	if s.cpSel >= len(paths) {
		s.cpSel = -1
	}
	s.mu.Unlock()
	if patch {
		u.moPatchBody()
	}
	if len(paths) > 0 && s.cpSel < 0 {
		u.moCPSelect(0)
	}
}

func (u *UI) moCPSelect(i int) {
	s := u.mo()
	s.mu.Lock()
	if i < 0 || i >= len(s.cpPaths) {
		s.mu.Unlock()
		return
	}
	p := s.cpPaths[i]
	s.mu.Unlock()
	pts, err := vrccampaths.LoadPoints(p.File)
	if err != nil {
		u.toast(i18n.T("motion.toast.readPathFailed") + err.Error())
		return
	}
	s.mu.Lock()
	s.cpSel, s.cpPts = i, pts
	lo := [3]float32{1e9, 1e9, 1e9}
	hi := [3]float32{-1e9, -1e9, -1e9}
	for _, pt := range pts {
		pos := [3]float32{float32(pt.Position.X), float32(pt.Position.Y), float32(pt.Position.Z)}
		for k := 0; k < 3; k++ {
			lo[k] = float32(math.Min(float64(lo[k]), float64(pos[k])))
			hi[k] = float32(math.Max(float64(hi[k]), float64(pos[k])))
		}
	}
	if len(pts) > 0 {
		s.cpCam.frame(lo, hi, 1.3, 1.5)
	}
	s.mu.Unlock()
	u.moPatchBody()
}

func (u *UI) moCPLoad() {
	s := u.mo()
	s.mu.Lock()
	var file string
	if s.cpSel >= 0 && s.cpSel < len(s.cpPaths) {
		file = s.cpPaths[s.cpSel].File
	}
	s.mu.Unlock()
	if file == "" || u.svc.VRCTools == nil {
		return
	}
	if err := u.svc.VRCTools.LoadCamPath(file); err != nil {
		u.toast(i18n.T("motion.toast.load") + err.Error())
		return
	}
	u.toast(i18n.T("motion.toast.sentToVrchat"))
}

func (u *UI) moCPCopy() {
	s := u.mo()
	s.mu.Lock()
	var file string
	if s.cpSel >= 0 && s.cpSel < len(s.cpPaths) {
		file = s.cpPaths[s.cpSel].File
	}
	s.mu.Unlock()
	if file == "" {
		return
	}
	u.eval("navigator.clipboard&&navigator.clipboard.writeText(" + jsQuote(file) + ")")
	u.toast(i18n.T("motion.toast.pathCopied"))
}

// ── shared orbit handlers (cp=true → camera-path view) ──

func (u *UI) moOrbit(val string, cp bool) {
	kind, fx, fy, ok := moPosParse(val)
	if !ok {
		return
	}
	s := u.mo()
	s.mu.Lock()
	cam := &s.cam
	dnYaw, dnPit, dnX, dnY := &s.dnYaw, &s.dnPit, &s.dnX, &s.dnY
	if cp {
		cam, dnYaw, dnPit, dnX, dnY = &s.cpCam, &s.cpDnYaw, &s.cpDnPit, &s.cpDnX, &s.cpDnY
	}
	switch kind {
	case "down":
		*dnYaw, *dnPit, *dnX, *dnY = cam.yaw, cam.pitch, fx, fy
		s.mu.Unlock()
		return
	case "move", "up":
		cam.yaw = *dnYaw - (fx-*dnX)*6
		cam.pitch = clampf32(*dnPit+(fy-*dnY)*6, -1.45, 1.45)
	}
	s.mu.Unlock()
	u.moPatchView(cp)
}

func (u *UI) moZoom(val string, cp bool) {
	in := strings.HasPrefix(val, "in:")
	s := u.mo()
	s.mu.Lock()
	if cp {
		s.cpCam.zoomBy(in, 0.8, 30)
	} else {
		s.cam.zoomBy(in, 0.8, 14)
	}
	s.mu.Unlock()
	u.moPatchView(cp)
}

// moPosParse parses "kind:fx,fy" pointer-transport values.
func moPosParse(val string) (kind string, fx, fy float32, ok bool) {
	k, rest, found := strings.Cut(val, ":")
	if !found {
		return "", 0, 0, false
	}
	xs, ys, _ := strings.Cut(rest, ",")
	x, err1 := strconv.ParseFloat(xs, 32)
	y, err2 := strconv.ParseFloat(ys, 32)
	if err1 != nil || err2 != nil {
		return "", 0, 0, false
	}
	return k, float32(x), float32(y), true
}

func (u *UI) moPatchView(cp bool) {
	if cp {
		u.eval("window.__patch('mo-cp-view'," + jsQuote(u.moCamPathSVG()) + ")")
		return
	}
	u.eval("window.__patch('mo-view'," + jsQuote(u.moSkeletonSVG()) + ")" + u.moAnimClockJS())
}

func (u *UI) moPatchBody() {
	u.eval("window.__patch('mo-body'," + jsQuote(u.moBody()) + ")" + u.moAnimClockJS())
}

// moAnimClockJS returns a snippet that re-seats the skeleton SMIL phase after a patch:
// inline-SVG SMIL clocks are rooted at page load, so without a seek a freshly patched
// non-loop take is already expired (frozen). Seeks synchronously in the SAME eval as
// the patch (ordering can't invert) + a setTimeout re-seek as belt for late timeline
// registration. NOT requestAnimationFrame - rAF starves in unfocused/occluded windows.
// "" unless playing.
func (u *UI) moAnimClockJS() string {
	s := u.mo()
	s.mu.Lock()
	playing, t := s.playing && s.player != nil, s.t
	s.mu.Unlock()
	if !playing {
		return ""
	}
	return fmt.Sprintf(";(function(){function k(){var s=document.querySelector('#mo-view svg');"+
		"if(s&&s.setCurrentTime)s.setCurrentTime(%.2f);}k();setTimeout(k,50);})();", t)
}

// ── motion studio ──

func moRecDir() string {
	p, err := config.DataPath("vr_recordings.x")
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "vr_recordings")
}

func (u *UI) moRecRefresh(patch bool) {
	dir := moRecDir()
	var names []string
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				names = append(names, strings.TrimSuffix(e.Name(), ".json"))
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	s := u.mo()
	s.mu.Lock()
	if names == nil {
		names = []string{} // non-nil = hydrated
	}
	s.recNames = names
	first := s.recName == "" && len(names) > 0
	s.mu.Unlock()
	if first {
		u.moRecSelect(names[0])
		return
	}
	if patch {
		u.moPatchBody()
	}
}

func (u *UI) moRecSelect(name string) {
	rec, err := vrmotion.Load(filepath.Join(moRecDir(), name+".json"))
	if err != nil {
		u.toast(i18n.T("motion.toast.loadRecording") + err.Error())
		return
	}
	s := u.mo()
	s.mu.Lock()
	s.rec, s.recName = rec, name
	s.player = vrmotion.NewPlayer(rec)
	s.t, s.playing = 0, false
	s.rt = nil
	if s.model != nil {
		s.rt = vrmik.Calibrate(s.model, rec)
	}
	if s.modelOn && s.model != nil {
		lo, hi := s.model.Bounds()
		s.cam.frame(lo, hi, 1.6, 1.0)
	} else {
		moFrameRec(&s.cam, rec)
	}
	s.mu.Unlock()
	u.moPatchBody()
}

// moFrameRec fits the orbit camera around a recording's world bounds.
func moFrameRec(cam *orbitCam, rec *vrmotion.Recording) {
	lo := [3]float32{1e9, 1e9, 1e9}
	hi := [3]float32{-1e9, -1e9, -1e9}
	for _, fr := range rec.Frames {
		for _, p := range fr.Poses {
			for k := range 3 {
				lo[k] = float32(math.Min(float64(lo[k]), float64(p.Pos[k])))
				hi[k] = float32(math.Max(float64(hi[k]), float64(p.Pos[k])))
			}
		}
	}
	if len(rec.Frames) > 0 {
		cam.frame(lo, hi, 1.4, 1.2)
	}
}

// moModelToggle loads/unloads the avatar mesh preview. Mesh renders paused/scrubbing
// (static PNG) AND during playback (moRunPreview JPEG stream). Reframes the camera on
// the model (on) / the take (off).
func (u *UI) moModelToggle(on bool) {
	s := u.mo()
	if !on {
		s.mu.Lock()
		s.modelOn = false
		rec := s.rec
		if rec != nil {
			moFrameRec(&s.cam, rec)
		}
		s.mu.Unlock()
		u.moPatchBody()
		return
	}
	path := u.svc.Cfg.Features.VRCTools.AvatarVRM
	if path == "" {
		u.toast(i18n.T("motion.toast.pickAvatarFirst"))
		u.moPatchBody()
		return
	}
	u.toast(i18n.T("motion.toast.loadingAvatar"))
	u.bg(func() { // parse off the act path; a big VRM can take a moment
		m, err := vrm.Load(path)
		if err != nil {
			u.toast(i18n.T("motion.toast.avatar") + err.Error())
			u.moPatchBody()
			return
		}
		dyn := vrmdyn.NewStateFromFile(m, path) // sidecar physbones.json > name heuristics
		s.mu.Lock()
		s.model, s.modelOn = m, true
		s.dyn, s.physOn = dyn, true
		s.rt = nil
		if s.rec != nil {
			s.rt = vrmik.Calibrate(m, s.rec)
		}
		lo, hi := m.Bounds()
		s.cam.frame(lo, hi, 1.6, 1.0)
		s.mu.Unlock()
		u.moPatchBody()
	})
}

func (u *UI) moScrub(val string) {
	v, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return
	}
	s := u.mo()
	s.mu.Lock()
	if s.player != nil {
		s.t = v / 1000 * s.player.Duration()
	}
	t, dur := s.t, 0.0
	if s.player != nil {
		dur = s.player.Duration()
	}
	s.mu.Unlock()
	u.eval("window.__patch('mo-view'," + jsQuote(u.moSkeletonSVG()) + ");" +
		"window.__patch('mo-time'," + jsQuote(htmlEscape(fmt.Sprintf("%.1f / %.1f s", t, dur))) + ");" +
		u.moAnimClockJS())
}

// moPlayPause toggles the 30fps OSC/VMC streaming goroutine.
func (u *UI) moPlayPause() {
	s := u.mo()
	s.mu.Lock()
	if s.player == nil {
		s.mu.Unlock()
		return
	}
	s.playing = !s.playing
	if s.playing && s.t >= s.player.Duration() {
		s.t = 0 // Play at the end = replay from start
	}
	starting := s.playing && s.stop == nil
	if starting {
		s.stop = make(chan struct{})
	}
	stop := s.stop
	s.mu.Unlock()
	if starting {
		u.bg(func() { u.moRunPlayback(stop) })
		u.bg(func() { u.moRunPreview(stop) })
	}
	u.moPatchBody()
}

func (u *UI) moStop() {
	s := u.mo()
	s.mu.Lock()
	s.playing, s.t = false, 0
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	s.mu.Unlock()
	u.moPatchBody()
}

// moRunPlayback is the 30fps streaming loop: advances t, sends OSC/VMC frames.
// Exits when stopped or (non-loop) at the end. Bounded: one goroutine, no queues -
// each tick sends the current frame directly (drop-nothing, UDP fire-and-forget).
func (u *UI) moRunPlayback(stop chan struct{}) {
	var oc *osc.Client
	var vs *vmc.Sender
	defer func() {
		if oc != nil {
			_ = oc.Close()
		}
		if vs != nil {
			_ = vs.Close()
		}
	}()
	tk := time.NewTicker(33 * time.Millisecond)
	defer tk.Stop()
	last := time.Now()
	for {
		select {
		case <-stop:
			return
		case now := <-tk.C:
			dt := now.Sub(last).Seconds()
			last = now
			s := u.mo()
			s.mu.Lock()
			if !s.playing || s.player == nil {
				s.mu.Unlock()
				continue
			}
			s.t += dt
			dur := s.player.Duration()
			done := false
			if s.t > dur {
				if s.loop {
					s.t = 0
				} else {
					s.t, s.playing, done = dur, false, true
				}
			}
			t := s.t
			player := s.player
			oscOn, vmcOn := s.oscOn, s.vmcOn
			s.mu.Unlock()
			sample := player.Sample(t)
			if oscOn {
				if oc == nil {
					oc, _ = osc.New(u.svc.Cfg.Features.VROverlay.ResolvedOSCAddr())
				}
				if oc != nil {
					moSendOSC(oc, sample)
				}
			}
			if vmcOn {
				if vs == nil {
					vs, _ = vmc.New(u.svc.Cfg.Features.VROverlay.ResolvedVMCAddr())
				}
				if vs != nil {
					vs.SendFrame(t, sample)
				}
			}
			if done {
				s.mu.Lock()
				if s.stop == stop { // release the slot so the next Play restarts the loop
					close(s.stop)
					s.stop = nil
				}
				s.mu.Unlock()
				u.moPatchBody()
				return
			}
		}
	}
}

// moRunPreview streams posed-mesh frames into the preview while playing with the avatar
// model on: ~15fps CPU raster → JPEG data URI → href swap on the SVG <image> (never
// innerHTML - nothing resets). Bounded: one goroutine, no queue, newest-wins (a slow
// render simply drops ticker ticks); exits with the playback goroutine via stop.
func (u *UI) moRunPreview(stop chan struct{}) {
	tk := time.NewTicker(66 * time.Millisecond)
	defer tk.Stop()
	var trail [][3]float32
	var trailRec *vrmotion.Recording // trail recomputes only when the take changes
	last := time.Now()
	for {
		select {
		case <-stop:
			return
		case now := <-tk.C:
			dt := now.Sub(last).Seconds()
			last = now
			s := u.mo()
			s.mu.Lock()
			if !s.playing || s.player == nil || !s.modelOn || s.model == nil || s.rec == nil {
				s.mu.Unlock()
				continue // paused or stick-figure mode (SMIL handles that)
			}
			rec, cam, name := s.rec, s.cam, s.recName
			model, player, t := s.model, s.player, s.t
			dyn, rt := s.dyn, s.rt
			restPose, marks := s.restPose, s.marks
			if !s.physOn {
				dyn = nil
			}
			s.mu.Unlock()
			if rec != trailRec {
				trail = trail[:0]
				for _, fr := range rec.Frames {
					if p, ok := fr.Poses[0]; ok {
						trail = append(trail, p.Pos)
					}
				}
				trailRec = rec
			}
			sample := player.Sample(t)
			frameSample, markSample := sample, map[int]vrmotion.Pose(nil)
			if restPose {
				frameSample = nil // rest-pose reference: mesh stays at A/T while the take runs
			}
			if marks || restPose { // rest mode always shows the moving points (the comparison)
				markSample = sample
			}
			img := motionrender.Render(motionrender.Frame{
				W: moPrevW, H: moPrevH,
				Cam: motionrender.Camera{Yaw: cam.yaw, Pitch: cam.pitch, Dist: cam.dist,
					Center: cam.center, FloorY: cam.floorY, GridR: cam.gridR},
				Model: model, Sample: frameSample, Trail: trail, Name: name,
				Dyn: dyn, DT: dt, RT: rt, Marks: markSample,
			})
			u.eval("(function(){var i=document.querySelector('#mo-view svg image');" +
				"if(i)i.setAttribute('href'," + jsQuote("data:image/jpeg;base64,"+jpegB64(img)) + ");})()")
		}
	}
}

// jpegBytes encodes a rendered frame to JPEG at quality q.
func jpegBytes(img *image.NRGBA, q int) []byte {
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: q})
	return buf.Bytes()
}

// jpegB64 encodes a rendered frame for data-URI streaming (JPEG ≈ 3-4× smaller than PNG).
func jpegB64(img *image.NRGBA) string {
	return base64.StdEncoding.EncodeToString(jpegBytes(img, 80))
}

// moSendOSC streams one sampled frame to VRChat (head + trackers, pos + ZXY euler).
func moSendOSC(c *osc.Client, sample map[int]vrmotion.Pose) {
	for key, p := range sample {
		rx, ry, rz := osc.QuatToEulerZXY(p.Rot[0], p.Rot[1], p.Rot[2], p.Rot[3])
		var pa, ra string
		var pargs, rargs []any
		if key == 0 {
			pa, pargs = osc.HeadPosition(p.Pos[0], p.Pos[1], p.Pos[2])
			ra, rargs = osc.HeadRotation(rx, ry, rz)
		} else {
			pa, pargs = osc.TrackerPosition(key, p.Pos[0], p.Pos[1], p.Pos[2])
			ra, rargs = osc.TrackerRotation(key, rx, ry, rz)
		}
		_ = c.Send(pa, pargs...)
		_ = c.Send(ra, rargs...)
	}
}

func (u *UI) moExport(path string) {
	s := u.mo()
	s.mu.Lock()
	rec := s.rec
	s.mu.Unlock()
	if rec == nil {
		u.toast(i18n.T("motion.toast.selectRecordingFirst"))
		return
	}
	if err := os.WriteFile(path, []byte(vrmotion.BuildAnim(rec, nil)), 0o644); err != nil {
		u.toast(i18n.T("motion.toast.export") + err.Error())
		return
	}
	u.toast(i18n.T("motion.toast.exported") + filepath.Base(path))
}

// ── render video (worker: render.motionvideo, C5) ──

// moRenderSize maps mode+quality to output dimensions.
func moRenderSize(mode string, high bool) (w, h int) {
	if mode == "equirect" {
		if high {
			return 4096, 2048
		}
		return 2048, 1024
	}
	if high {
		return 1920, 1080
	}
	return 1280, 720
}

func (u *UI) moRenderModal() {
	s := u.mo()
	s.mu.Lock()
	if s.rMode == "" {
		s.rMode = "orbit"
	}
	if s.rFPS == 0 {
		s.rFPS = 30
	}
	mode, high, fps, rendering, recName := s.rMode, s.rHigh, s.rFPS, s.rendering, s.recName
	s.mu.Unlock()
	if recName == "" {
		u.toast(i18n.T("motion.toast.selectRecordingFirst"))
		return
	}
	avatar := u.svc.Cfg.Features.VRCTools.AvatarVRM
	avNote := i18n.T("motion.label.stickFigureNoAvatar")
	if avatar != "" {
		avNote = filepath.Base(avatar)
	}
	w, h := moRenderSize(mode, high)
	q := "std"
	if high {
		q = "high"
	}
	goLbl := i18n.T("motion.label.chooseFileRender", i18n.A{"w": strconv.Itoa(w), "h": strconv.Itoa(h)})
	body := smartSelect("mo-render-mode", i18n.T("motion.label.camera"), "mo-render-mode:", mode, func() []ssOpt {
		return []ssOpt{
			{Val: "orbit", Label: i18n.T("motion.label.orbitFlatVideo"), Sub: i18n.T("motion.label.orbitSub")},
			{Val: "equirect", Label: i18n.T("motion.label.equirect"), Sub: i18n.T("motion.label.equirectSub")},
		}
	}) +
		smartSelect("mo-render-q", i18n.T("motion.label.quality"), "mo-render-q:", q, func() []ssOpt {
			return []ssOpt{
				{Val: "std", Label: i18n.T("motion.label.standard"), Sub: i18n.T("motion.label.standardSub")},
				{Val: "high", Label: i18n.T("motion.label.high"), Sub: i18n.T("motion.label.highSub")},
			}
		}) +
		smartSelect("mo-render-fps", i18n.T("motion.label.frameRate"), "mo-render-fps:", strconv.Itoa(fps), func() []ssOpt {
			return []ssOpt{
				{Val: "30", Label: i18n.T("motion.label.fps", i18n.A{"n": "30"})},
				{Val: "60", Label: i18n.T("motion.label.fps", i18n.A{"n": "60"}), Sub: i18n.T("motion.label.doubleRenderTime")},
			}
		}) +
		`<div class=set-note>` + i18n.T("motion.label.renderNote", i18n.A{"take": html.EscapeString(recName), "avatar": html.EscapeString(avNote)}) + `</div>`
	foot := btn(goLbl, "primary", "pick-save:mp4:mo-render-go", "") + btn(i18n.T("common.close"), "outline", "modal-close", "")
	if rendering {
		foot = `<span class=set-note>` + i18n.T("motion.label.renderingNote") + `</span>` + btn(i18n.T("common.close"), "outline", "modal-close", "")
	}
	u.openModal(modal(i18n.T("motion.label.renderVideoTitle"), body, foot))
}

// moRenderGo runs the render job in the worker pool; out = user-picked .mp4 path.
// Worker "progress" events (percent + frame/frames + phase render/encode) patch the
// #mo-render-prog row live (same pattern as mpRunExport / spoutInstall).
func (u *UI) moRenderGo(out string) {
	if out == "" {
		return
	}
	s := u.mo()
	s.mu.Lock()
	if s.rendering {
		s.mu.Unlock()
		u.toast(i18n.T("motion.toast.renderRunning"))
		return
	}
	mode, high, fps, recName := s.rMode, s.rHigh, s.rFPS, s.recName
	s.rendering = true
	s.rPct, s.rFrame, s.rFrames, s.rPhase, s.rDone = 0, 0, 0, "", ""
	s.mu.Unlock()
	w, h := moRenderSize(mode, high)
	params := map[string]any{
		"recording": filepath.Join(moRecDir(), recName+".json"),
		"avatar":    u.svc.Cfg.Features.VRCTools.AvatarVRM,
		"mode":      mode, "width": w, "height": h, "fps": fps, "out": out,
	}
	u.closeModal()
	u.toast(i18n.T("motion.toast.rendering", i18n.A{"mode": mode, "w": strconv.Itoa(w), "h": strconv.Itoa(h)}))
	u.moPatchRenderProg()
	onProgress := func(event string, data json.RawMessage) {
		if event != "progress" {
			return
		}
		var p struct {
			Percent float64 `json:"percent"`
			Frame   int     `json:"frame"`
			Frames  int     `json:"frames"`
			Phase   string  `json:"phase"`
		}
		if json.Unmarshal(data, &p) != nil {
			return
		}
		u.moSetQuiet(func(s *moSt) {
			s.rPct, s.rFrame, s.rFrames = p.Percent, p.Frame, p.Frames
			if p.Phase != "" {
				s.rPhase = p.Phase
			}
		})
		u.moPatchRenderProg()
	}
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		raw, err := u.svc.Workers.RunStreamBackground(ctx, "render", "render.motionvideo", params, onProgress)
		if err != nil {
			u.moSetQuiet(func(s *moSt) {
				s.rendering, s.rDone, s.rOK = false, i18n.T("motion.toast.renderFailed")+err.Error(), false
			})
			u.moPatchRenderProg()
			u.toast(i18n.T("motion.toast.renderFailed") + err.Error())
			return
		}
		var res struct {
			Frames    int    `json:"frames"`
			Spherical bool   `json:"spherical_metadata"`
			SphErr    string `json:"spherical_error"`
		}
		_ = json.Unmarshal(raw, &res)
		msg := i18n.Tn("motion.toast.rendered", res.Frames, i18n.A{"path": filepath.Base(out)})
		if res.Spherical {
			msg += i18n.T("motion.toast.tagged360")
		}
		if res.SphErr != "" {
			msg += i18n.T("motion.toast.tag360Failed") + res.SphErr
		}
		u.moSetQuiet(func(s *moSt) { s.rendering, s.rDone, s.rOK = false, msg, res.SphErr == "" })
		u.moPatchRenderProg()
		u.toast(msg)
	})
}

// moRenderProgHTML renders the live render-job row under the studio list (empty when idle).
func (u *UI) moRenderProgHTML() string {
	s := u.mo()
	s.mu.Lock()
	rendering, pct, fr, total, phase := s.rendering, s.rPct, s.rFrame, s.rFrames, s.rPhase
	done := s.rDone
	s.mu.Unlock()
	if rendering {
		cap := i18n.T("motion.label.startingRender")
		switch {
		case phase == "encode":
			cap = i18n.T("motion.label.encoding")
		case total > 0:
			cap = i18n.T("motion.label.renderingFrame", i18n.A{"frame": strconv.Itoa(fr), "total": strconv.Itoa(total), "pct": fmt.Sprintf("%.0f", pct)})
		}
		return progressBar(pct/100, cap)
	}
	if done == "" {
		return ""
	}
	return `<div class=mo-info>` + html.EscapeString(done) + `</div>`
}

func (u *UI) moPatchRenderProg() {
	u.eval("window.__patch('mo-render-prog'," + jsQuote(u.moRenderProgHTML()) + ")")
}

// ── avatars ──

func (u *UI) moAvatarSet(path string) {
	if path == "" {
		return
	}
	u.svc.Cfg.Features.VRCTools.AvatarVRM = path
	u.saveCfg()
	u.toast(i18n.T("motion.toast.activeAvatar") + filepath.Base(path))
	s := u.mo()
	s.mu.Lock()
	reload := s.modelOn
	s.mu.Unlock()
	if reload {
		u.moModelToggle(true) // live mesh preview follows the selection
		return
	}
	u.moPatchBody()
}

func (u *UI) moAvatarImport(src string) {
	if src == "" {
		return
	}
	p := src
	if managed, err := config.ImportAvatar(src); err == nil {
		p = managed
	}
	u.moAvatarSet(p)
}

func (u *UI) moAvatarSync() {
	if u.svc.SyncVRMAvatars == nil {
		u.toast(i18n.T("motion.toast.peerSyncUnavailable"))
		return
	}
	u.toast(i18n.T("motion.toast.syncingAvatars"))
	u.bg(func() {
		pulled, skipped, errored := u.svc.SyncVRMAvatars()
		msg := i18n.T("motion.toast.avatarSyncResult", i18n.A{"synced": strconv.Itoa(pulled), "uptodate": strconv.Itoa(skipped)})
		if errored > 0 {
			msg += i18n.Tn("motion.toast.syncErrors", errored)
		}
		u.toast(msg)
		u.moPatchBody()
	})
}
