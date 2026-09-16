package featurehost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/twitch"
)

// TwitchProxy is the daemon-side stand-in for the subprocessed Twitch manager. It mirrors
// sign-in/connection state, republishes child chat/alert/stats events onto the eventbus
// (webui, VR overlays + peer mesh unchanged), advertises the twitch capability while the
// child owns a session, caches the last viewer snapshot, appends every bus chat/alert
// (local AND peer-origin) to the persistent chat log, and proxies auth + helix ops.
//
// Twitch federation: with no LOCAL session, a paired instance that holds one serves every
// op (chat/moderation/title reads+writes) as if signed in locally. The federated route is a
// remotectl twitch.* call to the serving peer (request/response, so errors surface) - the
// serving box executes with ITS token; the token never crosses the link. A local sign-in
// always wins (fedClient returns nil while a local session is live); the watcher disarms
// federation on local login or serving-peer loss (internal/app/twitchfederation.go).
type TwitchProxy struct {
	host    *Host
	log     *logbus.Bus
	bus     *eventbus.Bus
	chatlog *twitch.ChatLog
	rate    twitch.ChatRate // bounded 60 s chat-message window (Live strip "N msg/min"); zero value ready
	seen    twitch.IDWindow // chat MessageIDs already handled: a paired instance copy of the same line is dropped

	mu          sync.Mutex
	st          twitchState
	lastViewers twitch.ViewerInfo  // last stats snapshot (served over twitch.state)
	fedCli      *remotectl.Client  // armed serving peer (nil = no federation)
	fedName     string             // serving peer's display name ("via <name>")
	fedSelf     twitch.User        // serving peer's Twitch identity
	onEvent     func(twitch.Event) // optional direct hook (Fyne no-bus fallback)
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
			"viewers":  p.onViewers,
			"chatters": func(data json.RawMessage) { p.publish(twitch.TopicChatters, data) },
			"state":    p.onState,
		},
		OnDown: func() {
			p.mu.Lock()
			p.st = twitchState{}
			p.lastViewers = twitch.ViewerInfo{}
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
		// Persist chat + alerts from the bus, not the child pipe: captures this instance's
		// events AND a paired peer's (bus fanout includes local publishes). Low-throughput
		// single-writer file append - fine on the subscriber goroutine. The same tap feeds the
		// bounded chat-rate window, so the Live strip's "N msg/min" is right for local AND
		// federated chat.
		logEv := func(e eventbus.Event) {
			var ev twitch.Event
			if json.Unmarshal(e.Data, &ev) != nil {
				return
			}
			if ev.Kind == twitch.KindChat && p.seen.Seen(ev.MessageID) {
				return // the same message via a paired instance session - already counted + logged
			}
			if ev.Kind == twitch.KindChat {
				p.rate.Add(time.Now())
			}
			if chatlog != nil {
				chatlog.Append(ev)
			}
		}
		bus.Subscribe(twitch.TopicChat, logEv)
		bus.Subscribe(twitch.TopicEvent, logEv)
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
		// No bus = no subscriber-side persistence; count + append directly so history still works.
		if ev.Kind == twitch.KindChat && p.seen.Seen(ev.MessageID) {
			return
		}
		if ev.Kind == twitch.KindChat {
			p.rate.Add(time.Now())
		}
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

// onViewers caches the latest stream stats (served over twitch.state to federation borrowers)
// and republishes them onto the bus (the tab's viewer chip + VR overlay).
func (p *TwitchProxy) onViewers(data json.RawMessage) {
	var vi twitch.ViewerInfo
	if json.Unmarshal(data, &vi) == nil {
		p.mu.Lock()
		p.lastViewers = vi
		p.mu.Unlock()
	}
	p.publish(twitch.TopicViewers, data)
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

// connected reports whether the child owns a live EventSub session.
func (p *TwitchProxy) connected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.Connected && p.st.Self.ID != ""
}

// LocalConnected reports a live LOCAL session (EventSub up + identity known) - the state in
// which this box serves federation borrowers (remotectl twitch.* handlers gate on it).
func (p *TwitchProxy) LocalConnected() bool { return p.connected() }

// SignedIn reports a usable Twitch session from ANY consumer's view: local sealed token OR an
// armed federation (so the tab + Settings light up like a local sign-in).
func (p *TwitchProxy) SignedIn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.SignedIn || p.fedCli != nil
}

// LocalSignedIn mirrors ONLY the child's sealed-token state (auth flows + the federation
// watcher; a local session always wins over federation).
func (p *TwitchProxy) LocalSignedIn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.SignedIn
}

// Federated reports whether an armed federation is serving this box (no local session).
func (p *TwitchProxy) Federated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.st.SignedIn && p.fedCli != nil
}

// Via names the serving peer while federated ("" = local session or none).
func (p *TwitchProxy) Via() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.st.SignedIn && p.fedCli != nil {
		return p.fedName
	}
	return ""
}

// Self returns the signed-in user. Without a local session an armed federation answers with
// the serving peer's identity (so every consumer shows who is streaming).
func (p *TwitchProxy) Self() twitch.User {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.st.SignedIn && p.fedCli != nil {
		return p.fedSelf
	}
	return p.st.Self
}

// LocalSelf returns ONLY the local child's signed-in user (served over twitch.state).
func (p *TwitchProxy) LocalSelf() twitch.User {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st.Self
}

// LiveInfo returns the last cached stream stats (served over twitch.state).
func (p *TwitchProxy) LiveInfo() twitch.ViewerInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastViewers
}

// ChatRate reports chat messages seen in the last 60 s (local session or a federated peer's -
// both flow through the bus tap). Alerts are not counted.
func (p *TwitchProxy) ChatRate() int { return p.rate.PerMinute(time.Now()) }

// SetFederated arms federation: cli tunnels ops to the serving peer, self is that peer's Twitch
// identity, name is its "via" label. A local session always overrides (fedClient returns nil).
func (p *TwitchProxy) SetFederated(cli *remotectl.Client, peerName string, self twitch.User) {
	p.mu.Lock()
	p.fedCli, p.fedName, p.fedSelf = cli, strings.TrimSpace(peerName), self
	p.mu.Unlock()
}

// ClearFederated drops the federation (serving peer gone/unlinked, or local login won).
func (p *TwitchProxy) ClearFederated() {
	p.mu.Lock()
	p.fedCli, p.fedName, p.fedSelf = nil, "", twitch.User{}
	p.mu.Unlock()
}

// fedClient returns the armed serving-peer client, or nil when a local session is live (local
// always wins) or no federation is armed.
func (p *TwitchProxy) fedClient() *remotectl.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st.SignedIn {
		return nil
	}
	return p.fedCli
}

// Kick wakes the child's supervise loop (call after config changes; sign-in kicks itself).
func (p *TwitchProxy) Kick() { _ = p.host.Send("kick", nil) }

// SetOnEvent registers a direct chat/alert hook (Fyne fallback when there's no bus).
func (p *TwitchProxy) SetOnEvent(fn func(twitch.Event)) {
	p.mu.Lock()
	p.onEvent = fn
	p.mu.Unlock()
}

// Auth exposes the device-flow surface (proxied into the child, which owns the token). Auth
// flows are LOCAL-ONLY and NEVER federated - a borrower can never re-auth the serving session.
func (p *TwitchProxy) Auth() *TwitchAuthProxy { return &TwitchAuthProxy{p: p} }

// SendChat sends a chat message: through the child if it owns the session, else through the
// armed federation peer (request/response - the error surfaces).
func (p *TwitchProxy) SendChat(ctx context.Context, text, replyParentID string) error {
	if p.connected() {
		_, err := p.host.Call(ctx, "chat.send", twitchSendReq{Text: text, ReplyParentID: replyParentID})
		return err
	}
	if cli := p.fedClient(); cli != nil {
		return cli.TwitchSendChat(ctx, text, replyParentID)
	}
	return fmt.Errorf("twitch: no local session and no serving peer")
}

// Moderate runs a moderation action: locally via the child, else through the federation peer.
func (p *TwitchProxy) Moderate(ctx context.Context, cmd twitch.ModerateCmd) error {
	if p.connected() {
		_, err := p.host.Call(ctx, "chat.moderate", cmd)
		return err
	}
	if cli := p.fedClient(); cli != nil {
		return cli.TwitchModerate(ctx, cmd)
	}
	return fmt.Errorf("twitch: no local session and no serving peer")
}

// ApplyTitlePreset resolves a preset's {variables} → sets the stream title (+ category), locally
// or through the federation peer (its now-playing feeds the variables).
func (p *TwitchProxy) ApplyTitlePreset(ctx context.Context, preset config.TitlePreset) error {
	if !p.connected() {
		if cli := p.fedClient(); cli != nil {
			return cli.TwitchApplyTitlePreset(ctx, preset)
		}
	}
	_, err := p.host.Call(ctx, "title.apply", preset)
	return err
}

// SetTitle sets the stream title/category directly, locally or through the federation peer.
func (p *TwitchProxy) SetTitle(ctx context.Context, title, gameID string) error {
	if !p.connected() {
		if cli := p.fedClient(); cli != nil {
			return cli.TwitchSetTitle(ctx, title, gameID)
		}
	}
	_, err := p.host.Call(ctx, "title.set", twitchTitleReq{Title: title, GameID: gameID})
	return err
}

// SearchCategories proxies a category fuzzy-search (for the preset editor), locally or through
// the federation peer.
func (p *TwitchProxy) SearchCategories(ctx context.Context, q string) ([]twitch.Game, error) {
	if !p.connected() {
		if cli := p.fedClient(); cli != nil {
			return cli.TwitchSearchCategories(ctx, q)
		}
	}
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
// *twitch.Auth offered the UI). LOCAL-ONLY: never federated - the serving side exposes no
// auth verb, so a borrower cannot re-auth, refresh, or revoke the serving session.
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
