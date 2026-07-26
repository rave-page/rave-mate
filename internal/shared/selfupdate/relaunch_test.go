package selfupdate

import (
	"os"
	"testing"
	"time"
)

// TakeRelaunchCooldown parses + clears the one-shot env, rejects junk, and caps the wait.
func TestTakeRelaunchCooldown(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", 0},
		{"10", 10 * time.Second},
		{"junk", 0},
		{"-5", 0},
		{"0", 0},
		{"9999", relaunchCooldownMax}, // bogus value must not become a sleep bomb
	}
	for _, c := range cases {
		t.Setenv(RelaunchCooldownEnv, c.env)
		if got := TakeRelaunchCooldown(); got != c.want {
			t.Errorf("env %q: got %v, want %v", c.env, got, c.want)
		}
		if v := os.Getenv(RelaunchCooldownEnv); v != "" {
			t.Errorf("env %q: not cleared (still %q)", c.env, v)
		}
	}
}
