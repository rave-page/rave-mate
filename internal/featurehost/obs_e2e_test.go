package featurehost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"rave.page/mate/internal/logbus"
)

// obsEnvelope mirrors the obs-websocket op/d wrapper for the fake server.
type obsEnvelope struct {
	Op   int             `json:"op"`
	Data json.RawMessage `json:"d"`
}

func obsMarshal(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// fakeOBSServer runs a minimal obs-websocket v5 server (no auth): handshake, answers
// GetRecordStatus (inactive), then drives a STARTED→STOPPED record cycle for outputPath.
func fakeOBSServer(t *testing.T, outputPath string) (host string, port int) {
	t.Helper()
	wj := func(ctx context.Context, ws *websocket.Conn, v any) {
		raw, _ := json.Marshal(v)
		_ = ws.Write(ctx, websocket.MessageText, raw)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		ctx := r.Context()
		wj(ctx, ws, obsEnvelope{Op: 0, Data: obsMarshal(map[string]any{"rpcVersion": 1})})
		var id obsEnvelope
		if _, raw, err := ws.Read(ctx); err != nil || json.Unmarshal(raw, &id) != nil || id.Op != 1 {
			return
		}
		wj(ctx, ws, obsEnvelope{Op: 2, Data: obsMarshal(map[string]any{"negotiatedRpcVersion": 1})})

		// Answer the child's initial GetRecordStatus.
		var req obsEnvelope
		if _, raw, err := ws.Read(ctx); err != nil || json.Unmarshal(raw, &req) != nil || req.Op != 6 {
			return
		}
		var rf struct {
			RequestType string `json:"requestType"`
			RequestID   string `json:"requestId"`
		}
		_ = json.Unmarshal(req.Data, &rf)
		wj(ctx, ws, obsEnvelope{Op: 7, Data: obsMarshal(map[string]any{
			"requestType": rf.RequestType, "requestId": rf.RequestID,
			"requestStatus": map[string]any{"result": true, "code": 100},
			"responseData":  map[string]any{"outputActive": false},
		})})

		// Record cycle: STARTED → (brief) → STOPPED with the output path.
		ev := func(state string, path string) {
			d := map[string]any{"outputState": state}
			if path != "" {
				d["outputPath"] = path
			}
			wj(ctx, ws, obsEnvelope{Op: 5, Data: obsMarshal(map[string]any{
				"eventType": "RecordStateChanged", "eventData": d,
			})})
		}
		ev("OBS_WEBSOCKET_OUTPUT_STARTED", "")
		time.Sleep(300 * time.Millisecond)
		ev("OBS_WEBSOCKET_OUTPUT_STOPPED", outputPath)
		<-ctx.Done()
	}))
	t.Cleanup(srv.Close)
	hostPort := strings.TrimPrefix(srv.URL, "http://")
	h, portStr, _ := strings.Cut(hostPort, ":")
	p, _ := strconv.Atoi(portStr)
	return h, p
}

// TestObsFeatureE2E runs the obs feature in a real child process against a fake
// obs-websocket server and asserts the finished recording + status mirror arrive.
func TestObsFeatureE2E(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "set.mkv")
	if err := os.WriteFile(out, []byte("fake-video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	host, port := fakeOBSServer(t, out)

	log := logbus.New(500)
	p, err := NewObsProxy(log, func() ObsConfig {
		return ObsConfig{Host: host, Port: port}
	})
	if err != nil {
		t.Fatal(err)
	}
	p.host.command = func() *exec.Cmd {
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "RAVE_MATE_TEST_FEATURE=obs")
		return cmd
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recCh, unsub := p.SubscribeRecordings()
	defer unsub()
	if err := p.Host().Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.Host().Stop()

	select {
	case r := <-recCh:
		if r.Path != out {
			t.Fatalf("path = %q want %q", r.Path, out)
		}
		if r.Bytes != int64(len("fake-video-bytes")) {
			t.Fatalf("bytes = %d", r.Bytes)
		}
		if r.EndedAt.Before(r.StartedAt) || r.StartedAt.IsZero() {
			t.Fatalf("window wrong: %+v", r)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no finished recording arrived")
	}

	// Status mirror reflects connected (recording already stopped).
	waitFor(t, "connected status", 5*time.Second, func() bool {
		st := p.Status()
		return st.Connected && !st.Recording
	})
}
