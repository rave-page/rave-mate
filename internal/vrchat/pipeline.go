package vrchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/coder/websocket"
)

// pipelineBase is the VRChat realtime event socket (var for tests).
var pipelineBase = "wss://pipeline.vrchat.cloud"

const maxPipeFrame = 1 << 20

// PipelineEvent is one realtime event (friend-online, friend-location,
// notification, user-update, …). Content is the decoded inner JSON.
type PipelineEvent struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
}

// Pipeline is a receive-only connection to wss://pipeline.vrchat.cloud.
type Pipeline struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	events chan PipelineEvent
	err    error // set before cancel; read after Done()
}

// DialPipeline connects with the session auth cookie. The token rides the query
// string per VRChat's protocol - never log the URL.
func DialPipeline(ctx context.Context, authToken string) (*Pipeline, error) {
	if authToken == "" {
		return nil, errors.New("vrchat: pipeline needs an auth token")
	}
	u := pipelineBase + "/?authToken=" + url.QueryEscape(authToken)
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPHeader: http.Header{"User-Agent": []string{userAgent}},
	})
	if err != nil {
		return nil, fmt.Errorf("vrchat: pipeline dial: %w", err)
	}
	ws.SetReadLimit(maxPipeFrame)
	pCtx, cancel := context.WithCancel(ctx)
	p := &Pipeline{ws: ws, ctx: pCtx, cancel: cancel, events: make(chan PipelineEvent, 64)}
	go p.readLoop()
	return p, nil
}

// Events streams decoded pipeline events. Closed when the connection dies.
func (p *Pipeline) Events() <-chan PipelineEvent { return p.events }

// Done fires when the connection is gone; Err then reports why.
func (p *Pipeline) Done() <-chan struct{} { return p.ctx.Done() }

// Err returns the terminal error (nil on clean Close). Valid after Done().
func (p *Pipeline) Err() error { return p.err }

// Close tears the connection down.
func (p *Pipeline) Close() {
	p.cancel()
	_ = p.ws.Close(websocket.StatusNormalClosure, "")
}

// readLoop decodes wire frames {"type","content"} (content = JSON-in-a-string)
// until error. An {"err":...} frame means the token was rejected.
func (p *Pipeline) readLoop() {
	defer func() {
		close(p.events)
		p.cancel()
	}()
	for {
		typ, raw, err := p.ws.Read(p.ctx)
		if err != nil {
			if p.ctx.Err() == nil {
				p.err = err
			}
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var fr struct {
			Type    string `json:"type"`
			Content string `json:"content"`
			Err     string `json:"err"`
		}
		if json.Unmarshal(raw, &fr) != nil {
			continue
		}
		if fr.Err != "" {
			p.err = fmt.Errorf("vrchat: pipeline rejected: %s", fr.Err)
			_ = p.ws.Close(websocket.StatusNormalClosure, "")
			return
		}
		if fr.Type == "" {
			continue
		}
		content := json.RawMessage(fr.Content)
		if !json.Valid(content) { // some events carry plain strings
			content, _ = json.Marshal(fr.Content)
		}
		select {
		case p.events <- PipelineEvent{Type: fr.Type, Content: content}:
		case <-p.ctx.Done():
			return
		}
	}
}
