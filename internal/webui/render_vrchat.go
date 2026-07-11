package webui

import (
	"fmt"
	"html"
	"maps"
	"math"
	"strings"
	"unicode/utf8"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/vrccampaths"
	"rave.page/mate/internal/vrchat"
)

// renderVRChat is the VRChat tab at parity with the Fyne views: account status region (live-ticked),
// then two sub-views - Profile (status/bio editors, animated-emoji flipbook generator, camera paths
// with inline 3-D preview, screenshots browser) and Groups (full group-management workspace over
// the local session; render_vrchat_groups.go).
func (u *UI) renderVRChat() string {
	if u.svc.Vrchat == nil {
		return panel(i18n.T("tab.vrchat"), "") + emptyState(i18n.T("vrchat.unavailable"))
	}
	st := u.svc.Vrchat.State()
	var b strings.Builder
	b.WriteString(panel(i18n.T("tab.vrchat"), i18n.T("vrchat.subtitle")))
	b.WriteString(`<div id=vrc-status-region>` + u.vrcStatusRegion() + `</div>`)
	b.WriteString(subTabs("vrcg-sub:", u.vrcgSub(), [2]string{"profile", i18n.T("vrchat.subtab.profile")}, [2]string{"groups", i18n.T("vrchat.subtab.groups")}))

	if u.vrcgSub() == "groups" {
		b.WriteString(`<div id=vrcg-body>` + u.vrcgBody() + `</div>`)
		return b.String()
	}

	if st.LoggedIn {
		u.ensureVRCSeed()
		b.WriteString(section(i18n.T("vrchat.section.statusBio"), `<div id=vrc-editor>`+u.vrcEditorHTML()+`</div>`))
	} else {
		b.WriteString(section(i18n.T("vrchat.section.statusBio"), hint("info", i18n.T("vrchat.hint.signInToEditProfile"))))
	}

	b.WriteString(section(i18n.T("vrchat.section.emotes"), u.vrcEmotesHTML()))

	if u.svc.VRCTools != nil {
		b.WriteString(section(i18n.T("vrchat.section.cameraPaths"), `<div id=vrc-campaths>`+u.vrcCampathsBody()+`</div>`))
		b.WriteString(section(i18n.T("vrchat.section.photos"), `<div id=vrc-photos-body>`+u.photosBody()+`</div>`))
	}
	return b.String()
}

// ── account / pipeline status (live-ticked) ──

func (u *UI) vrcStatusRegion() string {
	if u.svc.Vrchat == nil {
		return ""
	}
	st := u.svc.Vrchat.State()
	if !st.LoggedIn {
		return `<div class="rp-card">` + statusRow("muted", i18n.T("tab.vrchat"), i18n.T("vrchat.status.notSignedIn")) + `</div>`
	}
	variant, line := "muted", i18n.T("vrchat.pipeline.off")
	if u.svc.VrchatPipe != nil {
		s := u.svc.VrchatPipe.Status()
		switch {
		case s.Connected:
			variant, line = "success", i18n.T("vrchat.pipeline.live")
		case s.LastError != "":
			variant, line = "warning", i18n.T("vrchat.pipeline.idleWithError", i18n.A{"error": s.LastError})
		default:
			variant, line = "muted", i18n.T("vrchat.pipeline.idle")
		}
	}
	return `<div class="rp-card">` + statusRow(variant, i18n.T("vrchat.status.signedInAs", i18n.A{"name": orDash(st.DisplayName)}), line) + `</div>`
}

// ── status & bio editor ──

func (u *UI) vrcEditorHTML() string {
	f := &u.svc.Cfg.Features.VRChat
	vrcMu.Lock()
	status, desc, bio := vrcEd.status, vrcEd.desc, vrcEd.bio
	ev := map[string]string{}
	maps.Copy(ev, vrcEd.eventVars)
	vrcMu.Unlock()

	resolved := vrcResolveBio(bio, f.BioVars, ev)
	descN := utf8.RuneCountInString(desc)
	bioN := utf8.RuneCountInString(bio)

	descCls, bioCls := "vrc-count", "vrc-count"
	if descN > vrchat.MaxStatusDescription {
		descCls += " over"
	}
	if bioN > vrchat.MaxBio {
		bioCls += " over"
	}
	descOn := `oninput='var c=document.getElementById("vrc-desc-count");if(c){c.textContent=[...this.value].length+" / ` +
		fmt.Sprint(vrchat.MaxStatusDescription) + `";c.className="vrc-count"+([...this.value].length>` + fmt.Sprint(vrchat.MaxStatusDescription) + `?" over":"")}'`
	bioOn := `oninput='var c=document.getElementById("vrc-bio-count");if(c){c.textContent=[...this.value].length+" / ` +
		fmt.Sprint(vrchat.MaxBio) + `";c.className="vrc-count"+([...this.value].length>` + fmt.Sprint(vrchat.MaxBio) + `?" over":"")}'`

	var b strings.Builder

	// Status card.
	b.WriteString(`<div class="rp-card vrc-card"><div class=vrc-h>` + html.EscapeString(i18n.T("vrchat.card.status")) + tipTopic("vrchat-presence") + `</div>`)
	b.WriteString(`<form data-act=vrc-status>`)
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(i18n.T("vrchat.field.presence")) + `</span>` +
		`<select class="field-input select-input" name=status>` + vrcPresenceOptions(status) + `</select></label>`)
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(i18n.T("vrchat.field.statusMessage")) + ` ` +
		`<b class="` + descCls + `" id=vrc-desc-count>` + fmt.Sprintf("%d / %d", descN, vrchat.MaxStatusDescription) + `</b></span>` +
		`<input class=field-input name=desc maxlength=32 value="` + html.EscapeString(desc) + `" ` + descOn + `></label>`)
	b.WriteString(`<button class="rp-btn rp-btn--go" type=submit>` + html.EscapeString(i18n.T("vrchat.action.saveStatus")) + `</button></form>`)
	b.WriteString(`<div class=btn-row>` + vrcPresetSelect("vrc-status-preset", i18n.T("vrchat.preset.loadStatusPlaceholder"), statusPresetNamesW(f.StatusPresets)) +
		btn(i18n.T("vrchat.action.presets"), "outline", "vrc-status-presets", "") + `</div></div>`)

	// Bio card.
	b.WriteString(`<div class="rp-card vrc-card"><div class=vrc-h>` + html.EscapeString(i18n.T("vrchat.card.bio")) + `</div>`)
	b.WriteString(`<form data-act=vrc-bio>`)
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(i18n.T("vrchat.card.bio")) + ` ` +
		`<b class="` + bioCls + `" id=vrc-bio-count>` + fmt.Sprintf("%d / %d", bioN, vrchat.MaxBio) + `</b></span>` +
		`<textarea class=field-input name=bio rows=4 ` + bioOn + `>` + html.EscapeString(bio) + `</textarea></label>`)
	b.WriteString(`<button class="rp-btn rp-btn--go" type=submit>` + html.EscapeString(i18n.T("vrchat.action.saveBio")) + `</button></form>`)
	b.WriteString(hint("info", i18n.T("vrchat.hint.bioSaveInfo")))
	if resolved != bio {
		b.WriteString(`<div class=vrc-preview-wrap>` + html.EscapeString(i18n.T("vrchat.editor.placeholderPreview")) + `<div class=vrc-preview>` + html.EscapeString(resolved) + `</div></div>`)
	}
	b.WriteString(`<div class=btn-row>` + vrcPresetSelect("vrc-bio-preset", i18n.T("vrchat.preset.loadBioPlaceholder"), bioPresetNamesW(f.BioPresets)) +
		btn(i18n.T("vrchat.action.presets"), "outline", "vrc-bio-presets", "") + btn(i18n.T("vrchat.action.variables"), "outline", "vrc-bio-vars", "") +
		btn(i18n.T("vrchat.action.refreshEvents"), "ghost", "vrc-events-refresh", "") + `</div></div>`)

	return b.String()
}

// vrcPathBtn is a button whose data-val is a filesystem path - uses real double-quotes + HTML-escape
// (NOT %q, which would double Windows backslashes and corrupt the path).
func vrcPathBtn(label, variant, act, path string) string {
	return fmt.Sprintf(`<button class="rp-btn rp-btn--%s" data-act=%s data-val="%s">%s</button>`,
		variant, attrQ(act), html.EscapeString(path), html.EscapeString(label))
}

func vrcPresenceOptions(cur string) string {
	var out strings.Builder
	out.WriteString(`<option value="">` + html.EscapeString(i18n.T("vrchat.presence.placeholder")) + `</option>`)
	for _, s := range vrchat.Statuses {
		sel := ""
		if s == cur {
			sel = " selected"
		}
		fmt.Fprintf(&out, `<option value=%s%s>%s</option>`, attrQ(s), sel, html.EscapeString(s))
	}
	return out.String()
}

// vrcPresetSelect builds a name-picker <select> that dispatches act (val = chosen name) on change.
func vrcPresetSelect(act, placeholder string, names []string) string {
	var o strings.Builder
	fmt.Fprintf(&o, `<select class="field-input select-input" data-act=%s><option value="">%s</option>`, attrQ(act), html.EscapeString(placeholder))
	for _, n := range names {
		fmt.Fprintf(&o, `<option value=%s>%s</option>`, attrQ(n), html.EscapeString(n))
	}
	o.WriteString(`</select>`)
	return o.String()
}

// ── emotes (flipbook generator) ──

func (u *UI) vrcEmotesHTML() string {
	f := &u.svc.Cfg.Features.VRChat
	var b strings.Builder
	b.WriteString(`<div class="rp-card vrc-card">`)
	b.WriteString(hint("info", i18n.T("vrchat.emotes.hint")))
	b.WriteString(`<form data-act=vrc-emote-gen>`)
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(i18n.T("vrchat.emotes.field.source")) + `</span><input class=field-input name=source placeholder="C:\path\clip.mp4"></label>`)
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(i18n.T("vrchat.emotes.field.name")) + `</span><input class=field-input name=name placeholder="emoji name"></label>`)
	b.WriteString(fpair(`<label class=field><span class=field-label>`+html.EscapeString(i18n.T("vrchat.emotes.field.frames"))+`</span><select class="field-input select-input" name=frames>`+
		vrcFrameOptions()+`</select></label>`,
		`<label class=field><span class=field-label>`+html.EscapeString(i18n.T("vrchat.emotes.field.fps"))+`</span><input class=field-input name=fps type=number value=20 min=1 max=120></label>`))
	b.WriteString(fpair(`<label class=field><span class=field-label>`+html.EscapeString(i18n.T("vrchat.emotes.field.trimStart"))+`</span><input class=field-input name=trimStart placeholder="optional"></label>`,
		`<label class=field><span class=field-label>`+html.EscapeString(i18n.T("vrchat.emotes.field.trimEnd"))+`</span><input class=field-input name=trimEnd placeholder="optional"></label>`))
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(i18n.T("vrchat.emotes.field.outputDir")) + `</span><input class=field-input name=outdir value="` + html.EscapeString(f.ResolvedFlipbookDir()) + `"></label>`)
	b.WriteString(`<label class=row><span class=row-label>` + html.EscapeString(i18n.T("vrchat.emotes.pingpong")) + `</span>` +
		`<span class=switch><input type=checkbox name=pingpong value=1><span class=switch-track></span></span></label>`)
	b.WriteString(`<label class=row><span class=row-label>` + html.EscapeString(i18n.T("vrchat.emotes.crop")) + `</span>` +
		`<span class=switch><input type=checkbox name=crop value=1><span class=switch-track></span></span></label>`)
	b.WriteString(`<div class=btn-row>` +
		`<input class=field-input name=cropx placeholder="x" style="width:70px">` +
		`<input class=field-input name=cropy placeholder="y" style="width:70px">` +
		`<input class=field-input name=cropw placeholder="w" style="width:70px">` +
		`<input class=field-input name=croph placeholder="h" style="width:70px"></div>`)
	b.WriteString(`<button class="rp-btn rp-btn--go" type=submit>` + html.EscapeString(i18n.T("vrchat.emotes.generate")) + `</button></form>`)
	b.WriteString(`<div id=vrc-emote-result></div>`)
	b.WriteString(`<div class=btn-row>` +
		vrcPathBtn(i18n.T("vrchat.action.openOutputFolder"), "outline", "open-url", f.ResolvedFlipbookDir()) +
		btn(i18n.T("vrchat.action.openEmojiUploadPage"), "explore", "open-url", vrcEmojiUploadURL) + `</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// ── camera paths ──

func (u *UI) vrcCampathsBody() string {
	if u.svc.VRCTools == nil {
		return emptyState(i18n.T("vrchat.tools.unavailable"))
	}
	paths := vrcSortedPaths(u)
	if len(paths) == 0 {
		return emptyState(i18n.T("vrchat.campaths.empty"))
	}
	vrcMu.Lock()
	sel := vrcCampathSel
	vrcMu.Unlock()
	if sel < 0 || sel >= len(paths) {
		sel = 0
	}

	var list strings.Builder
	list.WriteString(`<div class=vrc-plist>`)
	for i, p := range paths {
		cls := "vrc-plist-item"
		if i == sel {
			cls += " active"
		}
		fmt.Fprintf(&list, `<button class=%s data-act=%s>%s</button>`, attrQ(cls), attrQ(fmt.Sprintf("vrc-campath:%d", i)), html.EscapeString(vrcPathLabel(p)))
	}
	list.WriteString(`</div>`)

	p := paths[sel]
	svg := `<div class=vrc-preview>` + html.EscapeString(i18n.T("vrchat.campaths.failedToRead")) + `</div>`
	if pts, err := vrccampaths.LoadPoints(p.File); err == nil {
		svg = campathSVG(pts, 600, 340)
	}
	where := p.WorldName
	if p.Local {
		where = i18n.T("vrchat.campaths.playerRelative")
	} else if where == "" {
		where = i18n.T("vrchat.campaths.unknownWorld")
	}
	info := fmt.Sprintf(`<div class=vrc-cp-info><b>%s</b><br>%s</div>`,
		html.EscapeString(p.Name), html.EscapeString(i18n.T("vrchat.campaths.info", i18n.A{
			"where":     where,
			"keyframes": i18n.Tn("vrchat.campaths.keyframes", p.Points),
			"duration":  fmt.Sprintf("%.1f", p.DurationSec),
			"when":      p.SavedAt.Format("2006-01-02 15:04"),
		})))
	buttons := btnRow(
		btn(i18n.T("vrchat.action.loadIntoVRChat"), "primary", "vrc-campath-load", ""),
		vrcPathBtn(i18n.T("vrchat.action.copyFilePath"), "ghost", "copy", p.File),
		btn(i18n.T("vrchat.action.organizeNow"), "outline", "vrc-campath-organize", ""),
	)
	detail := svg + info + buttons + hint("info", i18n.T("vrchat.campaths.svgHint"))
	return masterDetail(list.String(), detail)
}

// campathSVG renders a static 3-D (fixed isometric) preview of a camera path as inline SVG: floor
// grid, speed-coloured polyline, keyframe dots + facing arrows, and a start marker. Own projection
// (does not touch graph.go).
func campathSVG(pts []vrccampaths.Point, w, h int) string {
	if len(pts) == 0 {
		return `<div class=vrc-preview>Empty path.</div>`
	}
	type node struct {
		pos, fwd [3]float64
		spd      float64
	}
	nodes := make([]node, len(pts))
	lo := [3]float64{1e9, 1e9, 1e9}
	hi := [3]float64{-1e9, -1e9, -1e9}
	for i, p := range pts {
		pos := [3]float64{p.Position.X, p.Position.Y, p.Position.Z}
		for k := range 3 {
			if pos[k] < lo[k] {
				lo[k] = pos[k]
			}
			if pos[k] > hi[k] {
				hi[k] = pos[k]
			}
		}
		nodes[i] = node{pos: pos, fwd: vrcEulerFwd(p.Rotation), spd: p.Speed}
	}
	center := [3]float64{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	floorY := lo[1]
	diag := math.Sqrt(vrcSq(hi[0]-lo[0]) + vrcSq(hi[1]-lo[1]) + vrcSq(hi[2]-lo[2]))
	gridR := math.Max(1, (hi[0]-lo[0]+hi[2]-lo[2])/2)
	dist := diag*1.3 + 1.5
	const yaw, pitch = 0.6, 0.35
	cy, sy := math.Cos(yaw), math.Sin(yaw)
	cp, sp := math.Cos(pitch), math.Sin(pitch)
	fl := float64(h) * 0.9
	proj := func(p [3]float64) (float64, float64) {
		dx, dy, dz := p[0]-center[0], p[1]-center[1], p[2]-center[2]
		x1 := dx*cy + dz*sy
		z1 := -dx*sy + dz*cy
		y2 := dy*cp - z1*sp
		z2 := dy*sp + z1*cp
		depth := dist - z2
		if depth < 0.15 {
			depth = 0.15
		}
		return float64(w)/2 + fl*x1/depth, float64(h)/2 - fl*y2/depth
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" class="vrc-svg" preserveAspectRatio="xMidYMid meet">`, w, h)
	// quote attr values before /> - unquoted eats the "/" and unclosed graphics elements
	// swallow the rest of the SVG (see graph.go)
	b.WriteString(`<rect width="100%" height="100%" class="vrc-svg-bg"/>`)
	const n = 6
	step := (2 * gridR) / n
	for i := 0; i <= n; i++ {
		d := -gridR + step*float64(i)
		ax, ay := proj([3]float64{center[0] - gridR, floorY, center[2] + d})
		bx, by := proj([3]float64{center[0] + gridR, floorY, center[2] + d})
		vrcLine(&b, ax, ay, bx, by, "vrc-grid", "")
		cx, cyy := proj([3]float64{center[0] + d, floorY, center[2] - gridR})
		dx2, dy2 := proj([3]float64{center[0] + d, floorY, center[2] + gridR})
		vrcLine(&b, cx, cyy, dx2, dy2, "vrc-grid", "")
	}
	maxSpd := 0.1
	for _, nd := range nodes {
		if nd.spd > maxSpd {
			maxSpd = nd.spd
		}
	}
	for i := 1; i < len(nodes); i++ {
		ax, ay := proj(nodes[i-1].pos)
		bx, by := proj(nodes[i].pos)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="2"/>`,
			ax, ay, bx, by, vrcSpeedColor(nodes[i-1].spd/maxSpd))
	}
	for _, nd := range nodes {
		px, py := proj(nd.pos)
		tip := [3]float64{nd.pos[0] + nd.fwd[0]*0.4, nd.pos[1] + nd.fwd[1]*0.4, nd.pos[2] + nd.fwd[2]*0.4}
		tx, ty := proj(tip)
		vrcLine(&b, px, py, tx, ty, "vrc-facing", "")
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="3" class="vrc-kf"/>`, px, py)
	}
	sx, syy := proj(nodes[0].pos)
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="6" class="vrc-marker"/>`, sx, syy)
	b.WriteString(`</svg>`)
	return b.String()
}

func vrcLine(b *strings.Builder, ax, ay, bx, by float64, class, _ string) {
	fmt.Fprintf(b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" class="%s"/>`, ax, ay, bx, by, class)
}

// vrcEulerFwd converts a VRChat euler (degrees) into a unit world-forward vector.
func vrcEulerFwd(r vrccampaths.Vec3) [3]float64 {
	const d2r = math.Pi / 180
	yaw, pitch := r.Y*d2r, r.X*d2r
	return [3]float64{math.Sin(yaw) * math.Cos(pitch), -math.Sin(pitch), math.Cos(yaw) * math.Cos(pitch)}
}

// vrcSpeedColor maps 0..1 → mint (slow) → pink (fast).
func vrcSpeedColor(fr float64) string {
	if fr < 0 {
		fr = 0
	}
	if fr > 1 {
		fr = 1
	}
	return fmt.Sprintf("rgb(%d,%d,%d)", int(8+247*fr), int(247-100*fr), int(155-100*fr))
}

func vrcSq(v float64) float64 { return v * v }

func vrcPathLabel(p vrccampaths.Path) string {
	where := p.WorldName
	if p.Local {
		where = i18n.T("vrchat.campaths.playerRelative")
	} else if where == "" {
		where = i18n.T("vrchat.campaths.unknown")
	}
	return i18n.T("vrchat.campaths.pathLabel", i18n.A{
		"where":    where,
		"name":     p.Name,
		"points":   fmt.Sprint(p.Points),
		"duration": fmt.Sprintf("%.0f", p.DurationSec),
	})
}

// ── photos ──

func (u *UI) photosBody() string {
	if u.svc.VRCTools == nil {
		return emptyState(i18n.T("vrchat.tools.unavailable"))
	}
	photos := u.svc.VRCTools.Photos()
	if len(photos) == 0 {
		return emptyState(i18n.T("vrchat.photos.empty"))
	}
	groups := vrcPhotoGroups(photos)
	vrcMu.Lock()
	grp := vrcPhotoGroup
	vrcMu.Unlock()
	if grp == "" || !vrcGroupExists(groups, grp) {
		grp = groups[0].label
	}

	var list strings.Builder
	list.WriteString(`<div class=vrc-glist>`)
	for _, g := range groups {
		cls := "vrc-glist-item"
		if g.label == grp {
			cls += " active"
		}
		fmt.Fprintf(&list, `<button class=%s data-act=%s><span>%s</span><span class=vrc-gcount>%d</span></button>`,
			attrQ(cls), attrQ("vrc-photos-group:"+g.label), html.EscapeString(g.label), g.count)
	}
	list.WriteString(`</div>`)

	const maxCells = 60
	const thumbW = 320 // ~2x a grid cell; browser downsamples, decode+cache is one-shot per file
	var cells strings.Builder
	shown, total := 0, 0
	for i := range photos {
		ph := photos[i]
		if grp != vrcAllPhotos && ph.Label != grp {
			continue
		}
		total++
		if shown >= maxCells {
			continue
		}
		shown++
		// Cached resized-image endpoint: the browser lazy-loads + caches by URL (no base64 in
		// patches). onerror falls back to the placeholder tile if decode fails.
		imgHTML := `<div class="vrc-thumb vrc-thumb-ph"></div>`
		if src := u.imgURL(ph.File, thumbW); src != "" {
			imgHTML = `<img class=vrc-thumb loading=lazy src=` + attrQ(src) +
				` onerror="this.className='vrc-thumb vrc-thumb-broken'">`
		}
		fmt.Fprintf(&cells, `<button class=vrc-cell data-act="vrc-photo-view:%s" title=%q>%s<span class=vrc-cap>%s</span></button>`,
			html.EscapeString(ph.File), html.EscapeString(ph.Name), imgHTML, html.EscapeString(ph.Label))
	}
	note := ""
	if total > maxCells {
		note = `<div class=vrc-note>` + html.EscapeString(i18n.T("vrchat.photos.showingFirst", i18n.A{"shown": fmt.Sprint(maxCells), "total": fmt.Sprint(total)})) + `</div>`
	}
	detail := `<div class=vrc-grid-photos>` + cells.String() + `</div>` + note +
		`<div class=btn-row>` + vrcPathBtn(i18n.T("vrchat.action.openFolder"), "outline", "open-url", u.svc.VRCTools.PhotosDir()) + `</div>`
	return masterDetail(list.String(), detail)
}

// ── preset name helpers ──

func statusPresetNamesW(ps []config.VRChatStatusPreset) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func bioPresetNamesW(ps []config.VRChatBioPreset) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}
