package featurehost

import (
	"encoding/json"
	"testing"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/twitch"
)

// The strip's "N msg/min" counts chat lines only - follows/subs/cheers are alerts, not chat.
func TestTwitchProxyChatRateCountsChatOnly(t *testing.T) {
	p, err := NewTwitchProxy(logbus.New(16), nil, nil, func() string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	ev := func(kind twitch.EventKind) json.RawMessage {
		b, _ := json.Marshal(twitch.Event{Kind: kind, UserLogin: "x", Text: "hi"})
		return b
	}
	for range 3 {
		p.onEv(ev(twitch.KindChat))
	}
	p.onEv(ev(twitch.KindFollow))
	p.onEv(ev(twitch.KindCheer))
	if got := p.ChatRate(); got != 3 {
		t.Fatalf("ChatRate=%d, want 3 (alerts are not chat)", got)
	}
}
