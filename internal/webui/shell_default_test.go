package webui

// The Zig child is the DEFAULT window host (2026-07-27). These pin that, and every documented way
// back out - a default with no test is one refactor away from silently reverting, and the symptom
// would be "the UI feels worse" rather than a failure.

import (
	"testing"

	"rave.page/mate/internal/config"
)

func cfgShell(impl string) *config.Config {
	c := &config.Config{}
	c.Features.UI.ShellImpl = impl
	return c
}

func TestZigShellIsTheDefaultHost(t *testing.T) {
	for _, tc := range []struct {
		name, impl, env string
		cfg             *config.Config
		want            bool
	}{
		{name: "absent shellImpl takes the default", impl: "", want: true},
		{name: "explicit zig", impl: "zig", want: true},
		{name: "explicit go pins the in-process window", impl: "go", want: false},
		{name: "explicit cgo pins the in-process window", impl: "cgo", want: false},
		{name: "case is irrelevant", impl: "ZiG", want: true},
		{name: "surrounding space is irrelevant", impl: "  go  ", want: false},
		// An unrecognised value takes the default rather than silently disabling the shell: a typo
		// in a hand-edited config should not quietly change the renderer.
		{name: "unknown value takes the default", impl: "wat", want: true},
		{name: "env cgo overrides a zig config", impl: "zig", env: "cgo", want: false},
		{name: "env proc means the GO child, not zig", impl: "zig", env: "proc", want: false},
		{name: "env zig overrides a go config", impl: "go", env: "zig", want: true},
		// No config to consult (early boot, tests): still the default. A default that only applies
		// when a config object happens to exist is not a default.
		{name: "nil config takes the default", cfg: nil, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RAVE_MATE_SHELL", tc.env)
			cfg := tc.cfg
			if cfg == nil && tc.name != "nil config takes the default" {
				cfg = cfgShell(tc.impl)
			}
			if got := zigShellWanted(cfg); got != tc.want {
				t.Fatalf("zigShellWanted = %v, want %v (impl=%q env=%q)", got, tc.want, tc.impl, tc.env)
			}
		})
	}
}

// TestShellKindFollowsTheResolvedZigExe: wanting the Zig child is not having it. Only a RESOLVED
// exe moves the host to the proc shell; otherwise the ladder lands on the in-process window - never
// on the proc shell with the Go child, which is the host where the rAF surfaces stalled.
func TestShellKindFollowsTheResolvedZigExe(t *testing.T) {
	t.Setenv("RAVE_MATE_SHELL", "")
	prev := zigShellExe
	t.Cleanup(func() { zigShellExe = prev })

	zigShellExe = ""
	if got := shellKind(); got != "cgo" {
		t.Fatalf("no resolved zig exe: shellKind = %q, want cgo (the in-process window)", got)
	}
	zigShellExe = `C:\somewhere\rave-shell.exe`
	if got := shellKind(); got != "proc" {
		t.Fatalf("resolved zig exe: shellKind = %q, want proc", got)
	}
}

// TestExplicitProcStillGetsTheGoChild keeps the debugging path alive: RAVE_MATE_SHELL=proc must
// select the proc shell WITHOUT a Zig child, or there is no way left to exercise the Go child.
func TestExplicitProcStillGetsTheGoChild(t *testing.T) {
	t.Setenv("RAVE_MATE_SHELL", "proc")
	prev := zigShellExe
	t.Cleanup(func() { zigShellExe = prev })
	zigShellExe = ""
	if got := shellKind(); got != "proc" {
		t.Fatalf("shellKind = %q, want proc", got)
	}
	if zigShellWanted(cfgShell("")) {
		t.Fatal("RAVE_MATE_SHELL=proc asked for the Go child, but the Zig child was still wanted")
	}
}
