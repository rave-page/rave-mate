package webui

// Track-compatibility marks ("works well together"): mark all C(N,2) pairs over the
// current multi-selection (Collection collSel / Browse batch) with a kind (blend /
// double drop / energy), discover partners (direct + depth-2 friends-of-friends) from
// a track's detail rail, remove marks. Storage: libdb track_compat (path-keyed pairs).

import (
	"fmt"
	"html"
	"path/filepath"
	"sort"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

const (
	compatMaxSel  = 100 // pair marking is quadratic; 100 tracks = 4950 pairs
	compatMaxHits = 200 // discovery result cap (direct + depth-2)
	compatRailMax = 8   // partners shown inline in the detail rail
)

func init() {
	onPrefix("lib-compat-mark:", func(u *UI, m actMsg) { u.libCompatMarkModal(m.arg("lib-compat-mark:")) })
	onPrefix("lib-compat-do:", func(u *UI, m actMsg) { u.libCompatDo(m.arg("lib-compat-do:")) })
	onPrefix("lib-compat-find:", func(u *UI, m actMsg) { u.libCompatFindModal(m.arg("lib-compat-find:")) })
	onPrefix("lib-compat-go:", func(u *UI, m actMsg) {
		u.closeModal()
		u.libSelect(m.arg("lib-compat-go:"), nil)
	})
	onPrefix("lib-compat-rm:", func(u *UI, m actMsg) { u.libCompatRemove(m.arg("lib-compat-rm:")) })
}

func compatKindLabel(kind string) string { return i18n.T("library.compat.kind." + kind) }

// filterSmartDB is FilterSmart with DB-prepped inputs - REQUIRED for any rules that may
// carry a compat anchor (plain FilterSmart fails a compat rule closed).
func (u *UI) filterSmartDB(tracks []musiclib.Track, r musiclib.SmartRules) []musiclib.Track {
	return musiclib.FilterSmartPrep(tracks, r, u.svc.Lib.SmartPrep(r))
}

// compatKindsLabel joins one partner's (possibly multiple) mark kinds for display.
func compatKindsLabel(kinds []string) string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, compatKindLabel(k))
	}
	return strings.Join(out, " · ")
}

// libCompatMarkModal opens the kind picker over the active selection. src ∈ coll|browse|pub
// (pub = the Publish tab's resolved tracklist selection).
func (u *UI) libCompatMarkModal(src string) {
	if u.svc.Lib == nil {
		u.toast(i18n.T("library.dbUnavailable"))
		return
	}
	s := u.lib()
	s.mu.Lock()
	var paths []string
	switch src {
	case "browse":
		for p := range s.batch {
			if libKind(p, false) == "audio" {
				paths = append(paths, p)
			}
		}
	case "pub":
		for p := range u.pubTSel() {
			paths = append(paths, p)
		}
	default:
		for p := range s.collSel {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	s.compatPaths = paths
	s.mu.Unlock()
	if len(paths) < 2 {
		u.toast(i18n.T("library.compat.needTwo"))
		return
	}
	if len(paths) > compatMaxSel {
		u.toast(i18n.T("library.compat.tooMany", i18n.A{"count": fmt.Sprint(len(paths)), "max": fmt.Sprint(compatMaxSel)}))
		return
	}
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.compat.modalDesc", i18n.A{"count": fmt.Sprint(len(paths))})) + `</p>`)
	for _, k := range libdb.CompatKinds {
		b.WriteString(itemRow(compatKindLabel(k), i18n.T("library.compat.hint."+k),
			btn(i18n.T("library.compat.markAction"), "primary", "lib-compat-do:"+k, "")))
	}
	u.openModal(modal(i18n.T("library.compat.modalTitle"), b.String(), ""))
}

// libCompatDo writes all pairs among the pending selection with the picked kind.
func (u *UI) libCompatDo(kind string) {
	u.closeModal()
	if u.svc.Lib == nil {
		return
	}
	s := u.lib()
	s.mu.Lock()
	paths := s.compatPaths
	s.compatPaths = nil
	s.mu.Unlock()
	if len(paths) < 2 {
		return
	}
	u.bg(func() {
		n, err := u.svc.Lib.AddCompatPairs(kind, paths)
		if err != nil {
			u.toast(i18n.T("library.compat.markFailed") + err.Error())
			return
		}
		u.toast(i18n.T("library.compat.markedToast", i18n.A{"n": fmt.Sprint(n), "kind": compatKindLabel(kind)}))
		u.libPatchDetail()
	})
}

// compatHit is one discovery result: a partner (depth 1 = marked pair) or a
// friend-of-friend (depth 2, via the intermediate track).
type compatHit struct {
	path  string
	kinds []string // depth 1: this pair's mark kinds; depth 2: the second hop's kinds
	via   string   // depth 2: intermediate path
	depth int
}

// compatDiscover merges direct partners (kinds grouped per path) + depth-2 hits, capped.
// Pure - callers fetch direct via CompatFor and second via CompatForMany.
func compatDiscover(from string, direct []libdb.CompatRow, second map[string][]libdb.CompatRow, limit int) []compatHit {
	var out []compatHit
	idx := map[string]int{} // path → out index (depth-1 kind merge)
	seen := map[string]bool{from: true}
	for _, r := range direct {
		if i, ok := idx[r.Path]; ok {
			out[i].kinds = append(out[i].kinds, r.Kind)
			continue
		}
		idx[r.Path] = len(out)
		seen[r.Path] = true
		out = append(out, compatHit{path: r.Path, kinds: []string{r.Kind}, depth: 1})
	}
	if len(out) > limit {
		out = out[:limit]
	}
	nDirect := len(out)
	for i := 0; i < nDirect && len(out) < limit; i++ {
		via := out[i].path
		for _, r := range second[via] {
			if seen[r.Path] {
				continue
			}
			seen[r.Path] = true
			out = append(out, compatHit{path: r.Path, kinds: []string{r.Kind}, via: via, depth: 2})
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

// libCompatHits loads discovery results for path (DB reads; call off the render path or accept
// two small indexed queries).
func (u *UI) libCompatHits(path string) []compatHit {
	direct, err := u.svc.Lib.CompatFor(path)
	if err != nil || len(direct) == 0 {
		return nil
	}
	neighbors := make([]string, 0, len(direct))
	seen := map[string]bool{}
	for _, r := range direct {
		if !seen[r.Path] {
			seen[r.Path] = true
			neighbors = append(neighbors, r.Path)
		}
	}
	second, _ := u.svc.Lib.CompatForMany(neighbors)
	return compatDiscover(path, direct, second, compatMaxHits)
}

// libCompatTitle renders a hit's display title from the collection (falls back to filename).
func libCompatTitle(s *libSt, path string) string {
	if t, ok := s.byPath[path]; ok {
		return trackTitle(t)
	}
	return filepath.Base(path)
}

// libCompatFindModal shows direct + depth-2 compatible tracks for path (set building).
func (u *UI) libCompatFindModal(path string) {
	if u.svc.Lib == nil {
		u.toast(i18n.T("library.dbUnavailable"))
		return
	}
	s := u.lib()
	s.mu.Lock()
	s.compatFind = path
	s.mu.Unlock()
	u.bg(func() {
		hits := u.libCompatHits(path)
		s.mu.Lock()
		if s.compatFind != path { // superseded by a newer find
			s.mu.Unlock()
			return
		}
		name := libCompatTitle(s, path)
		var b strings.Builder
		if len(hits) == 0 {
			b.WriteString(emptyState(i18n.T("library.compat.none")))
		}
		for _, h := range hits {
			sub := compatKindsLabel(h.kinds)
			trail := []string{btn(i18n.T("library.open"), "outline", "lib-compat-go:"+h.path, "")}
			if h.depth == 2 {
				sub = i18n.T("library.compat.via", i18n.A{"name": libCompatTitle(s, h.via), "kind": sub})
			} else {
				trail = append(trail, btn("✕", "ghost", "lib-compat-rm:"+path+"\x1f"+h.path, ""))
			}
			b.WriteString(itemRow(libCompatTitle(s, h.path), sub, trail...))
		}
		s.mu.Unlock()
		u.openModal(modal(i18n.T("library.compat.findTitle", i18n.A{"name": name}), b.String(), ""))
	})
}

// libCompatRemove deletes a pair's marks (all kinds) and refreshes the discovery modal.
func (u *UI) libCompatRemove(arg string) {
	a, b, ok := strings.Cut(arg, "\x1f")
	if !ok || u.svc.Lib == nil {
		return
	}
	if err := u.svc.Lib.RemoveCompat(a, b, ""); err != nil {
		u.toast(i18n.T("library.compat.markFailed") + err.Error())
		return
	}
	u.toast(i18n.T("library.compat.removed"))
	u.libCompatFindModal(a)
	u.libPatchDetail()
}

// libCompatSectionHTML: detail-rail section - direct partners (capped) + find button. direct is
// resolved off-thread + cached on the selection (see libDetailData); ready=false shows a loading
// line until the first resolve lands. Caller holds s.mu.
func (u *UI) libCompatSectionHTML(s *libSt, path string, direct []libdb.CompatRow, ready bool) string {
	if u.svc.Lib == nil {
		return ""
	}
	var b strings.Builder
	if len(direct) == 0 {
		empty := i18n.T("library.compat.sectionEmpty")
		if !ready {
			empty = i18n.T("library.remote.col.loading")
		}
		b.WriteString(`<p class=page-sub>` + html.EscapeString(empty) + `</p>`)
	} else {
		hits := compatDiscover(path, direct, nil, compatRailMax)
		for _, h := range hits {
			b.WriteString(itemRow(libCompatTitle(s, h.path), compatKindsLabel(h.kinds),
				btn(i18n.T("library.open"), "ghost", "lib-compat-go:"+h.path, "")))
		}
	}
	b.WriteString(btnRow(btn(i18n.T("library.compat.findBtn"), "outline", "lib-compat-find:"+path, "")))
	return b.String()
}
