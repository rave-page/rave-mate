//go:build manual

// Manual (audio-device) integration test for the subprocessed player: spawns the real `player`
// feature child, plays a generated WAV, and asserts the play/tick/seek/stop IPC round-trip.
// Needs a working audio output (oto) - run locally: `go test -tags manual -run Player ./internal/featurehost/`.
package featurehost

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/audioengine"
	"rave.page/mate/internal/logbus"
)

// writeSineWAV writes a 16-bit PCM mono WAV of the given seconds (a valid file beep can decode).
func writeSineWAV(t *testing.T, path string, seconds float64) {
	t.Helper()
	const rate = 8000
	n := int(seconds * rate)
	data := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(data[i*2:], uint16(v))
	}
	var buf []byte
	put := func(s string) { buf = append(buf, s...) }
	put32 := func(v uint32) { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); buf = append(buf, b...) }
	put16 := func(v uint16) { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); buf = append(buf, b...) }
	put("RIFF")
	put32(uint32(36 + len(data)))
	put("WAVE")
	put("fmt ")
	put32(16)
	put16(1)        // PCM
	put16(1)        // mono
	put32(rate)     // sample rate
	put32(rate * 2) // byte rate
	put16(2)        // block align
	put16(16)       // bits/sample
	put("data")
	put32(uint32(len(data)))
	buf = append(buf, data...)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlayerChildPlaySeekStop(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "tone.wav")
	writeSineWAV(t, wav, 3)

	var ticks atomic.Int64
	var lastCur atomic.Uint64 // float bits
	log := logbus.New(64)
	h, err := New(Options{
		Name: "player",
		Log:  log,
		Init: func() any { return struct{}{} },
		OnEvent: map[string]func(json.RawMessage){
			"tick": func(data json.RawMessage) {
				var tk playerTick
				if json.Unmarshal(data, &tk) == nil {
					ticks.Add(1)
					lastCur.Store(math.Float64bits(tk.Cur))
				}
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.command = func() *exec.Cmd {
		exe, _ := os.Executable()
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "RAVE_MATE_TEST_FEATURE=player")
		return cmd
	}

	ctx := context.Background()
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "child ready", 5*time.Second, h.Running)

	raw, err := h.Call(ctx, "play", map[string]string{"path": wav})
	if err != nil {
		t.Fatalf("play: %v", err)
	}
	var st audioengine.State
	_ = json.Unmarshal(raw, &st)
	if !st.Playing {
		t.Fatalf("expected playing, got %+v", st)
	}

	// Position must advance (the tick stream proves the IPC + engine work).
	waitFor(t, "ticks advancing", 3*time.Second, func() bool { return ticks.Load() >= 3 })
	cur := math.Float64frombits(lastCur.Load())
	if cur <= 0 {
		t.Fatalf("position not advancing: cur=%v", cur)
	}

	// Seek forward (fire-and-forget) - the next ticks should reflect the jump.
	_ = h.Send("seek", map[string]float64{"sec": 2.0})
	time.Sleep(500 * time.Millisecond)
	if c := math.Float64frombits(lastCur.Load()); c < 1.5 {
		t.Fatalf("seek to 2.0 not reflected: cur=%v", c)
	}

	if _, err := h.Call(ctx, "stop", nil); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
