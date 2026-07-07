package encoderscan

import (
	"os"
	"path/filepath"
	"strings"
)

// obsconfig.go reads OBS's configured stream/record encoder straight from its profile ini - the
// authoritative source, works even when obs-websocket is disconnected. The live "is it actually
// streaming/recording" flag comes separately from obs-ws (proxy GetStreamStatus/GetRecordStatus).

// parseINI parses a minimal OBS-style ini into section → key → value (case-preserved keys).
func parseINI(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, ";") || strings.HasPrefix(l, "#") {
			continue
		}
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			section = l[1 : len(l)-1]
			if out[section] == nil {
				out[section] = map[string]string{}
			}
			continue
		}
		eq := strings.IndexByte(l, '=')
		if eq < 0 || section == "" {
			continue
		}
		out[section][strings.TrimSpace(l[:eq])] = strings.TrimSpace(l[eq+1:])
	}
	return out
}

// obsEncoderFromProfile picks the stream + record encoder ids from a parsed profile ini, honoring
// the active output mode (Advanced → AdvOut keys, else SimpleOutput keys).
func obsEncoderFromProfile(profile map[string]map[string]string) (stream, record string) {
	mode := profile["Output"]["Mode"]
	if strings.EqualFold(mode, "Advanced") {
		return profile["AdvOut"]["Encoder"], profile["AdvOut"]["RecEncoder"]
	}
	return profile["SimpleOutput"]["StreamEncoder"], profile["SimpleOutput"]["RecEncoder"]
}

// obsConfigDir is OBS's config root (%APPDATA%\obs-studio on Windows).
func obsConfigDir() string {
	if d := os.Getenv("APPDATA"); d != "" {
		return filepath.Join(d, "obs-studio")
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "obs-studio")
	}
	return ""
}

// obsActiveProfileDir reads the active profile's dir from OBS's [Basic] block. OBS 30+ keeps it in
// user.ini; older builds in global.ini - try user.ini first, then global.ini.
func obsActiveProfileDir(root string) string {
	for _, f := range []string{"user.ini", "global.ini"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue
		}
		if dir := parseINI(string(b))["Basic"]["ProfileDir"]; dir != "" {
			return dir
		}
	}
	return ""
}

// OBSConfigEncoder reads the active OBS profile's stream/record encoder ids. ok=false if OBS config
// isn't found. Never errors loudly - detection degrades to "OBS unknown".
func OBSConfigEncoder() (stream, record string, ok bool) {
	root := obsConfigDir()
	if root == "" {
		return "", "", false
	}
	dir := obsActiveProfileDir(root)
	if dir == "" {
		return "", "", false
	}
	prof, err := os.ReadFile(filepath.Join(root, "basic", "profiles", dir, "basic.ini"))
	if err != nil {
		return "", "", false
	}
	stream, record = obsEncoderFromProfile(parseINI(string(prof)))
	return stream, record, stream != "" || record != ""
}
