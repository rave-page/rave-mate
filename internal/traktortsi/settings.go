package traktortsi

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"regexp"
)

// ErrNoControllerEntry means the Settings.tsi had no DeviceIO.Config.Controller entry
// (an unexpected/foreign settings file - we refuse to guess).
var ErrNoControllerEntry = errors.New("traktortsi: no DeviceIO.Config.Controller entry in Settings.tsi")

// ctrlRe captures the base64 controller blob inside its XML entry. The Value is base64
// (A–Za–z0–9+/=), so the "-delimited attribute never contains a quote - a byte-level
// find/replace is safe and avoids reserialising the ~1 MB settings file.
var ctrlRe = regexp.MustCompile(`(Name="DeviceIO\.Config\.Controller" Type="3" Value=")([^"]*)(")`)

// ReadControllerBlob reads Settings.tsi and returns the decoded DIOM controller blob.
func ReadControllerBlob(tsiPath string) ([]byte, error) {
	raw, err := os.ReadFile(tsiPath)
	if err != nil {
		return nil, err
	}
	return ControllerBlobFromXML(raw)
}

// ControllerBlobFromXML decodes the controller blob from in-memory .tsi XML bytes (used for
// a downloaded/unzipped controller mapping that isn't on disk as Settings.tsi).
func ControllerBlobFromXML(xml []byte) ([]byte, error) {
	m := ctrlRe.FindSubmatch(xml)
	if m == nil {
		return nil, ErrNoControllerEntry
	}
	return base64.StdEncoding.DecodeString(string(m[2]))
}

// EncodeSettingsXML wraps a DIOM blob in a minimal NIXML settings document carrying just the
// controller entry - a valid importable .tsi (used for exports + tests).
func EncodeSettingsXML(blob []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(blob)
	return []byte(`<?xml version="1.0" encoding="UTF-8" standalone="no" ?>` + "\n" +
		`<NIXML><TraktorSettings>` +
		`<Entry Name="DeviceIO.Config.Controller" Type="3" Value="` + enc + `"></Entry>` +
		`</TraktorSettings></NIXML>`)
}

// WriteControllerBlob re-encodes blob and replaces the controller entry's Value in place,
// writing the file back. Callers MUST have checked IsRunning + taken a Backup first.
func WriteControllerBlob(tsiPath string, blob []byte) error {
	raw, err := os.ReadFile(tsiPath)
	if err != nil {
		return err
	}
	if !ctrlRe.Match(raw) {
		return ErrNoControllerEntry
	}
	enc := base64.StdEncoding.EncodeToString(blob)
	out := ctrlRe.ReplaceAllFunc(raw, func(m []byte) []byte {
		sub := ctrlRe.FindSubmatch(m)
		return bytes.Join([][]byte{sub[1], []byte(enc), sub[3]}, nil)
	})
	// Preserve the original file mode where possible; default 0644.
	mode := os.FileMode(0o644)
	if fi, e := os.Stat(tsiPath); e == nil {
		mode = fi.Mode()
	}
	return os.WriteFile(tsiPath, out, mode)
}
