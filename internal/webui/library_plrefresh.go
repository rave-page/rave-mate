package webui

// Folder-playlist refresh: a folder-bound playlist (imported folder / "mark folder as
// playlist") picks up files added to the folder AFTER creation - DJ software never
// re-scans those. Manual per-playlist button, bulk over all folder-bound playlists,
// and an auto flag (libdb playlists.auto_refresh) applied once per app run when the
// Library first loads.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
)

// libPlaylistFolderDir resolves the playlist's on-disk folder: the stored binding when
// it's a real directory, else the dominant parent dir of the member tracks (Traktor
// imports store tree-relative folder names, not fs paths).
func libPlaylistFolderDir(p libdb.PlaylistRow, paths []string) string {
	if p.Folder != "" && filepath.IsAbs(p.Folder) {
		if st, err := os.Stat(p.Folder); err == nil && st.IsDir() {
			return p.Folder
		}
	}
	counts := map[string]int{}
	best, bestN := "", 0
	for _, x := range paths {
		d := filepath.Dir(x)
		if counts[d]++; counts[d] > bestN {
			best, bestN = d, counts[d]
		}
	}
	return best
}

// libPlaylistRefreshFolder appends folder files missing from the playlist (sorted,
// non-recursive - mirrors libMarkDirPlaylist). Returns how many were added.
func (u *UI) libPlaylistRefreshFolder(id int64) (int, string, error) {
	p, ok, err := u.svc.Lib.PlaylistByID(id)
	if err != nil {
		return 0, "", err
	}
	if !ok || p.Kind == libdb.PlaylistSmart {
		return 0, "", fmt.Errorf("playlist %d not file-backed", id)
	}
	paths, _ := u.svc.Lib.PlaylistTracks(id)
	dir := libPlaylistFolderDir(p, paths)
	if dir == "" {
		return 0, p.Name, fmt.Errorf("%s: %s", p.Name, i18n.T("library.pl.noFolder"))
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, p.Name, err
	}
	have := map[string]bool{}
	for _, x := range paths {
		have[filepath.Clean(x)] = true
	}
	var add []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		fp := filepath.Join(dir, e.Name())
		if pubIsAudio(fp) && !have[filepath.Clean(fp)] {
			add = append(add, fp)
		}
	}
	if len(add) == 0 {
		return 0, p.Name, nil
	}
	sort.Strings(add)
	n, err := u.svc.Lib.AddToPlaylist(id, add...)
	// new files unknown to ANY source also get collection rows (probe + additive upsert
	// under the "folder" source) so they're playable/beatgriddable without a DJ software
	if err == nil && n > 0 {
		if got := u.fiPersistLoose(dir, add); got > 0 {
			u.toast(i18n.Tn("library.fi.refreshImported", got))
		}
	}
	return n, p.Name, err
}

// libRefreshFolderPlaylists sweeps folder-bound playlists (autoOnly limits to the
// auto_refresh-flagged set) and returns tracks added + playlists touched.
func (u *UI) libRefreshFolderPlaylists(autoOnly bool) (added, lists int) {
	if u.svc.Lib == nil {
		return 0, 0
	}
	rows, _ := u.svc.Lib.ListPlaylists()
	for _, p := range rows {
		// bulk = every file-backed playlist with a resolvable folder; auto = flagged only
		if p.Kind == libdb.PlaylistSmart || (autoOnly && !p.AutoRefresh) {
			continue
		}
		if n, _, err := u.libPlaylistRefreshFolder(p.ID); err == nil && n > 0 {
			added += n
			lists++
		}
	}
	return added, lists
}

// libAutoRefreshFolders runs the auto-flagged sweep once per app run (kicked by the
// first Library body render; background - a slow network folder must not block UI).
func (u *UI) libAutoRefreshFolders() {
	added, lists := u.libRefreshFolderPlaylists(true)
	if added > 0 {
		u.toast(i18n.T("library.pl.refreshAllToast", i18n.A{"n": fmt.Sprint(added), "m": fmt.Sprint(lists)}))
		u.libPatchBody()
	}
}

func init() {
	onPrefix("lib-pl-refresh:", func(u *UI, m actMsg) {
		id := int64(atoi(m.arg("lib-pl-refresh:")))
		u.bg(func() { // os.ReadDir + DB write off the actWorker
			n, name, err := u.libPlaylistRefreshFolder(id)
			if u.stopped() {
				return
			}
			switch {
			case err != nil:
				u.toast(err.Error())
			case n == 0:
				u.toast(i18n.T("library.pl.refreshNone"))
			default:
				u.toast(i18n.T("library.pl.refreshedToast", i18n.A{"n": fmt.Sprint(n), "name": name}))
				u.libPatchBody()
			}
		})
	})
	onExact("lib-pl-refresh-all", func(u *UI, _ actMsg) {
		u.bg(func() { // sweeps every folder playlist (os.ReadDir + DB writes) off the actWorker
			added, lists := u.libRefreshFolderPlaylists(false)
			if u.stopped() {
				return
			}
			if added == 0 {
				u.toast(i18n.T("library.pl.refreshNone"))
				return
			}
			u.toast(i18n.T("library.pl.refreshAllToast", i18n.A{"n": fmt.Sprint(added), "m": fmt.Sprint(lists)}))
			u.libPatchBody()
		})
	})
	onPrefix("lib-pl-autorefresh:", func(u *UI, m actMsg) {
		id := int64(atoi(m.arg("lib-pl-autorefresh:")))
		p, ok, err := u.svc.Lib.PlaylistByID(id)
		if err != nil || !ok {
			return
		}
		if err := u.svc.Lib.SetPlaylistAutoRefresh(id, !p.AutoRefresh); err != nil {
			u.toast(err.Error())
			return
		}
		u.libPatchBody()
	})
}
