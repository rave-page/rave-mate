package eventbus

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"rave.page/mate/internal/logbus"
)

func newTestBus(self string) (*Bus, *[][]byte) {
	var mu sync.Mutex
	var sent [][]byte
	b := New(logbus.New(16), self)
	b.SetTransport(func(p []byte) {
		mu.Lock()
		sent = append(sent, append([]byte(nil), p...))
		mu.Unlock()
	}, nil)
	return b, &sent
}

func TestLocalPubSub(t *testing.T) {
	b := New(logbus.New(16), "self")
	var got []Event
	unsub := b.Subscribe("t", func(e Event) { got = append(got, e) })
	b.Publish("t", json.RawMessage(`1`))
	b.Publish("other", json.RawMessage(`2`)) // different topic - ignored
	unsub()
	b.Publish("t", json.RawMessage(`3`)) // after unsub - ignored
	if len(got) != 1 || !got[0].Local || string(got[0].Data) != "1" {
		t.Fatalf("want one local event data=1, got %+v", got)
	}
}

func TestTransportBroadcastOnPublish(t *testing.T) {
	b, sent := newTestBus("self")
	*sent = nil // drop the SetTransport advertise frame
	b.Publish("t", json.RawMessage(`"x"`))
	if len(*sent) != 1 {
		t.Fatalf("want 1 broadcast, got %d", len(*sent))
	}
	var env Envelope
	if err := json.Unmarshal((*sent)[0], &env); err != nil {
		t.Fatal(err)
	}
	if env.Topic != "t" || env.Origin != "self" || env.Seq == 0 {
		t.Fatalf("bad envelope %+v", env)
	}
}

func TestInboundDedup(t *testing.T) {
	b := New(logbus.New(16), "self")
	var n int
	b.Subscribe("t", func(Event) { n++ })
	env := Envelope{Topic: "t", Origin: "peerA", Seq: 5, Data: json.RawMessage(`1`)}
	raw, _ := json.Marshal(env)
	b.Inbound("peerA", raw) // new → delivered
	b.Inbound("peerA", raw) // dup seq → dropped
	env.Seq = 4
	old, _ := json.Marshal(env)
	b.Inbound("peerA", old) // older seq → dropped
	if n != 1 {
		t.Fatalf("want 1 delivery after dedup, got %d", n)
	}
}

func TestOwnSeqEchoDropped(t *testing.T) {
	b, sent := newTestBus("self")
	var n int
	b.Subscribe("t", func(Event) { n++ })
	b.Publish("t", json.RawMessage(`1`)) // local delivery (n=1), broadcasts an envelope
	// Simulate a peer relaying our own frame back to us.
	last := (*sent)[len(*sent)-1]
	b.Inbound("peerA", last)
	if n != 1 {
		t.Fatalf("own echo should not re-deliver locally; got n=%d", n)
	}
}

func TestOriginRestartAcceptsResetSeq(t *testing.T) {
	b := New(logbus.New(16), "self")
	var got []string
	b.Subscribe("t", func(e Event) { got = append(got, string(e.Data)) })
	send := func(epoch, seq uint64, data string) {
		env := Envelope{Topic: "t", Origin: "peerA", Epoch: epoch, Seq: seq, Data: json.RawMessage(`"` + data + `"`)}
		raw, _ := json.Marshal(env)
		b.Inbound("peerA", raw)
	}
	// peerA runs at epoch 100, climbs to a high seq.
	send(100, 5000, "a")
	send(100, 5001, "b")
	// peerA RESTARTS: new epoch, seq resets to 1 - must NOT be dropped as stale (the bug).
	send(200, 1, "c")
	send(200, 2, "d")
	// A straggler from the dead epoch-100 process arrives late - must be dropped.
	send(100, 5002, "late")
	// Same-epoch dup after restart - dropped.
	send(200, 2, "dup")
	want := []string{`"a"`, `"b"`, `"c"`, `"d"`}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestCapabilityAdvertiseAndOwners(t *testing.T) {
	b := New(logbus.New(16), "self")
	caps := Envelope{Topic: TopicCaps, Origin: "peerB", Seq: 1, Caps: []string{"twitch", "obs"}}
	raw, _ := json.Marshal(caps)
	b.Inbound("peerB", raw)
	if got := b.Owners("twitch"); len(got) != 1 || got[0] != "peerB" {
		t.Fatalf("want peerB owns twitch, got %v", got)
	}
	if got := b.Owners("vr"); len(got) != 0 {
		t.Fatalf("want no vr owner, got %v", got)
	}
	// caps frames are control-only - must not be delivered as topic events.
	var n int
	b.Subscribe(TopicCaps, func(Event) { n++ })
	caps.Seq = 2
	raw2, _ := json.Marshal(caps)
	b.Inbound("peerB", raw2)
	if n != 0 {
		t.Fatalf("caps must not fan out to subscribers, got n=%d", n)
	}
}

func TestSendToCapabilityRoutes(t *testing.T) {
	var mu sync.Mutex
	routed := map[string][]byte{}
	b := New(logbus.New(16), "self")
	b.SetTransport(func([]byte) {}, func(node string, p []byte) {
		mu.Lock()
		routed[node] = append([]byte(nil), p...)
		mu.Unlock()
	})
	caps := Envelope{Topic: TopicCaps, Origin: "peerC", Seq: 1, Caps: []string{"twitch"}}
	raw, _ := json.Marshal(caps)
	b.Inbound("peerC", raw)
	n := b.SendToCapability("twitch", "twitch.send", json.RawMessage(`"hi"`))
	if n != 1 {
		t.Fatalf("want routed to 1 owner, got %d", n)
	}
	if _, ok := routed["peerC"]; !ok {
		t.Fatalf("want command routed to peerC, got %v", routed)
	}
}

func TestStatsCounters(t *testing.T) {
	b := New(logbus.New(16), "self")
	b.Publish("t", json.RawMessage(`1`))
	env := Envelope{Topic: "t", Origin: "peerA", Seq: 5, Data: json.RawMessage(`1`)}
	raw, _ := json.Marshal(env)
	b.Inbound("peerA", raw) // new
	b.Inbound("peerA", raw) // dup
	got := b.Stats()
	for _, want := range []string{"published=1", "inbound=1", "dupDropped=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stats missing %q: %s", want, got)
		}
	}
}
