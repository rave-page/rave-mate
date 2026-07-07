package traktormap

import (
	"archive/zip"
	"bytes"
	"testing"

	"rave.page/mate/internal/traktortsi"
)

// makeMappingZip builds an in-memory controller-mapping zip (a .tsi carrying one device),
// mirroring NI's download layout - lets us test extraction without the network.
func makeMappingZip(t *testing.T, device string) []byte {
	t.Helper()
	devi := traktortsi.MakeDevice(device, "DNHC4500_MAP_V1010", "All Ports", "All Ports")
	xml := traktortsi.EncodeSettingsXML(traktortsi.NewDIOM(devi))

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("Denon - DN-HC4500.tsi")
	_, _ = w.Write(xml)
	r, _ := zw.Create("Denon - DN-HC4500.pdf") // a sibling non-tsi entry, like the real zip
	_, _ = r.Write([]byte("%PDF-1.4 stub"))
	_ = zw.Close()
	return buf.Bytes()
}

func TestExtractDEVIFromZip(t *testing.T) {
	zipBytes := makeMappingZip(t, denonDevice)
	devi, err := extractDEVIFromZip(zipBytes, denonDevice)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	// The extracted DEVI installs + reads back under the right name.
	blob, err := traktortsi.AddDevice(traktortsi.NewDIOM(), devi)
	if err != nil {
		t.Fatalf("install extracted: %v", err)
	}
	if has, _ := traktortsi.HasDevice(blob, denonDevice); !has {
		t.Fatalf("extracted DEVI did not install as %q", denonDevice)
	}
}

func TestExtractMissingDevice(t *testing.T) {
	zipBytes := makeMappingZip(t, "Something.Else")
	if _, err := extractDEVIFromZip(zipBytes, denonDevice); err == nil {
		t.Fatal("expected error when the named device isn't in the zip")
	}
}
