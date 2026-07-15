package webui

import (
	"fmt"
	"html"
	"maps"
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
	paths, loaded := u.vrcCachedPaths() // off-thread WalkDir scan; loading until it lands
	if !loaded {
		return hint("info", i18n.T("vrchat.groups.loadingGeneric"))
	}
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
	u.cpvEnsure("vrc", p.File)
	svg := u.cpvView("vrc")
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
		u.cpvPlayBtn("vrc"),
		btn(i18n.T("vrchat.action.loadIntoVRChat"), "primary", "vrc-campath-load", ""),
		vrcPathBtn(i18n.T("vrchat.action.copyFilePath"), "ghost", "copy", p.File),
		btn(i18n.T("vrchat.action.organizeNow"), "outline", "vrc-campath-organize", ""),
	)
	detail := svg + info + buttons + hint("info", i18n.T("campath.hint"))
	return masterDetail(list.String(), detail)
}

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
	photos, loaded := u.vrcCachedPhotos() // off-thread WalkDir scan; loading until it lands
	if !loaded {
		return hint("info", i18n.T("vrchat.groups.loadingGeneric"))
	}
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
