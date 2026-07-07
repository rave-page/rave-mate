package maintenance

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// nmlSplit is the inverse of musiclib.resolveLocation (which the scan uses): OS path → Traktor
// LOCATION (VOLUME, "/:seg/:" DIR, FILE), so the test's synthetic entries resolve back to real paths.
func nmlSplit(p string) (volume, dir, file string) {
	file = filepath.Base(p)
	parent := filepath.Dir(p)
	if runtime.GOOS == "windows" {
		volume = filepath.VolumeName(parent)
		parent = strings.TrimPrefix(parent, volume)
	}
	parent = strings.Trim(filepath.ToSlash(parent), "/")
	if parent == "" {
		return volume, "/:", file
	}
	var sb strings.Builder
	for _, s := range strings.Split(parent, "/") {
		sb.WriteString("/:")
		sb.WriteString(s)
	}
	sb.WriteString("/:")
	return volume, sb.String(), file
}

// TestScanMissingFromCollection: a collection.nml referencing one present + one absent file reports
// exactly the absent path - the key behaviour that makes cleanup idempotent against re-import
// (missing is judged from the collection the import reads, not the already-cleaned DB).
func TestScanMissingFromCollection(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "here.mp3")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(dir, "gone.mp3") // never created

	// LOCATION VOLUME/DIR/FILE must resolve (via musiclib.resolveLocation) back to these OS paths.
	// On Windows VolumeName is "C:"; on Unix it's empty. Derive the NML form from the actual paths.
	entry := func(p string) string {
		v, d, f := nmlSplit(p)
		return fmt.Sprintf(`<ENTRY TITLE="t" ARTIST="a"><LOCATION DIR="%s" FILE="%s" VOLUME="%s"></LOCATION></ENTRY>`, d, f, v)
	}
	nml := `<?xml version="1.0"?><NML VERSION="20"><COLLECTION ENTRIES="2">` +
		entry(present) + entry(absent) + `</COLLECTION></NML>`
	path := filepath.Join(dir, "collection.nml")
	if err := os.WriteFile(path, []byte(nml), 0o600); err != nil {
		t.Fatal(err)
	}

	missing, pathless, err := ScanMissingFromCollection(path)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if pathless != 0 {
		t.Errorf("pathless = %d, want 0", pathless)
	}
	if len(missing) != 1 || missing[0] != absent {
		t.Fatalf("missing = %v, want [%s]", missing, absent)
	}
}
