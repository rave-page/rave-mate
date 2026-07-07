package vrchat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// servePipeline runs a local WS endpoint sending the given frames, then idles.
func servePipeline(t *testing.T, wantToken string, frames []string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("authToken"); got != wantToken {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		for _, f := range frames {
			if ws.Write(ctx, websocket.MessageText, []byte(f)) != nil {
				return
			}
		}
		<-ctx.Done()
	}))
	t.Cleanup(srv.Close)
	old := pipelineBase
	pipelineBase = "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Cleanup(func() { pipelineBase = old })
}

func TestPipelineEvents(t *testing.T) {
	servePipeline(t, "tok1", []string{
		`{"type":"friend-online","content":"{\"userId\":\"usr_2\"}"}`,
		`{"type":"notification","content":"plain text"}`,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := DialPipeline(ctx, "tok1")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ev := <-p.Events()
	if ev.Type != "friend-online" || string(ev.Content) != `{"userId":"usr_2"}` {
		t.Fatalf("event 1 = %+v", ev)
	}
	ev = <-p.Events()
	if ev.Type != "notification" || string(ev.Content) != `"plain text"` {
		t.Fatalf("event 2 = %+v", ev)
	}
}

func TestPipelineRejected(t *testing.T) {
	servePipeline(t, "tok1", []string{`{"err":"authToken invalid"}`})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := DialPipeline(ctx, "tok1")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	<-p.Done()
	if p.Err() == nil || !strings.Contains(p.Err().Error(), "rejected") {
		t.Fatalf("want rejection error, got %v", p.Err())
	}
}

func TestPipelineNoToken(t *testing.T) {
	if _, err := DialPipeline(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty token")
	}
}
