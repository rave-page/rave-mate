package webui

// Lighting-cue (DMX) recorder controls. Record/stop/select/play/loop/publish drive the DMX
// Router's lightcue engine (u.svc.DMX) + the WorldSync publisher (u.svc.WorldSync). The DMX
// store is not VR-goroutine-bound, so record start/stop lives here directly; playback runs on
// the Router's own bounded ticker (no UI goroutine). State is per-UI (selected take only).

import (
	"sync"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/vrcperm"
)

type lcSt struct {
	mu   sync.Mutex
	take string // selected take name
}

var (
	lcMu  sync.Mutex
	lcMap = map[*UI]*lcSt{}
)

func (u *UI) lc() *lcSt {
	lcMu.Lock()
	defer lcMu.Unlock()
	s := lcMap[u]
	if s == nil {
		s = &lcSt{}
		lcMap[u] = s
	}
	return s
}

func (s *lcSt) sel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.take
}

func (s *lcSt) setSel(name string) {
	s.mu.Lock()
	s.take = name
	s.mu.Unlock()
}

func init() {
	onExact("lc-record", func(u *UI, m actMsg) { u.lcRecord() })
	onExact("lc-stop", func(u *UI, m actMsg) { u.lcStopRecord() })
	onExact("lc-refresh", func(u *UI, m actMsg) { u.patchMain() })
	onExact("lc-select", func(u *UI, m actMsg) { u.lc().setSel(m.Val); u.patchMain() })
	onExact("lc-play", func(u *UI, m actMsg) { u.lcPlay() })
	onExact("lc-stopplay", func(u *UI, m actMsg) {
		if u.svc.DMX != nil {
			u.svc.DMX.StopPlay()
		}
		u.patchMain()
	})
	onExact("lc-loop", func(u *UI, m actMsg) {
		if u.svc.DMX != nil {
			u.svc.DMX.ToggleLoop()
		}
		u.patchMain()
	})
	onExact("lc-publish", func(u *UI, m actMsg) { u.lcPublish() })
}

func (u *UI) lcRecord() {
	if u.svc.DMX == nil {
		return
	}
	if u.svc.Cfg == nil || !u.svc.Cfg.Features.LightCue.Enabled {
		u.toast(i18n.T("lightcue.toast.enableFirst"))
		return
	}
	if !u.svc.DMX.StartRecord() {
		u.toast(i18n.T("lightcue.toast.enableDmxFirst"))
		return
	}
	u.toast(i18n.T("lightcue.toast.recording"))
	u.patchMain()
}

func (u *UI) lcStopRecord() {
	if u.svc.DMX == nil {
		return
	}
	name := u.svc.DMX.StopRecord()
	if name == "" {
		u.toast(i18n.T("lightcue.toast.nothingCaptured"))
		u.patchMain()
		return
	}
	u.lc().setSel(name)
	u.toast(i18n.T("lightcue.toast.saved") + name)
	u.patchMain()
}

func (u *UI) lcPlay() {
	if u.svc.DMX == nil {
		return
	}
	name := u.lc().sel()
	if name == "" {
		u.toast(i18n.T("lightcue.toast.selectTake"))
		return
	}
	if !u.svc.DMX.Play(name) {
		u.toast(i18n.T("lightcue.toast.loadFailed"))
		return
	}
	u.patchMain()
}

func (u *UI) lcPublish() {
	s := u.svc.WorldSync
	if s == nil {
		u.toast(i18n.T("lightcue.toast.worldSyncOff"))
		return
	}
	if u.svc.DMX == nil || u.svc.Cfg == nil {
		return
	}
	name := u.lc().sel()
	if name == "" {
		u.toast(i18n.T("lightcue.toast.selectTake"))
		return
	}
	u.toast(i18n.T("actions.toast.publishingGithub"))
	u.bg(func() {
		take, err := u.svc.DMX.LoadTake(name)
		if err != nil {
			u.toast(i18n.T("lightcue.toast.loadFailed"))
			return
		}
		ctx, cancel := u.actx()
		defer cancel()
		s.PublishLightCues(ctx, take)
		if url := s.RawURLFor(u.svc.Cfg.Features.WorldSync.LightCuesGistID, vrcperm.FileLightCues); url != "" {
			u.toast(i18n.T("lightcue.toast.published") + url)
		} else {
			st := s.Status("lightcues")
			if st.Err != "" {
				u.toast(i18n.T("lightcue.toast.publishFailed") + st.Err)
			} else {
				u.toast(i18n.T("lightcue.toast.published"))
			}
		}
		u.patchMain()
	})
}
