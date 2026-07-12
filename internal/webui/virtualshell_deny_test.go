package webui

import (
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/ui"
)

func TestVirtualDeniedActs(t *testing.T) {
	denied := []string{
		"open-url", "auth-login", "settings-rbmidi-folder",
		"ovl-png-open", "ovl-np-open",
		"settings-twitch-signin", "settings-gh-device", "world-gh-device",
		"mp-openext:library", "mp-reveal:publish",
		"lib-opendir:C:/x", "lib-reveal:C:/x/y.mp3", "lib-openext:C:/x/y.mp3", "lib-bk-open:C:/b",
		"pub-open:cap1", "pub-reveal:cap1",
		"settings-open:sets",
	}
	for _, act := range denied {
		if !virtualDenied(act) {
			t.Errorf("%s must be denied in a headless session", act)
		}
	}
	allowed := []string{"tab", "lib-section:collection", "mp-play:library", "mp-stop:library",
		"lib-track:C:/x/y.mp3", "copy", "modal-close"}
	for _, act := range allowed {
		if virtualDenied(act) {
			t.Errorf("%s must stay allowed in a headless session", act)
		}
	}
}

// A headless session's onAction must refuse desktop-opening acts with a toast BEFORE any
// handler runs (no browser/Explorer window may open on the host).
func TestHeadlessOnActionRefusesDesktopActs(t *testing.T) {
	evals := make(chan string, 64)
	hu := newHeadlessUI(ui.Services{}, func(string) {}, func(js string) { evals <- js })
	t.Cleanup(func() { hu.Stop(); releaseUIState(hu) })
	hu.onAction(`{"Act":"open-url","Val":"https://example.com"}`)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case js := <-evals:
			if strings.Contains(js, "__toast") {
				return
			}
		case <-deadline:
			t.Fatal("no refusal toast for open-url in a headless session")
		}
	}
}
