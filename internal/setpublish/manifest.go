// Package setpublish turns a finished local DJ set into a rave.page recording: it joins the
// recorder tracklist, the linked capture file, the cached waveform/loudness analysis and the
// resolved canonical track ids into one SetManifest, then publishes it (audio via the
// supervised publish worker, metadata via the recordings API).
//
// Replaces the backdated live-stream ingest fallback (playsync.UploadRecordedSet), which stays
// in place, deprecated, until the dedicated endpoints are live everywhere.
//
// Privacy invariant (playsync/mediasync.go): local file paths NEVER go on the wire. The only
// path in the manifest is AudioRef.LocalPath, which is `json:"-"` and is handed to the worker
// child only; what ships is the basename, as file_name.
//
// Threading: Assemble reads the DB, reads the analysis cache and may run worker jobs that take
// minutes. It is NEVER safe on the act lane - callers run it from u.bg / a debuglog.Go worker.
package setpublish

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/shared/logbus"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/worker"
)

const source = "setpublish"

// peaksBinRateHz matches the publish player's request so an already-analysed set is a cache hit
// rather than a second multi-minute decode.
const peaksBinRateHz = 100

// peaksContractVer pins the cached peaks blob shape written by internal/webui (mpPeakBlob /
// mpPeakContractVer). A blob below this - or shaped differently - is treated as a miss and
// re-analysed: degraded (slower), never wrong.
const peaksContractVer = 2

// SetManifest is everything a finished set needs to become a rave.page recording.
type SetManifest struct {
	RecordingID string    `json:"recordingId"` // LOCAL recorder Recording.ID
	Title       string    `json:"title"`
	StartedAt   time.Time `json:"startedAt"` // capture media t=0 - the audio file's origin
	EndedAt     time.Time `json:"endedAt"`
	DurationMs  int64     `json:"durationMs"`

	Audio    AudioRef        `json:"audio"`
	Tracks   []ManifestTrack `json:"tracks"`
	Waveform *WaveformRef    `json:"waveform,omitempty"`
	Loudness *LoudnessRef    `json:"loudness,omitempty"`

	// Warnings are non-fatal gaps the UI shows before the user commits (no waveform, no
	// loudness, tracks clamped to t=0, …).
	Warnings []string `json:"warnings,omitempty"`
}

// AudioRef is the capture file backing the set. LocalPath is worker-side only.
type AudioRef struct {
	LocalPath string `json:"-"`    // NEVER marshaled - the privacy invariant
	Name      string `json:"name"` // basename; the only path-derived string that ships
	SizeBytes int64  `json:"sizeBytes"`
	MimeType  string `json:"mimeType"`
	CaptureID string `json:"captureId"`
	Kind      string `json:"kind"` // libdb.SetKind*
}

// ManifestTrack is one tracklist entry with offsets already resolved against the capture.
type ManifestTrack struct {
	Number           int     `json:"number"`
	Title            string  `json:"title"`
	Artist           string  `json:"artist,omitempty"`
	Album            string  `json:"album,omitempty"`
	Key              string  `json:"key,omitempty"`
	BPM              float64 `json:"bpm,omitempty"`
	Deck             string  `json:"deck,omitempty"`
	StartOffsetMs    int64   `json:"startOffsetMs"`
	EndOffsetMs      int64   `json:"endOffsetMs,omitempty"`
	CanonicalTrackID string  `json:"canonicalTrackId,omitempty"`
	LUFS             float64 `json:"lufs,omitempty"` // per-track integrated estimate
	HasLUFS          bool    `json:"hasLufs,omitempty"`
}

// WaveformRef is the set's peak overview (worker probe.peaks output).
type WaveformRef struct {
	PeaksB64   string `json:"peaksB64"`
	BandsB64   string `json:"bandsB64,omitempty"` // 3 bytes/bucket spectral colour
	DurationMs int    `json:"durationMs"`
	Buckets    int    `json:"buckets"`
}

// LoudnessRef is the EBU R128 summary + momentary timeline (worker transcode.loudtl output).
// MomentaryB64 is base64 of a little-endian float32 array, one sample per StepMs.
type LoudnessRef struct {
	IntegratedLUFS float64 `json:"integratedLufs"`
	TruePeakDB     float64 `json:"truePeakDb"`
	LRA            float64 `json:"lra"`
	StepMs         int     `json:"stepMs"`
	MomentaryB64   string  `json:"momentaryB64"`
	Samples        int     `json:"samples"`
}

// Recordings is the recorder subset the assembler needs (an interface so it is testable
// without a live Recorder).
type Recordings interface {
	Get(id string) (recorder.Recording, bool)
}

// JobRunner is the worker-supervisor subset used here (satisfied by *worker.Supervisor).
type JobRunner interface {
	RunBackground(ctx context.Context, typ, method string, params any) (json.RawMessage, error)
	RunStreamBackground(ctx context.Context, typ, method string, params any, onProgress worker.ProgressFunc) (json.RawMessage, error)
}

// Assembler builds SetManifests. lib/st/jobs may each be nil; the manifest then simply carries
// fewer parts and says so in Warnings.
type Assembler struct {
	rec  Recordings
	lib  *libdb.DB
	st   *store.Store
	jobs JobRunner
	log  *logbus.Bus
}

// NewAssembler wires the assembler.
func NewAssembler(rec Recordings, lib *libdb.DB, st *store.Store, jobs JobRunner, log *logbus.Bus) *Assembler {
	return &Assembler{rec: rec, lib: lib, st: st, jobs: jobs, log: log}
}

// Assemble builds the manifest for one finished recording: capture file + per-track offsets +
// canonical ids + waveform + loudness (analysing on demand when the cache misses). Slow by
// nature - never call it on the act lane.
func (a *Assembler) Assemble(ctx context.Context, recordingID string) (SetManifest, error) {
	if a == nil || a.rec == nil {
		return SetManifest{}, fmt.Errorf("setpublish: no recorder")
	}
	r, ok := a.rec.Get(recordingID)
	if !ok {
		return SetManifest{}, fmt.Errorf("setpublish: recording %s not found", recordingID)
	}
	if r.EndedAt.IsZero() {
		return SetManifest{}, fmt.Errorf("setpublish: recording is still running")
	}
	cap, err := a.pickCapture(recordingID)
	if err != nil {
		return SetManifest{}, err
	}
	fi, err := os.Stat(cap.Path)
	if err != nil {
		return SetManifest{}, fmt.Errorf("setpublish: capture file unreadable: %w", err)
	}

	m := SetManifest{
		RecordingID: r.ID,
		Title:       strings.TrimSpace(r.Name),
		StartedAt:   cap.StartedAt,
		EndedAt:     cap.EndedAt,
		Audio: AudioRef{
			LocalPath: cap.Path,
			Name:      filepath.Base(cap.Path),
			SizeBytes: fi.Size(),
			MimeType:  MimeForPath(cap.Path),
			CaptureID: cap.ID,
			Kind:      cap.Kind,
		},
	}
	if m.Title == "" {
		m.Title = "Recorded set " + cap.StartedAt.Local().Format("2006-01-02 15:04")
	}
	if m.EndedAt.IsZero() {
		m.EndedAt = r.EndedAt
	}
	if !m.EndedAt.IsZero() && m.EndedAt.After(m.StartedAt) {
		m.DurationMs = m.EndedAt.Sub(m.StartedAt).Milliseconds()
	}

	mtime := fi.ModTime().Unix()
	if wf, werr := a.resolveWaveform(ctx, cap.Path, mtime); werr != nil {
		a.warn("waveform", werr)
		m.Warnings = append(m.Warnings, "waveform unavailable: "+werr.Error())
	} else {
		m.Waveform = wf
		if wf.DurationMs > 0 {
			m.DurationMs = int64(wf.DurationMs) // decoded duration beats the wall-clock window
		}
	}
	tl, lerr := a.resolveLoudness(ctx, cap.Path, mtime)
	if lerr != nil {
		a.warn("loudness", lerr)
		m.Warnings = append(m.Warnings, "loudness unavailable: "+lerr.Error())
	} else {
		m.Loudness = loudnessRef(tl)
	}

	m.Tracks, m.Warnings = a.buildTracks(r, cap, tl, m.DurationMs, m.Warnings)
	if len(m.Tracks) == 0 {
		return m, fmt.Errorf("setpublish: recording has no tracks")
	}
	return m, nil
}

// pickCapture returns the capture file to publish: the newest linked capture whose file is
// actually on disk (SetRecordingsFor is newest-first).
func (a *Assembler) pickCapture(recordingID string) (libdb.SetRecording, error) {
	if a.lib == nil {
		return libdb.SetRecording{}, fmt.Errorf("setpublish: no library")
	}
	caps, err := a.lib.SetRecordingsFor(recordingID)
	if err != nil {
		return libdb.SetRecording{}, err
	}
	if len(caps) == 0 {
		return libdb.SetRecording{}, fmt.Errorf("setpublish: no capture file linked to this set")
	}
	for _, c := range caps {
		if c.Path == "" {
			continue
		}
		if _, serr := os.Stat(c.Path); serr == nil {
			return c, nil
		}
	}
	return libdb.SetRecording{}, fmt.Errorf("setpublish: linked capture file is missing from disk")
}

// buildTracks resolves per-track offsets (start − capture start, clamped >= 0), canonical ids
// and per-track LUFS. Tracks that start after the audio ends are dropped - they belong to a
// different capture.
func (a *Assembler) buildTracks(r recorder.Recording, cap libdb.SetRecording, tl *worker.LoudTimeline, durMs int64, warn []string) ([]ManifestTrack, []string) {
	out := make([]ManifestTrack, 0, len(r.Tracks))
	clamped, dropped := 0, 0
	for i, t := range r.Tracks {
		if strings.TrimSpace(t.Title) == "" {
			continue
		}
		startMs := t.StartedAt.Sub(cap.StartedAt).Milliseconds()
		if startMs < 0 {
			startMs, clamped = 0, clamped+1
		}
		if durMs > 0 && startMs >= durMs {
			dropped++
			continue
		}
		mt := ManifestTrack{
			Number: len(out) + 1, Title: t.Title, Artist: t.Artist, Album: t.Album,
			Key: t.Key, BPM: t.BPM, Deck: t.Deck, StartOffsetMs: startMs,
		}
		// End offset: the track's own end, else the next track's start.
		endMs := int64(0)
		if !t.EndedAt.IsZero() {
			endMs = t.EndedAt.Sub(cap.StartedAt).Milliseconds()
		} else if i+1 < len(r.Tracks) {
			endMs = r.Tracks[i+1].StartedAt.Sub(cap.StartedAt).Milliseconds()
		}
		if durMs > 0 && endMs > durMs {
			endMs = durMs
		}
		if endMs > startMs {
			mt.EndOffsetMs = endMs
		}
		mt.CanonicalTrackID = a.canonicalID(t.Artist, t.Title)
		if tl != nil && tl.Step > 0 {
			endSec := float64(mt.EndOffsetMs) / 1000
			if mt.EndOffsetMs == 0 {
				endSec = 0 // to the end of the timeline
			}
			if lufs, ok := transcode.IntegrateMomentary(tl.Mom, tl.Step, float64(startMs)/1000, endSec); ok {
				mt.LUFS, mt.HasLUFS = lufs, true
			}
		}
		out = append(out, mt)
	}
	if clamped > 0 {
		warn = append(warn, fmt.Sprintf("%d track(s) started before the capture - clamped to 0:00", clamped))
	}
	if dropped > 0 {
		warn = append(warn, fmt.Sprintf("%d track(s) start after the audio ends - not published", dropped))
	}
	return out, warn
}

// canonicalID looks up the resolved backend track id for a played track ("" when unresolved).
func (a *Assembler) canonicalID(artist, title string) string {
	if a.lib == nil || title == "" {
		return ""
	}
	link, ok, err := a.lib.GetTrackLink(libdb.TrackHash(artist, title, 0))
	if err != nil || !ok || link.Provisional {
		return "" // a provisional id is ours, not canonical - let the backend re-resolve
	}
	return link.TrackID
}

// peaksBlob mirrors the cached peaks payload webui writes (mpPeakBlob). Kept structurally
// tolerant: a shape mismatch is a cache miss, which only costs a re-analysis.
type peaksBlob struct {
	D    float64 `json:"d"`
	P    []byte  `json:"p"`
	B    []byte  `json:"b,omitempty"`
	Rate int     `json:"rate,omitempty"`
	Samp int64   `json:"samp,omitempty"`
	Ver  int     `json:"ver,omitempty"`
}

// resolveWaveform returns the set's peak overview from the analysis cache, running the probe
// worker on a miss (a multi-hour set decodes for minutes).
func (a *Assembler) resolveWaveform(ctx context.Context, path string, mtime int64) (*WaveformRef, error) {
	if a.st != nil {
		if raw, ok := a.st.GetAnalysis(store.KindPeaks, path, mtime); ok {
			var b peaksBlob
			if json.Unmarshal(raw, &b) == nil && len(b.P) > 0 && b.Ver >= peaksContractVer && b.D > 0 {
				return &WaveformRef{
					PeaksB64:   base64.StdEncoding.EncodeToString(b.P),
					BandsB64:   base64.StdEncoding.EncodeToString(b.B),
					DurationMs: int(math.Round(b.D * 1000)),
					Buckets:    len(b.P),
				}, nil
			}
		}
	}
	if a.jobs == nil {
		return nil, fmt.Errorf("no worker runtime")
	}
	raw, err := a.jobs.RunBackground(ctx, "probe", "probe.peaks",
		map[string]any{"path": path, "binRateHz": peaksBinRateHz})
	if err != nil {
		return nil, err
	}
	var res struct {
		Peaks  string  `json:"peaks"`
		Bands  string  `json:"bands"`
		DurSec float64 `json:"durationSeconds"`
		Rate   int     `json:"rate"`
		Samp   int64   `json:"samples"`
		Lead   float64 `json:"leadSkipMs"`
	}
	if json.Unmarshal(raw, &res) != nil || res.Peaks == "" || res.DurSec <= 0 {
		return nil, fmt.Errorf("empty analysis")
	}
	peaks, derr := base64.StdEncoding.DecodeString(res.Peaks)
	if derr != nil || len(peaks) == 0 {
		return nil, fmt.Errorf("bad peaks payload")
	}
	bands, _ := base64.StdEncoding.DecodeString(res.Bands) // best-effort
	if a.st != nil {
		if b, merr := json.Marshal(peaksBlob{D: res.DurSec, P: peaks, B: bands,
			Rate: res.Rate, Samp: res.Samp, Ver: peaksContractVer}); merr == nil {
			a.st.PutAnalysis(store.KindPeaks, path, mtime, b)
		}
	}
	return &WaveformRef{
		PeaksB64: res.Peaks, BandsB64: res.Bands,
		DurationMs: int(math.Round(res.DurSec * 1000)), Buckets: len(peaks),
	}, nil
}

// resolveLoudness returns the EBU R128 timeline from cache, running transcode.loudtl on a miss.
func (a *Assembler) resolveLoudness(ctx context.Context, path string, mtime int64) (*worker.LoudTimeline, error) {
	if a.st != nil {
		if raw, ok := a.st.GetAnalysis(store.KindLoudnessTL, path, mtime); ok {
			var tl worker.LoudTimeline
			if json.Unmarshal(raw, &tl) == nil && len(tl.Mom) > 0 && tl.Step > 0 {
				return &tl, nil
			}
		}
	}
	if a.jobs == nil {
		return nil, fmt.Errorf("no worker runtime")
	}
	raw, err := a.jobs.RunBackground(ctx, "transcode", "transcode.loudtl", map[string]any{"input": path})
	if err != nil {
		return nil, err
	}
	var tl worker.LoudTimeline
	if json.Unmarshal(raw, &tl) != nil || len(tl.Mom) == 0 || tl.Step <= 0 {
		return nil, fmt.Errorf("empty loudness timeline")
	}
	if a.st != nil {
		a.st.PutAnalysis(store.KindLoudnessTL, path, mtime, raw)
	}
	return &tl, nil
}

// loudnessRef encodes a timeline for the wire: momentary LUFS as little-endian float32.
func loudnessRef(tl *worker.LoudTimeline) *LoudnessRef {
	if tl == nil || len(tl.Mom) == 0 {
		return nil
	}
	buf := make([]byte, 4*len(tl.Mom))
	for i, v := range tl.Mom {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(v)))
	}
	return &LoudnessRef{
		IntegratedLUFS: tl.I, TruePeakDB: tl.TP, LRA: tl.LRA,
		StepMs:       int(math.Round(tl.Step * 1000)),
		MomentaryB64: base64.StdEncoding.EncodeToString(buf),
		Samples:      len(tl.Mom),
	}
}

// MimeForPath maps a capture file extension to its upload MIME type.
func MimeForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac":
		return "audio/flac"
	case ".mp3":
		return "audio/mpeg"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".opus":
		return "audio/opus"
	case ".wav":
		return "audio/wav"
	case ".m4a", ".aac":
		return "audio/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".mp4":
		return "video/mp4"
	}
	return "application/octet-stream"
}

func (a *Assembler) warn(what string, err error) {
	if a.log != nil {
		a.log.Warn(source, what+" failed", map[string]any{"error": err.Error()})
	}
}
