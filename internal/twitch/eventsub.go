package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/coder/websocket"

	"rave.page/mate/internal/logbus"
)

const eventSubWSURL = "wss://eventsub.wss.twitch.tv/ws"

// subSpec is one EventSub subscription to create after the welcome (condition uses the self id).
type subSpec struct {
	typ     string
	version string
	chat    bool // condition needs user_id too
	mod     bool // condition needs moderator_user_id too
}

var subSpecs = []subSpec{
	{typ: "channel.chat.message", version: "1", chat: true},
	{typ: "channel.follow", version: "2", mod: true},
	{typ: "channel.subscribe", version: "1"},
	{typ: "channel.subscription.gift", version: "1"},
	{typ: "channel.subscription.message", version: "1"},
	{typ: "channel.cheer", version: "1"},
}

// EventSub owns the EventSub WebSocket: welcome → subscribe → keepalive watchdog → notification
// dispatch → reconnect. Run blocks until ctx is cancelled.
type EventSub struct {
	helix   *Helix
	log     *logbus.Bus
	selfID  string
	onEvent func(Event)
}

// NewEventSub builds the client. selfID is the signed-in user id (broadcaster == moderator == user);
// onEvent receives every decoded chat/alert.
func NewEventSub(helix *Helix, selfID string, onEvent func(Event), log *logbus.Bus) *EventSub {
	return &EventSub{helix: helix, log: log, selfID: selfID, onEvent: onEvent}
}

// Run connects + maintains the socket until ctx is cancelled, reconnecting with backoff.
func (e *EventSub) Run(ctx context.Context) error {
	wsURL := eventSubWSURL
	subscribe := true
	backoff := time.Second
	for ctx.Err() == nil {
		next, resub, err := e.session(ctx, wsURL, subscribe)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			e.log.Warn(source, "eventsub reconnecting", map[string]any{"error": err.Error(), "in": backoff.String()})
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			wsURL, subscribe = eventSubWSURL, true // fresh session → resubscribe
			continue
		}
		backoff = time.Second
		wsURL, subscribe = next, resub // server-directed reconnect carries subs (resub=false)
	}
	return ctx.Err()
}

// session runs one socket lifetime. Returns the next URL to connect to (a server reconnect url) and
// whether to resubscribe on it; or an error to trigger a fresh reconnect.
func (e *EventSub) session(ctx context.Context, wsURL string, subscribe bool) (string, bool, error) {
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ws, _, err := websocket.Dial(dctx, wsURL, nil)
	cancel()
	if err != nil {
		return "", true, fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(1 << 20) // 1 MiB

	keepalive := 30 * time.Second
	for {
		readCtx, rc := context.WithTimeout(ctx, keepalive+10*time.Second)
		_, raw, err := ws.Read(readCtx)
		rc()
		if err != nil {
			if ctx.Err() != nil {
				return "", true, nil
			}
			return "", true, fmt.Errorf("read: %w", err)
		}
		var msg struct {
			Metadata struct {
				MessageType      string `json:"message_type"`
				SubscriptionType string `json:"subscription_type"`
			} `json:"metadata"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		switch msg.Metadata.MessageType {
		case "session_welcome":
			var p struct {
				Session struct {
					ID                     string `json:"id"`
					KeepaliveTimeoutSecond int    `json:"keepalive_timeout_seconds"`
				} `json:"session"`
			}
			if json.Unmarshal(msg.Payload, &p) == nil && p.Session.KeepaliveTimeoutSecond > 0 {
				keepalive = time.Duration(p.Session.KeepaliveTimeoutSecond) * time.Second
			}
			if subscribe {
				if err := e.subscribeAll(ctx, p.Session.ID); err != nil {
					return "", true, fmt.Errorf("subscribe: %w", err)
				}
				e.log.Info(source, "eventsub connected", map[string]any{"subs": len(subSpecs)})
			}
		case "session_keepalive":
			// no-op; the read deadline reset above is the watchdog
		case "notification":
			var p struct {
				Event json.RawMessage `json:"event"`
			}
			if json.Unmarshal(msg.Payload, &p) != nil {
				continue
			}
			if ev, ok := decodeEvent(msg.Metadata.SubscriptionType, p.Event); ok && e.onEvent != nil {
				e.onEvent(ev)
			}
		case "session_reconnect":
			var p struct {
				Session struct {
					ReconnectURL string `json:"reconnect_url"`
				} `json:"session"`
			}
			if json.Unmarshal(msg.Payload, &p) == nil && p.Session.ReconnectURL != "" {
				return p.Session.ReconnectURL, false, nil // reconnect carries subs
			}
		case "revocation":
			e.log.Warn(source, "eventsub subscription revoked", map[string]any{"type": msg.Metadata.SubscriptionType})
		}
	}
}

// subscribeAll creates every EventSub subscription bound to the welcome session id.
func (e *EventSub) subscribeAll(ctx context.Context, sessionID string) error {
	for _, s := range subSpecs {
		cond := map[string]string{"broadcaster_user_id": e.selfID}
		if s.chat {
			cond["user_id"] = e.selfID
		}
		if s.mod {
			cond["moderator_user_id"] = e.selfID
		}
		if err := e.helix.CreateEventSubSub(ctx, s.typ, s.version, cond, sessionID); err != nil {
			return fmt.Errorf("%s: %w", s.typ, err)
		}
	}
	return nil
}
