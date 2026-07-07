package remotectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/worker"
)

// loopback wires two endpoints so each delivers to the other's OnControl - an in-process stand
// in for the peerlink ChanControl. Node ids are "A"/"B".
func loopback() (a, b *Endpoint) {
	var ea, eb *Endpoint
	ea = New(nil, func(_ string, payload []byte) error { eb.OnControl("A", payload); return nil })
	eb = New(nil, func(_ string, payload []byte) error { ea.OnControl("B", payload); return nil })
	return ea, eb
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestRoundTrip(t *testing.T) {
	server, client := loopback()
	server.Register("echo", func(_ context.Context, peer string, p json.RawMessage) (any, error) {
		return map[string]any{"peer": peer, "got": json.RawMessage(p)}, nil
	})
	raw, err := client.Call(ctx(t), "server", "echo", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var out struct {
		Peer string          `json:"peer"`
		Got  json.RawMessage `json:"got"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Peer != "B" { // server saw the request from node "B" (client's send tags it "B")
		t.Fatalf("peer=%q want B", out.Peer)
	}
	if string(out.Got) != `{"x":1}` {
		t.Fatalf("got=%s", out.Got)
	}
}

func TestErrorPropagation(t *testing.T) {
	server, client := loopback()
	server.Register("boom", func(context.Context, string, json.RawMessage) (any, error) {
		return nil, errors.New("kaboom")
	})
	if _, err := client.Call(ctx(t), "server", "boom", nil); err == nil || err.Error() != "kaboom" {
		t.Fatalf("err=%v want kaboom", err)
	}
}

func TestUnknownMethod(t *testing.T) {
	_, client := loopback()
	_, err := client.Call(ctx(t), "server", "nope", nil)
	if err == nil {
		t.Fatal("want error for unknown method")
	}
}

func TestTimeout(t *testing.T) {
	server, client := loopback()
	server.Register("hang", func(c context.Context, _ string, _ json.RawMessage) (any, error) {
		<-c.Done() // never replies in time
		return nil, c.Err()
	})
	cctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := client.Call(cctx, "server", "hang", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want deadline exceeded", err)
	}
}

func TestNoTarget(t *testing.T) {
	e := New(nil, func(string, []byte) error { return nil })
	if _, err := e.Call(ctx(t), "", "x", nil); err == nil {
		t.Fatal("empty target must error")
	}
}

// TestAutomationsRPC drives a fake automation.Manager through the full client→server path.
func TestAutomationsRPC(t *testing.T) {
	server, client := loopback()
	m := &fakeAuto{items: map[string]automation.Automation{}}
	RegisterAutomations(server, m)
	rc := NewClient(client, "server")

	saved, err := rc.SaveAutomation(ctx(t), automation.Automation{Label: "Trim"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == "" || saved.Label != "Trim" {
		t.Fatalf("saved=%+v", saved)
	}
	list, err := rc.ListAutomations(ctx(t))
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	on, err := rc.SetEnabled(ctx(t), saved.ID, true)
	if err != nil || !on.Enabled {
		t.Fatalf("setEnabled=%+v err=%v", on, err)
	}
	run, err := rc.RunManual(ctx(t), saved.ID, "/x/y.wav")
	if err != nil || run.Status != "success" || run.FilePath != "/x/y.wav" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	if err := rc.DeleteAutomation(ctx(t), saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := rc.ListAutomations(ctx(t)); len(list) != 0 {
		t.Fatalf("after delete len=%d", len(list))
	}
}

// TestMediaTranscodeRPC drives a transcode over the peer via a fake worker runner, asserting
// the controller gets the peer-side output path back.
func TestMediaTranscodeRPC(t *testing.T) {
	server, client := loopback()
	RegisterMedia(server, jobs.New(fakeRunner{}))
	rc := NewClient(client, "server")

	res, err := rc.Transcode(ctx(t), filepath.FromSlash("/in/song.wav"),
		transcode.Preset{ID: "mp3", Container: "mp3", AudioCodec: "mp3", AudioBitrateK: 320}, 0, 0)
	if err != nil {
		t.Fatalf("transcode: %v", err)
	}
	if !strings.Contains(res.Output, "rave-mate-transcoded") || !strings.Contains(res.Output, "song-mp3") {
		t.Fatalf("output=%q", res.Output)
	}
}

// fakeShot is a Screenshotter writing a tiny known PNG; VR capture is gated off to test the error.
type fakeShot struct{ vrEnabled bool }

func (f fakeShot) Screenshot(path string) bool   { return writePNG(path) }
func (f fakeShot) ScreenshotVR(path string) bool { return f.vrEnabled && writePNG(path) }

func writePNG(path string) bool {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	f, err := os.Create(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img) == nil
}

// TestScreenshotRPC round-trips an app-window grab (PNG bytes returned) and asserts a gated-off
// VR capture surfaces as an error.
func TestScreenshotRPC(t *testing.T) {
	server, client := loopback()
	RegisterScreenshot(server, fakeShot{vrEnabled: false})
	rc := NewClient(client, "server")

	pngBytes, err := rc.ScreenshotApp(ctx(t))
	if err != nil {
		t.Fatalf("screenshot app: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(pngBytes)); err != nil {
		t.Fatalf("result is not a valid PNG: %v", err)
	}
	if _, err := rc.ScreenshotVR(ctx(t)); err == nil {
		t.Fatal("VR capture gated off must error")
	}
}

// fakeRunner is a jobs.Runner that completes immediately (no real ffmpeg).
type fakeRunner struct{}

func (fakeRunner) RunStream(_ context.Context, _, _ string, _ any, _ worker.ProgressFunc) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

// fakeRec is a minimal RecorderSource for the recorder RPC tests.
type fakeRec struct {
	recs map[string]recorder.Recording
}

func (f *fakeRec) List() []recorder.Recording {
	out := make([]recorder.Recording, 0, len(f.recs))
	for _, r := range f.recs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}
func (f *fakeRec) Get(id string) (recorder.Recording, bool) { r, ok := f.recs[id]; return r, ok }
func (f *fakeRec) Export(id, format string) (string, error) {
	r, ok := f.recs[id]
	if !ok {
		return "", errors.New("not found")
	}
	return r.Export(format)
}
func (f *fakeRec) Delete(id string) error { delete(f.recs, id); return nil }

// fakeCaps is a minimal SetCaptureSource.
type fakeCaps struct{ rows []libdb.SetRecording }

func (f fakeCaps) ListSetRecordings(limit int) ([]libdb.SetRecording, error) {
	if limit > 0 && limit < len(f.rows) {
		return f.rows[:limit], nil
	}
	return f.rows, nil
}

// TestRecorderRPC drives the peer's publish cockpit: list (paged), tracklist (paged), captures,
// export, delete, and a not-found error.
func TestRecorderRPC(t *testing.T) {
	base := time.Date(2026, 7, 7, 20, 0, 0, 0, time.UTC)
	mk := func(id string, n int, when time.Time) recorder.Recording {
		tr := make([]recorder.Track, n)
		for i := range tr {
			tr[i] = recorder.Track{Title: fmt.Sprintf("T%d", i), Artist: "A", StartedAt: when.Add(time.Duration(i) * time.Minute)}
		}
		return recorder.Recording{ID: id, Name: "Set " + id, StartedAt: when, EndedAt: when.Add(time.Hour), Tracks: tr}
	}
	rec := &fakeRec{recs: map[string]recorder.Recording{
		"a": mk("a", 3, base),
		"b": mk("b", 250, base.Add(time.Hour)), // big tracklist → exercises paging
	}}
	caps := fakeCaps{rows: []libdb.SetRecording{{ID: "c1", RecordingID: "a", Path: "/x/a.ogg", Format: "ogg"}}}

	server, client := loopback()
	RegisterRecorder(server, rec, caps)
	rc := NewClient(client, "server")

	// listSets: newest first (b before a), summaries carry the track count without the tracks.
	list, err := rc.RecList(ctx(t), 0, 10)
	if err != nil || len(list.Sets) != 2 || list.Total != 2 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if list.Sets[0].ID != "b" || list.Sets[0].TrackCount != 250 {
		t.Fatalf("first=%+v", list.Sets[0])
	}
	// paged listSets: offset past the end → empty page, total preserved.
	if p, err := rc.RecList(ctx(t), 5, 10); err != nil || len(p.Sets) != 0 || p.Total != 2 {
		t.Fatalf("paged list=%+v err=%v", p, err)
	}

	// tracklist paging: first page of set b, then the tail.
	tl, err := rc.RecTracklist(ctx(t), "b", 0, 100)
	if err != nil || len(tl.Tracks) != 100 || tl.Total != 250 || tl.Name != "Set b" {
		t.Fatalf("tracklist=%+v err=%v", tl, err)
	}
	if tail, err := rc.RecTracklist(ctx(t), "b", 200, 100); err != nil || len(tail.Tracks) != 50 {
		t.Fatalf("tracklist tail len=%d err=%v", len(tail.Tracks), err)
	}
	// tracklist of an unknown set → error.
	if _, err := rc.RecTracklist(ctx(t), "nope", 0, 10); err == nil {
		t.Fatal("unknown set tracklist must error")
	}

	// captures round-trip.
	cp, err := rc.RecCaptures(ctx(t), 0)
	if err != nil || len(cp.Captures) != 1 || cp.Captures[0].ID != "c1" {
		t.Fatalf("captures=%+v err=%v", cp, err)
	}

	// export (JSON) contains the set id; delete removes it.
	out, err := rc.RecExport(ctx(t), "a", recorder.FormatJSON)
	if err != nil || !strings.Contains(out, `"id": "a"`) {
		t.Fatalf("export=%q err=%v", out, err)
	}
	if err := rc.RecDelete(ctx(t), "a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := rc.RecList(ctx(t), 0, 10); list.Total != 1 {
		t.Fatalf("after delete total=%d", list.Total)
	}
}

// TestRecorderMatchRPC drives recorder.matchHistory: round-trip (reconciled summary comes back with
// a ReconciledAt) + a not-found error from the matcher.
func TestRecorderMatchRPC(t *testing.T) {
	base := time.Date(2026, 7, 7, 20, 0, 0, 0, time.UTC)
	recs := map[string]recorder.Recording{
		"a": {ID: "a", Name: "Set a", StartedAt: base, EndedAt: base.Add(time.Hour), Tracks: []recorder.Track{{Title: "T0"}}},
	}
	server, client := loopback()
	RegisterRecorderMatch(server, func(id string) (recorder.Recording, error) {
		r, ok := recs[id]
		if !ok {
			return recorder.Recording{}, errors.New("recording not found")
		}
		r.ReconciledAt = base.Add(2 * time.Hour) // simulate a successful match
		recs[id] = r
		return r, nil
	})
	rc := NewClient(client, "server")

	// round-trip: matched set returns with a non-zero ReconciledAt + preserved track count.
	m, err := rc.RecMatchHistory(ctx(t), "a")
	if err != nil || m.ID != "a" || m.ReconciledAt.IsZero() || m.TrackCount != 1 {
		t.Fatalf("match=%+v err=%v", m, err)
	}
	// not-found: matcher error propagates.
	if _, err := rc.RecMatchHistory(ctx(t), "nope"); err == nil {
		t.Fatal("unknown set matchHistory must error")
	}
}

func TestNewClientNil(t *testing.T) {
	if NewClient(nil, "x") != nil || NewClient(New(nil, nil), "") != nil {
		t.Fatal("NewClient must be nil for empty endpoint/node")
	}
}

// fakeAuto is a minimal automation.Manager for the RPC tests (only the methods exercised).
type fakeAuto struct {
	items map[string]automation.Automation
	seq   int
}

func (f *fakeAuto) List() []automation.Automation {
	out := []automation.Automation{}
	for _, a := range f.items {
		out = append(out, a)
	}
	return out
}
func (f *fakeAuto) Get(id string) (automation.Automation, bool) { a, ok := f.items[id]; return a, ok }
func (f *fakeAuto) Save(a automation.Automation) (automation.Automation, error) {
	if a.ID == "" {
		f.seq++
		a.ID = "id" + string(rune('0'+f.seq))
	}
	f.items[a.ID] = a
	return a, nil
}
func (f *fakeAuto) Delete(id string) error { delete(f.items, id); return nil }
func (f *fakeAuto) RunManual(_ context.Context, id, filePath string) (automation.Run, error) {
	return automation.Run{ID: "r1", AutomationID: id, FilePath: filePath, Status: "success"}, nil
}
func (f *fakeAuto) Runs(int) []automation.Run                                       { return nil }
func (f *fakeAuto) ListSchedules() []automation.Schedule                            { return nil }
func (f *fakeAuto) SaveSchedule(s automation.Schedule) (automation.Schedule, error) { return s, nil }
func (f *fakeAuto) DeleteSchedule(string) error                                     { return nil }
func (f *fakeAuto) OnEvent(func(automation.RunEvent)) func()                        { return func() {} }
func (f *fakeAuto) StartRun(automation.RunMode, string, string) (string, error)     { return "", nil }
func (f *fakeAuto) CommitStep(string) error                                         { return nil }
func (f *fakeAuto) SkipStep(string) error                                           { return nil }
func (f *fakeAuto) AbortRun(string) error                                           { return nil }
func (f *fakeAuto) ProbeSilence(context.Context, string, float64, float64) (automation.SilenceResult, error) {
	return automation.SilenceResult{}, nil
}
func (f *fakeAuto) ListEvents(context.Context, string, int) ([]automation.MatchedEvent, error) {
	return nil, nil
}
func (f *fakeAuto) SetBackgroundCredentials(string, string) {}
