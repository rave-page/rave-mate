package webui

import (
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/ui"
)

// navTestUI builds a shell-less UI (eval/patch are no-ops without a shell).
func navTestUI() *UI {
	return &UI{svc: ui.Services{Cfg: &config.Config{}}, active: "live"}
}

// TestNavBackForward: setTab records history; back/forward restore the tab and sub-nav.
func TestNavBackForward(t *testing.T) {
	u := navTestUI()
	u.setTab("library")
	u.libSetSection("collection")
	u.setTab("settings")

	if got := u.activeTab(); got != "settings" {
		t.Fatalf("active = %q, want settings", got)
	}
	u.navBack() // → library/collection
	if got := u.activeTab(); got != "library" {
		t.Fatalf("after back, active = %q, want library", got)
	}
	u.mu.Lock()
	sec := u.libSection
	u.mu.Unlock()
	if sec != "collection" {
		t.Fatalf("after back, libSection = %q, want collection", sec)
	}
	u.navBack() // → library/browse ("" section pre-libSetSection)
	if got := u.activeTab(); got != "library" {
		t.Fatalf("after 2nd back, active = %q, want library", got)
	}
	u.navBack() // → live
	if got := u.activeTab(); got != "live" {
		t.Fatalf("after 3rd back, active = %q, want live", got)
	}
	u.navBack() // start of history: no-op
	if got := u.activeTab(); got != "live" {
		t.Fatalf("back past start changed active to %q", got)
	}
	u.navFwd() // → library/browse
	if got := u.activeTab(); got != "library" {
		t.Fatalf("after fwd, active = %q, want library", got)
	}
	u.navFwd() // → library/collection
	u.navFwd() // → settings
	if got := u.activeTab(); got != "settings" {
		t.Fatalf("after fwd to tip, active = %q, want settings", got)
	}
	u.navFwd() // tip: no-op
	if got := u.activeTab(); got != "settings" {
		t.Fatalf("fwd past tip changed active to %q", got)
	}
}

// TestNavForwardClearedOnNewNav: a fresh navigation drops the forward stack.
func TestNavForwardClearedOnNewNav(t *testing.T) {
	u := navTestUI()
	u.setTab("library")
	u.setTab("settings")
	u.navBack() // → library, forward = [settings]
	u.setTab("logs")
	u.navFwd() // forward was cleared → no-op, stays on logs
	if got := u.activeTab(); got != "logs" {
		t.Fatalf("forward not cleared: active = %q, want logs", got)
	}
}
