package dmx

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/artnet"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
)

func TestShouldRenderDirtyFlag(t *testing.T) {
	now := time.Now()
	if !shouldRender(1, 0, now, now) {
		t.Fatal("generation change must render")
	}
	if shouldRender(1, 1, now, now.Add(-500*time.Millisecond)) {
		t.Fatal("clean + keep-alive not due must skip")
	}
	if !shouldRender(1, 1, now, now.Add(-1100*time.Millisecond)) {
		t.Fatal("keep-alive due must render")
	}
}

// End-to-end over loopback UDP: ArtDmx in → store → PNG grid out + status.
func TestRouterIngestToGrid(t *testing.T) {
	png := filepath.Join(t.TempDir(), "grid.png")
	addr := "127.0.0.1:16454"
	cfg := config.DMXFeature{
		Enabled: true, ListenAddr: addr,
		Grid: config.DMXGrid{Enabled: true, Mode: "mono", FPSCap: 60},
	}
	r := New(logbus.New(64), func() config.DMXFeature { return cfg }, png)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("udp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	data := make([]byte, 512)
	data[0] = 255
	if _, err := conn.Write(artnet.BuildArtDmx(0, 1, 0, data)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		st := r.Status()
		if len(st.Universes) > 0 && st.GridFrames > 0 {
			if _, err := os.Stat(png); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("ingest/grid never converged: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := r.Status()
	if st.Universes[0].Universe != 0 || st.Universes[0].Packets != 1 {
		t.Fatalf("stats=%+v", st.Universes)
	}
	if txt := r.StatusText(); txt == "" {
		t.Fatal("empty status text")
	}
}

func TestRouterBindClashFailsStart(t *testing.T) {
	addr := "127.0.0.1:16455"
	ua, _ := net.ResolveUDPAddr("udp4", addr)
	held, err := net.ListenUDP("udp4", ua)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	cfg := config.DMXFeature{Enabled: true, ListenAddr: addr}
	r := New(logbus.New(16), func() config.DMXFeature { return cfg }, filepath.Join(t.TempDir(), "g.png"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err == nil {
		t.Fatal("port clash did not fail Start")
	}
}
