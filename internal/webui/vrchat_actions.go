package webui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/flipbook"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/vrccampaths"
	"rave.page/mate/internal/vrchat"
	"rave.page/mate/internal/vrcphotos"
)

// VRChat tab: state, action handlers, background account-seed + event-var resolution, preset/vars
// modals, emote generation, camera-path selection, and photo-thumbnail generation. Rendering lives
// in render_vrchat.go. Reuses the already-wired vrc-status / vrc-bio form actions (ui.go).

const vrcEmojiUploadURL = flipbook.EmojiUploadURL
const vrcAllPhotos = "All Photos"

// Package-scoped tab state (the single UI's struct fields live in ui.go, not owned here).
var (
	vrcMu         sync.Mutex
	vrcEd         vrcEditor
	vrcCampathSel int
	vrcPhotoGroup string
)

type vrcEditor struct {
	seeded, seeding   bool
	status, desc, bio string
	eventVars         map[string]string
}

func init() {
	onExact("vrc-status-presets", func(u *UI, _ actMsg) { u.openModal(u.vrcStatusPresetsModal()) })
	onExact("vrc-bio-presets", func(u *UI, _ actMsg) { u.openModal(u.vrcBioPresetsModal()) })
	onExact("vrc-bio-vars", func(u *UI, _ actMsg) { u.openModal(u.vrcBioVarsModal()) })
	onExact("vrc-status-preset", func(u *UI, m actMsg) { u.vrcLoadStatusPreset(m.Val) })
	onExact("vrc-bio-preset", func(u *UI, m actMsg) { u.vrcLoadBioPreset(m.Val) })
	onExact("vrc-status-preset-add", func(u *UI, m actMsg) { u.vrcAddStatusPreset(m.Form) })
	onPrefix("vrc-status-preset-del:", func(u *UI, m actMsg) { u.vrcDelStatusPreset(m.arg("vrc-status-preset-del:")) })
	onExact("vrc-bio-preset-add", func(u *UI, m actMsg) { u.vrcAddBioPreset(m.Form) })
	onPrefix("vrc-bio-preset-del:", func(u *UI, m actMsg) { u.vrcDelBioPreset(m.arg("vrc-bio-preset-del:")) })
	onExact("vrc-bio-vars-save", func(u *UI, m actMsg) { u.vrcSaveBioVars(m.Form) })
	onExact("vrc-events-refresh", func(u *UI, _ actMsg) { u.vrcRefreshEvents() })
	onExact("vrc-emote-gen", func(u *UI, m actMsg) { u.vrcEmoteGen(m.Form) })
	onPrefix("vrc-campath:", func(u *UI, m actMsg) { u.vrcSelectCampath(m.arg("vrc-campath:")) })
	onExact("vrc-campath-load", func(u *UI, _ actMsg) { u.vrcCampathLoad() })
	onExact("vrc-campath-organize", func(u *UI, _ actMsg) { u.vrcCampathOrganize() })
	onPrefix("vrc-photos-group:", func(u *UI, m actMsg) { u.vrcPhotosGroup(m.arg("vrc-photos-group:")) })
	onPrefix("vrc-photo-view:", func(u *UI, m actMsg) { u.vrcPhotoView(m.arg("vrc-photo-view:")) })

	onLiveTick("vrchat", func(u *UI) {
		u.eval("window.__patch('vrc-status-region'," + jsQuote(u.vrcStatusRegion()) + ")")
	})
}

func (u *UI) patchVRCEditor() {
	u.eval("window.__patch('vrc-editor'," + jsQuote(u.vrcEditorHTML()) + ")")
}
func (u *UI) patchCampaths() {
	u.eval("window.__patch('vrc-campaths'," + jsQuote(u.vrcCampathsBody()) + ")")
}
func (u *UI) patchPhotos() {
	u.eval("window.__patch('vrc-photos-body'," + jsQuote(u.photosBody()) + ")")
}

// ── seed + event vars ──

// ensureVRCSeed seeds the editor once from the live account (status/bio) + upcoming events.
func (u *UI) ensureVRCSeed() {
	if u.svc.Vrchat == nil {
		return
	}
	vrcMu.Lock()
	if vrcEd.seeded || vrcEd.seeding {
		vrcMu.Unlock()
		return
	}
	vrcEd.seeding = true
	vrcMu.Unlock()

	u.bg(func() {
		ev := u.vrcEventVars()
		ctx, cancel := u.actx()
		defer cancel()
		usr, err := u.svc.Vrchat.FetchUser(ctx)
		u.logErr("vrchat seed", err)
		vrcMu.Lock()
		vrcEd.eventVars = ev
		if usr != nil {
			if vrchat.ValidStatus(usr.Status) {
				vrcEd.status = usr.Status
			}
			vrcEd.desc = usr.StatusDescription
			vrcEd.bio = usr.Bio
		}
		vrcEd.seeded, vrcEd.seeding = true, false
		vrcMu.Unlock()
		u.patchVRCEditor()
	})
}

// vrcEventVars resolves {next_event}/{next_event_date} from the soonest upcoming rave.page event.
func (u *UI) vrcEventVars() map[string]string {
	out := map[string]string{}
	if u.svc.API == nil {
		return out
	}
	tok := ""
	if u.svc.Auth != nil {
		tok = u.svc.Auth.Token()
	}
	ctx, cancel := u.actx()
	defer cancel()
	events, err := u.svc.API.ListEvents(ctx, tok, "", "", 50)
	if err != nil || len(events) == 0 {
		return out
	}
	now := time.Now()
	soonest := -1
	for i := range events {
		if events[i].Start.Before(now) {
			continue
		}
		if soonest < 0 || events[i].Start.Before(events[soonest].Start) {
			soonest = i
		}
	}
	if soonest < 0 {
		return out
	}
	out["next_event"] = events[soonest].Title
	out["next_event_date"] = events[soonest].Start.Format("Mon Jan 2")
	return out
}

// vrcResolveBio substitutes {placeholders} (manual base overlaid by non-empty event values).
func vrcResolveBio(tmpl string, manual, event map[string]string) string {
	vars := map[string]string{}
	for k, v := range manual {
		vars[k] = v
	}
	for k, v := range event {
		if v != "" {
			vars[k] = v
		}
	}
	return twitch.ResolveTemplate(tmpl, vars)
}

func (u *UI) vrcRefreshEvents() {
	u.toast(i18n.T("vrchat.toast.refreshingEvents"))
	u.bg(func() {
		ev := u.vrcEventVars()
		vrcMu.Lock()
		vrcEd.eventVars = ev
		vrcMu.Unlock()
		u.patchVRCEditor()
	})
}

// ── presets ──

func (u *UI) vrcLoadStatusPreset(name string) {
	if name == "" {
		return
	}
	for _, p := range u.svc.Cfg.Features.VRChat.StatusPresets {
		if p.Name == name {
			vrcMu.Lock()
			vrcEd.status, vrcEd.desc = p.Status, p.Description
			vrcMu.Unlock()
			u.patchVRCEditor()
			return
		}
	}
}

// vrcLoadBioPreset resolves the preset's {placeholders} (current events overlay manual BioVars) into
// the editable bio text - the reused vrc-bio action saves the textarea verbatim, so expansion has to
// happen here, at load, not at save.
func (u *UI) vrcLoadBioPreset(name string) {
	if name == "" {
		return
	}
	f := &u.svc.Cfg.Features.VRChat
	for _, p := range f.BioPresets {
		if p.Name == name {
			vrcMu.Lock()
			vrcEd.bio = vrcResolveBio(p.Template, f.BioVars, vrcEd.eventVars)
			vrcMu.Unlock()
			u.patchVRCEditor()
			return
		}
	}
}

func (u *UI) vrcAddStatusPreset(form string) {
	f := &u.svc.Cfg.Features.VRChat
	m := parseForm(form)
	name, status := strings.TrimSpace(m["name"]), m["status"]
	if name == "" || status == "" {
		u.toast(i18n.T("vrchat.toast.needNamePresence"))
		return
	}
	f.StatusPresets = append(f.StatusPresets, config.VRChatStatusPreset{Name: name, Status: status, Description: m["desc"]})
	u.saveCfg()
	u.openModal(u.vrcStatusPresetsModal())
	u.patchVRCEditor()
}

func (u *UI) vrcDelStatusPreset(name string) {
	f := &u.svc.Cfg.Features.VRChat
	for i := range f.StatusPresets {
		if f.StatusPresets[i].Name == name {
			f.StatusPresets = append(f.StatusPresets[:i], f.StatusPresets[i+1:]...)
			break
		}
	}
	u.saveCfg()
	u.openModal(u.vrcStatusPresetsModal())
	u.patchVRCEditor()
}

func (u *UI) vrcAddBioPreset(form string) {
	f := &u.svc.Cfg.Features.VRChat
	m := parseForm(form)
	name := strings.TrimSpace(m["name"])
	if name == "" {
		u.toast(i18n.T("vrchat.toast.needName"))
		return
	}
	f.BioPresets = append(f.BioPresets, config.VRChatBioPreset{Name: name, Template: m["template"]})
	u.saveCfg()
	u.openModal(u.vrcBioPresetsModal())
	u.patchVRCEditor()
}

func (u *UI) vrcDelBioPreset(name string) {
	f := &u.svc.Cfg.Features.VRChat
	for i := range f.BioPresets {
		if f.BioPresets[i].Name == name {
			f.BioPresets = append(f.BioPresets[:i], f.BioPresets[i+1:]...)
			break
		}
	}
	u.saveCfg()
	u.openModal(u.vrcBioPresetsModal())
	u.patchVRCEditor()
}

func (u *UI) vrcSaveBioVars(form string) {
	f := &u.svc.Cfg.Features.VRChat
	if f.BioVars == nil {
		f.BioVars = map[string]string{}
	}
	m := parseForm(form)
	vrcMu.Lock()
	bio := vrcEd.bio
	vrcMu.Unlock()
	keys := map[string]bool{}
	for _, k := range twitch.TemplateVars(bio) {
		keys[k] = true
	}
	for k := range f.BioVars {
		keys[k] = true
	}
	for k := range keys {
		if v := strings.TrimSpace(m[k]); v == "" {
			delete(f.BioVars, k)
		} else {
			f.BioVars[k] = v
		}
	}
	u.saveCfg()
	u.closeModal()
	u.patchVRCEditor()
}

// ── modals ──

func (u *UI) vrcStatusPresetsModal() string {
	f := &u.svc.Cfg.Features.VRChat
	var body strings.Builder
	if len(f.StatusPresets) == 0 {
		body.WriteString(emptyState(i18n.T("vrchat.empty.noStatusPresets")))
	}
	for _, p := range f.StatusPresets {
		body.WriteString(itemRow(p.Name, p.Status+" · "+p.Description, btn(i18n.T("common.delete"), "destructive", "vrc-status-preset-del:"+p.Name, "")))
	}
	add := `<form data-act=vrc-status-preset-add class="rp-card vrc-card">` +
		`<input class=field-input name=name placeholder="` + i18n.T("vrchat.label.presetNamePlaceholder") + `">` +
		`<select class="field-input select-input" name=status>` + vrcPresenceOptions("") + `</select>` +
		`<input class=field-input name=desc placeholder="` + i18n.T("vrchat.label.statusMessagePlaceholder") + `" maxlength=32>` +
		`<button class="rp-btn rp-btn--go" type=submit>` + i18n.T("vrchat.label.addPreset") + `</button></form>`
	return modal(i18n.T("vrchat.label.statusPresets"), body.String()+add, "")
}

func (u *UI) vrcBioPresetsModal() string {
	f := &u.svc.Cfg.Features.VRChat
	var body strings.Builder
	if len(f.BioPresets) == 0 {
		body.WriteString(emptyState(i18n.T("vrchat.empty.noBioPresets")))
	}
	for _, p := range f.BioPresets {
		body.WriteString(itemRow(p.Name, vrcTrunc(p.Template, 64), btn(i18n.T("common.delete"), "destructive", "vrc-bio-preset-del:"+p.Name, "")))
	}
	add := `<form data-act=vrc-bio-preset-add class="rp-card vrc-card">` +
		`<input class=field-input name=name placeholder="` + i18n.T("vrchat.label.presetNamePlaceholder") + `">` +
		`<textarea class=field-input name=template rows=3 placeholder="` + i18n.T("vrchat.label.bioTemplatePlaceholder") + `"></textarea>` +
		`<button class="rp-btn rp-btn--go" type=submit>` + i18n.T("vrchat.label.addPreset") + `</button></form>`
	return modal(i18n.T("vrchat.label.bioPresets"), body.String()+add, "")
}

func (u *UI) vrcBioVarsModal() string {
	f := &u.svc.Cfg.Features.VRChat
	vrcMu.Lock()
	bio := vrcEd.bio
	vrcMu.Unlock()
	keys := map[string]bool{}
	for _, k := range twitch.TemplateVars(bio) {
		keys[k] = true
	}
	for k := range f.BioVars {
		keys[k] = true
	}
	var ordered []string
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	var body strings.Builder
	body.WriteString(hint("info", i18n.T("vrchat.label.bioVarsHint")))
	if len(ordered) == 0 {
		body.WriteString(emptyState(i18n.T("vrchat.empty.addPlaceholders")))
		return modal(i18n.T("vrchat.label.bioVariables"), body.String(), btn(i18n.T("common.close"), "outline", "modal-close", ""))
	}
	body.WriteString(`<form data-act=vrc-bio-vars-save class="rp-card vrc-card">`)
	for _, k := range ordered {
		cur := ""
		if f.BioVars != nil {
			cur = f.BioVars[k]
		}
		fmt.Fprintf(&body, `<label class=field><span class=field-label>%s</span><input class=field-input name=%s value=%s></label>`,
			htmlEscape(k), attrQ(k), attrQ(cur))
	}
	body.WriteString(`<button class="rp-btn rp-btn--go" type=submit>` + i18n.T("vrchat.label.saveVariables") + `</button></form>`)
	return modal(i18n.T("vrchat.label.bioVariables"), body.String(), "")
}

// ── emote generation ──

func (u *UI) vrcEmoteGen(form string) {
	f := &u.svc.Cfg.Features.VRChat
	m := parseForm(form)
	src := strings.TrimSpace(m["source"])
	if src == "" {
		u.toast(i18n.T("vrchat.toast.pickSource"))
		return
	}
	frames, _ := strconv.Atoi(m["frames"])
	if frames == 0 {
		u.toast(i18n.T("vrchat.toast.pickFrameTier"))
		return
	}
	fps, err := strconv.ParseFloat(strings.TrimSpace(m["fps"]), 64)
	if err != nil || fps <= 0 {
		u.toast(i18n.T("vrchat.toast.fpsPositive"))
		return
	}
	outDir := strings.TrimSpace(m["outdir"])
	if outDir == "" {
		outDir = f.ResolvedFlipbookDir()
	} else if outDir != f.ResolvedFlipbookDir() {
		f.FlipbookDir = outDir
		u.saveCfg()
	}
	o := flipbook.Options{
		Input: src, OutName: strings.TrimSpace(m["name"]), Frames: frames, FPS: fps,
		TrimStart: vrcParseSecs(m["trimStart"]), TrimEnd: vrcParseSecs(m["trimEnd"]),
		PingPong: m["pingpong"] != "", OutDir: outDir,
	}
	if m["crop"] != "" {
		o.Crop = &flipbook.Rect{X: vrcAtoi(m["cropx"]), Y: vrcAtoi(m["cropy"]), W: vrcAtoi(m["cropw"]), H: vrcAtoi(m["croph"])}
	}
	if err := o.Validate(); err != nil {
		u.toast(err.Error())
		return
	}
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		u.toast(i18n.T("vrchat.toast.ffmpegNotFound"))
		return
	}
	u.eval("window.__patch('vrc-emote-result'," + jsQuote(`<div class=vrc-note>Generating…</div>`) + ")")
	u.bg(func() {
		out, genErr := flipbook.Generate(ffmpeg, o)
		if genErr != nil {
			u.eval("window.__patch('vrc-emote-result'," + jsQuote(`<div class="vrc-note over">Failed: `+htmlEscape(genErr.Error())+`</div>`) + ")")
			return
		}
		msg := `<div class=vrc-note>Saved: ` + htmlEscape(out) + ` - upload on the VRChat website (Gallery ▸ Emoji), enable Sprite Sheet Mode. Custom emoji need VRC+.</div>`
		u.eval("window.__patch('vrc-emote-result'," + jsQuote(msg) + ")")
		u.toast(i18n.T("vrchat.toast.spriteGenerated"))
	})
}

// ── background scans (camera paths + photos) ──
//
// vrccampaths.Scan and vrcphotos.ScanAll are filepath.WalkDir sweeps of the VRChat dirs (hundreds–
// thousands of files, a stat/parse/sidecar-read each) - far too heavy for the serial render/act
// thread. Each is scanned ONCE off-thread into a process-global cache (the result depends only on
// the shared VRCTools + filesystem, not on which UI), served to render from that cache
// (stale-while-refresh; empty+loading until the FIRST scan lands, which re-renders). Invalidated by
// a short TTL and explicitly by OrganizeNow. Process-global (not per-*UI) so it needs no teardown.
const vrcScanTTL = 30 * time.Second

var (
	vrcScanMu     sync.Mutex
	vrcScanGen    int64 // bumped by vrcInvalidateScans; a scan that started at an older gen discards its (pre-mutation) result
	vrcPaths      []vrccampaths.Path
	vrcPathsAt    time.Time
	vrcPathsOK    bool // ≥1 scan completed
	vrcPathsBusy  bool // a scan is in flight
	vrcPhotos     []vrcphotos.Photo
	vrcPhotosAt   time.Time
	vrcPhotosOK   bool
	vrcPhotosBusy bool
)

// vrcCachedPaths returns the sorted camera paths from cache, kicking an off-thread rescan when cold
// or past the TTL. loaded=false only while the FIRST scan runs (render shows a loading state);
// afterwards it serves the last result while a refresh runs. Never blocks on the fs.
func (u *UI) vrcCachedPaths() ([]vrccampaths.Path, bool) {
	if u.svc.VRCTools == nil {
		return nil, true
	}
	vrcScanMu.Lock()
	defer vrcScanMu.Unlock()
	fresh := vrcPathsOK && time.Since(vrcPathsAt) < vrcScanTTL
	if !fresh && !vrcPathsBusy {
		vrcPathsBusy = true
		u.bg(u.vrcScanPaths)
	}
	return vrcPaths, vrcPathsOK
}

// vrcScanPaths runs the camera-path Scan off-thread + refreshes the cache, then re-renders the
// campaths pane if it is showing.
func (u *UI) vrcScanPaths() {
	vrcScanMu.Lock()
	gen := vrcScanGen
	vrcScanMu.Unlock()
	paths := vrcSortedPaths(u)
	vrcScanMu.Lock()
	if gen != vrcScanGen { // invalidated mid-scan (fs changed): discard stale pre-mutation data, next read rescans
		vrcPathsBusy = false
		vrcScanMu.Unlock()
		return
	}
	vrcPaths, vrcPathsAt, vrcPathsOK, vrcPathsBusy = paths, time.Now(), true, false
	vrcScanMu.Unlock()
	if !u.stopped() && u.activeTab() == "vrchat" && u.vrcgSub() == "profile" {
		u.patchCampaths()
	}
}

// vrcPathsPeek reads the cached camera paths without triggering a scan (ok=false if never scanned).
func vrcPathsPeek() ([]vrccampaths.Path, bool) {
	vrcScanMu.Lock()
	defer vrcScanMu.Unlock()
	return vrcPaths, vrcPathsOK
}

// vrcCachedPhotos returns the screenshot listing from cache, kicking an off-thread rescan when cold
// or past the TTL (see vrcCachedPaths). Never blocks on the fs.
func (u *UI) vrcCachedPhotos() ([]vrcphotos.Photo, bool) {
	if u.svc.VRCTools == nil {
		return nil, true
	}
	vrcScanMu.Lock()
	defer vrcScanMu.Unlock()
	fresh := vrcPhotosOK && time.Since(vrcPhotosAt) < vrcScanTTL
	if !fresh && !vrcPhotosBusy {
		vrcPhotosBusy = true
		u.bg(u.vrcScanPhotos)
	}
	return vrcPhotos, vrcPhotosOK
}

// vrcScanPhotos runs the screenshot ScanAll off-thread + refreshes the cache, then re-renders the
// photos pane if it is showing.
func (u *UI) vrcScanPhotos() {
	vrcScanMu.Lock()
	gen := vrcScanGen
	vrcScanMu.Unlock()
	photos := u.svc.VRCTools.Photos()
	vrcScanMu.Lock()
	if gen != vrcScanGen { // invalidated mid-scan (fs changed): discard stale pre-mutation data, next read rescans
		vrcPhotosBusy = false
		vrcScanMu.Unlock()
		return
	}
	vrcPhotos, vrcPhotosAt, vrcPhotosOK, vrcPhotosBusy = photos, time.Now(), true, false
	vrcScanMu.Unlock()
	if !u.stopped() && u.activeTab() == "vrchat" && u.vrcgSub() == "profile" {
		u.patchPhotos()
	}
}

// vrcInvalidateScans forces the next render to rescan camera paths + photos (the fs changed) while
// still serving the last results until the refresh lands.
func vrcInvalidateScans() {
	vrcScanMu.Lock()
	vrcScanGen++                                       // any in-flight scan (started pre-mutation) now discards its result
	vrcPathsAt, vrcPhotosAt = time.Time{}, time.Time{} // zero → past TTL → rescan on next read
	vrcScanMu.Unlock()
}

// ── camera paths ──

// vrcSortedPaths returns the camera paths in the display order (world groups A→Z, player-relative
// last, newest first within a group).
func vrcSortedPaths(u *UI) []vrccampaths.Path {
	if u.svc.VRCTools == nil {
		return nil
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
	return paths
}

func (u *UI) vrcSelectCampath(arg string) {
	idx, _ := strconv.Atoi(arg)
	vrcMu.Lock()
	vrcCampathSel = idx
	vrcMu.Unlock()
	u.patchCampaths()
}

func (u *UI) vrcCampathLoad() {
	if u.svc.VRCTools == nil {
		return
	}
	vrcMu.Lock()
	sel := vrcCampathSel
	vrcMu.Unlock()
	u.bg(func() {
		paths, ok := vrcPathsPeek()
		if !ok {
			paths = vrcSortedPaths(u) // cache cold - scan off-thread (already in bg)
		}
		if sel < 0 || sel >= len(paths) {
			return
		}
		if err := u.svc.VRCTools.LoadCamPath(paths[sel].File); err != nil {
			u.toast(i18n.T("vrchat.toast.loadFailed") + err.Error())
			return
		}
		u.toast(i18n.T("vrchat.toast.sentToVrchat"))
	})
}

func (u *UI) vrcCampathOrganize() {
	if u.svc.VRCTools == nil || !u.actStart("vrc-organize") {
		return
	}
	u.pendingAct("vrc-campath-organize")
	u.bg(func() { // OrganizeNow = WalkDir + copy/move - off the actWorker
		defer u.actEnd("vrc-organize")
		photos, paths := u.svc.VRCTools.OrganizeNow()
		vrcInvalidateScans() // fs changed - drop cached path/photo scans
		if u.stopped() {
			return
		}
		u.toast(i18n.T("vrchat.toast.organized", i18n.A{"paths": strconv.Itoa(paths), "photos": strconv.Itoa(photos)}))
		u.patchCampaths()
		u.patchPhotos()
	})
}

// ── photos ──

type vrcPhotoGrp struct {
	label string
	count int
}

// vrcPhotoGroups builds the left-list groups: All Photos first, labels A→Z, Unorganized last.
func vrcPhotoGroups(photos []vrcphotos.Photo) []vrcPhotoGrp {
	counts := map[string]int{}
	for _, p := range photos {
		counts[p.Label]++
	}
	if len(counts) == 0 {
		return nil
	}
	var labels []string
	for l := range counts {
		if l != vrcphotos.Unorganized {
			labels = append(labels, l)
		}
	}
	sort.Strings(labels)
	out := make([]vrcPhotoGrp, 0, len(labels)+2)
	out = append(out, vrcPhotoGrp{vrcAllPhotos, len(photos)})
	for _, l := range labels {
		out = append(out, vrcPhotoGrp{l, counts[l]})
	}
	if n := counts[vrcphotos.Unorganized]; n > 0 {
		out = append(out, vrcPhotoGrp{vrcphotos.Unorganized, n})
	}
	return out
}

func vrcGroupExists(groups []vrcPhotoGrp, label string) bool {
	for _, g := range groups {
		if g.label == label {
			return true
		}
	}
	return false
}

func (u *UI) vrcPhotosGroup(label string) {
	vrcMu.Lock()
	vrcPhotoGroup = label
	vrcMu.Unlock()
	u.patchPhotos()
}

// vrcPhotoView opens a full-resolution in-app preview modal (restores the Fyne behaviour;
// the raw file streams over mpMediaURL - Range-capable, browser-cached - instead of shelling
// to the OS viewer). An "Open in viewer" button still hands off to the OS when wanted.
func (u *UI) vrcPhotoView(file string) {
	name := filepath.Base(file)
	body := `<div class=vrc-modal-img-wrap>`
	if src := u.mpMediaURL(file); src != "" {
		body += `<img class=vrc-modal-img src=` + attrQ(src) + ` alt=` + attrQ(name) + `>`
	} else {
		body += emptyState(i18n.T("vrchat.photos.previewFailed"))
	}
	body += `</div>`
	foot := vrcPathBtn(i18n.T("vrchat.action.openInViewer"), "outline", "open-url", file) +
		btn(i18n.T("common.close"), "outline", "modal-close", "")
	u.openModal(modal(name, body, foot))
}

// ── small helpers ──

func vrcFrameOptions() string {
	var o strings.Builder
	for i, t := range flipbook.Tiers() {
		sel := ""
		if i == 1 { // 16-frame default
			sel = " selected"
		}
		fmt.Fprintf(&o, `<option value=%d%s>%d frames (%d×%d, %dpx)</option>`, t.Frames, sel, t.Frames, t.Grid, t.Grid, t.FrameRes)
	}
	return o.String()
}

func vrcTrunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func vrcParseSecs(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func vrcAtoi(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}
