package mediaroute

import (
	"image"
	"testing"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
)

// dropstats_test.go - the route's drop counters must reach the ONE reporter the router asks
// (design §12.4 item 4: mediaroute + mf_bridge drop counters "reach nobody"). The sink/source
// stages now implement PipelineReporter and the encode/decode wrapper sums them.

type countingSender struct{ sent int }

func (c *countingSender) Send(*image.NRGBA) error { c.sent++; return nil }
func (c *countingSender) Close()                  {}

func TestSpoutSinkReportsDrops(t *testing.T) {
	fs := &countingSender{}
	s := &spoutSink{log: logbus.New(16), fs: fs, name: "x", w: 4, h: 4}
	// wrong kind + short payload: both drop, neither reaches the sender
	if err := s.Write(&medialink.Frame{Kind: medialink.KindAudio, Payload: make([]byte, 4*4*4)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(&medialink.Frame{Kind: medialink.KindVideo, Payload: make([]byte, 8)}); err != nil {
		t.Fatal(err)
	}
	if fs.sent != 0 {
		t.Fatalf("sender got %d frames, want 0", fs.sent)
	}
	if got := s.PipeStats().Dropped; got != 2 {
		t.Fatalf("sink reports %d drops, want 2", got)
	}
	if got := medialink.InnerDrops(s); got != 2 {
		t.Fatalf("InnerDrops(sink) = %d, want 2 (the decode wrapper must see it)", got)
	}
	if got := medialink.InnerDrops(fs); got != 0 {
		t.Fatalf("InnerDrops on a non-reporter = %d, want 0", got)
	}
}

// TestSpoutSourceReportsFPSCapDrops: the per-route fps cap drops frames before encode; that count
// is the difference between "the sender is slow" and "we are throwing its frames away".
func TestSpoutSourceReportsFPSCapDrops(t *testing.T) {
	s := &spoutSource{name: "cam"}
	if got := s.PipeStats().Dropped; got != 0 {
		t.Fatalf("fresh source reports %d drops", got)
	}
	s.dropped.Add(3)
	if got := medialink.InnerDrops(s); got != 3 {
		t.Fatalf("InnerDrops(source) = %d, want 3", got)
	}
}
