package twitch

import (
	"encoding/json"
	"time"
)

// EventKind enumerates the Twitch events rave-mate surfaces (chat + alerts).
type EventKind string

const (
	KindChat    EventKind = "chat"
	KindFollow  EventKind = "follow"
	KindSub     EventKind = "sub"
	KindGiftSub EventKind = "giftsub"
	KindResub   EventKind = "resub"
	KindCheer   EventKind = "cheer"
)

// Event is a decoded Twitch event in a flat, bus-friendly shape (one type for chat + every alert).
type Event struct {
	Kind        EventKind `json:"kind"`
	UserID      string    `json:"userId,omitempty"`
	UserLogin   string    `json:"userLogin,omitempty"`
	UserName    string    `json:"userName,omitempty"`
	Text        string    `json:"text,omitempty"`      // chat text / resub message
	MessageID   string    `json:"messageId,omitempty"` // chat id (for moderation delete)
	Color       string    `json:"color,omitempty"`
	Mod         bool      `json:"mod,omitempty"`
	Subscriber  bool      `json:"subscriber,omitempty"`
	VIP         bool      `json:"vip,omitempty"`
	Broadcaster bool      `json:"broadcaster,omitempty"`
	Bits        int       `json:"bits,omitempty"`  // cheer amount / bits in a chat line
	Tier        string    `json:"tier,omitempty"`  // sub tier "1000"/"2000"/"3000"
	Gift        bool      `json:"gift,omitempty"`  // sub came via gift
	Anon        bool      `json:"anon,omitempty"`  // anonymous gifter/cheerer
	Total       int       `json:"total,omitempty"` // gifted count / cumulative sub months
	TS          int64     `json:"ts"`
}

// badge is one EventSub chat badge.
type badge struct {
	SetID string `json:"set_id"`
}

func hasBadge(bs []badge, id string) bool {
	for _, b := range bs {
		if b.SetID == id {
			return true
		}
	}
	return false
}

// decodeEvent maps an EventSub notification (subscription type + raw event) to an Event.
func decodeEvent(subType string, raw json.RawMessage) (Event, bool) {
	now := time.Now().UnixMilli()
	switch subType {
	case "channel.chat.message":
		var e struct {
			ChatterID    string `json:"chatter_user_id"`
			ChatterLogin string `json:"chatter_user_login"`
			ChatterName  string `json:"chatter_user_name"`
			Color        string `json:"color"`
			MessageID    string `json:"message_id"`
			Message      struct {
				Text string `json:"text"`
			} `json:"message"`
			Badges []badge `json:"badges"`
			Cheer  *struct {
				Bits int `json:"bits"`
			} `json:"cheer"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return Event{}, false
		}
		ev := Event{
			Kind: KindChat, UserID: e.ChatterID, UserLogin: e.ChatterLogin, UserName: e.ChatterName,
			Text: e.Message.Text, MessageID: e.MessageID, Color: e.Color,
			Mod:         hasBadge(e.Badges, "moderator"),
			Subscriber:  hasBadge(e.Badges, "subscriber"),
			VIP:         hasBadge(e.Badges, "vip"),
			Broadcaster: hasBadge(e.Badges, "broadcaster"),
			TS:          now,
		}
		if e.Cheer != nil {
			ev.Bits = e.Cheer.Bits
		}
		return ev, true

	case "channel.follow":
		var e struct {
			UserID    string `json:"user_id"`
			UserLogin string `json:"user_login"`
			UserName  string `json:"user_name"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return Event{}, false
		}
		return Event{Kind: KindFollow, UserID: e.UserID, UserLogin: e.UserLogin, UserName: e.UserName, TS: now}, true

	case "channel.subscribe":
		var e struct {
			UserID    string `json:"user_id"`
			UserLogin string `json:"user_login"`
			UserName  string `json:"user_name"`
			Tier      string `json:"tier"`
			IsGift    bool   `json:"is_gift"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return Event{}, false
		}
		return Event{Kind: KindSub, UserID: e.UserID, UserLogin: e.UserLogin, UserName: e.UserName, Tier: e.Tier, Gift: e.IsGift, TS: now}, true

	case "channel.subscription.gift":
		var e struct {
			UserID      string `json:"user_id"`
			UserLogin   string `json:"user_login"`
			UserName    string `json:"user_name"`
			Tier        string `json:"tier"`
			Total       int    `json:"total"`
			IsAnonymous bool   `json:"is_anonymous"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return Event{}, false
		}
		return Event{Kind: KindGiftSub, UserID: e.UserID, UserLogin: e.UserLogin, UserName: e.UserName, Tier: e.Tier, Total: e.Total, Gift: true, Anon: e.IsAnonymous, TS: now}, true

	case "channel.subscription.message":
		var e struct {
			UserID           string `json:"user_id"`
			UserLogin        string `json:"user_login"`
			UserName         string `json:"user_name"`
			Tier             string `json:"tier"`
			CumulativeMonths int    `json:"cumulative_months"`
			Message          struct {
				Text string `json:"text"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return Event{}, false
		}
		return Event{Kind: KindResub, UserID: e.UserID, UserLogin: e.UserLogin, UserName: e.UserName, Tier: e.Tier, Total: e.CumulativeMonths, Text: e.Message.Text, TS: now}, true

	case "channel.cheer":
		var e struct {
			UserID      string `json:"user_id"`
			UserLogin   string `json:"user_login"`
			UserName    string `json:"user_name"`
			Bits        int    `json:"bits"`
			Message     string `json:"message"`
			IsAnonymous bool   `json:"is_anonymous"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return Event{}, false
		}
		return Event{Kind: KindCheer, UserID: e.UserID, UserLogin: e.UserLogin, UserName: e.UserName, Bits: e.Bits, Text: e.Message, Anon: e.IsAnonymous, TS: now}, true
	}
	return Event{}, false
}
