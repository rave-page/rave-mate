package featurehost

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/twitch"
)

// twitchPeerCmdTimeout bounds a forwarded peer send/moderate call into the child.
const twitchPeerCmdTimeout = 15 * time.Second

// TwitchProxy is the daemon-side stand-in for the subprocessed Twitch manager. It mirrors
// sign-in/connection state, republishes child chat/alert/stats events onto the eventbus
// (webui, VR overlays + peer mesh unchanged), advertises the twitch capability while the
// child owns a session, forwards peer-directed send/moderate commands down, appends every
// bus chat/alert (local AND peer-origin) to the persistent chat log, and proxies auth +
// helix ops. Ops route to the owning peer when this instance isn't connected - parity with
// the old in-proc Manager.
type TwitchProxy struct {
	host    *Host
	log     *logbus.Bus
	bus     *eventbus.Bus
	chatlog *twitch.ChatLog

	mu      sync.Mutex
	st      twitchState
	onEvent func(twitch.Event) // optional direct hook (Fyne no-bus fallback)
}

// NewTwitchProxy builds the proxy + its host. clientID is re-read per (re)spawn so a
// config edit takes effect on module restart. bus and chatlog may be nil.
func NewTwitchProxy(log *logbus.Bus, bus *eventbus.Bus, chatlog *twitch.ChatLog, clientID func() string) (*TwitchProxy, error) {
	p := &TwitchProxy{log: log, bus: bus, chatlog: chatlog}
	h, err := New(Options{
		Name: "twitch",
		Log:  log,
		Init: func() any { return twitchInit{ClientID: clientID()} },
		OnEvent: map[string]func(json.RawMessage){
			"ev":       p.onEv,
			"viewers":  func(data json.RawMessage) { p.publish(twitch.TopicViewers, data) },
			"chatters": func(data json.RawMessage) { p.publish(twitch.TopicChatters, data) },
			"state":    p.onState,
		},
		OnDown: func() {
			p.mu.Lock()
			p.st = twitchState{}
			p.mu.Unlock()
			if p.bus != nil {
				p.bus.RemoveCap(twitch.CapTwitch)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	p.host = h
	if bus != nil {
		// Peer-directed commands: serve them here (forwarded into the child) while we own
		// the session. Off the bus goroutine - the Call blocks on the child.
		bus.Subscribe(twitch.TopicSendChat, func(e eventbus.Event) { p.onPeerCmd("chat.send", e.Data) })
		bus.Subscribe(twitch.TopicModerate, func(e eventbus.Event) { p.onPeerCmd("chat.moderate", e.Data) })
		// Persist chat + alerts from the bus, not the child pipe: captures this instance's
		// events AND a paired peer's (bus fanout includes local publishes). Low-throughput
		// single-writer file append - fine on the subscriber goroutine.
		if chatlog != nil {
			logEv := func(e eventbus.Event) {
				var ev twitch.Event
				if json.Unmarshal(e.Data, &ev) == nil {
					chatlog.Append(ev)
				}
			}
			bus.Subscribe(twitch.TopicChat, logEv)
			bus.Subscribe(twitch.TopicEvent, logEv)
		}
	}
	return p, nil
}

// Host exposes the supervising host (module Start/Stop, SetNotifier, Stats).
func (p *TwitchProxy) Host() *Host { return p.host }

// publish forwards a child event payload onto the bus verbatim.
func (p *TwitchProxy) publish(topic string, data json.RawMessage) {
	if p.bus != nil {
		p.bus.Publish(topic, data)
	}
}

// onEv republishes one decoded child event (chat or alert) onto the bus + the direct hook.
func (p *TwitchProxy) onEv(data json.RawMessage) {
	var ev twitch.Event
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	p.mu.Lock()
	hook := p.onEvent
	p.mu.Unlock()
	if hook != nil {
		hook(ev)
	}
	if p.bus == nil {
		// No bus = no subscriber-side persistence; append directly so history still works.
		if p.chatlog != nil {
			p.chatlog.Append(ev)
		}
		return
	}
	if ev.Kind == twitch.KindChat {
		p.bus.Publish(twitch.TopicChat, data)
	} else {
		p.bus.Publish(twitch.TopicEvent, data)
	}
}

// onState updates the mirror + syncs the twitch capability advertisement.
func (p *TwitchProxy) onState(data json.RawMessage) {
	var st twitchState
	if json.Unmarshal(data, &st) != nil {
		return
	}
	p.mu.Lock()
	p.st = st
	p.mu.Unlock()
	if p.bus != nil {
		if st.Connected && st.Self.ID != "" {
			p.bus.AddCap(twitch.CapTwitch)
		} else {
			p.bus.RemoveCap(twitch.CapTwitch)
		}
	}
}

// onPeerCmd forwards a peer's directed command into the child (only while we own a session).
func (p *TwitchProxy) onPeerCmd(method string, data json.RawMessage) {
	if !p.connected() {
		return
	}
	raw := append(json.RawMessage(nil), data...)
	debuglog.Go(p.log, "feature:twitch", func() {
		ctx, cancel := context.WithTimeout(context.Background(), twitchPeerCmdTimeout)
		defer cancel()
		if _, err := p.host.call(ctx, method, raw); err != nil {
			p.log.Warn("twitch", "peer "+method+" failed", map[string]any{"error": err.Error()})
		}
	})
}

// connected reports whether the child owns a live EventSub session.
func (p *TwitchProxy) connected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.Connected && p.st.Self.ID != ""
}

// SignedIn mirrors the child's sealed-token state (false while the child is down).
func (p *TwitchProxy) SignedIn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.SignedIn
}

// Self returns the signed-in user (zero until the child's EventSub connects).
func (p *TwitchProxy) Self() twitch.User {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.Self
}

// Kick wakes the child's supervise loop (call after config changes; sign-in kicks itself).
func (p *TwitchProxy) Kick() { _ = p.host.Send("kick", nil) }

// SetOnEvent registers a direct chat/alert hook (Fyne fallback when there's no bus).
func (p *TwitchProxy) SetOnEvent(fn func(twitch.Event)) {
	p.mu.Lock()
	p.onEvent = fn
	p.mu.Unlock()
}

// Auth exposes the device-flow surface (proxied into the child, which owns the token).
func (p *TwitchProxy) Auth() *TwitchAuthProxy { return &TwitchAuthProxy{p: p} }

// SendChat sends a chat message: through the child if it owns the session, else routed to
// the Twitch-owning peer.
func (p *TwitchProxy) SendChat(ctx context.Context, text, replyParentID string) error {
	if p.connected() {
		_, err := p.host.Call(ctx, "chat.send", twitchSendReq{Text: text, ReplyParentID: replyParentID})
		return err
	}
	return p.routeCmd(twitch.TopicSendChat, twitch.SendCmd{Text: text, ReplyParentID: replyParentID})
}

// Moderate runs a moderation action: locally via the child, else routed to the owning peer.
func (p *TwitchProxy) Moderate(ctx context.Context, cmd twitch.ModerateCmd) error {
	if p.connected() {
		_, err := p.host.Call(ctx, "chat.moderate", cmd)
		return err
	}
	return p.routeCmd(twitch.TopicModerate, cmd)
}

// routeCmd sends a directed command to the peer(s) owning the twitch capability.
func (p *TwitchProxy) routeCmd(topic string, payload any) error {
	if p.bus == nil {
		return fmt.Errorf("twitch: not connected and no peers")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if n := p.bus.SendToCapability(twitch.CapTwitch, topic, raw); n == 0 {
		return fmt.Errorf("twitch: no connected instance owns a Twitch session")
	}
	return nil
}

// ApplyTitlePreset resolves a preset's {variables} → sets the stream title (+ category).
func (p *TwitchProxy) ApplyTitlePreset(ctx context.Context, preset config.TitlePreset) error {
	_, err := p.host.Call(ctx, "title.apply", preset)
	return err
}

// SetTitle sets the stream title/category directly (child must be connected).
func (p *TwitchProxy) SetTitle(ctx context.Context, title, gameID string) error {
	_, err := p.host.Call(ctx, "title.set", twitchTitleReq{Title: title, GameID: gameID})
	return err
}

// SearchCategories proxies a category fuzzy-search (for the preset editor).
func (p *TwitchProxy) SearchCategories(ctx context.Context, q string) ([]twitch.Game, error) {
	raw, err := p.host.Call(ctx, "categories.search", twitchSearchReq{Query: q})
	if err != nil {
		return nil, err
	}
	var games []twitch.Game
	if err := json.Unmarshal(raw, &games); err != nil {
		return nil, err
	}
	return games, nil
}

// TwitchAuthProxy proxies the Device Code Flow into the child (same surface the in-proc
// *twitch.Auth offered the UI).
type TwitchAuthProxy struct{ p *TwitchProxy }

// StartDevice requests a device code + user code from Twitch.
func (a *TwitchAuthProxy) StartDevice(ctx context.Context) (twitch.DeviceAuth, error) {
	raw, err := a.p.host.Call(ctx, "auth.start", nil)
	if err != nil {
		return twitch.DeviceAuth{}, err
	}
	var da twitch.DeviceAuth
	if err := json.Unmarshal(raw, &da); err != nil {
		return twitch.DeviceAuth{}, err
	}
	return da, nil
}

// PollDevice polls until the user approves (child persists the sealed token + connects).
func (a *TwitchAuthProxy) PollDevice(ctx context.Context, da twitch.DeviceAuth) error {
	_, err := a.p.host.Call(ctx, "auth.poll", da)
	return err
}

// Logout clears the child's in-memory + on-disk token (mirror updates via "state").
func (a *TwitchAuthProxy) Logout() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.p.host.Call(ctx, "auth.logout", nil); err != nil {
		a.p.log.Warn("twitch", "logout failed", map[string]any{"error": err.Error()})
	}
}
