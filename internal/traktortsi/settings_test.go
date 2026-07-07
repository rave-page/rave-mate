package traktortsi

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// TestSettingsRoundTrip: read the controller blob from a synthetic Settings.tsi, add a
// device, write it back, and confirm the file now parses with the new device - proving the
// full XML ↔ DIOM ↔ XML path the installer uses.
func TestSettingsRoundTrip(t *testing.T) {
	blob := buildSyntheticDIOM([]Device{{Name: "Existing", OutPort: "x"}})
	enc := base64.StdEncoding.EncodeToString(blob)
	xml := `<?xml version="1.0"?><NIXML><TraktorSettings>` +
		`<Entry Name="Audio.DeviceName" Type="3" Value=""></Entry>` +
		`<Entry Name="DeviceIO.Config.Controller" Type="3" Value="` + enc + `"></Entry>` +
		`<Entry Name="Other.Thing" Type="1" Value="42"></Entry></TraktorSettings></NIXML>`

	path := filepath.Join(t.TempDir(), "Traktor Settings.tsi")
	if err := os.WriteFile(path, []byte(xml), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadControllerBlob(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	names, _ := DeviceNames(got)
	if len(names) != 1 || names[0] != "Existing" {
		t.Fatalf("read names = %v", names)
	}

	updated, err := AddDevice(got, makeDEVI("RavePage State", "None", "LoopBe Internal MIDI"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteControllerBlob(path, updated); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Re-read from disk → both devices present, other XML entries intact.
	reread, err := ReadControllerBlob(path)
	if err != nil {
		t.Fatal(err)
	}
	names, _ = DeviceNames(reread)
	if len(names) != 2 || names[1] != "RavePage State" {
		t.Fatalf("after write names = %v", names)
	}
	final, _ := os.ReadFile(path)
	if !contains(final, `Name="Other.Thing"`) || !contains(final, `Name="Audio.DeviceName"`) {
		t.Fatal("unrelated entries were lost")
	}
}

func contains(b []byte, s string) bool {
	return len(b) > 0 && bytesContains(b, []byte(s))
}
func bytesContains(b, sub []byte) bool {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return true
		}
	}
	return false
}
