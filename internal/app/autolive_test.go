package app

import (
	"context"
	"errors"
	"testing"

	"rave.page/mate/internal/stream"
)

// fakePub records Start/End calls; startErr fails the next Start (stays not-live).
type fakePub struct {
	starts   int
	ends     int
	titles   []string
	startErr error
}

func (f *fakePub) Start(_ context.Context, a stream.StartArgs) (stream.Status, error) {
	if f.startErr != nil {
		return stream.Status{}, f.startErr
	}
	f.starts++
	f.titles = append(f.titles, a.Title)
	return stream.Status{IsLive: true, Title: a.Title}, nil
}

func (f *fakePub) End(_ context.Context) (stream.Status, error) {
	f.ends++
	return stream.Status{}, nil
}

// one reconcile sample.
type step struct {
	streaming, signedIn, paused bool
	token, title                string
	wantStarts, wantEnds        int // cumulative after this step
	wantLive                    bool
}

func TestAutoLiveDriver(t *testing.T) {
	cases := []struct {
		name  string
		steps []step
	}{
		{"streaming+signedin+notpaused starts once, idempotent", []step{
			{streaming: true, signedIn: true, token: "t", title: "A - B", wantStarts: 1, wantEnds: 0, wantLive: true},
			{streaming: true, signedIn: true, token: "t", title: "A - B", wantStarts: 1, wantEnds: 0, wantLive: true}, // no re-start
		}},
		{"stream end -> End", []step{
			{streaming: true, signedIn: true, token: "t", wantStarts: 1, wantEnds: 0, wantLive: true},
			{streaming: false, signedIn: true, token: "t", wantStarts: 1, wantEnds: 1, wantLive: false},
		}},
		{"paused never starts", []step{
			{streaming: true, signedIn: true, paused: true, token: "t", wantStarts: 0, wantEnds: 0, wantLive: false},
		}},
		{"pause while live -> End", []step{
			{streaming: true, signedIn: true, token: "t", wantStarts: 1, wantEnds: 0, wantLive: true},
			{streaming: true, signedIn: true, paused: true, token: "t", wantStarts: 1, wantEnds: 1, wantLive: false},
		}},
		{"not signed in never starts", []step{
			{streaming: true, signedIn: false, token: "", wantStarts: 0, wantEnds: 0, wantLive: false},
		}},
		{"sign-out while live -> End", []step{
			{streaming: true, signedIn: true, token: "t", wantStarts: 1, wantEnds: 0, wantLive: true},
			{streaming: true, signedIn: false, token: "", wantStarts: 1, wantEnds: 1, wantLive: false},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := &fakePub{}
			d := &autoLiveDriver{pub: fp}
			for i, s := range tc.steps {
				d.tick(context.Background(), s.streaming, s.signedIn, s.paused, s.token, s.title)
				if fp.starts != s.wantStarts || fp.ends != s.wantEnds {
					t.Fatalf("step %d: starts=%d ends=%d, want starts=%d ends=%d", i, fp.starts, fp.ends, s.wantStarts, s.wantEnds)
				}
				if d.live != s.wantLive {
					t.Fatalf("step %d: live=%v, want %v", i, d.live, s.wantLive)
				}
			}
		})
	}
}

// A failed Start stays not-live and retries on the next tick.
func TestAutoLiveStartRetriesOnError(t *testing.T) {
	fp := &fakePub{startErr: errors.New("api down")}
	d := &autoLiveDriver{pub: fp}
	d.tick(context.Background(), true, true, false, "t", "X")
	if d.live {
		t.Fatal("failed Start should leave driver not-live")
	}
	fp.startErr = nil // API recovers
	d.tick(context.Background(), true, true, false, "t", "X")
	if !d.live || fp.starts != 1 {
		t.Fatalf("expected retry to start: live=%v starts=%d", d.live, fp.starts)
	}
}

// Empty now-playing title falls back to "Live set".
func TestAutoLiveDefaultTitle(t *testing.T) {
	fp := &fakePub{}
	d := &autoLiveDriver{pub: fp}
	d.tick(context.Background(), true, true, false, "t", "")
	if len(fp.titles) != 1 || fp.titles[0] != "Live set" {
		t.Fatalf("titles=%v, want [Live set]", fp.titles)
	}
}
