package config

import (
	"encoding/json"
	"testing"
)

func TestUIDefaultsToZigWebview(t *testing.T) {
	u := Default().Features.UI
	if !u.UseWebview() {
		t.Fatal("default UI must use the webview renderer")
	}
	if !u.ZigShell() {
		t.Fatal("default webview host must use the Zig shell")
	}
	if !u.VisualShellHosting() {
		t.Fatal("default Zig shell must use visual hosting")
	}
}

func TestMediaEditorDefaultsOn(t *testing.T) {
	cfg := Default()
	if err := json.Unmarshal([]byte(`{"version":1,"features":{}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Features.MediaEditor.Enabled {
		t.Fatal("config without mediaEditor must expose the Editor tab")
	}

	cfg = Default()
	if err := json.Unmarshal([]byte(`{"version":1,"features":{"mediaEditor":{"enabled":false}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Features.MediaEditor.Enabled {
		t.Fatal("explicit mediaEditor opt-out must survive load")
	}
}
