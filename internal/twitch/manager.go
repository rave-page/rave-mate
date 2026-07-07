package twitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
)

// Bus topics + the Twitch capability name (advertised when this instance is the connected owner).
const (
	TopicChat     = "twitch.chat"     // a chat message (broadcast)
	TopicEvent    = "twitch.event"    // follow/sub/cheer alert (broadcast)
	TopicSendChat = "twitch.sendChat" // directed: a peer asks the owner to send a chat message
	TopicModerate = "twitch.moderate" // directed: a peer asks the owner to moderate
	TopicViewers  = "twitch.viewers"  // viewer count + live state (broadcast, polled)
	TopicChatters = "twitch.chatters" // current chatter list (broadcast, polled)
	CapTwitch     = "twitch"
)

// ViewerInfo is the TopicViewers payload: live state + viewer count + what's playing.
type ViewerInfo struct {
	Live        bool   `json:"live"`
	ViewerCount int    `json:"viewerCount"`
	GameName    string `json:"gameName,omitempty"`
	Title       string `json:"title,omitempty"`
}

// ChatterInfo is the TopicChatters payload: total chatter count + the (capped) name list.
type ChatterInfo struct {
	Total int      `json:"total"`
	Names []string `json:"names,omitempty"`
}

// SendCmd is the TopicSendChat payload (directed to the Twitch-owning instance).
type SendCmd struct {
	Text          string `json:"text"`
	ReplyParentID string `json:"replyParentId,omitempty"`
}

// ModerateCmd is the TopicModerate payload. Action: "ban" | "timeout" | "delete".
type ModerateCmd struct {
	Action    string `json:"action"`
	UserID    string `json:"userId,omitempty"`
	Duration  int    `json:"duration,omitempty"` // timeout seconds
	Reason    string `json:"reason,omitempty"`
	MessageID string `json:"messageId,omitempty"`
}

// Manager owns the Twitch lifecycle: auth + helix + (when signed in) an EventSub socket. It
// publishes decoded chat/alerts onto the eventbus and serves directed commands from peers that
// lack a local Twitch connection. High-level ops (apply title preset, send chat, moderate) route
// locally when signed in, else over the bus to the owning peer.
type Manager struct {
	log *logbus.Bus
	bus *eventbus.Bus
	cfg func() config.TwitchFeature

	auth  *Auth
	helix *Helix

	mu         sync.Mutex
	self       User
	onEvent    func(Event)       // UI hook (chat/event view); additive to bus publish
	onViewers  func(ViewerInfo)  // UI hook for polled viewer count/state
	onChatters func(ChatterInfo) // UI hook for the polled chatter list
	kick       chan struct{}
}

// New builds the Twitch manager. bus may be nil (then cross-PC routing + publish are no-ops).
func New(log *logbus.Bus, bus *eventbus.Bus, cfg func() config.TwitchFeature) *Manager {
	clientID := cfg().ResolvedClientID()
	auth := NewAuth(clientID, log)
	auth.Load()
	return &Manager{
		log: log, bus: bus, cfg: cfg,
		auth:  auth,
		helix: NewHelix(auth, log),
		kick:  make(chan struct{}, 1),
	}
}

// Auth exposes the auth client (UI drives the Device Code Flow through it).
func (m *Manager) Auth() *Auth { return m.auth }

// SignedIn reports whether a local Twitch token is held.
func (m *Manager) SignedIn() bool { return m.auth.SignedIn() }

// Self returns the signed-in user (zero until connected).
func (m *Manager) Self() User { m.mu.Lock(); defer m.mu.Unlock(); return m.self }

// SetOnEvent registers a UI hook for every decoded chat/alert (additive to bus publish).
func (m *Manager) SetOnEvent(fn func(Event)) { m.mu.Lock(); m.onEvent = fn; m.mu.Unlock() }

// SetOnStats registers UI hooks for polled viewer count + chatter list (additive to bus publish).
func (m *Manager) SetOnStats(viewers func(ViewerInfo), chatters func(ChatterInfo)) {
	m.mu.Lock()
	m.onViewers, m.onChatters = viewers, chatters
	m.mu.Unlock()
}

// pollStats polls viewer count + chatters while connected and publishes them onto the bus + UI hooks.
func (m *Manager) pollStats(ctx context.Context, broadcasterID string) {
	m.pollViewers(ctx, broadcasterID)
	ct := time.NewTicker(30 * time.Second)
	if !m.pollChatters(ctx, broadcasterID) {
		ct.Stop() // permanent (missing-scope) failure on the first poll - never spam it
	}
	vt := time.NewTicker(15 * time.Second)
	defer vt.Stop()
	defer ct.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-vt.C:
			m.pollViewers(ctx, broadcasterID)
		case <-ct.C:
			if !m.pollChatters(ctx, broadcasterID) {
				ct.Stop() // stop retrying a permanent auth/scope failure (re-auth needed)
			}
		}
	}
}

func (m *Manager) pollViewers(ctx context.Context, broadcasterID string) {
	s, err := m.helix.GetStream(ctx, broadcasterID)
	if err != nil {
		m.log.Debug(source, "viewer poll failed", map[string]any{"error": err.Error()})
		return
	}
	info := ViewerInfo{Live: s.Live, ViewerCount: s.ViewerCount, GameName: s.GameName, Title: s.Title}
	m.mu.Lock()
	cb := m.onViewers
	m.mu.Unlock()
	if cb != nil {
		cb(info)
	}
	if m.bus != nil {
		if raw, err := json.Marshal(info); err == nil {
			m.bus.Publish(TopicViewers, raw)
		}
	}
}

// pollChatters polls the chatter list. Returns false on a PERMANENT failure (missing scope /
// revoked token) so the caller stops polling - otherwise a token lacking moderator:read:chatters
// 401-spams every tick. Transient errors (network / 429 / 5xx) return true to keep retrying.
func (m *Manager) pollChatters(ctx context.Context, broadcasterID string) (keep bool) {
	c, err := m.helix.GetChatters(ctx, broadcasterID, broadcasterID)
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Permanent() {
			m.log.Warn(source, "chatter list disabled - Twitch token is missing the moderator:read:chatters scope; sign out and back in to Twitch to grant it",
				map[string]any{"error": err.Error()})
			return false
		}
		m.log.Debug(source, "chatter poll failed", map[string]any{"error": err.Error()})
		return true
	}
	info := ChatterInfo(c)
	m.mu.Lock()
	cb := m.onChatters
	m.mu.Unlock()
	if cb != nil {
		cb(info)
	}
	if m.bus != nil {
		if raw, err := json.Marshal(info); err == nil {
			m.bus.Publish(TopicChatters, raw)
		}
	}
	return true
}

// Kick wakes the supervise loop (call after a successful sign-in to connect immediately).
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Start runs the supervise loop until ctx is cancelled: connect EventSub while signed in, serve
// peer commands, advertise the twitch capability. Implements module.Service.Start.
func (m *Manager) Start(ctx context.Context) error {
	if m.bus != nil {
		m.bus.Subscribe(TopicSendChat, func(e eventbus.Event) { m.onSendCmd(ctx, e) })
		m.bus.Subscribe(TopicModerate, func(e eventbus.Event) { m.onModerateCmd(ctx, e) })
	}
	for ctx.Err() == nil {
		if !m.auth.SignedIn() {
			select {
			case <-ctx.Done():
				return nil
			case <-m.kick:
			case <-time.After(5 * time.Second):
			}
			continue
		}
		self, err := m.helix.GetSelf(ctx)
		if err != nil {
			m.log.Warn(source, "get self failed", map[string]any{"error": err.Error()})
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(10 * time.Second):
			}
			continue
		}
		m.mu.Lock()
		m.self = self
		m.mu.Unlock()
		if m.bus != nil {
			m.bus.AddCap(CapTwitch)
		}
		m.log.Info(source, "connected", map[string]any{"login": self.Login})
		// Poll viewer count + chatters while connected, publishing onto the bus (so a VR/overlay
		// instance can render them). Cancelled when EventSub returns (disconnect / ctx cancel).
		sctx, scancel := context.WithCancel(ctx)
		go m.pollStats(sctx, self.ID)
		es := NewEventSub(m.helix, self.ID, m.handleEvent, m.log)
		_ = es.Run(ctx) // blocks until ctx cancel or fatal
		scancel()
		if m.bus != nil {
			m.bus.RemoveCap(CapTwitch)
		}
	}
	return nil
}

// handleEvent publishes a decoded event to the bus + the UI hook.
func (m *Manager) handleEvent(ev Event) {
	m.mu.Lock()
	cb := m.onEvent
	m.mu.Unlock()
	if cb != nil {
		cb(ev)
	}
	if m.bus == nil {
		return
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if ev.Kind == KindChat {
		m.bus.Publish(TopicChat, raw)
	} else {
		m.bus.Publish(TopicEvent, raw)
	}
}

// ── high-level ops (local when signed in, else routed to the owning peer) ──────────

// ApplyTitlePreset resolves a preset's {variables} → sets the stream title (+ optional category).
func (m *Manager) ApplyTitlePreset(ctx context.Context, p config.TitlePreset) error {
	title := ResolveTemplate(p.Template, p.Vars)
	gameID := ""
	if p.GameName != "" {
		if g, ok, err := m.helix.GameByName(ctx, p.GameName); err == nil && ok {
			gameID = g.ID
		}
	}
	return m.SetTitle(ctx, title, gameID)
}

// SetTitle sets the stream title/category directly (must be signed in locally).
func (m *Manager) SetTitle(ctx context.Context, title, gameID string) error {
	self := m.Self()
	if self.ID == "" {
		return fmt.Errorf("twitch: not connected")
	}
	return m.helix.ModifyChannel(ctx, self.ID, title, gameID)
}

// SearchCategories proxies a category fuzzy-search (for the preset editor).
func (m *Manager) SearchCategories(ctx context.Context, q string) ([]Game, error) {
	return m.helix.SearchCategories(ctx, q)
}

// SendChat sends a chat message: locally if signed in, else routed to the Twitch-owning peer.
func (m *Manager) SendChat(ctx context.Context, text, replyParentID string) error {
	if self := m.Self(); self.ID != "" {
		sent, reason, err := m.helix.SendChatMessage(ctx, self.ID, self.ID, text, replyParentID)
		if err != nil {
			return err
		}
		if !sent {
			return fmt.Errorf("twitch dropped message: %s", reason)
		}
		return nil
	}
	return m.routeCmd(TopicSendChat, SendCmd{Text: text, ReplyParentID: replyParentID})
}

// Moderate runs a moderation action: locally if signed in, else routed to the owning peer.
func (m *Manager) Moderate(ctx context.Context, cmd ModerateCmd) error {
	if self := m.Self(); self.ID != "" {
		return m.doModerate(ctx, self.ID, cmd)
	}
	return m.routeCmd(TopicModerate, cmd)
}

// routeCmd sends a directed command to the peer(s) owning the twitch capability.
func (m *Manager) routeCmd(topic string, payload any) error {
	if m.bus == nil {
		return fmt.Errorf("twitch: not connected and no peers")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if n := m.bus.SendToCapability(CapTwitch, topic, raw); n == 0 {
		return fmt.Errorf("twitch: no connected instance owns a Twitch session")
	}
	return nil
}

func (m *Manager) doModerate(ctx context.Context, selfID string, cmd ModerateCmd) error {
	switch cmd.Action {
	case "ban":
		return m.helix.BanUser(ctx, selfID, selfID, cmd.UserID, 0, cmd.Reason)
	case "timeout":
		return m.helix.BanUser(ctx, selfID, selfID, cmd.UserID, cmd.Duration, cmd.Reason)
	case "delete":
		return m.helix.DeleteChatMessage(ctx, selfID, selfID, cmd.MessageID)
	}
	return fmt.Errorf("twitch: unknown moderation action %q", cmd.Action)
}

// onSendCmd executes a peer's send-chat command (only when we own the session).
func (m *Manager) onSendCmd(ctx context.Context, e eventbus.Event) {
	self := m.Self()
	if self.ID == "" {
		return
	}
	var cmd SendCmd
	if json.Unmarshal(e.Data, &cmd) != nil || strings.TrimSpace(cmd.Text) == "" {
		return
	}
	if _, _, err := m.helix.SendChatMessage(ctx, self.ID, self.ID, cmd.Text, cmd.ReplyParentID); err != nil {
		m.log.Warn(source, "peer send-chat failed", map[string]any{"error": err.Error()})
	}
}

// onModerateCmd executes a peer's moderation command (only when we own the session).
func (m *Manager) onModerateCmd(ctx context.Context, e eventbus.Event) {
	self := m.Self()
	if self.ID == "" {
		return
	}
	var cmd ModerateCmd
	if json.Unmarshal(e.Data, &cmd) != nil {
		return
	}
	if err := m.doModerate(ctx, self.ID, cmd); err != nil {
		m.log.Warn(source, "peer moderate failed", map[string]any{"error": err.Error()})
	}
}

// ResolveTemplate substitutes {key} placeholders in a title template from vars (unknown keys left
// as-is so the user sees what's missing).
func ResolveTemplate(tmpl string, vars map[string]string) string {
	if len(vars) == 0 {
		return tmpl
	}
	out := tmpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

// TemplateVars extracts the {key} placeholder names from a template, in order, de-duplicated.
func TemplateVars(tmpl string) []string {
	var out []string
	seen := map[string]bool{}
	for {
		i := strings.IndexByte(tmpl, '{')
		if i < 0 {
			break
		}
		j := strings.IndexByte(tmpl[i:], '}')
		if j < 0 {
			break
		}
		key := tmpl[i+1 : i+j]
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
		tmpl = tmpl[i+j+1:]
	}
	return out
}
