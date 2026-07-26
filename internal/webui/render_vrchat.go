package webui

import (
	"fmt"
	"html"
	"maps"
	"strings"
	"unicode/utf8"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/flipbook"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/vrccampaths"
	"rave.page/mate/internal/vrchat"
	"rave.page/mate/internal/zigui"
)

// VRChat tab at parity with the Fyne views: account status region (live-ticked), then two
// sub-views - Profile (status/bio editors, animated-emoji flipbook generator, camera paths with
// inline 3-D preview, screenshots browser) and Groups (full group-management workspace over the
// local session; render_vrchat_groups.go).
//
// Zig-rendered (native/zigui/src/vrchat.zig): the *State builders below own everything impure
// (session/config/locks/off-thread caches/i18n/media URLs); the *HTML renderers stay the Go
// fallback + golden reference (zigui_golden_vrchat_test.go). Fragments the tick/actions patch
// (#vrc-status-region, #vrc-editor, #vrc-campaths, #vrc-photos-body, #vrcg-body) each export
// their own renderer so a patch and a full render share one markup source.

// ── resolved render state (JSON → Zig) ──

// vrcStatusSt is the account/pipeline status region. Present=false ⇒ renders empty.
type vrcStatusSt struct {
	Present bool   `json:"present"`
	Variant string `json:"variant"`
	Label   string `json:"label"`
	DL      string `json:"dl"` // strings.ToLower(Label)
	Line    string `json:"line"`
}

// vrcOptSt is one <option> row (presence picker / preset pickers).
type vrcOptSt struct {
	Val   string `json:"val"`
	Label string `json:"label"`
	Sel   bool   `json:"sel"`
}

// vrcPresetSelSt is a name-picker <select> that dispatches Act (val = chosen name).
type vrcPresetSelSt struct {
	Act         string   `json:"act"`
	Placeholder string   `json:"placeholder"`
	Names       []string `json:"names,omitempty"`
}

// vrcEditorSt is the status & bio editor (#vrc-editor).
type vrcEditorSt struct {
	StatusTitle    string         `json:"statusTitle"`
	StatusTip      string         `json:"statusTip"`             // legacy RAW tooltip markup (bridge)
	StatusTipS     *tipSt         `json:"statusTipSt,omitempty"` // structured tooltip - wins over StatusTip
	PresenceLabel  string         `json:"presenceLabel"`
	Presence       []vrcOptSt     `json:"presence,omitempty"` // [0] = placeholder option
	StatusMsgLabel string         `json:"statusMsgLabel"`
	DescCls        string         `json:"descCls"`
	DescCount      string         `json:"descCount"` // "n / max"
	DescVal        string         `json:"descVal"`
	MaxDesc        int            `json:"maxDesc"`
	SaveStatus     string         `json:"saveStatus"`
	StatusPreset   vrcPresetSelSt `json:"statusPreset"`
	PresetsLabel   string         `json:"presetsLabel"`
	BioTitle       string         `json:"bioTitle"` // card head + field label
	BioCls         string         `json:"bioCls"`
	BioCount       string         `json:"bioCount"`
	BioVal         string         `json:"bioVal"`
	MaxBio         int            `json:"maxBio"`
	SaveBio        string         `json:"saveBio"`
	BioHint        string         `json:"bioHint"`
	PreviewLabel   string         `json:"previewLabel"`
	Preview        string         `json:"preview"`    // resolved bio; "" = no preview block
	HasPreview     bool           `json:"hasPreview"` // resolved != raw
	BioPreset      vrcPresetSelSt `json:"bioPreset"`
	VarsLabel      string         `json:"varsLabel"`
	RefreshLabel   string         `json:"refreshLabel"`
}

// vrcFrameOptSt is one flipbook tier <option>.
type vrcFrameOptSt struct {
	Frames int  `json:"frames"`
	Grid   int  `json:"grid"`
	Res    int  `json:"res"`
	Sel    bool `json:"sel"`
}

// vrcEmotesSt is the animated-emoji flipbook generator card.
type vrcEmotesSt struct {
	Hint        string          `json:"hint"`
	SourceLabel string          `json:"sourceLabel"`
	NameLabel   string          `json:"nameLabel"`
	FramesLabel string          `json:"framesLabel"`
	FPSLabel    string          `json:"fpsLabel"`
	TrimStart   string          `json:"trimStart"`
	TrimEnd     string          `json:"trimEnd"`
	OutDirLabel string          `json:"outDirLabel"`
	FrameOpts   []vrcFrameOptSt `json:"frameOpts,omitempty"`
	OutDir      string          `json:"outDir"`
	PingPong    string          `json:"pingpong"`
	Crop        string          `json:"crop"`
	Generate    string          `json:"generate"`
	OpenFolder  string          `json:"openFolder"`
	OpenUpload  string          `json:"openUpload"`
	UploadURL   string          `json:"uploadUrl"`
}

// vrcPathItemSt is one camera-path list row.
type vrcPathItemSt struct {
	Idx    int    `json:"idx"`
	Label  string `json:"label"`
	Active bool   `json:"active"`
}

// vrcCampathsSt is the camera-paths master/detail (#vrc-campaths). State ∈
// {unavailable,loading,empty,detail}.
type vrcCampathsSt struct {
	State    string          `json:"state"`
	Msg      string          `json:"msg"` // unavailable/loading/empty text
	Items    []vrcPathItemSt `json:"items,omitempty"`
	SVG      string          `json:"svg"`     // pre-rendered cpvView viewer (trusted)
	PlayBtn  string          `json:"playBtn"` // pre-rendered cpvPlayBtn (trusted)
	Name     string          `json:"name"`
	Info     string          `json:"info"`
	Load     string          `json:"load"`
	Copy     string          `json:"copy"`
	CopyPath string          `json:"copyPath"`
	Organize string          `json:"organize"`
	Hint     string          `json:"hint"`
}

// vrcPhotoGrpSt is one photo-group list row.
type vrcPhotoGrpSt struct {
	Label  string `json:"label"`
	Count  int    `json:"count"`
	Active bool   `json:"active"`
}

// vrcPhotoCellSt is one thumbnail cell. TitleQ is Go-quoted (%q of the escaped name) - Go's
// strconv quoting has no Zig equivalent, so it is resolved here and emitted verbatim.
type vrcPhotoCellSt struct {
	File   string `json:"file"`
	TitleQ string `json:"titleQ"`
	Label  string `json:"label"`
	Src    string `json:"src"` // "" = placeholder tile
}

// vrcPhotosSt is the screenshots browser (#vrc-photos-body). State ∈
// {unavailable,loading,empty,detail}.
type vrcPhotosSt struct {
	State      string           `json:"state"`
	Msg        string           `json:"msg"`
	Groups     []vrcPhotoGrpSt  `json:"groups,omitempty"`
	Cells      []vrcPhotoCellSt `json:"cells,omitempty"`
	Note       string           `json:"note"` // "" = none
	OpenFolder string           `json:"openFolder"`
	PhotosDir  string           `json:"photosDir"`
}

// vrcTabSt is the resolved render state for the whole VRChat tab.
type vrcTabSt struct {
	Available    bool          `json:"available"`
	Title        string        `json:"title"`
	Sub          string        `json:"sub"`
	Unavailable  string        `json:"unavailable"`
	Status       vrcStatusSt   `json:"status"`
	SubActive    string        `json:"subActive"`
	SubTabs      []vgTabSt     `json:"subTabs,omitempty"`
	Groups       vrcgState     `json:"groups"`
	LoggedIn     bool          `json:"loggedIn"`
	SecStatusBio string        `json:"secStatusBio"`
	SignInHint   string        `json:"signInHint"`
	Editor       vrcEditorSt   `json:"editor"`
	SecEmotes    string        `json:"secEmotes"`
	Emotes       vrcEmotesSt   `json:"emotes"`
	HasTools     bool          `json:"hasTools"`
	SecCamPaths  string        `json:"secCamPaths"`
	CamPaths     vrcCampathsSt `json:"camPaths"`
	SecPhotos    string        `json:"secPhotos"`
	Photos       vrcPhotosSt   `json:"photos"`
}

// ── bridges ──

// renderVRChat renders the VRChat tab (Zig when linked, Go otherwise).
func (u *UI) renderVRChat() string {
	st := u.vrchatState()
	if zigui.Available() {
		if h, ok := zigWire("RenderVRChatV2", wireVrcTab(st), zigui.RenderVRChatV2,
			zigui.RenderVRChat, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vrchatHTML(st)
}

func (u *UI) vrcStatusRegion() string {
	st := u.vrcStatusState()
	if zigui.Available() {
		if h, ok := zigWire("RenderVRChatStatusV2", wireVrcStatus(st), zigui.RenderVRChatStatusV2,
			zigui.RenderVRChatStatus, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vrcStatusHTML(st)
}

func (u *UI) vrcEditorHTML() string {
	st := u.vrcEditorState()
	if zigui.Available() {
		if h, ok := zigWire("RenderVRChatEditorV2", wireVrcEditor(st), zigui.RenderVRChatEditorV2,
			zigui.RenderVRChatEditor, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vrcEditorRenderHTML(st)
}

func (u *UI) vrcCampathsBody() string {
	st := u.vrcCampathsState()
	if zigui.Available() {
		if h, ok := zigWire("RenderVRChatCampathsV2", wireVrcCampaths(st), zigui.RenderVRChatCampathsV2,
			zigui.RenderVRChatCampaths, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vrcCampathsHTML(st)
}

func (u *UI) photosBody() string {
	st := u.vrcPhotosState()
	if zigui.Available() {
		if h, ok := zigWire("RenderVRChatPhotosV2", wireVrcPhotos(st), zigui.RenderVRChatPhotosV2,
			zigui.RenderVRChatPhotos, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vrcPhotosHTML(st)
}

// ── state builders ──

// vrchatState resolves the whole tab: status region, active sub-view and (on Profile) the
// editor/emotes/campaths/photos sections.
func (u *UI) vrchatState() vrcTabSt {
	st := vrcTabSt{
		Available:   u.svc.Vrchat != nil,
		Title:       i18n.T("tab.vrchat"),
		Sub:         i18n.T("vrchat.subtitle"),
		Unavailable: i18n.T("vrchat.unavailable"),
		SubActive:   u.vrcgSub(),
		SubTabs: []vgTabSt{
			{"profile", i18n.T("vrchat.subtab.profile")},
			{"groups", i18n.T("vrchat.subtab.groups")},
		},
		SecStatusBio: i18n.T("vrchat.section.statusBio"),
		SignInHint:   i18n.T("vrchat.hint.signInToEditProfile"),
		SecEmotes:    i18n.T("vrchat.section.emotes"),
		SecCamPaths:  i18n.T("vrchat.section.cameraPaths"),
		SecPhotos:    i18n.T("vrchat.section.photos"),
	}
	if !st.Available {
		return st
	}
	st.Status = u.vrcStatusState()
	if st.SubActive == "groups" {
		st.Groups = u.vrcgBodyState()
		return st
	}
	st.LoggedIn = u.svc.Vrchat.State().LoggedIn
	if st.LoggedIn {
		u.ensureVRCSeed()
		st.Editor = u.vrcEditorState()
	}
	st.Emotes = u.vrcEmotesState()
	st.HasTools = u.svc.VRCTools != nil
	if st.HasTools {
		st.CamPaths = u.vrcCampathsState()
		st.Photos = u.vrcPhotosState()
	}
	return st
}

// vrcStatusState resolves the account + pipeline status line (live-ticked region).
func (u *UI) vrcStatusState() vrcStatusSt {
	if u.svc.Vrchat == nil {
		return vrcStatusSt{}
	}
	st := u.svc.Vrchat.State()
	mk := func(variant, label, line string) vrcStatusSt {
		return vrcStatusSt{Present: true, Variant: variant, Label: label, DL: strings.ToLower(label), Line: line}
	}
	if !st.LoggedIn {
		return mk("muted", i18n.T("tab.vrchat"), i18n.T("vrchat.status.notSignedIn"))
	}
	signedIn := i18n.T("vrchat.status.signedInAs", i18n.A{"name": orDash(st.DisplayName)})
	// federated session: a paired instance serves it - say so instead of the
	// local pipeline line (the pipeline runs on the serving instance).
	if st.Via != "" {
		return mk("success", signedIn, i18n.T("vrchat.status.viaPeer", i18n.A{"peer": st.Via}))
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
	return mk(variant, signedIn, line)
}

// vrcEditorState resolves the status/bio editor: draft values, rune counts, presets, preview.
func (u *UI) vrcEditorState() vrcEditorSt {
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

	presence := vrcPresenceOpts(status)
	return vrcEditorSt{
		StatusTitle: i18n.T("vrchat.card.status"), StatusTipS: tipTopicSt("vrchat-presence"),
		PresenceLabel: i18n.T("vrchat.field.presence"), Presence: presence,
		StatusMsgLabel: i18n.T("vrchat.field.statusMessage"),
		DescCls:        descCls, DescCount: fmt.Sprintf("%d / %d", descN, vrchat.MaxStatusDescription),
		DescVal: desc, MaxDesc: vrchat.MaxStatusDescription,
		SaveStatus: i18n.T("vrchat.action.saveStatus"),
		StatusPreset: vrcPresetSelSt{Act: "vrc-status-preset", Placeholder: i18n.T("vrchat.preset.loadStatusPlaceholder"),
			Names: statusPresetNamesW(f.StatusPresets)},
		PresetsLabel: i18n.T("vrchat.action.presets"),
		BioTitle:     i18n.T("vrchat.card.bio"),
		BioCls:       bioCls, BioCount: fmt.Sprintf("%d / %d", bioN, vrchat.MaxBio),
		BioVal: bio, MaxBio: vrchat.MaxBio,
		SaveBio: i18n.T("vrchat.action.saveBio"), BioHint: i18n.T("vrchat.hint.bioSaveInfo"),
		PreviewLabel: i18n.T("vrchat.editor.placeholderPreview"), Preview: resolved, HasPreview: resolved != bio,
		BioPreset: vrcPresetSelSt{Act: "vrc-bio-preset", Placeholder: i18n.T("vrchat.preset.loadBioPlaceholder"),
			Names: bioPresetNamesW(f.BioPresets)},
		VarsLabel: i18n.T("vrchat.action.variables"), RefreshLabel: i18n.T("vrchat.action.refreshEvents"),
	}
}

// vrcEmotesState resolves the flipbook generator's labels + tiers + output dir.
func (u *UI) vrcEmotesState() vrcEmotesSt {
	f := &u.svc.Cfg.Features.VRChat
	opts := make([]vrcFrameOptSt, 0, 4)
	for i, t := range flipbook.Tiers() {
		opts = append(opts, vrcFrameOptSt{Frames: t.Frames, Grid: t.Grid, Res: t.FrameRes, Sel: i == 1}) // 16-frame default
	}
	return vrcEmotesSt{
		Hint:        i18n.T("vrchat.emotes.hint"),
		SourceLabel: i18n.T("vrchat.emotes.field.source"),
		NameLabel:   i18n.T("vrchat.emotes.field.name"),
		FramesLabel: i18n.T("vrchat.emotes.field.frames"),
		FPSLabel:    i18n.T("vrchat.emotes.field.fps"),
		TrimStart:   i18n.T("vrchat.emotes.field.trimStart"),
		TrimEnd:     i18n.T("vrchat.emotes.field.trimEnd"),
		OutDirLabel: i18n.T("vrchat.emotes.field.outputDir"),
		FrameOpts:   opts,
		OutDir:      f.ResolvedFlipbookDir(),
		PingPong:    i18n.T("vrchat.emotes.pingpong"),
		Crop:        i18n.T("vrchat.emotes.crop"),
		Generate:    i18n.T("vrchat.emotes.generate"),
		OpenFolder:  i18n.T("vrchat.action.openOutputFolder"),
		OpenUpload:  i18n.T("vrchat.action.openEmojiUploadPage"),
		UploadURL:   vrcEmojiUploadURL,
	}
}

// vrcCampathsState resolves the cached path scan + the selected path's viewer/info/actions.
func (u *UI) vrcCampathsState() vrcCampathsSt {
	if u.svc.VRCTools == nil {
		return vrcCampathsSt{State: "unavailable", Msg: i18n.T("vrchat.tools.unavailable")}
	}
	paths, loaded := u.vrcCachedPaths() // off-thread WalkDir scan; loading until it lands
	if !loaded {
		return vrcCampathsSt{State: "loading", Msg: i18n.T("vrchat.groups.loadingGeneric")}
	}
	if len(paths) == 0 {
		return vrcCampathsSt{State: "empty", Msg: i18n.T("vrchat.campaths.empty")}
	}
	vrcMu.Lock()
	sel := vrcCampathSel
	vrcMu.Unlock()
	if sel < 0 || sel >= len(paths) {
		sel = 0
	}
	st := vrcCampathsSt{State: "detail", Items: make([]vrcPathItemSt, 0, len(paths))}
	for i, p := range paths {
		st.Items = append(st.Items, vrcPathItemSt{Idx: i, Label: vrcPathLabel(p), Active: i == sel})
	}
	p := paths[sel]
	u.cpvEnsure("vrc", p.File)
	where := p.WorldName
	if p.Local {
		where = i18n.T("vrchat.campaths.playerRelative")
	} else if where == "" {
		where = i18n.T("vrchat.campaths.unknownWorld")
	}
	st.SVG, st.PlayBtn = u.cpvView("vrc"), u.cpvPlayBtn("vrc")
	st.Name = p.Name
	st.Info = i18n.T("vrchat.campaths.info", i18n.A{
		"where":     where,
		"keyframes": i18n.Tn("vrchat.campaths.keyframes", p.Points),
		"duration":  fmt.Sprintf("%.1f", p.DurationSec),
		"when":      p.SavedAt.Format("2006-01-02 15:04"),
	})
	st.Load = i18n.T("vrchat.action.loadIntoVRChat")
	st.Copy, st.CopyPath = i18n.T("vrchat.action.copyFilePath"), p.File
	st.Organize = i18n.T("vrchat.action.organizeNow")
	st.Hint = i18n.T("campath.hint")
	return st
}

// vrcPhotosState resolves the cached photo scan into groups + the capped thumbnail grid
// (thumbnails go through the loopback resize endpoint - the browser caches them by URL).
func (u *UI) vrcPhotosState() vrcPhotosSt {
	if u.svc.VRCTools == nil {
		return vrcPhotosSt{State: "unavailable", Msg: i18n.T("vrchat.tools.unavailable")}
	}
	photos, loaded := u.vrcCachedPhotos() // off-thread WalkDir scan; loading until it lands
	if !loaded {
		return vrcPhotosSt{State: "loading", Msg: i18n.T("vrchat.groups.loadingGeneric")}
	}
	if len(photos) == 0 {
		return vrcPhotosSt{State: "empty", Msg: i18n.T("vrchat.photos.empty")}
	}
	groups := vrcPhotoGroups(photos)
	vrcMu.Lock()
	grp := vrcPhotoGroup
	vrcMu.Unlock()
	if grp == "" || !vrcGroupExists(groups, grp) {
		grp = groups[0].label
	}
	st := vrcPhotosSt{State: "detail", Groups: make([]vrcPhotoGrpSt, 0, len(groups)), Cells: []vrcPhotoCellSt{}}
	for _, g := range groups {
		st.Groups = append(st.Groups, vrcPhotoGrpSt{Label: g.label, Count: g.count, Active: g.label == grp})
	}
	const maxCells = 60
	const thumbW = 320 // ~2x a grid cell; browser downsamples, decode+cache is one-shot per file
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
		st.Cells = append(st.Cells, vrcPhotoCellSt{
			File: ph.File, TitleQ: fmt.Sprintf("%q", html.EscapeString(ph.Name)),
			Label: ph.Label, Src: u.imgURL(ph.File, thumbW),
		})
	}
	if total > maxCells {
		st.Note = i18n.T("vrchat.photos.showingFirst", i18n.A{"shown": fmt.Sprint(maxCells), "total": fmt.Sprint(total)})
	}
	st.OpenFolder, st.PhotosDir = i18n.T("vrchat.action.openFolder"), u.svc.VRCTools.PhotosDir()
	return st
}

// ── pure renderers (golden reference; byte-identical to native/zigui/src/vrchat.zig) ──

func vrchatHTML(st vrcTabSt) string {
	if !st.Available {
		return panel(st.Title, "") + emptyState(st.Unavailable)
	}
	var b strings.Builder
	b.WriteString(panel(st.Title, st.Sub))
	b.WriteString(`<div id=vrc-status-region>` + vrcStatusHTML(st.Status) + `</div>`)
	items := make([][2]string, 0, len(st.SubTabs))
	for _, t := range st.SubTabs {
		items = append(items, [2]string{t.Val, t.Label})
	}
	b.WriteString(subTabs("vrcg-sub:", st.SubActive, items...))

	if st.SubActive == "groups" {
		b.WriteString(`<div id=vrcg-body>` + vrcgBodyHTML(st.Groups) + `</div>`)
		return b.String()
	}

	if st.LoggedIn {
		b.WriteString(section(st.SecStatusBio, `<div id=vrc-editor>`+vrcEditorRenderHTML(st.Editor)+`</div>`))
	} else {
		b.WriteString(section(st.SecStatusBio, hint("info", st.SignInHint)))
	}

	b.WriteString(section(st.SecEmotes, vrcEmotesRenderHTML(st.Emotes)))

	if st.HasTools {
		b.WriteString(section(st.SecCamPaths, `<div id=vrc-campaths>`+vrcCampathsHTML(st.CamPaths)+`</div>`))
		b.WriteString(section(st.SecPhotos, `<div id=vrc-photos-body>`+vrcPhotosHTML(st.Photos)+`</div>`))
	}
	return b.String()
}

func vrcStatusHTML(st vrcStatusSt) string {
	if !st.Present {
		return ""
	}
	return `<div class="rp-card">` + statusRowDL(st.Variant, st.Label, st.DL, st.Line) + `</div>`
}

func vrcEditorRenderHTML(st vrcEditorSt) string {
	var b strings.Builder

	// Status card.
	b.WriteString(`<div class="rp-card vrc-card"><div class=vrc-h>` + html.EscapeString(st.StatusTitle) + tipOr(st.StatusTipS, st.StatusTip) + `</div>`)
	b.WriteString(`<form data-act=vrc-status>`)
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(st.PresenceLabel) + `</span>` +
		`<select class="field-input select-input" name=status>` + vrcOptionsHTML(st.Presence) + `</select></label>`)
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(st.StatusMsgLabel) + ` ` +
		`<b class="` + st.DescCls + `" id=vrc-desc-count>` + st.DescCount + `</b></span>` +
		`<input class=field-input name=desc maxlength=32 value="` + html.EscapeString(st.DescVal) + `" ` + vrcCountOn("vrc-desc-count", st.MaxDesc) + `></label>`)
	b.WriteString(`<button class="rp-btn rp-btn--go" type=submit>` + html.EscapeString(st.SaveStatus) + `</button></form>`)
	b.WriteString(`<div class=btn-row>` + vrcPresetSelectHTML(st.StatusPreset) +
		btn(st.PresetsLabel, "outline", "vrc-status-presets", "") + `</div></div>`)

	// Bio card.
	b.WriteString(`<div class="rp-card vrc-card"><div class=vrc-h>` + html.EscapeString(st.BioTitle) + `</div>`)
	b.WriteString(`<form data-act=vrc-bio>`)
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(st.BioTitle) + ` ` +
		`<b class="` + st.BioCls + `" id=vrc-bio-count>` + st.BioCount + `</b></span>` +
		`<textarea class=field-input name=bio rows=4 ` + vrcCountOn("vrc-bio-count", st.MaxBio) + `>` + html.EscapeString(st.BioVal) + `</textarea></label>`)
	b.WriteString(`<button class="rp-btn rp-btn--go" type=submit>` + html.EscapeString(st.SaveBio) + `</button></form>`)
	b.WriteString(hint("info", st.BioHint))
	if st.HasPreview {
		b.WriteString(`<div class=vrc-preview-wrap>` + html.EscapeString(st.PreviewLabel) + `<div class=vrc-preview>` + html.EscapeString(st.Preview) + `</div></div>`)
	}
	b.WriteString(`<div class=btn-row>` + vrcPresetSelectHTML(st.BioPreset) +
		btn(st.PresetsLabel, "outline", "vrc-bio-presets", "") + btn(st.VarsLabel, "outline", "vrc-bio-vars", "") +
		btn(st.RefreshLabel, "ghost", "vrc-events-refresh", "") + `</div></div>`)

	return b.String()
}

// vrcCountOn is the inline rune-counter for a length-limited field (display only).
func vrcCountOn(id string, max int) string {
	n := fmt.Sprint(max)
	return `oninput='var c=document.getElementById("` + id + `");if(c){c.textContent=[...this.value].length+" / ` +
		n + `";c.className="vrc-count"+([...this.value].length>` + n + `?" over":"")}'`
}

// vrcPresenceOpts is the presence picker's option list (placeholder + vrchat.Statuses).
func vrcPresenceOpts(cur string) []vrcOptSt {
	opts := make([]vrcOptSt, 0, len(vrchat.Statuses)+1)
	opts = append(opts, vrcOptSt{Val: "", Label: i18n.T("vrchat.presence.placeholder")})
	for _, s := range vrchat.Statuses {
		opts = append(opts, vrcOptSt{Val: s, Label: s, Sel: s == cur})
	}
	return opts
}

// vrcPresenceOptions renders the presence <option> list (Go-only preset modals).
func vrcPresenceOptions(cur string) string { return vrcOptionsHTML(vrcPresenceOpts(cur)) }

func vrcOptionsHTML(opts []vrcOptSt) string {
	var o strings.Builder
	for _, op := range opts {
		sel := ""
		if op.Sel {
			sel = " selected"
		}
		fmt.Fprintf(&o, `<option value=%s%s>%s</option>`, attrQ(op.Val), sel, html.EscapeString(op.Label))
	}
	return o.String()
}

// vrcPathBtn is a button whose data-val is a filesystem path - uses real double-quotes + HTML-escape
// (NOT %q, which would double Windows backslashes and corrupt the path).
func vrcPathBtn(label, variant, act, path string) string {
	return fmt.Sprintf(`<button class="rp-btn rp-btn--%s" data-act=%s data-val="%s">%s</button>`,
		variant, attrQ(act), html.EscapeString(path), html.EscapeString(label))
}

// vrcPresetSelectHTML builds a name-picker <select> that dispatches Act (val = chosen name) on change.
func vrcPresetSelectHTML(s vrcPresetSelSt) string {
	var o strings.Builder
	fmt.Fprintf(&o, `<select class="field-input select-input" data-act=%s><option value="">%s</option>`, attrQ(s.Act), html.EscapeString(s.Placeholder))
	for _, n := range s.Names {
		fmt.Fprintf(&o, `<option value=%s>%s</option>`, attrQ(n), html.EscapeString(n))
	}
	o.WriteString(`</select>`)
	return o.String()
}

func vrcEmotesRenderHTML(st vrcEmotesSt) string {
	var b strings.Builder
	b.WriteString(`<div class="rp-card vrc-card">`)
	b.WriteString(hint("info", st.Hint))
	b.WriteString(`<form data-act=vrc-emote-gen>`)
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(st.SourceLabel) + `</span><input class=field-input name=source placeholder="C:\path\clip.mp4"></label>`)
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(st.NameLabel) + `</span><input class=field-input name=name placeholder="emoji name"></label>`)
	b.WriteString(fpair(`<label class=field><span class=field-label>`+html.EscapeString(st.FramesLabel)+`</span><select class="field-input select-input" name=frames>`+
		vrcFrameOptionsHTML(st.FrameOpts)+`</select></label>`,
		`<label class=field><span class=field-label>`+html.EscapeString(st.FPSLabel)+`</span><input class=field-input name=fps type=number value=20 min=1 max=120></label>`))
	b.WriteString(fpair(`<label class=field><span class=field-label>`+html.EscapeString(st.TrimStart)+`</span><input class=field-input name=trimStart placeholder="optional"></label>`,
		`<label class=field><span class=field-label>`+html.EscapeString(st.TrimEnd)+`</span><input class=field-input name=trimEnd placeholder="optional"></label>`))
	b.WriteString(`<label class=field><span class=field-label>` + html.EscapeString(st.OutDirLabel) + `</span><input class=field-input name=outdir value="` + html.EscapeString(st.OutDir) + `"></label>`)
	b.WriteString(`<label class=row><span class=row-label>` + html.EscapeString(st.PingPong) + `</span>` +
		`<span class=switch><input type=checkbox name=pingpong value=1><span class=switch-track></span></span></label>`)
	b.WriteString(`<label class=row><span class=row-label>` + html.EscapeString(st.Crop) + `</span>` +
		`<span class=switch><input type=checkbox name=crop value=1><span class=switch-track></span></span></label>`)
	b.WriteString(`<div class=btn-row>` +
		`<input class=field-input name=cropx placeholder="x" style="width:70px">` +
		`<input class=field-input name=cropy placeholder="y" style="width:70px">` +
		`<input class=field-input name=cropw placeholder="w" style="width:70px">` +
		`<input class=field-input name=croph placeholder="h" style="width:70px"></div>`)
	b.WriteString(`<button class="rp-btn rp-btn--go" type=submit>` + html.EscapeString(st.Generate) + `</button></form>`)
	b.WriteString(`<div id=vrc-emote-result></div>`)
	b.WriteString(`<div class=btn-row>` +
		vrcPathBtn(st.OpenFolder, "outline", "open-url", st.OutDir) +
		btn(st.OpenUpload, "explore", "open-url", st.UploadURL) + `</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

func vrcFrameOptionsHTML(opts []vrcFrameOptSt) string {
	var o strings.Builder
	for _, t := range opts {
		sel := ""
		if t.Sel {
			sel = " selected"
		}
		fmt.Fprintf(&o, `<option value=%d%s>%d frames (%d×%d, %dpx)</option>`, t.Frames, sel, t.Frames, t.Grid, t.Grid, t.Res)
	}
	return o.String()
}

func vrcCampathsHTML(st vrcCampathsSt) string {
	switch st.State {
	case "unavailable", "empty":
		return emptyState(st.Msg)
	case "loading":
		return hint("info", st.Msg)
	}
	var list strings.Builder
	list.WriteString(`<div class=vrc-plist>`)
	for _, it := range st.Items {
		cls := "vrc-plist-item"
		if it.Active {
			cls += " active"
		}
		fmt.Fprintf(&list, `<button class=%s data-act=%s>%s</button>`, attrQ(cls), attrQ(fmt.Sprintf("vrc-campath:%d", it.Idx)), html.EscapeString(it.Label))
	}
	list.WriteString(`</div>`)

	info := fmt.Sprintf(`<div class=vrc-cp-info><b>%s</b><br>%s</div>`, html.EscapeString(st.Name), html.EscapeString(st.Info))
	buttons := btnRow(
		st.PlayBtn,
		btn(st.Load, "primary", "vrc-campath-load", ""),
		vrcPathBtn(st.Copy, "ghost", "copy", st.CopyPath),
		btn(st.Organize, "outline", "vrc-campath-organize", ""),
	)
	detail := st.SVG + info + buttons + hint("info", st.Hint)
	return masterDetail(list.String(), detail)
}

func vrcPhotosHTML(st vrcPhotosSt) string {
	switch st.State {
	case "unavailable", "empty":
		return emptyState(st.Msg)
	case "loading":
		return hint("info", st.Msg)
	}
	var list strings.Builder
	list.WriteString(`<div class=vrc-glist>`)
	for _, g := range st.Groups {
		cls := "vrc-glist-item"
		if g.Active {
			cls += " active"
		}
		fmt.Fprintf(&list, `<button class=%s data-act=%s><span>%s</span><span class=vrc-gcount>%d</span></button>`,
			attrQ(cls), attrQ("vrc-photos-group:"+g.Label), html.EscapeString(g.Label), g.Count)
	}
	list.WriteString(`</div>`)

	var cells strings.Builder
	for _, ph := range st.Cells {
		// Cached resized-image endpoint: the browser lazy-loads + caches by URL (no base64 in
		// patches). onerror falls back to the placeholder tile if decode fails.
		imgHTML := `<div class="vrc-thumb vrc-thumb-ph"></div>`
		if ph.Src != "" {
			imgHTML = `<img class=vrc-thumb loading=lazy src=` + attrQ(ph.Src) +
				` onerror="this.className='vrc-thumb vrc-thumb-broken'">`
		}
		cells.WriteString(`<button class=vrc-cell data-act="vrc-photo-view:` + html.EscapeString(ph.File) + `" title=` + ph.TitleQ + `>` +
			imgHTML + `<span class=vrc-cap>` + html.EscapeString(ph.Label) + `</span></button>`)
	}
	note := ""
	if st.Note != "" {
		note = `<div class=vrc-note>` + html.EscapeString(st.Note) + `</div>`
	}
	detail := `<div class=vrc-grid-photos>` + cells.String() + `</div>` + note +
		`<div class=btn-row>` + vrcPathBtn(st.OpenFolder, "outline", "open-url", st.PhotosDir) + `</div>`
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
