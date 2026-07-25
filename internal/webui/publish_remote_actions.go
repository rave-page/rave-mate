package webui

// Remote Publish actions: target switch + set select, and Export / Match-history / Delete routed
// through remotectl.Client. Every network call runs in a u.bg goroutine with a ctx timeout and
// patches the cache in on completion (the render path never blocks). LOCAL Publish dispatch is
// untouched; these fire only when a peer is targeted (the handlers in publish_actions.go branch to
// them). The control target (u.remoteTarget) is shared with the Library/Automations tabs.

import (
	"context"
	"fmt"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/session/sinks/recorder"
)

func init() {
	onPrefix("pub-target:", func(u *UI, m actMsg) { u.pubSetTarget(m.arg("pub-target:")) })
}

// pubSetTarget flips the shared control target and re-renders (empty = this computer). Resets both
// the publish and library remote caches so every remote tab agrees on the target.
func (u *UI) pubSetTarget(t string) {
	u.mirrorShutdown() // the Library mirror follows the shared target
	u.mu.Lock()
	u.remoteTarget = t
	u.mu.Unlock()
	ps := u.pubR()
	ps.mu.Lock()
	ps.resetFor(t)
	ps.mu.Unlock()
	u.pubSetSel("") // drop stale selection
	u.patchMain()
}

// pubRemoteSelect selects a set and kicks its tracklist fetch (captures are cached list-wide).
func (u *UI) pubRemoteSelect(id string) {
	u.pubSetSel(id)
	s := u.pubR()
	s.mu.Lock()
	s.selID = id
	s.tl, s.tlTotal, s.tlErr, s.tlLoading = nil, 0, "", false
	s.mu.Unlock()
	u.pubRemoteTracklistFetch(id)
	u.patchMain()
}

func (u *UI) pubRemoteExport(id, fmtKey string) {
	client := u.remoteClient(u.libRemoteTarget())
	if client == nil {
		return
	}
	// Text renders on the peer with THIS machine's saved style (older peers fall back to classic).
	line, noHeader := "", false
	if fmtKey == recorder.FormatText {
		opts := u.pubTxtOpts()
		line, noHeader = opts.Line, opts.Header == ""
	}
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), remotectl.DefaultCallTimeout)
		defer cancel()
		var content string
		var err error
		if line != "" || noHeader {
			content, err = client.RecExportStyled(ctx, id, fmtKey, line, noHeader)
		} else {
			content, err = client.RecExport(ctx, id, fmtKey)
		}
		if err != nil {
			u.toast(i18n.T("publish.remote.exportFail", i18n.A{"err": err.Error()}))
			return
		}
		u.openModal(pubExportModal(fmtKey, content))
	})
}

func (u *UI) pubRemoteMatch(id string) {
	client := u.remoteClient(u.libRemoteTarget())
	if client == nil {
		return
	}
	u.toast(i18n.T("publish.remote.matching"))
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		m, err := client.RecMatchHistory(ctx, id)
		if err != nil {
			u.toast(i18n.T("publish.remote.matchFail", i18n.A{"err": err.Error()}))
			return
		}
		u.toast(i18n.T("publish.remote.matched", i18n.A{"n": fmt.Sprint(m.TrackCount)}))
		u.pubRemoteListFetch() // refresh summaries (matched badge + track count)
	})
}

func (u *UI) pubRemoteDelOpen(id string) {
	name := i18n.T("publish.liveSet")
	s := u.pubR()
	s.mu.Lock()
	for i := range s.sets {
		if s.sets[i].ID == id {
			name = orSetName(s.sets[i].Name)
			break
		}
	}
	s.mu.Unlock()
	u.openModal(dlgChoiceHTML(dlgChoiceSt{
		Title:  i18n.T("publish.delete"),
		HasMsg: true, Msg: i18n.T("publish.remote.deleteConfirm", i18n.A{"name": name}),
		Btns: []uiBtn{
			{Label: i18n.T("publish.delete"), Variant: "destructive", Act: "pub-del-do:" + id},
			{Label: i18n.T("common.cancel"), Variant: "ghost", Act: "modal-close"},
		},
	}))
}

func (u *UI) pubRemoteDel(id string) {
	client := u.remoteClient(u.libRemoteTarget())
	u.closeModal()
	if client == nil {
		return
	}
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), remotectl.DefaultCallTimeout)
		defer cancel()
		if err := client.RecDelete(ctx, id); err != nil {
			u.toast(i18n.T("publish.remote.deleteFail", i18n.A{"err": err.Error()}))
			return
		}
		s := u.pubR()
		s.mu.Lock()
		s.selID = ""
		s.mu.Unlock()
		u.pubSetSel("")
		u.toast(i18n.T("publish.remote.deleted"))
		u.pubRemoteListFetch()
	})
}
