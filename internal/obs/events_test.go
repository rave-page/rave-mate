package obs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeOBS runs a minimal obs-websocket v5 server: Hello → Identify → Identified, then
// hands the conn to serve. Returns host, port.
func fakeOBS(t *testing.T, serve func(ctx context.Context, ws *websocket.Conn)) (string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		_ = writeJSON(ctx, ws, envelope{Op: opHello, Data: mustMarshal(helloData{RPCVersion: 1})})
		var id envelope
		if readJSON(ctx, ws, &id) != nil || id.Op != opIdentify {
			_ = ws.CloseNow()
			return
		}
		_ = writeJSON(ctx, ws, envelope{Op: opIdentified, Data: mustMarshal(map[string]int{"negotiatedRpcVersion": 1})})
		serve(ctx, ws)
		_ = ws.CloseNow()
	}))
	t.Cleanup(srv.Close)
	hostPort := strings.TrimPrefix(srv.URL, "http://")
	host, portStr, _ := strings.Cut(hostPort, ":")
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func TestClientEventFanOutAndDone(t *testing.T) {
	evData := mustMarshal(map[string]any{"outputState": "OBS_WEBSOCKET_OUTPUT_STOPPED", "outputPath": "C:/v/a.mkv"})
	// Gate the event send until the test has subscribed - readLoop starts in Connect, so an event
	// sent immediately could be fanned out to zero subscribers (dropped) before SubscribeEvents
	// registers, then the channel closes and <-ev yields a zero-value Event. Deterministic now.
	ready := make(chan struct{})
	host, port := fakeOBS(t, func(ctx context.Context, ws *websocket.Conn) {
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
		_ = writeJSON(ctx, ws, envelope{Op: opEvent, Data: mustMarshal(eventFrame{EventType: "RecordStateChanged", EventData: evData})})
		time.Sleep(100 * time.Millisecond) // let the client consume before close
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Connect(ctx, host, port, "")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	ev, unsub := c.SubscribeEvents()
	defer unsub()
	close(ready) // subscriber registered → let the server send the event
	select {
	case e := <-ev:
		if e.Type != "RecordStateChanged" {
			t.Fatalf("event type = %q", e.Type)
		}
		var d struct {
			OutputPath string `json:"outputPath"`
		}
		if json.Unmarshal(e.Data, &d) != nil || d.OutputPath != "C:/v/a.mkv" {
			t.Fatalf("event data = %s", e.Data)
		}
	case <-ctx.Done():
		t.Fatal("no event received")
	}

	// Server closes → Done fires + subscriber channel closes.
	select {
	case <-c.Done():
	case <-ctx.Done():
		t.Fatal("Done never fired after server close")
	}
	select {
	case _, ok := <-ev:
		if ok {
			return // a buffered event is fine; channel must close eventually
		}
	case <-ctx.Done():
		t.Fatal("event channel never closed")
	}
}

func TestGetRecordStatus(t *testing.T) {
	host, port := fakeOBS(t, func(ctx context.Context, ws *websocket.Conn) {
		var req envelope
		if readJSON(ctx, ws, &req) != nil || req.Op != opRequest {
			return
		}
		var rf requestFrame
		_ = json.Unmarshal(req.Data, &rf)
		resp := responseFrame{
			RequestType: rf.RequestType, RequestID: rf.RequestID,
			RequestStatus: requestStatus{Result: true, Code: 100},
			ResponseData:  mustMarshal(map[string]any{"outputActive": true, "outputDuration": 90000.0, "outputBytes": 777}),
		}
		_ = writeJSON(ctx, ws, envelope{Op: opResponse, Data: mustMarshal(resp)})
		<-ctx.Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Connect(ctx, host, port, "")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	st, err := c.GetRecordStatus(ctx)
	if err != nil {
		t.Fatalf("GetRecordStatus: %v", err)
	}
	if !st.Active || st.Duration != 90*time.Second || st.Bytes != 777 {
		t.Fatalf("status = %+v", st)
	}
}
