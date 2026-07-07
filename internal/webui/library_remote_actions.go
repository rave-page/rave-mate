package webui

// Remote Library actions: target switch + browse/collection navigation, remote transcode, and
// tag write/revert - all routed through remotectl.Client. Every network call runs in a u.bg
// goroutine with a ctx timeout and patches the cache in on completion (the render path never
// blocks). LOCAL Library dispatch is untouched; these acts only fire when a peer is targeted.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/transcode"
)

func init() {
	onPrefix("lib-target:", func(u *UI, m actMsg) { u.libSetTarget(strings.TrimPrefix(m.Act, "lib-target:")) })
	onExact("lib-r-refresh", func(u *UI, m actMsg) { u.libRemoteRefresh() })
	onPrefix("lib-r-nav:", func(u *UI, m actMsg) { u.libRemoteBrowseFetch(strings.TrimPrefix(m.Act, "lib-r-nav:")) })
	onPrefix("lib-r-file:", func(u *UI, m actMsg) { u.libRemoteSelectFile(strings.TrimPrefix(m.Act, "lib-r-file:")) })
	onPrefix("lib-r-copy:", func(u *UI, m actMsg) { u.libRemoteCopyPath(strings.TrimPrefix(m.Act, "lib-r-copy:")) })
	onPrefix("lib-r-preset:", func(u *UI, m actMsg) { u.libRemoteSetPreset(strings.TrimPrefix(m.Act, "lib-r-preset:")) })
	onPrefix("lib-r-trans:", func(u *UI, m actMsg) { u.libRemoteTranscode(strings.TrimPrefix(m.Act, "lib-r-trans:")) })
	onExact("lib-r-col-search", func(u *UI, m actMsg) { u.libRemoteCollSearch(parseForm(m.Form)["q"]) })
	onExact("lib-r-col-prev", func(u *UI, m actMsg) { u.libRemoteCollPage(-1) })
	onExact("lib-r-col-next", func(u *UI, m actMsg) { u.libRemoteCollPage(1) })
	onPrefix("lib-r-track:", func(u *UI, m actMsg) { u.libRemoteSelectTrack(strings.TrimPrefix(m.Act, "lib-r-track:")) })
	onPrefix("lib-r-tagwrite:", func(u *UI, m actMsg) { u.libRemoteTagWrite(strings.TrimPrefix(m.Act, "lib-r-tagwrite:")) })
	onPrefix("lib-r-tagrevert:", func(u *UI, m actMsg) { u.libRemoteTagRevert(strings.TrimPrefix(m.Act, "lib-r-tagrevert:")) })
}

// libSetTarget flips the shared control target and re-renders the tab (empty = this computer). Also
// resets the publish remote cache + selection so the Publish tab agrees on the target.
func (u *UI) libSetTarget(t string) {
	u.mu.Lock()
	u.remoteTarget = t
	u.mu.Unlock()
	s := u.libR()
	s.mu.Lock()
	s.resetFor(t)
	s.mu.Unlock()
	ps := u.pubR()
	ps.mu.Lock()
	ps.resetFor(t)
	ps.mu.Unlock()
	u.pubSetSel("")
	u.patchMain() // libRemoteEnsure (via libBody) kicks the initial load for the active section
}

func (u *UI) libRemotePatchDetail() {
	u.eval("window.__patch('lib-detail'," + jsQuote(u.libRemoteDetailHTML()) + ")")
}

// libRemoteRefresh re-lists the current browse cwd (or restarts from defaults if none yet).
func (u *UI) libRemoteRefresh() {
	s := u.libR()
	s.mu.Lock()
	dir := s.brDir
	s.mu.Unlock()
	u.libRemoteBrowseFetch(dir)
}

// ── browse ──

// libRemoteBrowseFetch lists dir on the peer off-thread and patches the body. dir "" resolves the
// peer's Music (then Home) folder from its defaults.
func (u *UI) libRemoteBrowseFetch(dir string) {
	client := u.remoteClient(u.libRemoteTarget())
	if client == nil {
		return
	}
	s := u.libR()
	s.mu.Lock()
	s.brLoading, s.brErr = true, ""
	s.mu.Unlock()
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*remotectl.DefaultCallTimeout)
		defer cancel()
		s.mu.Lock()
		haveDef := s.brDefaults != nil
		s.mu.Unlock()
		if dir == "" || !haveDef {
			if def, err := client.GetDefaults(ctx); err == nil {
				s.mu.Lock()
				s.brDefaults = &def
				s.mu.Unlock()
				if dir == "" {
					switch {
					case def.Music != "":
						dir = def.Music
					case def.Home != "":
						dir = def.Home
					}
				}
			}
		}
		listing, err := client.ListDirectory(ctx, dir, false)
		s.mu.Lock()
		s.brLoading, s.brSel = false, nil
		if err != nil {
			s.brErr = err.Error()
		} else {
			s.brErr = ""
			s.brListing = &listing
			s.brDir = listing.Path
		}
		s.mu.Unlock()
		u.libPatchBody()
	})
}

func (u *UI) libRemoteSelectFile(path string) {
	s := u.libR()
	s.mu.Lock()
	if s.brListing != nil {
		for i := range s.brListing.Entries {
			if e := s.brListing.Entries[i]; e.Path == path && !e.IsDirectory {
				s.brSel = &e
				break
			}
		}
	}
	s.mu.Unlock()
	u.libPatchBody() // re-render list (selection highlight) + detail together
}

func (u *UI) libRemoteCopyPath(path string) {
	u.eval("navigator.clipboard&&navigator.clipboard.writeText(" + jsQuote(path) + ")")
	u.toast(i18n.T("library.remote.toast.copied"))
}

func (u *UI) libRemoteSetPreset(label string) {
	s := u.libR()
	s.mu.Lock()
	s.transPreset = label
	s.mu.Unlock()
	u.libRemotePatchDetail()
}

// libRemoteTranscode runs the peer's media.transcode with the selected preset (blocking RPC off-thread).
func (u *UI) libRemoteTranscode(path string) {
	client := u.remoteClient(u.libRemoteTarget())
	if client == nil {
		return
	}
	s := u.libR()
	s.mu.Lock()
	presetLabel := s.transPreset
	s.mu.Unlock()
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	presets := transcode.AllPresets(custom)
	var p transcode.Preset
	found := false
	for _, pp := range presets {
		if pp.Label == presetLabel {
			p, found = pp, true
			break
		}
	}
	if !found && len(presets) > 0 {
		p, found = presets[0], true
	}
	if !found {
		u.toast(i18n.T("library.remote.toast.pickPreset"))
		return
	}
	u.toast(i18n.T("library.remote.toast.transcoding", i18n.A{"name": filepath.Base(path)}))
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		res, err := client.Transcode(ctx, path, p, 0, 0)
		if err != nil {
			u.toast(i18n.T("library.remote.toast.transcodeFail", i18n.A{"err": err.Error()}))
			return
		}
		u.toast(i18n.T("library.remote.toast.transcodeDone", i18n.A{"out": res.Output}))
	})
}

// ── collection ──

// libRemoteCollFetch pages the peer's collection (query+offset) off-thread and patches the body.
func (u *UI) libRemoteCollFetch() {
	client := u.remoteClient(u.libRemoteTarget())
	if client == nil {
		return
	}
	s := u.libR()
	s.mu.Lock()
	s.colLoading = true
	offset, query, needInfo := s.colOffset, s.colQuery, s.colInfo == nil
	s.mu.Unlock()
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*remotectl.DefaultCallTimeout)
		defer cancel()
		if needInfo {
			if info, err := client.LibraryInfo(ctx); err == nil {
				s.mu.Lock()
				s.colInfo, s.colInfoErr = &info, ""
				s.mu.Unlock()
			} else {
				s.mu.Lock()
				s.colInfoErr = err.Error()
				s.mu.Unlock()
			}
		}
		res, err := client.LibraryTracks(ctx, offset, remoteLibPageSize, query)
		s.mu.Lock()
		s.colLoading, s.colSel = false, nil
		if err != nil {
			s.colErr = err.Error()
		} else {
			s.colErr = ""
			s.colTracks, s.colTotal = res.Tracks, res.Total
		}
		s.mu.Unlock()
		u.libPatchBody()
	})
}

func (u *UI) libRemoteCollSearch(q string) {
	s := u.libR()
	s.mu.Lock()
	s.colQuery, s.colOffset = strings.TrimSpace(q), 0
	s.mu.Unlock()
	u.libRemoteCollFetch()
}

func (u *UI) libRemoteCollPage(delta int) {
	s := u.libR()
	s.mu.Lock()
	no := s.colOffset + delta*remoteLibPageSize
	if no < 0 {
		no = 0
	}
	if s.colTotal > 0 && no >= s.colTotal {
		s.mu.Unlock()
		return
	}
	s.colOffset = no
	s.mu.Unlock()
	u.libRemoteCollFetch()
}

func (u *UI) libRemoteSelectTrack(path string) {
	s := u.libR()
	s.mu.Lock()
	for i := range s.colTracks {
		if s.colTracks[i].Path == path {
			t := s.colTracks[i]
			s.colSel = &t
			break
		}
	}
	s.mu.Unlock()
	u.libPatchBody()
}

func (u *UI) libRemoteTagWrite(path string) {
	client := u.remoteClient(u.libRemoteTarget())
	if client == nil {
		return
	}
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), remotectl.DefaultCallTimeout)
		defer cancel()
		res, err := client.WriteTags(ctx, path)
		if err != nil {
			u.toast(i18n.T("library.remote.toast.tagsFail", i18n.A{"err": err.Error()}))
			return
		}
		if res.Written == 0 {
			u.toast(i18n.T("library.remote.toast.tagsNone"))
			return
		}
		u.toast(i18n.T("library.remote.toast.tagsWrote", i18n.A{"n": fmt.Sprint(res.Written)}))
	})
}

func (u *UI) libRemoteTagRevert(path string) {
	client := u.remoteClient(u.libRemoteTarget())
	if client == nil {
		return
	}
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), remotectl.DefaultCallTimeout)
		defer cancel()
		if err := client.RevertTags(ctx, path); err != nil {
			u.toast(i18n.T("library.remote.toast.revertFail", i18n.A{"err": err.Error()}))
			return
		}
		u.toast(i18n.T("library.remote.toast.reverted"))
	})
}
