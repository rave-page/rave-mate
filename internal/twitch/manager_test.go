package twitch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestResolveTemplate(t *testing.T) {
	got := ResolveTemplate("{genre} set @ {club} - {event}", map[string]string{
		"genre": "DnB", "club": "Tresor", "event": "Friday",
	})
	if want := "DnB set @ Tresor - Friday"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// unknown placeholder is left intact
	if got := ResolveTemplate("hi {missing}", map[string]string{"x": "y"}); got != "hi {missing}" {
		t.Fatalf("unexpected %q", got)
	}
}

func TestTemplateVars(t *testing.T) {
	got := TemplateVars("{genre} @ {club} ({genre})")
	if len(got) != 2 || got[0] != "genre" || got[1] != "club" {
		t.Fatalf("got %v want [genre club]", got)
	}
}

func TestDecodeChatEvent(t *testing.T) {
	raw := json.RawMessage(`{
		"chatter_user_id":"99","chatter_user_login":"bob","chatter_user_name":"Bob",
		"color":"#fff","message_id":"m1","message":{"text":"hi"},
		"badges":[{"set_id":"moderator"},{"set_id":"subscriber"}],
		"cheer":{"bits":100}}`)
	ev, ok := decodeEvent("channel.chat.message", raw)
	if !ok || ev.Kind != KindChat || ev.UserLogin != "bob" || ev.Text != "hi" {
		t.Fatalf("bad chat decode: %+v ok=%v", ev, ok)
	}
	if !ev.Mod || !ev.Subscriber || ev.Bits != 100 || ev.MessageID != "m1" {
		t.Fatalf("missing chat fields: %+v", ev)
	}
}

func TestDecodeCheerEvent(t *testing.T) {
	raw := json.RawMessage(`{"user_id":"1","user_login":"a","user_name":"A","bits":500,"message":"pog"}`)
	ev, ok := decodeEvent("channel.cheer", raw)
	if !ok || ev.Kind != KindCheer || ev.Bits != 500 {
		t.Fatalf("bad cheer decode: %+v ok=%v", ev, ok)
	}
}

func TestHTTPErrorPermanent(t *testing.T) {
	cases := []struct {
		code int
		perm bool
	}{
		{401, true},  // missing scope / revoked - re-auth needed
		{403, true},  // forbidden
		{429, false}, // rate-limited - transient
		{500, false}, // server error - transient
		{404, false},
	}
	for _, c := range cases {
		e := &HTTPError{Method: "GET", Path: "/chat/chatters", StatusCode: c.code, Message: "x"}
		if e.Permanent() != c.perm {
			t.Errorf("status %d Permanent()=%v, want %v", c.code, e.Permanent(), c.perm)
		}
	}
	// The chatter-poll back-off decision keys off errors.As unwrapping to *HTTPError.
	err := error(&HTTPError{Path: "/chat/chatters", StatusCode: 401, Message: "Missing scope: moderator:read:chatters"})
	var he *HTTPError
	if !errors.As(err, &he) || !he.Permanent() {
		t.Fatalf("errors.As failed to classify the 401 as permanent: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("Error() lost the status code: %q", err.Error())
	}
}
