package localmedia

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestListDirectoryShape pins the web-byte-exact JSON: dirs first, kind classification, parent,
// non-null empty entries, and error-on-bad-path.
func TestListDirectoryShape(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "a.mp3"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "b.mp4"), []byte("y"), 0o644))
	must(os.WriteFile(filepath.Join(dir, ".hidden"), []byte("z"), 0o644))

	got := ListDirectory(dir, false)
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
	if len(got.Entries) != 3 { // .hidden excluded
		t.Fatalf("entries=%d want 3", len(got.Entries))
	}
	if !got.Entries[0].IsDirectory || got.Entries[0].Name != "sub" {
		t.Fatalf("dir not first: %+v", got.Entries[0])
	}
	if got.Parent == nil || *got.Parent != filepath.Dir(filepath.Clean(dir)) {
		t.Fatalf("parent wrong: %v", got.Parent)
	}
	kinds := map[string]string{}
	for _, e := range got.Entries {
		kinds[e.Name] = e.Kind
	}
	if kinds["a.mp3"] != "audio" || kinds["b.mp4"] != "video" || kinds["sub"] != "directory" {
		t.Fatalf("kind classification wrong: %v", kinds)
	}

	// includeHidden surfaces the dotfile.
	if n := len(ListDirectory(dir, true).Entries); n != 4 {
		t.Fatalf("includeHidden entries=%d want 4", n)
	}

	// JSON: empty entries marshal as [] (not null); error present as a key.
	raw, _ := json.Marshal(ListDirectory(filepath.Join(dir, "nope"), false))
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	if string(m["entries"]) != "[]" {
		t.Fatalf("entries on error = %s want []", m["entries"])
	}
	if _, ok := m["error"]; !ok {
		t.Fatal("error key missing on bad path")
	}
	if string(m["parent"]) != "null" {
		t.Fatalf("parent on error = %s want null", m["parent"])
	}
}

func TestDefaultsKeys(t *testing.T) {
	raw, _ := json.Marshal(Defaults())
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"home", "desktop", "documents", "downloads", "music", "videos", "pictures"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("defaults missing key %q (%s)", k, raw)
		}
	}
}
