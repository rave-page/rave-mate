package featurehost

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

// TestTwitchFeatureE2E runs the twitch feature in a real child process with an isolated
// (token-less) config dir and asserts the signed-out state mirror + local op behaviour.
func TestTwitchFeatureE2E(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfgDir := t.TempDir()
	log := logbus.New(500)
	p, err := NewTwitchProxy(log, nil, nil, func() string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	p.host.command = func() *exec.Cmd {
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(),
			"RAVE_MATE_TEST_FEATURE=twitch",
			"RAVE_MATE_CONFIG_DIR="+cfgDir, // no sealed token → signed out
		)
		return cmd
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Host().Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.Host().Stop()
	waitFor(t, "child running", 15*time.Second, p.host.Running)

	if p.SignedIn() {
		t.Error("SignedIn true with no sealed token")
	}
	if p.Self().ID != "" {
		t.Errorf("Self = %+v, want zero", p.Self())
	}
	// Not connected + no bus → SendChat reports no route (parity with the old Manager).
	if err := p.SendChat(ctx, "hi", ""); err == nil || !strings.Contains(err.Error(), "no peers") {
		t.Errorf("SendChat err = %v, want no-peers routing error", err)
	}
	// Signed-out title op surfaces the child's not-connected error.
	tctx, tcancel := context.WithTimeout(ctx, 10*time.Second)
	defer tcancel()
	if err := p.SetTitle(tctx, "title", ""); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("SetTitle err = %v, want not-connected", err)
	}
	// Logout with no token is a clean no-op RPC round-trip.
	p.Auth().Logout()
	// Unknown method errors without killing the child.
	if _, err := p.host.Call(tctx, "nope", nil); err == nil {
		t.Error("unknown method did not error")
	}
	if !p.host.Running() {
		t.Error("child died on unknown method")
	}
}
