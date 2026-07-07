package encoderscan

import (
	"os"
	"path/filepath"
	"strings"
)

// parsecEncoderFromLog scans Parsec host-log text (newest lines matter most) for the active
// hardware encoder family. Parsec logs the encoder it negotiated ("NVENC", "AMD"/"AMF", "QSV");
// best-effort - ok=false if no family token is found (process presence still flags it critical).
func parsecEncoderFromLog(logText string) (EncoderFamily, bool) {
	lines := strings.Split(logText, "\n")
	// Walk newest → oldest so a re-negotiated encoder wins.
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.ToLower(lines[i])
		if !strings.Contains(l, "encod") && !strings.Contains(l, "nvenc") &&
			!strings.Contains(l, "amf") && !strings.Contains(l, "qsv") {
			continue
		}
		switch {
		case strings.Contains(l, "nvenc"), strings.Contains(l, "nvidia"):
			return FamilyNVENC, true
		case strings.Contains(l, "amf"), strings.Contains(l, "vce"), strings.Contains(l, "amd"):
			return FamilyAMF, true
		case strings.Contains(l, "qsv"), strings.Contains(l, "quicksync"), strings.Contains(l, "quick sync"):
			return FamilyQSV, true
		}
	}
	return FamilyUnknown, false
}

// parsecLogPath is the Parsec host log (%APPDATA%\Parsec\log.txt on Windows).
func parsecLogPath() string {
	if d := os.Getenv("APPDATA"); d != "" {
		return filepath.Join(d, "Parsec", "log.txt")
	}
	return ""
}

// ParsecEncoder reads Parsec's log and returns the active encoder family. Adapter is not derivable
// from the log alone (left ""; live GPU util attributes the adapter). ok=false if unavailable.
func ParsecEncoder() (family EncoderFamily, adapter string, ok bool) {
	p := parsecLogPath()
	if p == "" {
		return FamilyUnknown, "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return FamilyUnknown, "", false
	}
	// Only the tail matters; cap the scan to the last ~64 KB.
	if len(b) > 64*1024 {
		b = b[len(b)-64*1024:]
	}
	fam, found := parsecEncoderFromLog(string(b))
	return fam, "", found
}
