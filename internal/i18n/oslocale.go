package i18n

import "os"

// envLocale reads the POSIX locale env vars (highest precedence first). Set on Unix;
// usually empty on Windows (where oslocale_windows.go queries the OS instead).
func envLocale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		if v := os.Getenv(k); v != "" && v != "C" && v != "POSIX" {
			// LANGUAGE may be a colon list ("de:en") - take the first.
			if i := indexByte(v, ':'); i >= 0 {
				v = v[:i]
			}
			return v
		}
	}
	return ""
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
