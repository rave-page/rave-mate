package webui

import (
	"encoding/json"
	"testing"
	"time"

	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/ui"
)

// The owner saw every chat line twice: two paired instances both signed in to the channel each
// publish the line on the mesh bus under their own origin, and the bus cannot equate two origins.
// The feed must show one row per Twitch MessageID whichever origin delivers it.
func TestTwitchFeedDedupesDualSessionCopies(t *testing.T) {
	bus := eventbus.New(logbus.New(16), "self")
	u := &UI{svc: ui.Services{EventBus: bus}}
	u.subscribeTwitch()
	line := func(id, text string) json.RawMessage {
		b, _ := json.Marshal(twitch.Event{Kind: twitch.KindChat, MessageID: id, UserLogin: "raver", Text: text, TS: time.Now().UnixMilli()})
		return b
	}
	bus.Publish(twitch.TopicChat, line("m-1", "hello"))
	bus.Publish(twitch.TopicChat, line("m-1", "hello")) // the peer copy of the same line
	bus.Publish(twitch.TopicChat, line("m-2", "second"))
	deadline := time.Now().Add(2 * time.Second)
	for {
		u.twMu.Lock()
		n := len(u.twitchRows)
		u.twMu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let a (wrong) third delivery land before counting
	u.twMu.Lock()
	defer u.twMu.Unlock()
	if len(u.twitchRows) != 2 {
		t.Fatalf("feed has %d rows, want 2 (m-1 once, m-2 once): %+v", len(u.twitchRows), u.twitchRows)
	}
}
