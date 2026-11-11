package gpubench

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/gpubudget"
)

func allowGate() encoderscan.BenchGate {
	return encoderscan.BenchGate{HeadroomPresent: true, FreeMB: 8000, ReserveMB: 768, BenchCostMB: 400, MachineIdle: true}
}

func waitBusyClear(r *Runner) {
	for i := 0; i < 200; i++ {
		r.mu.Lock()
		b := r.busy
		r.mu.Unlock()
		if !b {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWantTickRunsAndPersists(t *testing.T) {
	b := gpubudget.New(gpubudget.Options{Store: gpubudget.NewCostStore("", nil),
		Adapter: func() gpubudget.Adapter { return gpubudget.Adapter{Vendor: "nvidia", Driver: "x"} }})
	var calls int32
	r := New(Options{
		Budget: b, Gate: allowGate,
		Encode: func(_ context.Context, tg Target) (float64, error) {
			atomic.AddInt32(&calls, 1)
			return float64(tg.W) * float64(tg.H) * float64(tg.FPS), nil
		},
	})
	r.WantEncode("gpu0", "h264", "h264", 3840, 2160, 60)
	if !r.Tick(context.Background()) {
		t.Fatal("Tick should kick a wanted benchmark")
	}
	waitBusyClear(r)
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("timer calls = %d, want 1", calls)
	}
	if v, ok := b.Throughput(gpubudget.OpEncode, gpubudget.Geom{W: 3840, H: 2160, FPS: 60, Codec: "h264"}); !ok || v <= 0 {
		t.Fatalf("throughput not persisted: %v ok=%v", v, ok)
	}
	// Already measured => Want is a no-op, Tick has nothing to do.
	r.WantEncode("gpu0", "h264", "h264", 3840, 2160, 60)
	if r.Tick(context.Background()) {
		t.Fatal("Tick ran an already-measured bucket")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("re-measured (calls=%d)", calls)
	}
}

func TestTickSkipsWhenRouteLive(t *testing.T) {
	b := gpubudget.New(gpubudget.Options{Store: gpubudget.NewCostStore("", nil),
		Adapter: func() gpubudget.Adapter { return gpubudget.Adapter{} }})
	var calls int32
	r := New(Options{
		Budget: b, Gate: allowGate,
		Encode:    func(context.Context, Target) (float64, error) { atomic.AddInt32(&calls, 1); return 1e9, nil },
		RouteLive: func(string) bool { return true },
	})
	r.WantEncode("gpu0", "h264", "h264", 1920, 1080, 60)
	if r.Tick(context.Background()) {
		t.Fatal("Tick must not run while a route is live on the adapter")
	}
	waitBusyClear(r)
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("benchmarked while a route was live (calls=%d)", calls)
	}
}

func TestRunOneGateDenied(t *testing.T) {
	b := gpubudget.New(gpubudget.Options{Store: gpubudget.NewCostStore("", nil),
		Adapter: func() gpubudget.Adapter { return gpubudget.Adapter{} }})
	r := New(Options{
		Budget: b,
		Gate: func() encoderscan.BenchGate {
			return encoderscan.BenchGate{HeadroomPresent: true, FreeMB: 8000, MachineIdle: true, ProtectedBusy: true}
		},
		Encode: func(context.Context, Target) (float64, error) { return 1e9, nil },
	})
	if _, err := r.runOne(context.Background(), Target{Op: gpubudget.OpEncode, W: 1920, H: 1080, FPS: 60, Codec: "h264"}); err == nil {
		t.Fatal("gate must deny when a protected encoder is live")
	}
	if _, ok := b.Throughput(gpubudget.OpEncode, gpubudget.Geom{W: 1920, H: 1080, FPS: 60, Codec: "h264"}); ok {
		t.Fatal("a denied benchmark must not persist")
	}
}

func TestRunOneTimeoutKills(t *testing.T) {
	b := gpubudget.New(gpubudget.Options{Store: gpubudget.NewCostStore("", nil),
		Adapter: func() gpubudget.Adapter { return gpubudget.Adapter{} }})
	r := New(Options{
		Budget:  b,
		Gate:    allowGate,
		Timeout: 30 * time.Millisecond,
		Encode: func(ctx context.Context, _ Target) (float64, error) {
			<-ctx.Done() // a hung session: the Timer must observe the deadline
			return 0, ctx.Err()
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), r.o.Timeout)
	defer cancel()
	if _, err := r.runOne(ctx, Target{Op: gpubudget.OpEncode, W: 3840, H: 2160, FPS: 60, Codec: "h264"}); err == nil {
		t.Fatal("a hung timer must surface the deadline as an error")
	}
}

func TestDecodeTimerUnsupported(t *testing.T) {
	b := gpubudget.New(gpubudget.Options{Store: gpubudget.NewCostStore("", nil),
		Adapter: func() gpubudget.Adapter { return gpubudget.Adapter{} }})
	r := New(Options{Budget: b, Gate: allowGate}) // no Decode timer
	if _, err := r.runOne(context.Background(), Target{Op: gpubudget.OpDecode, W: 1920, H: 1080, FPS: 60, Codec: "h264"}); err == nil {
		t.Fatal("decode with no timer must error")
	}
}

func TestRunAllTable(t *testing.T) {
	b := gpubudget.New(gpubudget.Options{Store: gpubudget.NewCostStore("", nil),
		Adapter: func() gpubudget.Adapter { return gpubudget.Adapter{Vendor: "v", Driver: "d"} }})
	r := New(Options{
		Budget: b, Gate: allowGate,
		Encode: func(_ context.Context, tg Target) (float64, error) { return float64(tg.W*tg.H) * 60, nil },
	})
	out := r.RunAll(context.Background(), EncodeTargets("gpu0", "h264", "h264", 60))
	if out == "" || !contains(out, "Mpps") {
		t.Fatalf("table missing results:\n%s", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
