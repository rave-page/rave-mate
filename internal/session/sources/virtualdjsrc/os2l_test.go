package virtualdjsrc

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// TestHandleOS2L drives one OS2L connection over net.Pipe: the priming feedback frame must
// precede decode (net.Pipe is synchronous, so the server's Write unblocks only once we read),
// concatenated JSON objects decode in sequence, and only beat frames with bpm>0 become master
// BPM Observations (button + zero-bpm beat ignored).
func TestHandleOS2L(t *testing.T) {
	srv, cli := net.Pipe()
	s := New(logbus.New(16), Config{OS2L: true})

	emitted := make(chan session.Observation, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.handleOS2L(ctx, srv, func(o session.Observation) { emitted <- o })

	// Priming frame MUST arrive before any beat is sent → proves the server speaks first.
	_ = cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	dec := json.NewDecoder(cli)
	var prime os2lMsg
	if err := dec.Decode(&prime); err != nil {
		t.Fatalf("read priming frame: %v", err)
	}
	if prime.Evt != "feedback" {
		t.Fatalf("priming frame evt: got %q want feedback", prime.Evt)
	}

	// Stream CONCATENATED objects: two beats, one non-beat button, one zero-bpm beat.
	blob := `{"evt":"beat","bpm":128.0}` +
		`{"evt":"beat","change":true,"pos":4,"bpm":130.5}` +
		`{"evt":"button","name":"pad1","state":"on"}` +
		`{"evt":"beat","bpm":0}`
	go func() { _, _ = cli.Write([]byte(blob)) }()

	// Only the two positive-bpm beats emit, in order.
	for _, wantBPM := range []float64{128.0, 130.5} {
		select {
		case o := <-emitted:
			if o.Source != session.SourceVDJOS2L {
				t.Errorf("source: got %q want %q", o.Source, session.SourceVDJOS2L)
			}
			if o.Scope.Kind != session.ScopeMaster {
				t.Errorf("scope: got %+v want master", o.Scope)
			}
			if o.Fields[session.FieldBPM] != wantBPM {
				t.Errorf("bpm: got %v want %v", o.Fields[session.FieldBPM], wantBPM)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for beat bpm=%v", wantBPM)
		}
	}
	// No third emit: the button frame + zero-bpm beat are ignored, not errors.
	select {
	case o := <-emitted:
		t.Fatalf("unexpected extra observation (non-beat should be ignored): %+v", o)
	case <-time.After(100 * time.Millisecond):
	}
}
