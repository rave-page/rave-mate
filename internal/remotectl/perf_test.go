package remotectl

import "testing"

// fakePerf is a PerfSource returning a fixed multi-line report.
type fakePerf struct{}

func (fakePerf) Perf() string { return "build dev\nuptime 1s\ncpu% 1.0" }

// TestPerfRPC round-trips the app.perf method: register on the server, fetch via the typed client.
func TestPerfRPC(t *testing.T) {
	server, client := loopback()
	RegisterPerf(server, fakePerf{})
	rc := NewClient(client, "server")

	text, err := rc.Perf(ctx(t))
	if err != nil {
		t.Fatalf("perf: %v", err)
	}
	if text != "build dev\nuptime 1s\ncpu% 1.0" {
		t.Fatalf("text=%q", text)
	}
}

// Nil endpoint/source registration must be a no-op (mirrors the other Register* guards).
func TestRegisterPerfNil(t *testing.T) {
	RegisterPerf(nil, fakePerf{})
	server, client := loopback()
	RegisterPerf(server, nil)
	if _, err := NewClient(client, "server").Perf(ctx(t)); err == nil {
		t.Fatal("unregistered app.perf must error")
	}
}
