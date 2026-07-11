package webui

// Sorted-copy set builder: "Create sorted copy" on a playlist generates a NEW manual
// playlist (original untouched) with tracks grouped by a chosen criterion - works-together
// clusters / energy (BPM bands) / key (harmonic) / last played / date added / release
// date - with optional generated divider tracks (2s quiet noise via the transcode worker)
// between groups so boundaries are visible in any DJ software.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

var (
	plSortBys = []string{"compat", "energy", "key", "lastplayed", "added", "released"}
	// divider style token → generated track title (user-chosen name style)
	plDividerTitles = map[string]string{
		"dots":   "............",
		"dashes": "---------------",
		"lines":  "________________",
	}
	plDividerStyles = []string{"none", "dots", "dashes", "lines"}
)

func init() {
	onPrefix("lib-plsort:", func(u *UI, m actMsg) {
		u.libSetQuiet(func(s *libSt) {
			s.plsortID = atoi64(m.arg("lib-plsort:"))
			if s.plsortBy == "" {
				s.plsortBy = "key"
			}
			if s.plsortDiv == "" {
				s.plsortDiv = "none"
			}
		})
		u.libPlSortModal()
	})
	onExact("lib-plsort-by", func(u *UI, m actMsg) {
		u.libSetQuiet(func(s *libSt) { s.plsortBy = m.Val })
		u.libPlSortModal()
	})
	onExact("lib-plsort-div", func(u *UI, m actMsg) {
		u.libSetQuiet(func(s *libSt) { s.plsortDiv = m.Val })
		u.libPlSortModal()
	})
	onExact("lib-plsort-go", func(u *UI, m actMsg) { u.libPlSortGo() })
}

// libPlSortModal renders the sorted-copy options (group-by + divider style).
func (u *UI) libPlSortModal() {
	s := u.lib()
	s.mu.Lock()
	by, div := s.plsortBy, s.plsortDiv
	s.mu.Unlock()
	byOpts := make([][2]string, 0, len(plSortBys))
	for _, b := range plSortBys {
		byOpts = append(byOpts, [2]string{b, i18n.T("library.plsort.by." + b)})
	}
	divOpts := make([][2]string, 0, len(plDividerStyles))
	for _, d := range plDividerStyles {
		label := i18n.T("library.plsort.div.none")
		if d != "none" {
			label = plDividerTitles[d]
		}
		divOpts = append(divOpts, [2]string{d, label})
	}
	body := `<p class=page-sub>` + esc(i18n.T("library.plsort.desc")) + `</p>` +
		pbSelect(i18n.T("library.plsort.groupBy"), "lib-plsort-by", byOpts, by) +
		pbSelect(i18n.T("library.plsort.dividers"), "lib-plsort-div", divOpts, div) +
		`<p class=page-sub>` + esc(i18n.T("library.plsort.divHint")) + `</p>` +
		btnRow(btn(i18n.T("library.plsort.create"), "primary", "lib-plsort-go", ""),
			btn(i18n.T("common.cancel"), "outline", "modal-close", ""))
	u.openModal(modal(i18n.T("library.plsort.title"), body, ""))
}

// libPlSortGo builds the sorted copy: resolve items → group → dividers → new playlist.
func (u *UI) libPlSortGo() {
	u.closeModal()
	if u.svc.Lib == nil {
		u.toast(i18n.T("library.dbUnavailable"))
		return
	}
	s := u.lib()
	s.mu.Lock()
	id, by, div := s.plsortID, s.plsortBy, s.plsortDiv
	s.mu.Unlock()
	row, ok, _ := u.svc.Lib.PlaylistByID(id)
	if !ok {
		return
	}
	u.libTracksAsync(s, fmt.Sprintf("plsort:%d", id), func(all []musiclib.Track) {
		items := u.libPlaylistItems(row, all)
		u.bg(func() {
			s.mu.Lock()
			tracks := make([]musiclib.Track, 0, len(items))
			for _, it := range items {
				if it.Path == "" {
					continue
				}
				if t, tok := s.byPath[it.Path]; tok {
					tracks = append(tracks, t)
				} else {
					tracks = append(tracks, musiclib.Track{Path: it.Path})
				}
			}
			s.mu.Unlock()
			if len(tracks) == 0 {
				u.toast(i18n.T("library.pl.emptyTracks"))
				return
			}
			var adj map[string][]libdb.CompatRow
			if by == "compat" {
				paths := make([]string, len(tracks))
				for i, t := range tracks {
					paths[i] = t.Path
				}
				adj, _ = u.svc.Lib.CompatForMany(paths)
			}
			groups := plGroupTracks(tracks, by, adj)
			var divs []string
			divFailed := false
			if div != "none" && len(groups) > 1 {
				var dok bool
				divs, dok = u.libEnsureDividers(div, len(groups)-1)
				divFailed = !dok
			}
			paths := plInterleave(groups, divs)
			name := i18n.T("library.plsort.sortedName", i18n.A{"name": row.Name, "by": i18n.T("library.plsort.by." + by)})
			newID, err := u.svc.Lib.CreatePlaylist(name, libdb.PlaylistManual, "")
			if err != nil {
				u.toast(i18n.T("library.toast.createFailed") + err.Error())
				return
			}
			if err := u.svc.Lib.ReplacePlaylistTracks(newID, paths); err != nil {
				u.toast(i18n.T("library.toast.createFailed") + err.Error())
				return
			}
			if divFailed {
				u.toast(i18n.T("library.plsort.noFfmpeg"))
			}
			u.toast(i18n.T("library.plsort.createdToast", i18n.A{
				"name": name, "tracks": fmt.Sprint(len(paths)), "groups": fmt.Sprint(len(groups))}))
			if len(divs) > 0 {
				u.libReload() // hydrate the new divider rows (titles in playlist views)
			} else {
				u.patchMain()
			}
		})
	})
}

// libEnsureDividers returns n divider file paths for style, generating the first via the
// transcode worker (ffmpeg lavfi) and copying the rest (playlist_tracks dedups by path,
// so each boundary needs its own file). Reused across runs; library rows upserted under
// a synthetic "rave-mate" source. ok=false → degrade (no dividers).
func (u *UI) libEnsureDividers(style string, n int) ([]string, bool) {
	title := plDividerTitles[style]
	if title == "" || n <= 0 {
		return nil, false
	}
	dir, err := config.DataPath("dividers")
	if err != nil || os.MkdirAll(dir, 0o755) != nil {
		return nil, false
	}
	srcID, err := u.svc.Lib.EnsureSource("rave-mate", dir)
	if err != nil {
		u.logErr("divider source", err)
		srcID = 0
	}
	var out []string
	first := ""
	for i := 1; i <= n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("divider-%s-%d.mp3", style, i))
		if !pathOnDisk(p) {
			if first == "" {
				if u.svc.Workers == nil {
					return nil, false
				}
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				_, gerr := u.svc.Workers.Run(ctx, "transcode", "transcode.gendivider", map[string]any{"output": p, "seconds": 2})
				cancel()
				if gerr != nil || !pathOnDisk(p) {
					u.logErr("divider gen", gerr)
					return nil, false
				}
			} else if cerr := plCopyFile(first, p); cerr != nil {
				u.logErr("divider copy", cerr)
				return nil, false
			}
		}
		if first == "" {
			first = p
		}
		out = append(out, p)
		if srcID != 0 {
			_ = u.svc.Lib.UpsertDividerTrack(srcID, musiclib.Track{Path: p, Title: title, DurationSec: 2})
		}
	}
	return out, true
}

func plCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// ── pure grouping logic (unit-tested) ──

// plGroup is one ordered output group (label for tests/ordering; dividers are unlabeled).
type plGroup struct {
	label string
	paths []string
}

// plGroupTracks groups + orders playlist tracks by the criterion. Input order = playlist
// order (preserved within groups unless the criterion sorts). adj only used for "compat".
func plGroupTracks(tracks []musiclib.Track, by string, adj map[string][]libdb.CompatRow) []plGroup {
	switch by {
	case "compat":
		return plGroupCompat(tracks, adj)
	case "energy":
		return plGroupEnergy(tracks)
	case "key":
		return plGroupKey(tracks)
	case "lastplayed":
		return plGroupDate(tracks, func(t musiclib.Track) string { return t.LastPlayed })
	case "added":
		return plGroupDate(tracks, func(t musiclib.Track) string { return t.ImportDate })
	case "released":
		return plGroupDate(tracks, func(t musiclib.Track) string { return t.ReleaseDate })
	default:
		paths := make([]string, len(tracks))
		for i, t := range tracks {
			paths[i] = t.Path
		}
		return []plGroup{{label: "", paths: paths}}
	}
}

// plGroupCompat: union-find clusters over works-together edges among members (size ≥2),
// ordered by first playlist occurrence; singletons collected into one trailing group.
func plGroupCompat(tracks []musiclib.Track, adj map[string][]libdb.CompatRow) []plGroup {
	member := make(map[string]bool, len(tracks))
	for _, t := range tracks {
		member[t.Path] = true
	}
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" || parent[x] == x {
			parent[x] = x
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	union := func(a, b string) { parent[find(a)] = find(b) }
	for p, rows := range adj {
		if !member[p] {
			continue
		}
		for _, r := range rows {
			if member[r.Path] {
				union(p, r.Path)
			}
		}
	}
	clusterOf := map[string]int{} // root → group index
	var groups []plGroup
	var single []string
	sizes := map[string]int{}
	for _, t := range tracks {
		sizes[find(t.Path)]++
	}
	for _, t := range tracks {
		root := find(t.Path)
		if sizes[root] < 2 {
			single = append(single, t.Path)
			continue
		}
		gi, ok := clusterOf[root]
		if !ok {
			gi = len(groups)
			clusterOf[root] = gi
			groups = append(groups, plGroup{label: fmt.Sprintf("cluster %d", gi+1)})
		}
		groups[gi].paths = append(groups[gi].paths, t.Path)
	}
	if len(single) > 0 {
		groups = append(groups, plGroup{label: "", paths: single})
	}
	return groups
}

// plGroupEnergy: BPM bands from the smart-editor feel presets (thresholds = next band's
// BPMMin so gaps/overlaps resolve deterministically), within-band BPM ascending; BPM 0 last.
func plGroupEnergy(tracks []musiclib.Track) []plGroup {
	feels := musiclib.FeelPresets()
	bandOf := func(bpm float64) int {
		for i := 0; i < len(feels)-1; i++ {
			if bpm < feels[i+1].BPMMin {
				return i
			}
		}
		return len(feels) - 1
	}
	groups := make([]plGroup, len(feels)+1)
	for i, f := range feels {
		groups[i].label = f.Label
	}
	groups[len(feels)].label = ""
	type tb struct {
		path string
		bpm  float64
	}
	perBand := make([][]tb, len(feels)+1)
	for _, t := range tracks {
		b := len(feels) // unknown
		if t.BPM > 0 {
			b = bandOf(t.BPM)
		}
		perBand[b] = append(perBand[b], tb{t.Path, t.BPM})
	}
	for b := range perBand {
		sort.SliceStable(perBand[b], func(i, j int) bool { return perBand[b][i].bpm < perBand[b][j].bpm })
		for _, x := range perBand[b] {
			groups[b].paths = append(groups[b].paths, x.path)
		}
	}
	return plDropEmpty(groups)
}

// plGroupKey: Camelot buckets in harmonic wheel order (1A,1B,…,12B); unparseable keys last.
func plGroupKey(tracks []musiclib.Track) []plGroup {
	idx := func(k musiclib.Key) int {
		i := (k.Num - 1) * 2
		if !k.Minor { // A (minor) before B (major) per slot
			i++
		}
		return i
	}
	groups := make([]plGroup, 25)
	for n := 1; n <= 12; n++ {
		groups[(n-1)*2].label = musiclib.Key{Num: n, Minor: true}.Camelot()
		groups[(n-1)*2+1].label = musiclib.Key{Num: n, Minor: false}.Camelot()
	}
	groups[24].label = ""
	for _, t := range tracks {
		gi := 24
		if k, ok := musiclib.ParseKey(t.Key); ok {
			gi = idx(k)
		}
		groups[gi].paths = append(groups[gi].paths, t.Path)
	}
	return plDropEmpty(groups)
}

// plGroupDate: year-month buckets, newest first; within a bucket date desc; unparseable last.
func plGroupDate(tracks []musiclib.Track, get func(musiclib.Track) string) []plGroup {
	type td struct {
		path string
		at   time.Time
	}
	buckets := map[string][]td{}
	var unknown []string
	for _, t := range tracks {
		at, ok := parseDateLoose(get(t))
		if !ok {
			unknown = append(unknown, t.Path)
			continue
		}
		key := at.Format("2006-01")
		buckets[key] = append(buckets[key], td{t.Path, at})
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	var groups []plGroup
	for _, k := range keys {
		xs := buckets[k]
		sort.SliceStable(xs, func(i, j int) bool { return xs[i].at.After(xs[j].at) })
		g := plGroup{label: k}
		for _, x := range xs {
			g.paths = append(g.paths, x.path)
		}
		groups = append(groups, g)
	}
	if len(unknown) > 0 {
		groups = append(groups, plGroup{label: "", paths: unknown})
	}
	return groups
}

func plDropEmpty(groups []plGroup) []plGroup {
	out := groups[:0]
	for _, g := range groups {
		if len(g.paths) > 0 {
			out = append(out, g)
		}
	}
	return append([]plGroup(nil), out...)
}

// parseDateLoose parses the date formats DJ libraries carry (Traktor "2024/3/17",
// ISO dates, RFC3339 last-played stamps).
func parseDateLoose(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, f := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02", "2006/1/2", "2006/01/02", "2006"} {
		if t, err := time.Parse(f, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// plInterleave flattens groups with dividers[i] between group i and i+1 (never leading or
// trailing; missing dividers skip silently - the degrade path).
func plInterleave(groups []plGroup, dividers []string) []string {
	var out []string
	for i, g := range groups {
		if i > 0 && i-1 < len(dividers) && dividers[i-1] != "" {
			out = append(out, dividers[i-1])
		}
		out = append(out, g.paths...)
	}
	return out
}
