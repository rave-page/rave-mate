package crewlink

// service_test.go - module lifecycle: the Start-generation guard against the
// module.Manager.Restart race. Restart cancels the old ctx and synchronously Starts the next
// generation WITHOUT waiting for goroutines, so the old generation's teardown watcher can fire
// AFTER the new Start installed its sink/fields - it must no-op, never kill the new uplink.

import (
	"context"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mocapnode"
)

// fakeRouter records the installed sink (the mocap.Service seam).
type fakeRouter struct {
	mu   sync.Mutex
	sink func(mocapnode.Packet)
}

func (f *fakeRouter) SetSink(fn func(mocapnode.Packet)) {
	f.mu.Lock()
	f.sink = fn
	f.mu.Unlock()
}

func (f *fakeRouter) Inject(mocapnode.Packet) bool { return true }

func (f *fakeRouter) current() func(mocapnode.Packet) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sink
}

func TestModuleRestartRaceKeepsNewGeneration(t *testing.T) {
	router := &fakeRouter{}
	role := "node"
	svc := New(logbus.New(64), func() config.CrewFeature {
		return config.CrewFeature{Enabled: true, EventID: "ev1", Role: role}
	}, "http://unused.invalid", staticTokens("tok"), router)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	if err := svc.Start(ctx1); err != nil {
		t.Fatalf("start gen1: %v", err)
	}
	if router.current() == nil {
		t.Fatal("gen1 sink not installed")
	}
	svc.mu.Lock()
	n1 := svc.node
	svc.mu.Unlock()

	// Reproduce the race deterministically: start gen2 while gen1's ctx is still live (its
	// teardown watcher has definitely not run), then fire the stale teardown by hand.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	if err := svc.Start(ctx2); err != nil {
		t.Fatalf("start gen2: %v", err)
	}
	svc.mu.Lock()
	n2, gen2 := svc.node, svc.gen
	svc.mu.Unlock()
	if n2 == nil || n2 == n1 {
		t.Fatalf("gen2 node not installed (n1=%p n2=%p)", n1, n2)
	}

	svc.teardown(gen2 - 1) // the delayed gen1 watcher firing AFTER gen2's Start

	if st := svc.Status(); !st.Running {
		t.Fatal("stale teardown killed the new generation's status")
	}
	sink := router.current()
	if sink == nil {
		t.Fatal("stale teardown cleared the new generation's sink")
	}
	// The surviving sink is gen2's: packets land in gen2's uplink queue, not gen1's.
	sink(goldenPacket(0))
	if got := len(n2.queue); got != 1 {
		t.Fatalf("gen2 queue = %d packets, want 1", got)
	}
	if got := len(n1.queue); got != 0 {
		t.Fatalf("gen1 queue = %d packets, want 0", got)
	}

	// The REAL gen1 watcher (ctx cancel) must no-op the same way.
	cancel1()
	time.Sleep(50 * time.Millisecond) // give a buggy watcher the chance to misfire
	if st := svc.Status(); !st.Running || router.current() == nil {
		t.Fatal("gen1 ctx-cancel watcher tore down the new generation")
	}

	// Role flip on restart: master owns direct routing - Start(master) clears the node sink
	// even though the superseded node generation never got to clear it.
	role = "master"
	ctx3, cancel3 := context.WithCancel(context.Background())
	defer cancel3()
	if err := svc.Start(ctx3); err != nil {
		t.Fatalf("start gen3 (master): %v", err)
	}
	if router.current() != nil {
		t.Fatal("master-role start must clear the node sink")
	}

	// A genuine (latest-gen) stop still restores direct routing + clears the fields.
	role = "node"
	ctx4, cancel4 := context.WithCancel(context.Background())
	if err := svc.Start(ctx4); err != nil {
		t.Fatalf("start gen4: %v", err)
	}
	if router.current() == nil {
		t.Fatal("gen4 sink not installed")
	}
	cancel4()
	waitFor(t, "gen4 teardown", 5*time.Second, func() bool {
		return !svc.Status().Running && router.current() == nil
	})
}
