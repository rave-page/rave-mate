package twitch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
)

// HTTPError is a non-2xx Twitch Helix response. Permanent() marks auth/scope failures (401/403)
// that won't recover without re-authorization - callers should stop retrying rather than spam.
type HTTPError struct {
	Method, Path, Message string
	StatusCode            int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("twitch helix %s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// Permanent reports whether retrying is futile until the user re-authorizes (missing scope /
// revoked/expired token → 401/403).
func (e *HTTPError) Permanent() bool { return e.StatusCode == 401 || e.StatusCode == 403 }

// User is a Twitch user (helix/users). For the signed-in account, ID serves as broadcaster_id,
// moderator_id, sender_id, and the EventSub user_id all at once.
type User struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
}

// Game is a Twitch category (helix/games, helix/search/categories).
type Game struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BoxArtURL string `json:"box_art_url"`
}

// Helix is a thin Twitch Helix REST client: injects Client-Id + Bearer (via Auth) and respects the
// token-bucket Ratelimit-* headers.
type Helix struct {
	auth *Auth
	log  *logbus.Bus
	hc   *http.Client

	mu          sync.Mutex
	rlRemaining int
	rlReset     time.Time
}

// NewHelix builds a Helix client backed by the given auth.
func NewHelix(auth *Auth, log *logbus.Bus) *Helix {
	return &Helix{auth: auth, log: log, hc: &http.Client{Timeout: 20 * time.Second}, rlRemaining: 1}
}

// GetSelf returns the signed-in user (helix/users, no params).
func (h *Helix) GetSelf(ctx context.Context) (User, error) {
	var r struct {
		Data []User `json:"data"`
	}
	if err := h.do(ctx, http.MethodGet, "/users", nil, nil, &r); err != nil {
		return User{}, err
	}
	if len(r.Data) == 0 {
		return User{}, fmt.Errorf("twitch: no user returned")
	}
	return r.Data[0], nil
}

// ModifyChannel sets the stream title and/or category (empty fields are left unchanged). gameID ""
// leaves the category; "0" clears it.
func (h *Helix) ModifyChannel(ctx context.Context, broadcasterID, title, gameID string) error {
	body := map[string]any{}
	if title != "" {
		body["title"] = title
	}
	if gameID != "" {
		body["game_id"] = gameID
	}
	if len(body) == 0 {
		return nil
	}
	q := url.Values{"broadcaster_id": {broadcasterID}}
	return h.do(ctx, http.MethodPatch, "/channels", q, body, nil)
}

// GameByName resolves an exact category name → Game (helix/games?name=).
func (h *Helix) GameByName(ctx context.Context, name string) (Game, bool, error) {
	var r struct {
		Data []Game `json:"data"`
	}
	q := url.Values{"name": {name}}
	if err := h.do(ctx, http.MethodGet, "/games", q, nil, &r); err != nil {
		return Game{}, false, err
	}
	if len(r.Data) == 0 {
		return Game{}, false, nil
	}
	return r.Data[0], true, nil
}

// SearchCategories fuzzy-searches categories (helix/search/categories?query=).
func (h *Helix) SearchCategories(ctx context.Context, query string) ([]Game, error) {
	var r struct {
		Data []Game `json:"data"`
	}
	q := url.Values{"query": {query}}
	err := h.do(ctx, http.MethodGet, "/search/categories", q, nil, &r)
	return r.Data, err
}

// StreamInfo is the live-stream snapshot from helix/streams (empty/Live=false when offline).
type StreamInfo struct {
	Live        bool
	ViewerCount int
	GameName    string
	Title       string
	StartedAt   string
}

// GetStream returns the broadcaster's current stream (helix/streams?user_id=). Live=false when the
// channel is offline (Twitch returns an empty data array). No extra scope required.
func (h *Helix) GetStream(ctx context.Context, userID string) (StreamInfo, error) {
	var r struct {
		Data []struct {
			Type        string `json:"type"`
			ViewerCount int    `json:"viewer_count"`
			GameName    string `json:"game_name"`
			Title       string `json:"title"`
			StartedAt   string `json:"started_at"`
		} `json:"data"`
	}
	q := url.Values{"user_id": {userID}}
	if err := h.do(ctx, http.MethodGet, "/streams", q, nil, &r); err != nil {
		return StreamInfo{}, err
	}
	if len(r.Data) == 0 {
		return StreamInfo{Live: false}, nil
	}
	d := r.Data[0]
	return StreamInfo{Live: d.Type == "live", ViewerCount: d.ViewerCount, GameName: d.GameName, Title: d.Title, StartedAt: d.StartedAt}, nil
}

// Chatters is the current viewer (chatter) list from helix/chat/chatters. Total is the full count;
// Names is capped at the requested page size (first 1000).
type Chatters struct {
	Total int
	Names []string
}

// GetChatters returns the channel's current chatters (helix/chat/chatters?broadcaster_id=&moderator_id=).
// Requires the moderator:read:chatters scope; broadcasterID==modID for one's own channel.
func (h *Helix) GetChatters(ctx context.Context, broadcasterID, modID string) (Chatters, error) {
	var r struct {
		Data []struct {
			UserLogin string `json:"user_login"`
			UserName  string `json:"user_name"`
		} `json:"data"`
		Total int `json:"total"`
	}
	q := url.Values{"broadcaster_id": {broadcasterID}, "moderator_id": {modID}, "first": {"1000"}}
	if err := h.do(ctx, http.MethodGet, "/chat/chatters", q, nil, &r); err != nil {
		return Chatters{}, err
	}
	names := make([]string, 0, len(r.Data))
	for _, d := range r.Data {
		if d.UserName != "" {
			names = append(names, d.UserName)
		} else {
			names = append(names, d.UserLogin)
		}
	}
	return Chatters{Total: r.Total, Names: names}, nil
}

// SendChatMessage sends a chat message (helix/chat/messages). Returns whether Twitch accepted it +
// a drop reason when it didn't (blocked term, etc). replyParentID "" = not a reply.
func (h *Helix) SendChatMessage(ctx context.Context, broadcasterID, senderID, message, replyParentID string) (bool, string, error) {
	body := map[string]any{"broadcaster_id": broadcasterID, "sender_id": senderID, "message": message}
	if replyParentID != "" {
		body["reply_parent_message_id"] = replyParentID
	}
	var r struct {
		Data []struct {
			IsSent     bool `json:"is_sent"`
			DropReason *struct {
				Message string `json:"message"`
			} `json:"drop_reason"`
		} `json:"data"`
	}
	if err := h.do(ctx, http.MethodPost, "/chat/messages", nil, body, &r); err != nil {
		return false, "", err
	}
	if len(r.Data) == 0 {
		return false, "", nil
	}
	d := r.Data[0]
	reason := ""
	if d.DropReason != nil {
		reason = d.DropReason.Message
	}
	return d.IsSent, reason, nil
}

// BanUser bans (durationSec==0) or times out (>0) a user (helix/moderation/bans).
func (h *Helix) BanUser(ctx context.Context, broadcasterID, modID, userID string, durationSec int, reason string) error {
	data := map[string]any{"user_id": userID}
	if durationSec > 0 {
		data["duration"] = durationSec
	}
	if reason != "" {
		data["reason"] = reason
	}
	q := url.Values{"broadcaster_id": {broadcasterID}, "moderator_id": {modID}}
	return h.do(ctx, http.MethodPost, "/moderation/bans", q, map[string]any{"data": data}, nil)
}

// DeleteChatMessage removes one chat message (helix/moderation/chat). messageID "" clears all chat.
func (h *Helix) DeleteChatMessage(ctx context.Context, broadcasterID, modID, messageID string) error {
	q := url.Values{"broadcaster_id": {broadcasterID}, "moderator_id": {modID}}
	if messageID != "" {
		q.Set("message_id", messageID)
	}
	return h.do(ctx, http.MethodDelete, "/moderation/chat", q, nil, nil)
}

// CreateEventSubSub creates an EventSub subscription bound to a WebSocket session
// (helix/eventsub/subscriptions).
func (h *Helix) CreateEventSubSub(ctx context.Context, typ, version string, condition map[string]string, sessionID string) error {
	body := map[string]any{
		"type":      typ,
		"version":   version,
		"condition": condition,
		"transport": map[string]string{"method": "websocket", "session_id": sessionID},
	}
	return h.do(ctx, http.MethodPost, "/eventsub/subscriptions", nil, body, nil)
}

// do issues a Helix request: auth header + Client-Id, rate-limit gate, one retry on 429.
func (h *Helix) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	tok, err := h.auth.Token(ctx)
	if err != nil {
		return err
	}
	var raw []byte
	if body != nil {
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	endpoint := helixBase + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	for range 2 { // one retry after a 429 (gate waits for the bucket to reset)
		h.gate(ctx)
		var rdr io.Reader
		if raw != nil {
			rdr = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Client-Id", h.auth.ClientID())
		if raw != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := h.hc.Do(req)
		if err != nil {
			return err
		}
		h.readRL(resp)
		if resp.StatusCode == http.StatusTooManyRequests {
			_ = resp.Body.Close()
			continue // gate() on the next loop waits until the bucket resets
		}
		if resp.StatusCode >= 400 {
			var e struct {
				Message string `json:"message"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&e)
			_ = resp.Body.Close()
			return &HTTPError{Method: method, Path: path, StatusCode: resp.StatusCode, Message: e.Message}
		}
		defer func() { _ = resp.Body.Close() }()
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	return fmt.Errorf("twitch helix %s %s: rate-limited", method, path)
}

// gate blocks until the token bucket has room (best-effort, from the last Ratelimit-* headers).
func (h *Helix) gate(ctx context.Context) {
	h.mu.Lock()
	wait := time.Duration(0)
	if h.rlRemaining <= 0 && time.Now().Before(h.rlReset) {
		wait = time.Until(h.rlReset)
	}
	h.mu.Unlock()
	if wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// readRL records the token-bucket headers from a response.
func (h *Helix) readRL(resp *http.Response) {
	rem, errR := strconv.Atoi(resp.Header.Get("Ratelimit-Remaining"))
	reset, errT := strconv.ParseInt(resp.Header.Get("Ratelimit-Reset"), 10, 64)
	h.mu.Lock()
	if errR == nil {
		h.rlRemaining = rem
	}
	if errT == nil {
		h.rlReset = time.Unix(reset, 0)
	}
	h.mu.Unlock()
}
