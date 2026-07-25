package featurehost

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/twitch"
)

func init() { Register("twitch", func() Feature { return &twitchFeature{} }) }

// twitchInit seeds the child with the configured client id. The OAuth token itself is NOT
// passed - the child loads + refreshes + persists the sealed twitch.bin itself (refresh
// tokens rotate; the file must stay single-writer, and helix lives here).
type twitchInit struct {
	ClientID string `json:"clientId"` // "" = bundled default (resolved in the child)
}

// twitchState mirrors sign-in + EventSub connectivity to the daemon ("state" events).
type twitchState struct {
	SignedIn  bool        `json:"signedIn"`
	Connected bool        `json:"connected"`
	Self      twitch.User `json:"self"`
}

// Wire shapes shared with twitchproxy.go; keep both sides in sync.
type twitchSendReq struct {
	Text          string `json:"text"`
	ReplyParentID string `json:"replyParentId,omitempty"`
}

type twitchTitleReq struct {
	Title  string `json:"title"`
	GameID string `json:"gameId,omitempty"`
}

type twitchSearchReq struct {
	Query string `json:"query"`
}

// twitchFeature hosts the Twitch Manager in the child: auth (device flow + sealed token),
// EventSub, viewer/chatter polling, helix ops. Decoded events + stats + state stream up as
// "ev"/"viewers"/"chatters"/"state"; the daemon proxy owns bus publish + peer routing.
type twitchFeature struct {
	rt  *Runtime
	mgr *twitch.Manager

	mu sync.Mutex
	st twitchState
}

func (f *twitchFeature) Init(params json.RawMessage, rt *Runtime) error {
	var cfg twitchInit
	if err := json.Unmarshal(params, &cfg); err != nil {
		return err
	}
	f.rt = rt
	// bus nil: peer routing + publish happen in the daemon proxy, not here.
	f.mgr = twitch.New(rt.Log, nil, func() config.TwitchFeature {
		return config.TwitchFeature{Enabled: true, ClientID: cfg.ClientID}
	})
	f.mgr.SetOnEvent(func(ev twitch.Event) { rt.Emit("ev", ev) })
	f.mgr.SetOnStats(
		func(vi twitch.ViewerInfo) { rt.Emit("viewers", vi) },
		func(ci twitch.ChatterInfo) { rt.Emit("chatters", ci) },
	)
	f.mgr.SetOnConnect(func(self twitch.User, connected bool) {
		f.setState(func(s *twitchState) { s.Connected, s.Self = connected, self })
	})
	return nil
}

// Start emits the initial sign-in state then runs the Manager supervise loop (connects
// EventSub whenever signed in - always listening, no UI involvement).
func (f *twitchFeature) Start(ctx context.Context) error {
	f.setState(func(s *twitchState) { s.SignedIn = f.mgr.SignedIn() })
	return f.mgr.Start(ctx)
}

// HandleEvent consumes parent→child "kick" (wake the supervise loop now).
func (f *twitchFeature) HandleEvent(event string, _ json.RawMessage) {
	if event == "kick" {
		f.mgr.Kick()
	}
}

// setState mutates + emits the mirrored state (always emitted - transitions are rare).
func (f *twitchFeature) setState(mut func(*twitchState)) {
	f.mu.Lock()
	mut(&f.st)
	st := f.st
	f.mu.Unlock()
	f.rt.Emit("state", st)
}

// Handle serves the daemon proxy's auth + helix RPCs.
func (f *twitchFeature) Handle(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "auth.start":
		da, err := f.mgr.Auth().StartDevice(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(da)
	case "auth.poll":
		var da twitch.DeviceAuth
		if err := json.Unmarshal(params, &da); err != nil {
			return nil, err
		}
		if err := f.mgr.Auth().PollDevice(ctx, da); err != nil {
			return nil, err
		}
		f.setState(func(s *twitchState) { s.SignedIn = true })
		f.mgr.Kick() // connect EventSub now
		return nil, nil
	case "auth.logout":
		f.mgr.Auth().Logout()
		f.setState(func(s *twitchState) { *s = twitchState{} })
		return nil, nil
	case "chat.send":
		var req twitchSendReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if strings.TrimSpace(req.Text) == "" {
			return nil, nil
		}
		return nil, f.mgr.SendChat(ctx, req.Text, req.ReplyParentID)
	case "chat.moderate":
		var cmd twitch.ModerateCmd
		if err := json.Unmarshal(params, &cmd); err != nil {
			return nil, err
		}
		return nil, f.mgr.Moderate(ctx, cmd)
	case "title.apply":
		var p config.TitlePreset
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, f.mgr.ApplyTitlePreset(ctx, p)
	case "title.set":
		var req twitchTitleReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, f.mgr.SetTitle(ctx, req.Title, req.GameID)
	case "categories.search":
		var req twitchSearchReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		games, err := f.mgr.SearchCategories(ctx, req.Query)
		if err != nil {
			return nil, err
		}
		return json.Marshal(games)
	default:
		return nil, errUnknownMethod(method)
	}
}
