package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/osc"
	"rave.page/mate/internal/vmc"
	"rave.page/mate/internal/vrm"
	"rave.page/mate/internal/vrmik"
	"rave.page/mate/internal/vrmotion"
)

// motionRecordingsDir mirrors vroverlay/motion.go's storage location.
func motionRecordingsDir() string {
	p, err := config.DataPath("vr_recordings.x")
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "vr_recordings")
}

func motionRecordingNames(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}

// motionStudioDialog previews recorded VR motion in a 3D orbit view (drag to rotate, scroll to zoom)
// with playback + scrub, optionally streaming the take into VRChat over OSC.
func (u *UI) motionStudioDialog() {
	dir := motionRecordingsDir()
	names := motionRecordingNames(dir)

	view := newMotionView()

	var (
		player    *vrmotion.Player
		loadedRec *vrmotion.Recording
		recName   string
		playing   bool
		t         float64
		oc        *osc.Client
		vmcS      *vmc.Sender
		seeking   bool
	)

	timeLbl := widget.NewLabel("0.0 / 0.0 s")
	slider := widget.NewSlider(0, 1)
	loopChk := widget.NewCheck("Loop", nil)
	oscChk := widget.NewCheck("Send as OSC trackers (FBT only)", nil)
	vmcChk := widget.NewCheck("Stream to VMC (VTuber)", nil)

	// Avatar-model preview: render the loaded .vrm mesh (posed via simple IK) instead of the stick figure.
	avatarChk := widget.NewCheck("Avatar model", func(on bool) {
		view.showModel = on
		if on {
			view.frameModel()
		} else if loadedRec != nil {
			view.setRecording(loadedRec)
		}
		view.Refresh()
	})
	loadAvatar := func(path string) {
		go func() { // parse off the UI thread; a big VRM can take a moment
			m, err := vrm.Load(path)
			fyne.Do(func() {
				if err != nil {
					dialog.ShowError(err, u.win)
					return
				}
				view.setModel(m)
				view.showModel = true
				view.frameModel()
				avatarChk.SetChecked(true)
				view.Refresh()
			})
		}()
	}
	var refreshSynced func() // set below; forward-declared so the picker can refresh the synced list
	loadAvatarBtn := widget.NewButton("Load avatar (.vrm/.fbx)…", func() {
		fd := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, u.win)
				return
			}
			if rc == nil { // cancelled
				return
			}
			p := rc.URI().Path()
			_ = rc.Close()
			if managed, err := config.ImportAvatar(p); err == nil { // copy into the peer-synced avatars dir
				p = managed
			}
			u.svc.Cfg.Features.VRCTools.AvatarVRM = p
			u.saveCfg()
			loadAvatar(p)
			if refreshSynced != nil {
				refreshSynced() // imported file now appears in the synced list
			}
		}, u.win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".vrm", ".glb", ".gltf", ".fbx"}))
		fd.Show()
	})

	// Synced avatars: models in the peer-replicated managed dir (config.VRMAvatarsDir) - picking one
	// activates it directly (already managed, no import copy). List rebuilt on every dialog open.
	var syncedEntries []config.AvatarEntry
	syncedSel := widget.NewSelect(nil, nil)
	syncedSel.PlaceHolder = "Synced avatars…"
	syncStatus := mutedLabel("")
	refreshSynced = func() {
		syncedEntries = config.ListAvatars()
		opts := make([]string, len(syncedEntries))
		for i, e := range syncedEntries {
			opts[i] = fmt.Sprintf("%s (%s)", e.Name, humanBytes(e.Size))
		}
		syncedSel.Options = opts
		if len(opts) == 0 {
			syncedSel.Disable()
			syncStatus.SetText("(none synced yet - paired instances sync automatically)")
		} else {
			syncedSel.Enable()
		}
		syncedSel.Refresh()
	}
	syncedSel.OnChanged = func(string) {
		i := syncedSel.SelectedIndex()
		if i < 0 || i >= len(syncedEntries) {
			return
		}
		p := syncedEntries[i].Path
		u.svc.Cfg.Features.VRCTools.AvatarVRM = p
		u.saveCfg()
		loadAvatar(p)
	}
	syncNowBtn := widget.NewButton("Sync now", nil)
	syncNowBtn.OnTapped = func() {
		if u.svc.SyncVRMAvatars == nil {
			return
		}
		syncNowBtn.Disable()
		syncStatus.SetText("Syncing…")
		goUI("vrm-sync", func() { // blocking all-peer reconcile off the UI thread
			pulled, skipped, errored := u.svc.SyncVRMAvatars()
			fyne.Do(func() {
				syncNowBtn.Enable()
				msg := fmt.Sprintf("%d synced · %d up-to-date", pulled, skipped)
				if errored > 0 {
					msg += fmt.Sprintf(" · %d errors", errored)
				}
				syncStatus.SetText(msg)
				u.Notify("Avatar sync", msg)
				refreshSynced()
			})
		})
	}
	refreshSynced()

	load := func(name string) {
		rec, err := vrmotion.Load(filepath.Join(dir, name+".json"))
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		player = vrmotion.NewPlayer(rec)
		loadedRec = rec
		recName = name
		t, playing = 0, false
		view.setRecording(rec)
		dur := math.Max(player.Duration(), 0.001)
		seeking = true
		slider.Max = dur
		slider.SetValue(0)
		seeking = false
		view.sample = player.Sample(0)
		view.Refresh()
	}

	slider.OnChanged = func(v float64) {
		if seeking || player == nil {
			return
		}
		t = v
		view.sample = player.Sample(t)
		view.name = recName
		timeLbl.SetText(fmt.Sprintf("%.1f / %.1f s", t, player.Duration()))
	}

	list := widget.NewList(
		func() int { return len(names) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) { o.(*widget.Label).SetText(names[i]) },
	)
	list.OnSelected = func(i widget.ListItemID) {
		if i >= 0 && i < len(names) {
			load(names[i])
		}
	}

	playBtn := widget.NewButton("Play", nil)
	playBtn.OnTapped = func() {
		if player == nil {
			return
		}
		playing = !playing
		if playing {
			playBtn.SetText("Pause")
			if oscChk.Checked && oc == nil {
				oc, _ = osc.New(u.svc.Cfg.Features.VROverlay.ResolvedOSCAddr())
			}
			if vmcChk.Checked && vmcS == nil {
				vmcS, _ = vmc.New(u.svc.Cfg.Features.VROverlay.ResolvedVMCAddr())
			}
		} else {
			playBtn.SetText("Play")
		}
	}
	stopBtn := widget.NewButton("Stop", func() {
		playing = false
		playBtn.SetText("Play")
		t = 0
		seeking = true
		slider.SetValue(0)
		seeking = false
		if player != nil {
			view.sample = player.Sample(0)
		}
	})
	refreshBtn := widget.NewButton("Refresh", func() {
		names = motionRecordingNames(dir)
		list.Refresh()
	})
	exportBtn := widget.NewButton("Export .anim", func() {
		if loadedRec == nil {
			dialog.ShowInformation("Export", "Select a recording first.", u.win)
			return
		}
		rec := loadedRec
		fd := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
			if err != nil {
				dialog.ShowError(err, u.win)
				return
			}
			if w == nil { // cancelled
				return
			}
			defer func() { _ = w.Close() }()
			if _, werr := w.Write([]byte(vrmotion.BuildAnim(rec, nil))); werr != nil {
				dialog.ShowError(werr, u.win)
			}
		}, u.win)
		fd.SetFileName(recName + ".anim")
		fd.Show()
	})

	// Render/playback ticker (30 fps): ease the camera every frame (smooth orbit), advance time +
	// stream OSC while playing.
	stop := make(chan struct{})
	go func() {
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
				view.ease()
				if playing && player != nil {
					t += dt
					dur := player.Duration()
					if t > dur {
						if loopChk.Checked {
							t = 0
						} else {
							t, playing = dur, false
						}
					}
				}
				var samp map[int]vrmotion.Pose
				if player != nil {
					samp = player.Sample(t)
				}
				if playing && oscChk.Checked && oc != nil {
					sendMotionOSC(oc, samp)
				}
				if playing && vmcChk.Checked && vmcS != nil {
					vmcS.SendFrame(t, samp)
				}
				tt, done := t, !playing
				fyne.Do(func() {
					view.sample, view.name = samp, recName
					view.Refresh()
					if player != nil {
						seeking = true
						slider.SetValue(tt)
						seeking = false
						timeLbl.SetText(fmt.Sprintf("%.1f / %.1f s", tt, player.Duration()))
					}
					if done && playBtn.Text == "Pause" {
						playBtn.SetText("Play")
					}
				})
			}
		}
	}()

	controls := container.NewVBox(
		slider,
		container.NewHBox(playBtn, stopBtn, loopChk, oscChk, vmcChk, avatarChk, timeLbl),
		widget.NewLabelWithStyle("Drag to orbit · scroll to zoom", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		mutedLabel("VMC (VTuber): streams this take as raw HMD+controller+tracker devices to a VMC receiver (VSeeFace/Warudo/VNyan @ "+u.svc.Cfg.Features.VROverlay.ResolvedVMCAddr()+") - the renderer does the IK and animates your avatar. This is the real motion-playback path.\n\nOSC trackers FEED VRChat's full-body tracking - they can't play a recorded animation onto your VRChat avatar: VRChat always drives head + hands from your live HMD/controllers (the exact points recorded here), and desktop mode ignores trackers entirely. To replay a take onto a VRChat avatar, Export .anim → drive it from the avatar's Animator."),
	)
	left := container.NewBorder(widget.NewLabel("Recordings"),
		container.NewVBox(refreshBtn, exportBtn, loadAvatarBtn, syncedSel, syncNowBtn, syncStatus), nil, nil, list)
	body := container.NewBorder(nil, controls, container.NewGridWrap(fyne.NewSize(180, 320), left), nil, view)

	d := dialog.NewCustom("Motion studio", "Done", body, u.win)
	d.Resize(fyne.NewSize(860, 520))
	d.SetOnClosed(func() {
		close(stop)
		if oc != nil {
			_ = oc.Close()
		}
		if vmcS != nil {
			_ = vmcS.Close()
		}
	})
	// Preload a previously chosen avatar in the background so "Avatar model" is instantly viewable.
	if p := u.svc.Cfg.Features.VRCTools.AvatarVRM; p != "" {
		go func() {
			if m, err := vrm.Load(p); err == nil {
				fyne.Do(func() { view.setModel(m) })
			}
		}()
	}
	if len(names) > 0 {
		list.Select(0)
	}
	d.Show()
}

// sendMotionOSC streams one sampled frame to VRChat (head + trackers, pos + ZXY euler).
func sendMotionOSC(c *osc.Client, sample map[int]vrmotion.Pose) {
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

// ── 3D orbit preview widget ──────────────────────────────────────────────────

// motionView is a draggable/scrollable 3D skeleton preview. The camera orbits the recording's centre;
// drag rotates (yaw/pitch), scroll zooms (distance). Angles ease toward targets each frame for smooth
// motion. Rendered via a canvas.Raster shade fn (resizes to the widget, software-projected).
type motionView struct {
	widget.BaseWidget
	raster *canvas.Raster

	yaw, pitch, dist    float32
	tyaw, tpitch, tdist float32
	center              [3]float32
	floorY              float32
	gridR               float32

	sample map[int]vrmotion.Pose
	trail  [][3]float32 // head path (world)
	name   string
	hasRec bool

	model        *vrm.Model // loaded avatar; nil until "Load avatar…" succeeds
	showModel    bool       // render the posed mesh instead of the stick figure
	triCap       int        // max triangles rasterized per frame (0 = uncapped)
	cappedLogged bool       // one-shot log when triangle downsampling kicks in
}

func newMotionView() *motionView {
	v := &motionView{yaw: 0.6, pitch: 0.35, dist: 3.5, tyaw: 0.6, tpitch: 0.35, tdist: 3.5, gridR: 1.5, triCap: 40000}
	v.raster = canvas.NewRaster(v.render)
	v.ExtendBaseWidget(v)
	return v
}

// setModel stores a loaded avatar (does not change framing or toggle model mode).
func (v *motionView) setModel(m *vrm.Model) {
	v.model = m
	if m != nil {
		v.hasRec = true // render can proceed even with no recording selected
	}
}

// frameModel orbits the camera around the avatar's rest bounds.
func (v *motionView) frameModel() {
	if v.model == nil {
		return
	}
	lo, hi := v.model.Bounds()
	v.center = [3]float32{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	v.floorY = lo[1]
	diag := float32(math.Sqrt(float64(sq(hi[0]-lo[0]) + sq(hi[1]-lo[1]) + sq(hi[2]-lo[2]))))
	v.gridR = float32(math.Max(1, float64((hi[0]-lo[0]+hi[2]-lo[2])/2)))
	v.tdist = diag*1.6 + 1.0
	v.dist = v.tdist
	v.hasRec = true
}

func (v *motionView) MinSize() fyne.Size { return fyne.NewSize(520, 320) }

func (v *motionView) CreateRenderer() fyne.WidgetRenderer { return &motionViewRenderer{v: v} }

// setRecording frames the camera around a recording's bounds + caches the head trail.
func (v *motionView) setRecording(rec *vrmotion.Recording) {
	lo, hi := motionBounds(rec)
	v.center = [3]float32{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	v.floorY = lo[1]
	diag := float32(math.Sqrt(float64(sq(hi[0]-lo[0]) + sq(hi[1]-lo[1]) + sq(hi[2]-lo[2]))))
	v.gridR = float32(math.Max(1, float64((hi[0]-lo[0]+hi[2]-lo[2])/2)))
	v.tdist = diag*1.4 + 1.2
	v.dist = v.tdist
	v.trail = v.trail[:0]
	for _, fr := range rec.Frames {
		if p, ok := fr.Poses[0]; ok {
			v.trail = append(v.trail, p.Pos)
		}
	}
	v.hasRec = true
}

func (v *motionView) Dragged(e *fyne.DragEvent) {
	v.tyaw -= e.Dragged.DX * 0.012
	v.tpitch = clamp32(v.tpitch+e.Dragged.DY*0.012, -1.45, 1.45)
}
func (v *motionView) DragEnd() {}

func (v *motionView) Scrolled(e *fyne.ScrollEvent) {
	v.tdist = clamp32(v.tdist*float32(math.Pow(0.9, float64(e.Scrolled.DY/40))), 0.8, 14)
}

// ease nudges the live camera toward its target each frame (smooth orbit/zoom).
func (v *motionView) ease() {
	v.yaw += (v.tyaw - v.yaw) * 0.3
	v.pitch += (v.tpitch - v.pitch) * 0.3
	v.dist += (v.tdist - v.dist) * 0.3
}

func (v *motionView) Refresh() { v.raster.Refresh() }

// project maps a world point to screen px + depth for the current camera (orbit at v.dist).
func (v *motionView) project(p [3]float32, w, h int) (int, int, float32) {
	dx, dy, dz := p[0]-v.center[0], p[1]-v.center[1], p[2]-v.center[2]
	cy, sy := float32(math.Cos(float64(v.yaw))), float32(math.Sin(float64(v.yaw)))
	x1 := dx*cy + dz*sy
	z1 := -dx*sy + dz*cy
	cp, sp := float32(math.Cos(float64(v.pitch))), float32(math.Sin(float64(v.pitch)))
	y2 := dy*cp - z1*sp
	z2 := dy*sp + z1*cp
	depth := v.dist - z2
	if depth < 0.15 {
		depth = 0.15
	}
	f := float32(h) * 0.9
	sx := float32(w)/2 + f*x1/depth
	syv := float32(h)/2 - f*y2/depth
	return int(sx), int(syv), depth
}

// render is the canvas.Raster shade fn: draws the 3D scene at the widget's pixel size.
func (v *motionView) render(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fillImg(img, mpBG)
	if !v.hasRec {
		drawText5(img, "Select a recording to preview", 16, h/2, mpText)
		return img
	}
	// Floor grid (X-Z plane at floorY) for spatial reference.
	const n = 6
	step := (2 * v.gridR) / n
	for i := 0; i <= n; i++ {
		d := -v.gridR + step*float32(i)
		a1, b1, _ := v.project([3]float32{v.center[0] - v.gridR, v.floorY, v.center[2] + d}, w, h)
		a2, b2, _ := v.project([3]float32{v.center[0] + v.gridR, v.floorY, v.center[2] + d}, w, h)
		drawLine(img, image.Pt(a1, b1), image.Pt(a2, b2), mpGrid)
		c1, e1, _ := v.project([3]float32{v.center[0] + d, v.floorY, v.center[2] - v.gridR}, w, h)
		c2, e2, _ := v.project([3]float32{v.center[0] + d, v.floorY, v.center[2] + v.gridR}, w, h)
		drawLine(img, image.Pt(c1, e1), image.Pt(c2, e2), mpGrid)
	}
	// Head trail (converted to avatar space in model mode so it aligns with the posed mesh).
	var prev image.Point
	for i, p := range v.trail {
		if v.showModel {
			p = vrmik.ConvPos(p)
		}
		sx, sy, _ := v.project(p, w, h)
		cur := image.Pt(sx, sy)
		if i > 0 {
			drawLine(img, prev, cur, mpTrail)
		}
		prev = cur
	}
	// Avatar mesh (model mode) replaces the stick figure.
	if v.showModel && v.model != nil {
		v.renderModel(img, w, h)
		if v.name != "" {
			drawText5(img, v.name, 12, h-14, mpText)
		}
		return img
	}
	// Skeleton dots + bones (head → each tracker).
	if v.sample != nil {
		head, hasHead := v.sample[0]
		var hpt image.Point
		if hasHead {
			hx, hy, _ := v.project(head.Pos, w, h)
			hpt = image.Pt(hx, hy)
		}
		for key, p := range v.sample {
			sx, sy, depth := v.project(p.Pos, w, h)
			pt := image.Pt(sx, sy)
			col, base := mpTrk, 5
			if key == 0 {
				col, base = mpHead, 8
			} else if hasHead {
				drawLine(img, hpt, pt, mpTrk)
			}
			r := int(float32(base) * (v.dist / depth))
			if r < 2 {
				r = 2
			}
			if r > 18 {
				r = 18
			}
			drawDisc(img, pt, r, col)
		}
	}
	if v.name != "" {
		drawText5(img, v.name, 12, h-14, mpText)
	}
	return img
}

// modelLight is a fixed world-space light direction for flat shading (unit length).
var modelLight = [3]float32{0.40, 0.82, 0.41}

var mpAvatar = color.NRGBA{R: 0x9A, G: 0x7A, B: 0xE0, A: 255} // brand-violet-ish avatar base

// renderModel poses the avatar from the current sample (rest pose if none) and rasterizes its
// skinned triangles: flat-shaded, depth-buffered, brand-tinted. Triangles are downsampled to triCap
// (logged once) so a heavy avatar can't stall the preview.
func (v *motionView) renderModel(img *image.NRGBA, w, h int) {
	m := v.model
	local := vrmik.Pose(m, v.sample)
	world := m.WorldFrom(local)
	skin := m.SkinMatrices(world)
	db := newDepthBuffer(w, h)

	total := 0
	for mi := range m.Meshes {
		total += len(m.Meshes[mi].Indices) / 3
	}
	tstep := 1 // draw every tstep-th triangle when over the cap
	if v.triCap > 0 && total > v.triCap {
		tstep = total/v.triCap + 1
		if !v.cappedLogged {
			v.cappedLogged = true
			if uiLog != nil {
				uiLog.Info("motion", "avatar preview capping triangles",
					map[string]any{"total": total, "cap": v.triCap, "step": tstep})
			}
		}
	}

	for mi := range m.Meshes {
		pts := m.PosedPositions(mi, world, skin)
		idx := m.Meshes[mi].Indices
		for i := 0; i+2 < len(idx); i += 3 * tstep {
			p0, p1, p2 := pts[idx[i]], pts[idx[i+1]], pts[idx[i+2]]
			x0, y0, z0 := v.project(p0, w, h)
			x1, y1, z1 := v.project(p1, w, h)
			x2, y2, z2 := v.project(p2, w, h)
			col := shadeFlat(mpAvatar, faceNormal(p0, p1, p2), modelLight)
			fillTriangle(img, db, projVert{x0, y0, z0}, projVert{x1, y1, z1}, projVert{x2, y2, z2}, col)
		}
	}
}

type motionViewRenderer struct{ v *motionView }

func (r *motionViewRenderer) Layout(s fyne.Size)           { r.v.raster.Resize(s) }
func (r *motionViewRenderer) MinSize() fyne.Size           { return r.v.MinSize() }
func (r *motionViewRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.v.raster} }
func (r *motionViewRenderer) Refresh()                     { r.v.raster.Refresh() }
func (r *motionViewRenderer) Destroy()                     {}

// motionBounds returns the world AABB across all frames, padded to avoid a degenerate axis.
func motionBounds(rec *vrmotion.Recording) (lo, hi [3]float32) {
	lo = [3]float32{1e9, 1e9, 1e9}
	hi = [3]float32{-1e9, -1e9, -1e9}
	any := false
	for _, fr := range rec.Frames {
		for _, p := range fr.Poses {
			any = true
			for i := 0; i < 3; i++ {
				if p.Pos[i] < lo[i] {
					lo[i] = p.Pos[i]
				}
				if p.Pos[i] > hi[i] {
					hi[i] = p.Pos[i]
				}
			}
		}
	}
	if !any {
		return [3]float32{-1, 0, -1}, [3]float32{1, 2, 1}
	}
	for i := 0; i < 3; i++ {
		if hi[i]-lo[i] < 0.5 {
			c := (hi[i] + lo[i]) / 2
			lo[i], hi[i] = c-0.25, c+0.25
		}
	}
	return lo, hi
}

var (
	mpBG    = color.NRGBA{R: 10, G: 10, B: 14, A: 255}
	mpGrid  = color.NRGBA{R: 34, G: 34, B: 46, A: 255}
	mpTrail = color.NRGBA{R: 0x4a, G: 0x2a, B: 0x48, A: 255}
	mpHead  = color.NRGBA{R: 0xF7, G: 0x08, B: 0x64, A: 255} // brand pink
	mpTrk   = color.NRGBA{R: 0x08, G: 0xF7, B: 0x9B, A: 255} // brand mint
	mpText  = color.NRGBA{R: 0xc8, G: 0xc8, B: 0xd0, A: 255}
)

func drawText5(img *image.NRGBA, s string, x, y int, col color.NRGBA) {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(col), Face: basicfont.Face7x13, Dot: fixed.P(x, y+11)}
	d.DrawString(s)
}

func fillImg(img *image.NRGBA, c color.NRGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func drawDisc(img *image.NRGBA, c image.Point, r int, col color.NRGBA) {
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(c.X+dx, c.Y+dy, col)
			}
		}
	}
}

func drawLine(img *image.NRGBA, a, b image.Point, col color.NRGBA) {
	dx := absi(b.X - a.X)
	dy := -absi(b.Y - a.Y)
	sx, sy := 1, 1
	if a.X > b.X {
		sx = -1
	}
	if a.Y > b.Y {
		sy = -1
	}
	err := dx + dy
	x, y := a.X, a.Y
	for i := 0; i < 5000; i++ { // bound the walk (offscreen-safe)
		img.SetNRGBA(x, y, col)
		if x == b.X && y == b.Y {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x += sx
		}
		if e2 <= dx {
			err += dx
			y += sy
		}
	}
}

func absi(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sq(v float32) float32 { return v * v }
func clamp32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
