package perfmon

import (
	"testing"
	"time"
)

func TestSysProbeTick(t *testing.T) {
	var p sysProbe
	if _, _, _, ok := p.tick(); ok {
		t.Fatal("first tick must warm up (ok=false)")
	}
	time.Sleep(50 * time.Millisecond)
	cpu, used, total, ok := p.tick()
	if !ok {
		t.Fatal("second tick not ok")
	}
	if total <= 0 || used <= 0 || used > total {
		t.Fatalf("mem used=%v total=%v", used, total)
	}
	if cpu < 0 || cpu > 101 {
		t.Fatalf("cpu%%=%v out of range", cpu)
	}
}
