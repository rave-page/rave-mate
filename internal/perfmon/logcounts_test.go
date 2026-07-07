package perfmon

import (
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

func TestLogCounts(t *testing.T) {
	bus := logbus.New(64)
	bus.Info("api", "fine", nil)
	bus.Warn("vroverlay", "w1", nil)
	bus.Warn("vroverlay", "w2", nil)
	bus.Warn("api", "w3", nil)
	bus.Error("obs", "e1", nil)
	out := LogCounts(bus, 10*time.Minute)
	for _, want := range []string{"WARN 3", "vroverlay 2", "api 1", "ERROR 1", "obs 1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if LogCounts(nil, time.Minute) != "(no log bus)" {
		t.Fatal("nil bus")
	}
}
