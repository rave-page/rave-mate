package webui

// Preparation playlist: pick (or create) one manual playlist as the active prep target
// (picker in the collection toolbar + the cue-editor rail, shared selection persisted
// with the cue prefs). The P key then adds the current track - the open cue-editor
// track, else the selected collection row. If the track is already in, a toast says so
// and HOLDING P for ~1s removes it again (release earlier = keep).

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/i18n"
)

const prepHold = time.Second

// prepSt is the hold-P gesture state (timer armed between keydown and keyup/fire).
type prepSt struct {
	mu    sync.Mutex
	timer *time.Timer
	path  string // armed-for-removal path ("" = nothing pending)
}

// prepPlaylist resolves the configured prep playlist (0/false = none or deleted).
func (u *UI) prepPlaylist() (int64, string, bool) {
	id := u.cePrefs().PrepPlaylist
	if id == 0 || u.svc.Lib == nil {
		return 0, "", false
	}
	row, ok, err := u.svc.Lib.PlaylistByID(id)
	if err != nil || !ok {
		return 0, "", false
	}
	return id, row.Name, true
}

// prepTarget is the track P acts on: the open (local) cue-editor track, else the
// selected collection row.
func (u *UI) prepTarget() string {
	c := u.ce()
	c.mu.Lock()
	if c.active && c.rce == nil {
		p := c.path
		c.mu.Unlock()
		return p
	}
	c.mu.Unlock()
	s := u.lib()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sel != nil && s.sel.inColl {
		return s.sel.path
	}
	return ""
}

// prepKey handles P down/up. Down: add when absent; when already in, toast the hint +
// arm the hold-to-remove timer. Up before the threshold cancels it.
func (u *UI) prepKey(down bool) {
	pr := &u.prep
	if !down {
		pr.mu.Lock()
		if pr.timer != nil {
			pr.timer.Stop()
		}
		pr.timer, pr.path = nil, ""
		pr.mu.Unlock()
		return
	}
	id, name, ok := u.prepPlaylist()
	if !ok {
		u.toast(i18n.T("library.prep.pickFirst"))
		return
	}
	path := u.prepTarget()
	if path == "" {
		u.toast(i18n.T("library.prep.noTrack"))
		return
	}
	in := false
	if pls, err := u.svc.Lib.PlaylistsForTrack(path); err == nil {
		for _, p := range pls {
			if p.ID == id {
				in = true
				break
			}
		}
	}
	if !in {
		if _, err := u.svc.Lib.AddToPlaylist(id, path); err != nil {
			u.toast(i18n.T("library.prep.failed") + err.Error())
			return
		}
		u.toast(i18n.T("library.prep.added", i18n.A{"name": name}))
		u.libPatchBody() // playlist counts + facet views follow
		return
	}
	u.toast(i18n.T("library.prep.already", i18n.A{"name": name}))
	pr.mu.Lock()
	if pr.timer != nil {
		pr.timer.Stop()
	}
	p := path
	pr.path = p
	pr.timer = time.AfterFunc(prepHold, func() {
		pr.mu.Lock()
		armed := pr.path == p
		pr.timer, pr.path = nil, ""
		pr.mu.Unlock()
		if !armed {
			return
		}
		if err := u.svc.Lib.RemoveFromPlaylist(id, p); err != nil {
			u.toast(i18n.T("library.prep.failed") + err.Error())
			return
		}
		u.toast(i18n.T("library.prep.removed", i18n.A{"name": name}))
		u.libPatchBody()
	})
	pr.mu.Unlock()
}

// prepSelectState registers + resolves the prep-playlist picker. id must be unique per
// surface (smartselect state is keyed on it): "prep-coll" toolbar, "prep-rail" cue rail.
func (u *UI) prepSelectState(id string) selState {
	cur := ""
	if pid := u.cePrefs().PrepPlaylist; pid != 0 {
		cur = fmt.Sprint(pid)
	}
	s := resolveSmartSelect(id, "prep-pick:", cur, func() []ssOpt {
		opts := []ssOpt{
			{Val: "", Label: i18n.T("library.prep.none")},
			{Val: "__new", Label: i18n.T("library.prep.new")},
		}
		if u.svc.Lib == nil {
			return opts
		}
		rows, _ := u.svc.Lib.ListPlaylists()
		for _, p := range rows {
			if p.Kind != "manual" { // imported = replaced on DJ re-sync, smart = rule-driven
				continue
			}
			opts = append(opts, ssOpt{Val: fmt.Sprint(p.ID), Label: p.Name,
				Badge: i18n.Tn("library.prep.tracks", p.TrackCount)})
		}
		return opts
	})
	s.Label = i18n.T("library.prep.label")
	return s
}

// prepSelectHTML renders the picker for surfaces that embed markup (the cue-editor rail);
// the collection toolbar carries the resolved state through libCollSt instead.
func (u *UI) prepSelectHTML(id string) string { return selHTML(u.prepSelectState(id)) }

// prepNewModal asks for the new prep playlist's name.
func (u *UI) prepNewModal() {
	body := fmt.Sprintf(`<form data-act=prep-new-create class=mform>%s<button class="rp-btn rp-btn--primary" type=submit>%s</button></form>`,
		labeledInput("name", i18n.T("library.prep.newName"), ""), esc(i18n.T("library.prep.createBtn")))
	u.openModal(modal(i18n.T("library.prep.newTitle"), body, ""))
}

// prepCreate creates a manual playlist and selects it as the prep target.
func (u *UI) prepCreate(name string) {
	u.closeModal()
	if u.svc.Lib == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		u.toast(i18n.T("library.toast.enterName"))
		return
	}
	id, err := u.svc.Lib.CreatePlaylist(name, "manual", "")
	if err != nil {
		u.toast(i18n.T("library.toast.createFailed") + err.Error())
		return
	}
	u.cePrefsMut(func(p *cePrefsSt) { p.PrepPlaylist = id })
	u.toast(i18n.T("library.prep.created", i18n.A{"name": name}))
	u.patchMain()
}

func init() {
	onPrefix("prep-pick:", func(u *UI, m actMsg) {
		v := m.arg("prep-pick:")
		if v == "__new" {
			u.prepNewModal()
			return
		}
		id := atoi64(v) // "" -> 0 = none
		u.cePrefsMut(func(p *cePrefsSt) { p.PrepPlaylist = id })
		u.patchMain() // both pickers re-render with the shared selection
	})
	onExact("prep-new-create", func(u *UI, m actMsg) { u.prepCreate(parseForm(m.Form)["name"]) })
}
