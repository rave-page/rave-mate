package remotectl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/twitch"
)

// ── twitch federation (drive + read one paired instance's Twitch session) ──────
//
// Security: the serving box executes every op with ITS OAuth token; the token NEVER
// crosses the link. NO auth verb is registered - StartDevice/PollDevice/Logout stay
// local-only (see the auth-surface test), so a peer can never re-auth, refresh, or
// revoke the serving session. Reads (state, searchCategories) and writes (title,
// preset, chat, moderation) run through the MAC'd pair on the serving box.

// TwitchStateResult is the identity + live stream snapshot a borrower reads to confirm the
// serving peer. It carries NO token. SignedIn=false answers on a peer with no usable session.
type TwitchStateResult struct {
	SignedIn    bool   `json:"signedIn"` // a live, usable session (EventSub connected + identity known)
	Login       string `json:"login,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	UserID      string `json:"userId,omitempty"`
	Live        bool   `json:"live,omitempty"`
	ViewerCount int    `json:"viewerCount,omitempty"`
	GameName    string `json:"gameName,omitempty"`
	Title       string `json:"title,omitempty"`
}

// TwitchSearchParams is a category fuzzy-search request.
type TwitchSearchParams struct {
	Query string `json:"query"`
}

// TwitchSetTitleParams sets the stream title/category directly.
type TwitchSetTitleParams struct {
	Title  string `json:"title"`
	GameID string `json:"gameId,omitempty"`
}

// TwitchSendChatParams sends a chat message (optionally as a reply).
type TwitchSendChatParams struct {
	Text          string `json:"text"`
	ReplyParentID string `json:"replyParentId,omitempty"`
}

// TwitchServer is the narrow proxy view the twitch.* handlers serve from (satisfied by
// *featurehost.TwitchProxy). Deliberately auth-free: no token, no device-flow, no logout.
type TwitchServer interface {
	// LocalConnected reports a live LOCAL session (EventSub up + identity known) - the only
	// state in which this box may serve a borrower.
	LocalConnected() bool
	LocalSelf() twitch.User
	LiveInfo() twitch.ViewerInfo
	SearchCategories(ctx context.Context, q string) ([]twitch.Game, error)
	SetTitle(ctx context.Context, title, gameID string) error
	ApplyTitlePreset(ctx context.Context, preset config.TitlePreset) error
	SendChat(ctx context.Context, text, replyParentID string) error
	Moderate(ctx context.Context, cmd twitch.ModerateCmd) error
}

// RegisterTwitch serves this instance's Twitch session to paired peers. state always answers
// (signedIn=false without a usable session) so a borrower can discover + confirm the serving
// identity; the write verbs execute here with this box's token. No auth verb is exposed.
func RegisterTwitch(e *Endpoint, src TwitchServer) {
	if e == nil || src == nil {
		return
	}
	// gate ensures only a box that actually holds a live session serves a borrower (a box that
	// is itself borrowing has LocalConnected()=false and refuses - no serve loops).
	gate := func() error {
		if !src.LocalConnected() {
			return fmt.Errorf("twitch not connected on this peer")
		}
		return nil
	}
	e.Register(MethodTwitchState, func(context.Context, string, json.RawMessage) (any, error) {
		self := src.LocalSelf()
		res := TwitchStateResult{SignedIn: src.LocalConnected(), Login: self.Login, DisplayName: self.DisplayName, UserID: self.ID}
		if vi := src.LiveInfo(); res.SignedIn {
			res.Live, res.ViewerCount, res.GameName, res.Title = vi.Live, vi.ViewerCount, vi.GameName, vi.Title
		}
		return res, nil
	})
	e.Register(MethodTwitchSearchCats, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		var p TwitchSearchParams
		_ = json.Unmarshal(raw, &p)
		games, err := src.SearchCategories(ctx, strings.TrimSpace(p.Query))
		if err != nil {
			return nil, err
		}
		if games == nil {
			games = []twitch.Game{}
		}
		return games, nil
	})
	e.Register(MethodTwitchSetTitle, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		var p TwitchSetTitleParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if err := src.SetTitle(ctx, p.Title, p.GameID); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
	e.Register(MethodTwitchApplyPreset, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		var p config.TitlePreset
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if err := src.ApplyTitlePreset(ctx, p); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
	e.Register(MethodTwitchSendChat, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		var p TwitchSendChatParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if err := src.SendChat(ctx, p.Text, p.ReplyParentID); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
	e.Register(MethodTwitchModerate, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		var cmd twitch.ModerateCmd
		if err := json.Unmarshal(raw, &cmd); err != nil {
			return nil, err
		}
		if err := src.Moderate(ctx, cmd); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
}

// ── twitch federation client (the borrower's peer-tunneled surface) ────────────

// TwitchState reads the serving peer's identity + live stream snapshot (no token crosses).
func (c *Client) TwitchState(ctx context.Context) (TwitchStateResult, error) {
	return Do[TwitchStateResult](ctx, c.e, c.nodeID, MethodTwitchState, nil)
}

// TwitchSearchCategories runs a category search on the serving peer's session.
func (c *Client) TwitchSearchCategories(ctx context.Context, q string) ([]twitch.Game, error) {
	return Do[[]twitch.Game](ctx, c.e, c.nodeID, MethodTwitchSearchCats, TwitchSearchParams{Query: q})
}

// TwitchSetTitle sets the serving peer's stream title/category.
func (c *Client) TwitchSetTitle(ctx context.Context, title, gameID string) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodTwitchSetTitle, TwitchSetTitleParams{Title: title, GameID: gameID})
	return err
}

// TwitchApplyTitlePreset resolves + applies a title preset on the serving peer (its now-playing
// data feeds the {variables}).
func (c *Client) TwitchApplyTitlePreset(ctx context.Context, preset config.TitlePreset) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodTwitchApplyPreset, preset)
	return err
}

// TwitchSendChat sends a chat message through the serving peer's session.
func (c *Client) TwitchSendChat(ctx context.Context, text, replyParentID string) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodTwitchSendChat, TwitchSendChatParams{Text: text, ReplyParentID: replyParentID})
	return err
}

// TwitchModerate runs a moderation action through the serving peer's session.
func (c *Client) TwitchModerate(ctx context.Context, cmd twitch.ModerateCmd) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodTwitchModerate, cmd)
	return err
}
