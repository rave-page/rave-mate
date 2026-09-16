package webui

// In-app file/dir/save browser - THE file browser (CAPABILITIES.md), hosted in a modal, replacing
// the native OS dialog for every Browse in the app. Backed by internal/localmedia (the same listing
// that feeds the studio WS + remote control) and internal/shellplaces (OS Quick Access). Pure-Go
// modal (like libRenameModal): patched into __modal, acts reach Go via data-act, no Zig twin.
//
// Apply flow: runPick pins the asker's modal token, then opens THIS modal (a new session). On
// Choose the asker's token is RESTORED before the target act re-runs, so both tok-guarded handlers
// (updateModalIf: arSetFile/aeField) and full-re-open handlers (libMoveDir/re-dest) apply correctly
// - pickApply (the native-dialog return trip + its guard) is untouched, so pick_bind_test stays green.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/localmedia"
)

const pkOwner = "pickbrowse"

const (
	pkMaxRows   = 500 // render cap; a megadir shows "N of M"
	pkRecentMax = 8
	pkSideQA    = 14 // sidebar cap per group
)

// pickFilter is a caller's advisory type narrowing for a file/multi pick.
type pickFilter struct {
	exts  []string // lowercase, no dot; empty = all files
	label string
	multi bool
}

var (
	pkAudio = []string{"mp3", "flac", "wav", "aiff", "aif", "m4a", "ogg", "opus", "aac", "wma"}
	pkVideo = []string{"mp4", "mov", "mkv", "avi", "webm", "m4v", "wmv", "flv"}
	pkImage = []string{"png", "jpg", "jpeg", "gif", "webp", "bmp"}
)

// pickFilters narrows a file pick per its target act (advisory - "All files" always available). A
// missing entry = any file, single-select.
var pickFilters = map[string]pickFilter{
	"ed-img:path":        {exts: pkImage, label: "Images"},
	"edv-src":            {exts: pkVideo, label: "Video"},
	"lib-id-addpath":     {exts: pkAudio, label: "Audio", multi: true},
	"lib-import-path":    {exts: []string{"nml", "xml", "m3u8", "m3u", "csv", "txt"}, label: "Library files"},
	"vrc-emote-source":   {exts: []string{"mp4", "mov", "webm", "gif", "png", "jpg", "jpeg"}, label: "Video / image"},
	"mo-pc-view":         {exts: []string{"ply", "pcd", "rmpc", "las", "laz", "xyz", "e57"}, label: "Point clouds"},
	"mo-avatar-import":   {exts: []string{"vrca", "glb", "gltf", "fbx", "prefab"}, label: "Avatar"},
	"set:gridfix-python": {exts: []string{"exe"}, label: "python.exe"},
	"vr-lay-imp":         {exts: []string{"json"}, label: "Layout JSON"},
}

// pkState is one UI's picker session. Held off the UI struct (like libSts).
type pkState struct {
	mu sync.Mutex

	open   bool
	kind   string   // dir | file | save | multi
	target string   // act to apply the chosen path(s) to
	askTok modalTok // the modal on screen when Browse was clicked (restored on choose)
	pkTok  modalTok // this picker modal's own session
	title  string

	filter    pickFilter
	filterAll bool   // "All files" toggle overrides filter.exts
	saveExt   string // enforced extension for kind=save (no dot)
	saveName  string
	confirmOW bool // save: target exists, primary armed to overwrite

	dir      string
	back     []string
	fwd      []string
	search   string
	sortBy   string // name | modified | size | type
	sortDesc bool
	grid     bool
	hidden   bool
	sel      map[string]bool // multi selection
	selOne   string          // single file selection
	hlIdx    int             // keyboard-highlighted row (index into the visible list); Go owns it
	jumpBuf  string          // type-to-jump prefix
	jumpAt   time.Time       // last type-to-jump keystroke (prefix resets after ~1s)

	// listing cache: read off the act lane (a cold share must not wedge it)
	entries []localmedia.Entry
	listErr string
	gen     int // navigation generation; a stale bg read is dropped
}

var (
	pkMu  sync.Mutex
	pkSts = map[*UI]*pkState{}
)

func (u *UI) pk() *pkState {
	pkMu.Lock()
	defer pkMu.Unlock()
	s := pkSts[u]
	if s == nil {
		s = &pkState{sortBy: "name", sel: map[string]bool{}}
		pkSts[u] = s
	}
	return s
}

func init() {
	onPrefix("pk-nav:", func(u *UI, m actMsg) { u.pkNavigate(m.arg("pk-nav:"), true) })
	onExact("pk-up", func(u *UI, _ actMsg) { u.pkUp() })
	onExact("pk-back", func(u *UI, _ actMsg) { u.pkHistory(true) })
	onExact("pk-fwd", func(u *UI, _ actMsg) { u.pkHistory(false) })
	onExact("pk-goto", func(u *UI, m actMsg) { u.pkGoto(m.Val) })
	onExact("pk-search", func(u *UI, m actMsg) { u.pkSetSearch(m.Val) })
	onPrefix("pk-sort:", func(u *UI, m actMsg) { u.pkSort(m.arg("pk-sort:")) })
	onPrefix("pk-view:", func(u *UI, m actMsg) { u.pkView(m.arg("pk-view:") == "grid") })
	onExact("pk-hidden", func(u *UI, _ actMsg) { u.pkToggleHidden() })
	onPrefix("pk-filter:", func(u *UI, m actMsg) { u.pkFilterAll(m.arg("pk-filter:") == "all") })
	onPrefix("pk-open:", func(u *UI, m actMsg) { u.pkOpenEntry(m.arg("pk-open:")) })
	onPrefix("pk-sel:", func(u *UI, m actMsg) { u.pkToggleSel(m.arg("pk-sel:")) })
	onExact("pk-name", func(u *UI, m actMsg) { u.pkSetName(m.Val) })
	onExact("pk-choose", func(u *UI, _ actMsg) { u.pkChoose() })
	onExact("pk-pin", func(u *UI, _ actMsg) { u.pkPinCurrent() })
	onPrefix("pk-unpin:", func(u *UI, m actMsg) { u.pkUnpin(m.arg("pk-unpin:")) })
	onExact("pk-sys", func(u *UI, _ actMsg) { u.pkSysDialog() })
	onExact("pk-key", func(u *UI, m actMsg) { u.pkKey(m.Val) })
	onExact("pk-jump", func(u *UI, m actMsg) { u.pkJump(m.Val) })
}

// pickOpen opens the in-app browser for kind (dir|file|save|multi). container = the save extension
// (from pick-save:<container>:<target>), empty otherwise. Replaces the native dialog everywhere.
func (u *UI) pickOpen(kind, container, target string) {
	if target == "" {
		return
	}
	ask := u.modalCur()
	f := pickFilters[target]
	if kind == "file" && f.multi {
		kind = "multi"
	}
	s := u.pk()
	s.mu.Lock()
	s.open, s.kind, s.target, s.askTok = true, kind, target, ask
	s.filter, s.filterAll = f, false
	s.saveExt = strings.TrimPrefix(strings.ToLower(container), ".")
	s.saveName, s.confirmOW = "", false
	s.search, s.sortBy, s.sortDesc, s.grid = "", "name", false, false
	s.sel = map[string]bool{}
	s.selOne = ""
	s.back, s.fwd = nil, nil
	s.title = pkTitle(kind, f)
	start := u.pkStartDir(s)
	s.dir = start
	s.mu.Unlock()

	tok := u.openModalAs(pkOwner, u.pkModalHTML())
	s.mu.Lock()
	s.pkTok = tok
	s.mu.Unlock()
	u.pkLoad(start, tok)
}

func pkTitle(kind string, f pickFilter) string {
	switch kind {
	case "dir":
		return i18n.T("picker.title.dir")
	case "save":
		return i18n.T("picker.title.save")
	case "multi":
		return i18n.T("picker.title.multi")
	default:
		return i18n.T("picker.title.file")
	}
}

// pkStartDir picks a sensible initial directory. Caller holds s.mu.
func (u *UI) pkStartDir(s *pkState) string {
	for _, r := range u.pkRecent() {
		if isDir(r) {
			return r
		}
	}
	d := localmedia.Defaults()
	for _, c := range []string{d.Music, d.Home, d.Documents} {
		if c != "" && isDir(c) {
			return c
		}
	}
	if dr := libDrives(); len(dr) > 0 {
		return dr[0]
	}
	return "."
}

func isDir(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// ── navigation ──

// pkNavigate changes the current dir. push records history (Back). Loads off the act lane.
func (u *UI) pkNavigate(dir string, push bool) {
	dir = filepath.Clean(dir)
	s := u.pk()
	s.mu.Lock()
	if !s.open {
		s.mu.Unlock()
		return
	}
	if push && s.dir != dir {
		s.back = append(s.back, s.dir)
		s.fwd = nil
	}
	s.dir, s.search, s.selOne, s.confirmOW = dir, "", "", false
	s.hlIdx = 0
	s.sel = map[string]bool{}
	tok := s.pkTok
	s.mu.Unlock()
	u.pkLoad(dir, tok)
}

func (u *UI) pkUp() {
	s := u.pk()
	s.mu.Lock()
	cur := s.dir
	s.mu.Unlock()
	if parent := filepath.Dir(cur); parent != cur {
		u.pkNavigate(parent, true)
	}
}

func (u *UI) pkHistory(back bool) {
	s := u.pk()
	s.mu.Lock()
	var dst string
	if back && len(s.back) > 0 {
		dst = s.back[len(s.back)-1]
		s.back = s.back[:len(s.back)-1]
		s.fwd = append(s.fwd, s.dir)
	} else if !back && len(s.fwd) > 0 {
		dst = s.fwd[len(s.fwd)-1]
		s.fwd = s.fwd[:len(s.fwd)-1]
		s.back = append(s.back, s.dir)
	} else {
		s.mu.Unlock()
		return
	}
	s.dir, s.search, s.selOne = dst, "", ""
	s.hlIdx = 0
	s.sel = map[string]bool{}
	tok := s.pkTok
	s.mu.Unlock()
	u.pkLoad(dst, tok)
}

// pkGoto handles the editable path field: a dir navigates; an existing file (file/save kind) selects
// it; anything else is ignored (keeps the field's typed value on the next render).
func (u *UI) pkGoto(val string) {
	val = strings.TrimSpace(val)
	if val == "" {
		return
	}
	p := filepath.Clean(val)
	if isDir(p) {
		u.pkNavigate(p, true)
		return
	}
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		s := u.pk()
		s.mu.Lock()
		kind := s.kind
		s.mu.Unlock()
		switch kind {
		case "file":
			u.pkNavigate(filepath.Dir(p), true)
			u.pkOpenEntry(p) // select
		case "save":
			u.pkSetName(filepath.Base(p))
			u.pkNavigate(filepath.Dir(p), true)
		case "multi":
			u.pkNavigate(filepath.Dir(p), true)
			u.pkToggleSel(p)
		}
	}
}

// pkLoad reads dir via localmedia OFF the act lane, then re-renders the whole modal (guarded by tok).
func (u *UI) pkLoad(dir string, tok modalTok) {
	s := u.pk()
	s.mu.Lock()
	s.gen++
	gen, hidden := s.gen, s.hidden
	s.mu.Unlock()
	u.bg(func() {
		lst := localmedia.ListDirectory(dir, hidden)
		s.mu.Lock()
		if s.gen != gen { // superseded by a newer navigation
			s.mu.Unlock()
			return
		}
		s.entries, s.listErr = lst.Entries, lst.Error
		s.mu.Unlock()
		if !u.updateModalIf(tok, func() string { return u.pkModalHTML() }) {
			return // picker no longer on screen
		}
	})
}

// ── controls ──

func (u *UI) pkSetSearch(q string) {
	s := u.pk()
	s.mu.Lock()
	s.search, s.hlIdx = q, 0
	s.mu.Unlock()
	u.pkPatchEntries()
}

func (u *UI) pkSort(key string) {
	s := u.pk()
	s.mu.Lock()
	if s.sortBy == key {
		s.sortDesc = !s.sortDesc
	} else {
		s.sortBy, s.sortDesc = key, false
	}
	s.hlIdx = 0
	s.mu.Unlock()
	u.pkRerender() // the sort chip's active/direction state lives in the toolbar, not #pk-entries
}

func (u *UI) pkView(grid bool) {
	s := u.pk()
	s.mu.Lock()
	s.grid = grid
	s.mu.Unlock()
	u.pkRerender() // the List/Grid toggle's active state is in the toolbar
}

func (u *UI) pkFilterAll(all bool) {
	s := u.pk()
	s.mu.Lock()
	s.filterAll, s.hlIdx = all, 0
	s.mu.Unlock()
	u.pkRerender() // the filter chip's active state is in the toolbar
}

// pkRerender re-renders the whole picker modal (toolbar chip states + entries). For chip clicks
// where losing input focus is irrelevant; pkSetSearch keeps patching only #pk-entries so the
// search box holds focus while typing.
func (u *UI) pkRerender() {
	s := u.pk()
	s.mu.Lock()
	tok := s.pkTok
	s.mu.Unlock()
	u.updateModalIf(tok, func() string { return u.pkModalHTML() })
}

func (u *UI) pkToggleHidden() {
	s := u.pk()
	s.mu.Lock()
	s.hidden, s.hlIdx = !s.hidden, 0
	dir, tok := s.dir, s.pkTok
	s.mu.Unlock()
	u.pkLoad(dir, tok) // hidden changes the listing, not just the view
}

func (u *UI) pkOpenEntry(path string) {
	if isDir(path) {
		u.pkNavigate(path, true)
		return
	}
	s := u.pk()
	s.mu.Lock()
	if s.kind == "multi" {
		s.mu.Unlock()
		u.pkToggleSel(path)
		return
	}
	s.selOne = path
	s.mu.Unlock()
	u.pkPatchEntries()
	u.pkPatchFoot()
}

func (u *UI) pkToggleSel(path string) {
	s := u.pk()
	s.mu.Lock()
	if s.sel[path] {
		delete(s.sel, path)
	} else {
		s.sel[path] = true
	}
	s.mu.Unlock()
	u.pkPatchEntries()
	u.pkPatchFoot()
}

func (u *UI) pkSetName(name string) {
	s := u.pk()
	s.mu.Lock()
	s.saveName, s.confirmOW = name, false
	s.mu.Unlock()
}

// ── keyboard nav (shell.go picker keydown -> pk-key / pk-jump) ──

// pkKey moves the highlighted row or acts on it. val = optional "s" (shift) + one of
// down/up/home/end/pgdn/pgup/enter/updir. Go owns hlIdx; the re-render styles the highlighted row.
func (u *UI) pkKey(val string) {
	cmd := strings.TrimPrefix(val, "s")
	s := u.pk()
	s.mu.Lock()
	if !s.open {
		s.mu.Unlock()
		return
	}
	vis, _ := pkVisible(s)
	n := len(vis)
	if n == 0 {
		s.mu.Unlock()
		return
	}
	if s.hlIdx < 0 {
		s.hlIdx = 0
	}
	if s.hlIdx >= n {
		s.hlIdx = n - 1
	}
	switch cmd {
	case "down":
		if s.hlIdx < n-1 {
			s.hlIdx++
		}
	case "up":
		if s.hlIdx > 0 {
			s.hlIdx--
		}
	case "home":
		s.hlIdx = 0
	case "end":
		s.hlIdx = n - 1
	case "pgdn":
		if s.hlIdx += 10; s.hlIdx > n-1 {
			s.hlIdx = n - 1
		}
	case "pgup":
		if s.hlIdx -= 10; s.hlIdx < 0 {
			s.hlIdx = 0
		}
	case "updir":
		s.mu.Unlock()
		u.pkUp()
		return
	case "enter":
		e, kind := vis[s.hlIdx], s.kind
		s.mu.Unlock()
		u.pkKeyEnter(e, kind)
		return
	default:
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	u.pkPatchEntries()
}

// pkKeyEnter opens a highlighted folder or chooses a highlighted file (per the picker kind).
func (u *UI) pkKeyEnter(e localmedia.Entry, kind string) {
	if e.IsDirectory {
		u.pkNavigate(e.Path, true)
		return
	}
	switch kind {
	case "multi":
		u.pkToggleSel(e.Path)
	case "save":
		u.pkSetName(filepath.Base(e.Path)) // fill the filename; user presses Save to overwrite
		u.pkPatchFoot()
	default: // file
		s := u.pk()
		s.mu.Lock()
		s.selOne = e.Path
		s.mu.Unlock()
		u.pkChoose()
	}
}

// pkJump is type-to-jump: a printable key appends to a prefix (reset after ~1s) and highlights the
// first visible entry whose name starts with it.
func (u *UI) pkJump(ch string) {
	if ch == "" {
		return
	}
	s := u.pk()
	s.mu.Lock()
	if !s.open {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	if now.Sub(s.jumpAt) > time.Second {
		s.jumpBuf = ""
	}
	s.jumpAt = now
	s.jumpBuf += strings.ToLower(ch)
	vis, _ := pkVisible(s)
	for i, e := range vis {
		if strings.HasPrefix(strings.ToLower(e.Name), s.jumpBuf) {
			s.hlIdx = i
			break
		}
	}
	s.mu.Unlock()
	u.pkPatchEntries()
}

// ── pins / recent ──

func (u *UI) pkPinCurrent() {
	s := u.pk()
	s.mu.Lock()
	dir := s.dir
	tok := s.pkTok
	s.mu.Unlock()
	u.libMarks(u.lib()).Toggle(dir, filepath.Base(dir))
	u.updateModalIf(tok, func() string { return u.pkModalHTML() })
}

func (u *UI) pkUnpin(path string) {
	s := u.pk()
	s.mu.Lock()
	tok := s.pkTok
	s.mu.Unlock()
	m := u.libMarks(u.lib())
	if m.Has(path) {
		m.Toggle(path, "")
	}
	u.updateModalIf(tok, func() string { return u.pkModalHTML() })
}

// pkRecent returns this app's recently-visited/picked folders (most-recent first).
func (u *UI) pkRecent() []string {
	f, err := config.DataPath("recent-folders.json")
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	var list []string
	_ = json.Unmarshal(raw, &list)
	if len(list) > pkRecentMax {
		list = list[:pkRecentMax]
	}
	return list
}

// pkRecord moves dir to the front of the recent list (deduped, capped, persisted 0600).
func (u *UI) pkRecord(dir string) {
	dir = filepath.Clean(dir)
	if dir == "" || dir == "." {
		return
	}
	f, err := config.DataPath("recent-folders.json")
	if err != nil {
		return
	}
	list := u.pkRecent()
	out := []string{dir}
	for _, p := range list {
		if !strings.EqualFold(p, dir) {
			out = append(out, p)
		}
		if len(out) >= pkRecentMax {
			break
		}
	}
	if raw, err := json.MarshalIndent(out, "", "  "); err == nil {
		tmp := f + ".tmp"
		if os.WriteFile(tmp, raw, 0o600) == nil {
			_ = os.Rename(tmp, f)
		}
	}
}

// ── apply ──

func (u *UI) pkChoose() {
	s := u.pk()
	s.mu.Lock()
	if !s.open { // one-shot: the session already resolved (or was cancelled)
		s.mu.Unlock()
		return
	}
	kind, target, ask, dir := s.kind, s.target, s.askTok, s.dir
	var paths []string
	switch kind {
	case "dir":
		paths = []string{dir}
	case "file":
		if s.selOne != "" {
			paths = []string{s.selOne}
		}
	case "multi":
		for p := range s.sel {
			paths = append(paths, p)
		}
		sort.Strings(paths)
	case "save":
		name := enforceExt(strings.TrimSpace(s.saveName), s.saveExt)
		if name != "" {
			dst := filepath.Join(dir, name)
			if !s.confirmOW && fileExists(dst) { // arm overwrite, re-render, wait for a second Save
				s.confirmOW = true
				tok := s.pkTok
				s.mu.Unlock()
				u.updateModalIf(tok, func() string { return u.pkModalHTML() })
				return
			}
			paths = []string{dst}
		}
	}
	if len(paths) == 0 {
		s.mu.Unlock()
		u.toast(i18n.T("picker.nothingSelected"))
		return
	}
	s.open = false
	s.mu.Unlock()
	u.pkRecord(dir)
	u.pkApply(ask, target, paths)
}

// pkApply restores the asker's modal identity (so a tok-guarded handler re-renders it) then re-runs
// the target act with each path. For a page target (ask not live) it closes the picker + patches main.
func (u *UI) pkApply(ask modalTok, target string, paths []string) {
	if ask.live() {
		u.modalMu.Lock()
		u.modalTk = ask // evict the picker session, restore the asker's identity
		u.modalMu.Unlock()
		for _, p := range paths {
			u.onActMsg(actMsg{Act: target, Val: p, tok: ask})
		}
		return
	}
	u.closeModal() // page target: clear the picker slot
	for _, p := range paths {
		u.onActMsg(actMsg{Act: target, Val: p})
	}
	u.patchMain()
}

// pkSysDialog is the escape hatch: the native OS dialog (Windows only). It reuses the original
// pickApply return-trip contract verbatim.
func (u *UI) pkSysDialog() {
	if runtime.GOOS != "windows" {
		return
	}
	s := u.pk()
	s.mu.Lock()
	kind, target, ask, saveExt := s.kind, s.target, s.askTok, s.saveExt
	s.open = false
	s.mu.Unlock()
	u.closeModal()
	u.runNativePick(kind, saveExt, target, ask)
}

func enforceExt(name, ext string) string {
	if name == "" || ext == "" {
		return name
	}
	if strings.EqualFold(filepath.Ext(name), "."+ext) {
		return name
	}
	return name + "." + ext
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
