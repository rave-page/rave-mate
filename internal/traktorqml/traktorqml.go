// Package traktorqml installs/removes the Traktor "api-client" QML mod that makes Traktor
// POST live deck/channel/master state to http://localhost:8080 - the feed internal/traktor
// listens for. Unlike the common approach (replace the whole D2 QML folder, which ships a
// fixed D2.qml that a Traktor update then clobbers/breaks), this PATCHES the live D2.qml in
// place: two inserted lines (`import "./Api"` + `ApiModule {}`) plus a self-contained `Api/`
// folder dropped beside it. So a Traktor update that rewrites D2.qml is recovered by simply
// re-applying the patch to the NEW stock file, never overwriting it with a stale one.
//
// The D2 folder lives under Program Files, so Apply/Revert need elevation (the caller
// self-elevates - see the `traktor-qml` subcommand). Every change backs up D2.qml first and
// is reversible. The embedded Api/ QML is Erik Minekus's MIT-licensed mod (see assets NOTICE).
package traktorqml

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/traktorcfg"
)

const source = "traktor-qml"

// Markers inserted into D2.qml. apiImport goes after the import block; apiModule as the first
// child of Mapping{}. apiModule's presence is the "is patched" signal.
const (
	apiImport = `import "./Api"`
	apiModule = `ApiModule {}`
)

//go:embed assets/Api/*.qml assets/Api/*.js
var apiAssets embed.FS

// Install is a discovered Traktor install + its D2 QML paths.
type Install struct {
	Version string // "4.2.0" (from the folder name "Traktor Pro 4.2.0"? no - Program Files uses "Traktor Pro 4")
	Root    string // ...\Native Instruments\Traktor Pro 4
	D2Dir   string // ...\Resources64\qml\CSI\D2
	D2QML   string // ...\D2\D2.qml
	ApiDir  string // ...\D2\Api
}

// d2RelCandidates are the known D2 layouts, most-likely first (Pro 4 Win drops "Common").
var d2RelCandidates = []string{
	filepath.Join("Resources64", "qml", "CSI", "D2"),
	filepath.Join("Resources64", "qml", "CSI", "Common", "D2"),
	filepath.Join("Resources", "qml", "CSI", "D2"),
	filepath.Join("Resources", "qml", "CSI", "Common", "D2"),
}

// Discover finds every Traktor Pro install under Program Files that has a D2/D2.qml,
// newest-version first.
func Discover() ([]Install, error) {
	base := programFilesNI()
	if base == "" {
		return nil, fmt.Errorf("could not resolve Program Files\\Native Instruments")
	}
	ents, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", base, err)
	}
	var out []Install
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "Traktor Pro") {
			continue // skip the "Traktor Kontrol * Driver" dirs
		}
		root := filepath.Join(base, e.Name())
		for _, rel := range d2RelCandidates {
			d2 := filepath.Join(root, rel)
			qml := filepath.Join(d2, "D2.qml")
			if st, err := os.Stat(qml); err == nil && !st.IsDir() {
				out = append(out, Install{
					Version: strings.TrimSpace(strings.TrimPrefix(e.Name(), "Traktor Pro")),
					Root:    root, D2Dir: d2, D2QML: qml, ApiDir: filepath.Join(d2, "Api"),
				})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j].Version, out[i].Version) })
	return out, nil
}

// programFilesNI resolves "<Program Files>\Native Instruments" (Windows). Empty elsewhere.
func programFilesNI() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		if _, err := os.Stat(`C:\Program Files`); err == nil {
			pf = `C:\Program Files`
		}
	}
	if pf == "" {
		return ""
	}
	return filepath.Join(pf, "Native Instruments")
}

// versionLess compares dotted version strings numerically ("4.1.1" < "4.2.0").
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, nb := atoi(pa[i]), atoi(pb[i])
		if na != nb {
			return na < nb
		}
	}
	return len(pa) < len(pb)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Newest returns the newest install with a D2.qml (ok=false if none found).
func Newest() (Install, bool, error) {
	all, err := Discover()
	if err != nil {
		return Install{}, false, err
	}
	if len(all) == 0 {
		return Install{}, false, nil
	}
	return all[0], true, nil
}

// Status reports the mod state of an install.
type Status struct {
	Install    Install
	Patched    bool // D2.qml carries our ApiModule marker
	ApiPresent bool // Api/ApiClient.js exists
	HasBackup  bool // at least one D2.qml.ravebak-* exists
	Healthy    bool // Patched && ApiPresent (fully installed)
}

// Probe inspects an install without changing anything.
func Probe(in Install) Status {
	s := Status{Install: in}
	if b, err := os.ReadFile(in.D2QML); err == nil {
		s.Patched = strings.Contains(string(b), apiModule)
	}
	if st, err := os.Stat(filepath.Join(in.ApiDir, "ApiClient.js")); err == nil && !st.IsDir() {
		s.ApiPresent = true
	}
	s.HasBackup = len(backups(in.D2QML)) > 0
	s.Healthy = s.Patched && s.ApiPresent
	return s
}

// Apply installs the mod into the given install: backs up D2.qml, writes the embedded Api/
// folder, and patches D2.qml in place (idempotent). Requires write access to Program Files
// (elevation) and Traktor must be closed. Returns the backup path (empty if nothing changed).
func Apply(in Install, log *logbus.Bus) (string, error) {
	if running, _ := traktorcfg.IsRunning(); running {
		return "", fmt.Errorf("Traktor is running - quit it first (it may hold the QML files)")
	}
	orig, err := os.ReadFile(in.D2QML)
	if err != nil {
		return "", fmt.Errorf("read D2.qml: %w", err)
	}
	patched, changed, err := patchD2(string(orig))
	if err != nil {
		return "", err
	}
	apiNeeds := !Probe(in).ApiPresent
	if !changed && !apiNeeds {
		return "", nil // already fully installed
	}
	bak, err := backupFile(in.D2QML)
	if err != nil {
		return "", fmt.Errorf("backup D2.qml: %w", err)
	}
	if err := writeAPI(in.ApiDir); err != nil {
		return bak, fmt.Errorf("install Api/: %w", err)
	}
	if changed {
		if err := atomicWrite(in.D2QML, []byte(patched)); err != nil {
			return bak, fmt.Errorf("write patched D2.qml: %w", err)
		}
	}
	if log != nil {
		log.Info(source, "api-client mod applied", map[string]any{"d2": in.D2Dir, "patched": changed, "backup": bak})
	}
	return bak, nil
}

// Revert removes the mod: unpatches D2.qml (removes the two inserted lines) and deletes the
// Api/ folder. Version-safe - it edits whatever D2.qml is currently there rather than
// restoring an old backup over a newer stock file. Requires elevation; Traktor must be closed.
func Revert(in Install, log *logbus.Bus) error {
	if running, _ := traktorcfg.IsRunning(); running {
		return fmt.Errorf("Traktor is running - quit it first")
	}
	orig, err := os.ReadFile(in.D2QML)
	if err != nil {
		return fmt.Errorf("read D2.qml: %w", err)
	}
	unpatched, changed := unpatchD2(string(orig))
	if changed {
		if _, err := backupFile(in.D2QML); err != nil {
			return fmt.Errorf("backup D2.qml: %w", err)
		}
		if err := atomicWrite(in.D2QML, []byte(unpatched)); err != nil {
			return fmt.Errorf("write D2.qml: %w", err)
		}
	}
	if err := os.RemoveAll(in.ApiDir); err != nil {
		return fmt.Errorf("remove Api/: %w", err)
	}
	if log != nil {
		log.Info(source, "api-client mod reverted", map[string]any{"d2": in.D2Dir, "unpatched": changed})
	}
	return nil
}

// ── pure transforms (unit-tested) ─────────────────────────────────────────────

// patchD2 injects `import "./Api"` after the import block and `ApiModule {}` as the first
// child of Mapping{}. Idempotent (no-op if already patched). Pure - no filesystem.
func patchD2(src string) (string, bool, error) {
	if strings.Contains(src, apiModule) {
		return src, false, nil
	}
	lines := strings.Split(src, "\n")

	lastImport := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "import ") {
			lastImport = i
		}
	}
	if lastImport < 0 {
		return "", false, fmt.Errorf("D2.qml: no import block found")
	}
	withImport := make([]string, 0, len(lines)+1)
	withImport = append(withImport, lines[:lastImport+1]...)
	withImport = append(withImport, apiImport)
	withImport = append(withImport, lines[lastImport+1:]...)

	mapBrace := findMappingBrace(withImport)
	if mapBrace < 0 {
		return "", false, fmt.Errorf("D2.qml: no Mapping {} block found")
	}
	indent := leadingWS(withImport[mapBrace]) + "  "
	final := make([]string, 0, len(withImport)+2)
	final = append(final, withImport[:mapBrace+1]...)
	final = append(final, indent+apiModule, "")
	final = append(final, withImport[mapBrace+1:]...)
	return strings.Join(final, "\n"), true, nil
}

// unpatchD2 removes the two inserted lines (and one blank line we add after ApiModule).
func unpatchD2(src string) (string, bool) {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	changed := false
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == apiImport || t == apiModule {
			changed = true
			if t == apiModule && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
				i++ // also drop the blank line we inserted after ApiModule
			}
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n"), changed
}

// findMappingBrace returns the index of the `{` that opens the Mapping block - either the
// `{` line following a `Mapping` line, or a same-line `Mapping {`. -1 if not found.
func findMappingBrace(lines []string) int {
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "Mapping" {
			for j := i + 1; j < len(lines); j++ {
				tj := strings.TrimSpace(lines[j])
				if tj == "" {
					continue
				}
				if tj == "{" {
					return j
				}
				break
			}
		}
		if strings.HasPrefix(t, "Mapping") && strings.Contains(t, "{") {
			return i
		}
	}
	return -1
}

func leadingWS(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// ── filesystem helpers ─────────────────────────────────────────────────────────

// writeAPI writes the embedded Api/ QML+JS into dir (creating it), overwriting to pin the
// shipped version.
func writeAPI(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fs.WalkDir(apiAssets, "assets/Api", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := apiAssets.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, filepath.Base(p)), b, 0o644)
	})
}

// backupFile copies path to "<path>.ravebak-<ts>" and returns the backup path.
func backupFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	bak := fmt.Sprintf("%s.ravebak-%s", path, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(bak, b, 0o644); err != nil {
		return "", err
	}
	return bak, nil
}

// backups lists existing D2.qml.ravebak-* for path, newest first.
func backups(path string) []string {
	m, _ := filepath.Glob(path + ".ravebak-*")
	sort.Sort(sort.Reverse(sort.StringSlice(m)))
	return m
}

// atomicWrite writes data to a temp sibling then renames over path (same dir = atomic).
func atomicWrite(path string, data []byte) error {
	tmp := path + ".ravetmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
