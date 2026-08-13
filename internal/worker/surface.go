package worker

// surface worker: the P3 frame producer for a native render surface in the webview shell child
// (SDL_WEBVIEW_SURFACE_DESIGN §4.5). Own worker type because it is GPU-bearing: it holds a D3D11
// device and a ring of shared textures, and a driver fault there must kill this child and nothing
// else. The daemon never touches any of it - it starts the job and reads counters.
//
//	surface.card → internal/testcard into a shared-texture ring, until the job is cancelled
//
// The generator's geometry is NEGOTIATED, not configured: the shell child writes the surface's full
// rect into the shared control block and this loop re-publishes the ring to match. That is what
// keeps the compositor's copy 1:1, which is what makes a scrolled-out surface CROP instead of
// squash.

import (
	"encoding/json"
	"fmt"
	"image"
	"time"

	"rave.page/mate/internal/surfacepub"
	"rave.page/mate/internal/testcard"
)

func surfaceHandlers() map[string]Handler {
	return map[string]Handler{
		"surface.ping": func(json.RawMessage, EmitFunc) (json.RawMessage, error) { return json.RawMessage(`{}`), nil },
		"surface.card": surfaceCard,
	}
}

type surfaceCardIn struct {
	ID         string `json:"id"`
	W          int    `json:"w"`
	H          int    `json:"h"`
	FPS        int    `json:"fps"`
	MaxSeconds int    `json:"maxSeconds"`
}

// SurfaceCardStats is one progress event: the generator's ground truth beside the transport's.
type SurfaceCardStats struct {
	Gen  testcard.GenStats `json:"gen"`
	Pub  surfacepub.Stats  `json:"pub"`
	Note string            `json:"note,omitempty"`
}

// surfaceSink adapts the publisher to testcard.FrameSink and stamps every frame with its SOURCE
// PTS - time since this producer started, monotonic. The transport carries it per frame, so the
// consumer can say which picture it presented and how old it was; "latest wins with no identity" is
// exactly how a frozen route reads healthy.
type surfaceSink struct {
	pub *surfacepub.Pub
	t0  time.Time
}

func (s *surfaceSink) Send(img *image.NRGBA) error {
	return s.pub.Send(img, time.Since(s.t0).Nanoseconds())
}

// Close is deliberately a no-op: the publisher outlives any one generator. A resize restarts the
// GENERATOR (new size, new ring generation) while the control block - and therefore the consumer's
// attachment - stays put. Tearing the endpoint down here would make every resize a re-probe race.
func (s *surfaceSink) Close() {}

const (
	// how long to wait for the shell child to attach and ask for a size before falling back to the
	// requested one. It normally answers within a frame or two.
	wantWaitSecs = 5
	// a size must hold still this long before the ring is republished - a resize DRAG must not
	// rebuild a GPU ring per frame.
	sizeSettle = 400 * time.Millisecond
	// safety cap on a forgotten diagnostic producer.
	defaultMaxSeconds = 6 * 3600
)

func surfaceCard(params json.RawMessage, emit EmitFunc) (json.RawMessage, error) {
	var in surfaceCardIn
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, err
	}
	if in.ID == "" {
		return nil, fmt.Errorf("surface.card: missing surface id")
	}
	if in.W <= 0 || in.H <= 0 {
		in.W, in.H = testcard.DefaultW, testcard.DefaultH
	}
	if in.FPS <= 0 {
		in.FPS = testcard.DefaultFPS
	}
	if in.MaxSeconds <= 0 {
		in.MaxSeconds = defaultMaxSeconds
	}

	pub, err := surfacepub.Open(in.ID)
	if err != nil {
		return nil, err
	}
	defer pub.Close()

	sink := &surfaceSink{pub: pub, t0: time.Now()}
	var gen *testcard.Gen
	defer func() {
		if gen != nil {
			gen.Stop()
		}
	}()

	start := time.Now()
	deadline := start.Add(time.Duration(in.MaxSeconds) * time.Second)
	curW, curH := 0, 0
	pendW, pendH := 0, 0
	pendSince := time.Time{}
	lastEmit := time.Time{}

	for time.Now().Before(deadline) {
		wantW, wantH := pub.Want()
		switch {
		case wantW > 0 && wantH > 0:
			wantW, wantH = surfacepub.ClampGeometry(wantW, wantH)
		case curW == 0 && time.Since(start) < wantWaitSecs*time.Second:
			wantW, wantH = 0, 0 // still waiting for the consumer to say how big its element is
		default:
			wantW, wantH = surfacepub.ClampGeometry(in.W, in.H)
		}
		if wantW > 0 && wantH > 0 {
			if wantW != pendW || wantH != pendH {
				pendW, pendH, pendSince = wantW, wantH, time.Now()
			}
			if (wantW != curW || wantH != curH) && time.Since(pendSince) >= sizeSettle {
				if gen != nil {
					gen.Stop()
					gen = nil
				}
				if err := pub.SetGeometry(wantW, wantH); err != nil {
					return nil, err
				}
				g, gerr := testcard.NewGen(sink, wantW, wantH, in.FPS)
				if gerr != nil {
					return nil, gerr
				}
				gen, curW, curH = g, wantW, wantH
			}
		}
		if time.Since(lastEmit) >= 2*time.Second {
			lastEmit = time.Now()
			st := SurfaceCardStats{Pub: pub.Stats()}
			if gen != nil {
				st.Gen = gen.Stats()
			} else {
				st.Note = "waiting for the surface to attach and report its size"
			}
			emit("stats", st)
		}
		time.Sleep(150 * time.Millisecond)
	}
	return json.RawMessage(`{"stopped":"max duration reached"}`), nil
}
