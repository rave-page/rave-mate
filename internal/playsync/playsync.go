// Package playsync feeds rave-mate's captured play data to the rave.page play/tracklist layer
// (PLAY_LAYER_INTEGRATION_BRIEF). It closes the two capture-side gaps:
//
//   - Gap 1 (fingerprint identification): each played track's Chromaprint (written to the
//     libdb change_log by internal/setfp) is identified against the canonical corpus
//     (POST /tracks/fingerprint/identify). A match links the local track to a canonical
//     track_id; a miss seeds a provisional track (POST /tracks) + submits the print so future
//     plays link. The resolution is cached in libdb.track_links so a drain is idempotent.
//
//   - Gap 2 (offline/recorded sets): a set captured while the DJ app was closed (or any
//     recorder Recording that never went live) is published as a backend stream by replaying
//     its played tracks through Create/Ingest/End with backdated timestamps + a recorded flag.
//     Idempotent via libdb.set_uploads.
//
// All backend writes are owner-scoped → they need the DJ's access token (brokered by rave-app
// over the ctl ADOPT-TOKEN handoff, or rave-mate's own browser-deeplink session). Unauthed,
// every method degrades to a no-op (ErrUnauthenticated) instead of erroring loudly - the local
// capture data is preserved and a later drain syncs it once a token is present.
package playsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/logbus"
)

const source = "playsync"

// ErrUnauthenticated is returned when no access token is available (sync deferred, not failed).
var ErrUnauthenticated = errors.New("playsync: no access token")

// defaultMinConfidence is the acoustic-match floor below which an identify candidate is treated
// as a miss (→ provisional). Tuned conservatively; the backend caps confidence at 0.99.
const defaultMinConfidence = 0.5

// trackAPI is the subset of *api.Client playsync calls (an interface so the sync logic is
// unit-testable with a fake - no live API).
type trackAPI interface {
	IdentifyFingerprint(ctx context.Context, token, fingerprintB64 string, limit int) ([]api.FpCandidate, error)
	SubmitFingerprint(ctx context.Context, token, trackID, fingerprintB64 string, durationMs, sampleRate int) error
	CreateProvisionalTrack(ctx context.Context, token string, t api.NewTrack) (string, error)
	ReportObservation(ctx context.Context, token, trackID string, o api.Observation) error
	CreateStream(ctx context.Context, userToken string, req api.CreateStreamReq) (api.CreateStreamResp, error)
	Ingest(ctx context.Context, streamID, publishToken string, events []api.IngestEvent) error
	EndStream(ctx context.Context, streamID, publishToken string) error
	UploadLibraryTracks(ctx context.Context, token string, tracks []api.LibraryTrack) (api.LibraryBulkResp, error)
	ListLibraryTracks(ctx context.Context, token string, limit, offset int) ([]api.LibraryTrackOut, error)
	UploadTrackWaveform(ctx context.Context, token, libraryTrackID, peaksB64, bandsB64 string, durationMs int) (int, error)
	UploadTrackArtwork(ctx context.Context, token, libraryTrackID, contentType string, data []byte) error
	ListPlaylists(ctx context.Context, token string) ([]api.PlaylistOut, error)
	GetPlaylist(ctx context.Context, token, id string, includeItems bool) (api.PlaylistOut, error)
	CreatePlaylist(ctx context.Context, token, title, description, visibility string) (api.PlaylistOut, error)
	UpdatePlaylist(ctx context.Context, token, id, title, description, visibility string) (api.PlaylistOut, error)
	DeletePlaylist(ctx context.Context, token, id string) error
	PutPlaylistItems(ctx context.Context, token, id string, items []api.PlaylistItemIn) (api.PlaylistOut, error)
}

// Syncer pushes captured play data to the backend. Safe for concurrent use (stateless beyond
// its deps). lib may be nil (every method then no-ops).
type Syncer struct {
	api           trackAPI
	lib           *libdb.DB
	log           *logbus.Bus
	token         func() string // current access token; "" = unauthed
	minConfidence float64
	probe         ProbeRunner // media probing (peaks/artwork) via the worker pool; nil = SyncMedia unavailable
}

// ProbeRunner is the worker-supervisor subset SyncMedia uses (satisfied by *worker.Supervisor).
type ProbeRunner interface {
	RunBackground(ctx context.Context, typ, method string, params any) (json.RawMessage, error)
}

// SetProbe wires the worker pool for media probing (peaks/artwork extraction).
func (s *Syncer) SetProbe(p ProbeRunner) {
	if s != nil {
		s.probe = p
	}
}

// New constructs a Syncer. token returns the current access token ("" when signed out).
func New(apiClient trackAPI, lib *libdb.DB, log *logbus.Bus, token func() string) *Syncer {
	if token == nil {
		token = func() string { return "" }
	}
	return &Syncer{api: apiClient, lib: lib, log: log, token: token, minConfidence: defaultMinConfidence}
}

// Track is one played track to identify or upload.
type Track struct {
	Artist, Title, Album, Key string
	BPM                       float64
	DurationSec               float64
	Deck                      string
	StartedAt                 time.Time
	EndedAt                   time.Time
}

func (t Track) hash() string { return libdb.TrackHash(t.Artist, t.Title, 0) }
func (t Track) durationMs() int {
	if t.DurationSec > 0 {
		return int(t.DurationSec * 1000)
	}
	if !t.EndedAt.IsZero() && t.EndedAt.After(t.StartedAt) {
		return int(t.EndedAt.Sub(t.StartedAt).Milliseconds())
	}
	return 0
}

func (s *Syncer) tok() (string, error) {
	t := s.token()
	if t == "" {
		return "", ErrUnauthenticated
	}
	return t, nil
}

// IdentifyTrack resolves one played track to a backend track_id, caching the result in
// track_links (so repeat calls are cheap + idempotent). A fingerprint match links to the
// canonical track + submits the print to grow the corpus; a miss seeds a provisional track.
// Without a fingerprint and without a token it can only return a cached link or an error.
func (s *Syncer) IdentifyTrack(ctx context.Context, t Track) (libdb.TrackLink, error) {
	if s == nil || s.lib == nil {
		return libdb.TrackLink{}, fmt.Errorf("playsync: no library")
	}
	if t.Title == "" {
		return libdb.TrackLink{}, fmt.Errorf("playsync: empty title")
	}
	hash := t.hash()
	if link, ok, err := s.lib.GetTrackLink(hash); err != nil {
		return libdb.TrackLink{}, err
	} else if ok {
		return link, nil // already resolved
	}

	token, err := s.tok()
	if err != nil {
		return libdb.TrackLink{}, err
	}
	fp, hasFP, _ := s.lib.FingerprintForTrack(hash)

	link := libdb.TrackLink{TrackHash: hash}
	if hasFP {
		cands, err := s.api.IdentifyFingerprint(ctx, token, fp, 5)
		if err != nil {
			return libdb.TrackLink{}, fmt.Errorf("identify: %w", err)
		}
		if len(cands) > 0 && cands[0].Confidence >= s.minConfidence {
			link.TrackID = cands[0].TrackID
			link.Confidence = cands[0].Confidence
		}
	}
	if link.TrackID == "" {
		// No acoustic match (or no fingerprint): seed a provisional track.
		id, err := s.api.CreateProvisionalTrack(ctx, token, api.NewTrack{Title: t.Title, DurationMs: t.durationMs()})
		if err != nil {
			return libdb.TrackLink{}, fmt.Errorf("provisional: %w", err)
		}
		link.TrackID = id
		link.Provisional = true
	}

	// Grow the corpus: attach our print + report observed metadata (best-effort, non-fatal).
	if hasFP {
		if err := s.api.SubmitFingerprint(ctx, token, link.TrackID, fp, t.durationMs(), 0); err != nil {
			s.warn("submit fingerprint", err)
		}
	}
	if err := s.api.ReportObservation(ctx, token, link.TrackID, api.Observation{
		Title: t.Title, ArtistText: t.Artist, Album: t.Album, Key: t.Key, BPM: t.BPM, DurationMs: t.durationMs(),
	}); err != nil {
		s.warn("observation", err)
	}

	if err := s.lib.SaveTrackLink(link); err != nil {
		return libdb.TrackLink{}, err
	}
	s.info("track linked", map[string]any{"title": t.Title, "trackId": link.TrackID, "provisional": link.Provisional, "confidence": link.Confidence})
	return link, nil
}

// Result reports a drain outcome.
type Result struct {
	Linked      int // matched to an existing canonical track
	Provisional int // seeded a new provisional track
	Cached      int // already resolved before this run
	Failed      int
}

// DrainRecording identifies every played track of a finished recording (Gap 1). Per-track
// failures are counted, not fatal - a partial sync still improves coverage. No-op (nil error)
// when unauthed, so the next drain with a token picks it up.
func (s *Syncer) DrainRecording(ctx context.Context, recordingID string) (Result, error) {
	if s == nil || s.lib == nil {
		return Result{}, nil
	}
	if _, err := s.tok(); err != nil {
		return Result{}, nil // deferred until authed
	}
	played, err := s.lib.PlayedTracksFor(recordingID)
	if err != nil {
		return Result{}, err
	}
	var res Result
	for _, p := range played {
		if ctx.Err() != nil {
			break
		}
		hash := libdb.TrackHash(p.Artist, p.Title, 0)
		if _, ok, _ := s.lib.GetTrackLink(hash); ok {
			res.Cached++
			continue
		}
		link, err := s.IdentifyTrack(ctx, Track{
			Artist: p.Artist, Title: p.Title, Album: p.Album, Key: p.Key, BPM: p.BPM,
			Deck: p.Deck, StartedAt: p.StartedAt, EndedAt: p.EndedAt,
		})
		switch {
		case err != nil:
			res.Failed++
			s.warn("identify track", err)
		case link.Provisional:
			res.Provisional++
		default:
			res.Linked++
		}
	}
	s.info("recording drained", map[string]any{"recording": recordingID, "linked": res.Linked, "provisional": res.Provisional, "cached": res.Cached, "failed": res.Failed})
	return res, nil
}

// RecordedSet is an offline/recorded set to publish to the backend (Gap 2).
type RecordedSet struct {
	RecordingID string
	Title       string
	StartedAt   time.Time
	EndedAt     time.Time
	Tracks      []Track // in play order
}

// UploadRecordedSet publishes a recorded set as a backend stream by replaying its tracklist
// through Create/Ingest/End with backdated timestamps + metadata.recorded=true (Gap 2).
// Idempotent: a set already in set_uploads returns its existing stream id.
//
// NOTE: the recorded-set ingest contract is the brief's fallback (reuse the live Create/Ingest/
// End path backdated) pending a dedicated backend endpoint - open decision #2. Each track is
// replayed as a backdated "deck.loaded" event mirroring the live publisher's wire shape, so the
// backend's now-playing derivation produces the set-log. If the API dev ships a recorded-set
// endpoint, only this method changes.
func (s *Syncer) UploadRecordedSet(ctx context.Context, set RecordedSet) (string, error) {
	if s == nil || s.lib == nil {
		return "", fmt.Errorf("playsync: no library")
	}
	if set.RecordingID == "" {
		return "", fmt.Errorf("upload: missing recording id")
	}
	if up, ok, err := s.lib.GetSetUpload(set.RecordingID); err != nil {
		return "", err
	} else if ok {
		return up.StreamID, nil // already published
	}
	token, err := s.tok()
	if err != nil {
		return "", err
	}
	if len(set.Tracks) == 0 {
		return "", fmt.Errorf("upload: empty set")
	}

	title := set.Title
	if title == "" {
		title = "Recorded set " + set.StartedAt.Local().Format("2006-01-02 15:04")
	}
	meta := map[string]any{
		"software": "rave-mate",
		"recorded": true,
	}
	if !set.StartedAt.IsZero() {
		meta["recorded_started_at"] = set.StartedAt.UTC().Format(time.RFC3339)
	}
	if !set.EndedAt.IsZero() {
		meta["recorded_ended_at"] = set.EndedAt.UTC().Format(time.RFC3339)
	}
	created, err := s.api.CreateStream(ctx, token, api.CreateStreamReq{
		Title: title, Kind: "dj_set", Source: "recorded", Metadata: meta,
	})
	if err != nil {
		return "", fmt.Errorf("create recorded stream: %w", err)
	}

	events := make([]api.IngestEvent, 0, len(set.Tracks))
	for i, t := range set.Tracks {
		deck := t.Deck
		if deck == "" {
			deck = "A"
		}
		st := map[string]any{
			"title": t.Title, "artist": t.Artist, "isPlaying": true,
		}
		if t.Album != "" {
			st["album"] = t.Album
		}
		if t.Key != "" {
			st["key"] = t.Key
		}
		if t.BPM > 0 {
			st["bpm"] = t.BPM
		}
		if !t.StartedAt.IsZero() {
			st["loadedAt"] = t.StartedAt.UnixMilli()
		}
		ev := api.IngestEvent{Type: "deck.loaded", Deck: deck, State: st, Seq: uint64(i + 1)}
		if !t.StartedAt.IsZero() {
			ev.TS = t.StartedAt.UnixMilli()
		}
		events = append(events, ev)
	}
	// Send in batches (mirrors the live publisher's 50/flush cap).
	const batch = 50
	for off := 0; off < len(events); off += batch {
		end := min(off+batch, len(events))
		if err := s.api.Ingest(ctx, created.StreamID, created.PublishToken, events[off:end]); err != nil {
			return "", fmt.Errorf("ingest recorded set: %w", err)
		}
	}
	if err := s.api.EndStream(ctx, created.StreamID, created.PublishToken); err != nil {
		s.warn("end recorded stream", err) // reaper cleans up; the set-log is already ingested
	}

	if err := s.lib.SaveSetUpload(libdb.SetUpload{
		RecordingID: set.RecordingID, StreamID: created.StreamID, TrackCount: len(set.Tracks),
	}); err != nil {
		return created.StreamID, err
	}
	s.info("recorded set uploaded", map[string]any{"recording": set.RecordingID, "stream": created.StreamID, "tracks": len(set.Tracks)})
	return created.StreamID, nil
}

func (s *Syncer) info(msg string, f map[string]any) {
	if s.log != nil {
		s.log.Info(source, msg, f)
	}
}

func (s *Syncer) warn(what string, err error) {
	if s.log != nil {
		s.log.Warn(source, what+" failed", map[string]any{"error": err.Error()})
	}
}
