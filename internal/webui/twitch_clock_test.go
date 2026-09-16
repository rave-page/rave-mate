package webui

import (
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/twitch"
)

// Feed rows carry the local HH:MM the line arrived; an event without a timestamp renders no stamp
// at all (both renderers agree on the empty case - see twitch.zig renderTime).
func TestTwitchRowsCarryClockStamp(t *testing.T) {
	if got := twClock(0); got != "" {
		t.Fatalf("twClock(0)=%q, want empty", got)
	}
	ts := time.Date(2026, 9, 16, 21, 4, 59, 0, time.Local)
	if got := twClock(ts.UnixMilli()); got != "21:04" {
		t.Fatalf("twClock=%q, want 21:04 (minutes, local)", got)
	}
	chat := twChatRow(twitch.Event{Kind: twitch.KindChat, UserLogin: "raver", Text: "hi", TS: ts.UnixMilli()}, false)
	if !strings.Contains(twRowHTML(chat), `<span class=tw-time>21:04</span><span class=tw-name`) {
		t.Fatalf("chat row lacks the clock stamp before the name: %s", twRowHTML(chat))
	}
	alert := twAlertRow(twitch.Event{Kind: twitch.KindFollow, UserLogin: "raver", TS: ts.UnixMilli()})
	if !strings.Contains(twRowHTML(alert), `"><span class=tw-time>21:04</span>`) {
		t.Fatalf("alert row lacks the clock stamp: %s", twRowHTML(alert))
	}
	if html := twRowHTML(twChatRow(twitch.Event{Kind: twitch.KindChat, UserLogin: "x", Text: "y"}, false)); strings.Contains(html, "tw-time") {
		t.Fatalf("a row without TS must render no stamp: %s", html)
	}
}
