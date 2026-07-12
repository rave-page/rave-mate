package webui

// Motion-studio point-cloud preview + RMPC export (task #83, rave-mate side). Preview is an
// in-proc carve-out (one frame, bounded, user-paced - same rule as the mesh preview): pose the
// current frame, sample the avatar mesh into a preview-capped point set, project via the shared
// orbitCam and draw SVG discs. The heavy full-density export runs out-of-process in the "render"
// worker (render.pointcloud) exactly like the C5 video render. Web/VR viewers consume the RMPC
// file - see .devnotes/POINTCLOUD_FORMAT.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/pointcloud"
	"rave.page/mate/internal/vrmik"
	"rave.page/mate/internal/vrmotion"
)

// moPCPreviewPoints caps the in-proc preview cloud so the SVG stays light (export uses the
// selected density). moPCPreviewPoints discs ≈ a few hundred KB of SVG.
const moPCPreviewPoints = 2500

// moViewHTML returns the current preview body: point cloud when that mode is on (model
// loaded), else the skeleton/mesh view. Used by every full-frame patch of #mo-view.
func (u *UI) moViewHTML() string {
	if u.moPCActive() {
		return u.moPointCloudSVG(false)
	}
	return u.moSkeletonSVG()
}

// moViewDragHTML is the cheap mid-drag frame for the active view mode.
func (u *UI) moViewDragHTML() string {
	if u.moPCActive() {
		return u.moPointCloudSVG(true)
	}
	return u.moSkeletonSVGOpt(true)
}

// moPCActive reports whether point-cloud preview mode is on with a usable model.
func (u *UI) moPCActive() bool {
	s := u.mo()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pcOn && s.modelOn && s.model != nil
}

// moPointCloudSVG renders the current posed frame as a projected point cloud (floor grid +
// discs). Physics is intentionally skipped here (deterministic, and moRunPreview is the sole
// dyn.Stepper while playing) - hair/tail secondary motion applies to the exported cloud only.
func (u *UI) moPointCloudSVG(drag bool) string {
	const w, h = moPrevW, moPrevH
	s := u.mo()
	s.mu.Lock()
	model, player, cam := s.model, s.player, s.cam
	rec, name := s.rec, s.recName
	rt, sel := s.rt, s.pcSel
	t0, color := s.t, s.pcColor
	s.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class=mo-svg viewBox="0 0 %d %d" preserveAspectRatio="xMidYMid meet">`, w, h)
	b.WriteString(`<rect width="100%" height="100%" fill="rgba(0,0,0,.3)"/>`)
	if model == nil || rec == nil || sel == nil || sel.Count() == 0 {
		b.WriteString(`<text x="20" y="200" class=mo-svgtext>` + html.EscapeString(i18n.T("motion.pcNoModel")) + `</text></svg>`)
		return b.String()
	}
	b.WriteString(cam.gridSVG(w, h))

	var sample map[int]vrmotion.Pose
	if player != nil {
		sample = player.Sample(t0)
	}
	local := vrmik.PoseRT(model, sample, rt)
	world := model.WorldFrom(local)
	skin := model.SkinMatrices(world)
	pts := sel.Positions(model, world, skin, nil)

	r := float32(1.7)
	if drag {
		r = 1.4
	}
	colors := sel.Colors
	for i, p := range pts {
		x, y := cam.project(p, w, h)
		fill := "var(--rp-mint,#08F79B)"
		if color && colors != nil {
			j := i * 3
			fill = fmt.Sprintf("#%02x%02x%02x", colors[j], colors[j+1], colors[j+2])
		}
		b.WriteString(svgDisc(x, y, r, fill))
	}
	b.WriteString(`<text x="12" y="388" class=mo-svgtext>` +
		html.EscapeString(i18n.T("motion.pcPreviewCaption", i18n.A{"name": name, "count": strconv.Itoa(sel.Count())})) + `</text>`)
	b.WriteString(`</svg>`)
	return b.String()
}

// moPCToggle turns point-cloud preview mode on/off; needs a loaded avatar mesh.
func (u *UI) moPCToggle(on bool) {
	s := u.mo()
	s.mu.Lock()
	hasModel := s.modelOn && s.model != nil
	s.pcOn = on && hasModel
	s.mu.Unlock()
	if on && !hasModel {
		u.toast(i18n.T("motion.toast.pcNeedsModel"))
	}
	if on && hasModel {
		u.moRebuildPCSel()
	}
	u.moPatchBody()
}

// moRebuildPCSel recomputes the preview selection (called on model load / colour change /
// enabling the mode). No-op without a model.
func (u *UI) moRebuildPCSel() {
	s := u.mo()
	s.mu.Lock()
	model, color := s.model, s.pcColor
	s.mu.Unlock()
	if model == nil {
		return
	}
	sel := pointcloud.Select(model, moPCPreviewPoints, color)
	s.mu.Lock()
	s.pcSel = sel
	s.mu.Unlock()
}

// moPCPoints maps the density choice to a target point count for the export.
func moPCPoints(density string) int {
	switch density {
	case "low":
		return 8000
	case "high":
		return 80000
	default:
		return 24000
	}
}

// moPCExport runs the render.pointcloud worker; out = user-picked .rmpc path. Reuses the
// #mo-render-prog row + rendering flag (mutually exclusive with the video render).
func (u *UI) moPCExport(out string) {
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
	model, recName := s.model, s.recName
	density, color := s.pcDensity, s.pcColor
	if model == nil || recName == "" {
		s.mu.Unlock()
		u.toast(i18n.T("motion.toast.pcNeedsModel"))
		return
	}
	s.rendering = true
	s.rPct, s.rFrame, s.rFrames, s.rPhase, s.rDone = 0, 0, 0, "", ""
	s.mu.Unlock()

	params := map[string]any{
		"recording": filepath.Join(moRecDir(), recName+".json"),
		"avatar":    u.svc.Cfg.Features.VRCTools.AvatarVRM,
		"fps":       30,
		"points":    moPCPoints(density),
		"color":     color,
		"out":       out,
	}
	u.toast(i18n.T("motion.toast.pcExporting"))
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
		raw, err := u.svc.Workers.RunStreamBackground(ctx, "render", "render.pointcloud", params, onProgress)
		if err != nil {
			u.moSetQuiet(func(s *moSt) { s.rendering, s.rDone, s.rOK = false, i18n.T("motion.toast.pcFailed")+err.Error(), false })
			u.moPatchRenderProg()
			u.toast(i18n.T("motion.toast.pcFailed") + err.Error())
			return
		}
		var res struct {
			Frames int   `json:"frames"`
			Points int   `json:"points"`
			Bytes  int64 `json:"bytes"`
		}
		_ = json.Unmarshal(raw, &res)
		msg := i18n.T("motion.toast.pcExported", i18n.A{
			"frames": strconv.Itoa(res.Frames), "points": strconv.Itoa(res.Points),
			"size": humanBytes(uint64(res.Bytes)), "path": filepath.Base(out),
		})
		u.moSetQuiet(func(s *moSt) { s.rendering, s.rDone, s.rOK = false, msg, true })
		u.moPatchRenderProg()
		u.toast(msg)
	})
}
