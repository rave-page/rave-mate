package featurehost

import (
	"context"
	"encoding/json"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/stream"
)

func init() { Register("stream", func() Feature { return &streamFeature{} }) }

// streamInit is the init wire config for the stream feature.
type streamInit struct {
	APIBaseURL string `json:"apiBaseURL"`
}

// streamFeature hosts the live-stream publisher in the child: API calls + batching run
// out-of-process. The daemon feeds merged session updates as "update" events; handles
// "start"/"end" controls; emits "status" mirrors. The publish token exists only in the
// child's memory (passed once in the start params).
type streamFeature struct {
	rt   *Runtime
	pub  *stream.Publisher
	feed chan session.Update
}

func (f *streamFeature) Init(params json.RawMessage, rt *Runtime) error {
	var p streamInit
	if err := json.Unmarshal(params, &p); err != nil {
		return err
	}
	f.rt = rt
	f.feed = make(chan session.Update, 256)
	apiC := api.New(p.APIBaseURL, rt.Log)
	f.pub = stream.New(rt.Log, apiC, f.subscribeFeed)
	return nil
}

// subscribeFeed adapts the IPC update feed to the publisher's subscribe contract.
// Drains stale buffered updates from a previous stream; unsubscribe is a no-op (the
// publisher's run loop stops reading on its ctx).
func (f *streamFeature) subscribeFeed() (<-chan session.Update, func()) {
	for {
		select {
		case <-f.feed:
			continue
		default:
		}
		break
	}
	return f.feed, func() {}
}

func (f *streamFeature) Start(ctx context.Context) error {
	// Status mirror → daemon proxy (drives the dashboard + autoRecord).
	ch, unsub := f.pub.SubscribeStatus()
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			// Wind down: end a live stream so the server isn't left to the reaper.
			if st := f.pub.Status(); st.IsLive {
				_, _ = f.pub.End(context.Background())
			}
			return nil
		case st := <-ch:
			f.rt.Emit("status", st)
		}
	}
}

// HandleEvent consumes the daemon's merged-update feed (fire-and-forget frames).
func (f *streamFeature) HandleEvent(event string, data json.RawMessage) {
	if event != "update" {
		return
	}
	var u session.Update
	if json.Unmarshal(data, &u) != nil {
		return
	}
	select {
	case f.feed <- u:
	default: // drop on overflow - parity with the in-proc subscriber buffer
	}
}

func (f *streamFeature) Handle(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "start":
		var args stream.StartArgs
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, err
		}
		st, err := f.pub.Start(ctx, args)
		if err != nil {
			return nil, err
		}
		return json.Marshal(st)
	case "end":
		st, err := f.pub.End(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(st)
	case "status":
		return json.Marshal(f.pub.Status())
	}
	return nil, errUnknownMethod(method)
}
