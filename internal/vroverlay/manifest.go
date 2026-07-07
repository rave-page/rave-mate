package vroverlay

import (
	"encoding/json"
	"os"

	"rave.page/mate/internal/config"
)

// vrAppKey is the SteamVR application key for the rave-mate overlay (must match the manifest).
const vrAppKey = "rave.page.mate.overlay"

// writeManifest writes a SteamVR .vrmanifest (next to config) pointing at the current exe, so
// SteamVR can list + auto-launch the overlay. Returns the manifest path.
func writeManifest() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	path, err := config.DataPath("rave-mate.vrmanifest")
	if err != nil {
		return "", err
	}
	manifest := map[string]any{
		"source": "builtin",
		"applications": []map[string]any{{
			"app_key":              vrAppKey,
			"launch_type":          "binary",
			"binary_path_windows":  exe,
			"binary_path_linux":    exe,
			"is_dashboard_overlay": true,
			"strings": map[string]any{
				"en_us": map[string]any{
					"name":        "rave-mate",
					"description": "Twitch chat + alerts overlay for DJs",
				},
			},
		}},
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}
