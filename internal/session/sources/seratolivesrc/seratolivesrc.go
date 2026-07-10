// Package seratolivesrc scrapes a Serato Live Playlist page
// (https://serato.com/playlists/<user>/live) and emits the CURRENT track as a
// master-scope now-playing Observation. This is the REMOTE complement to the local
// file-tail seratosrc: it works with any Serato setup (controllers, all 4 decks) with
// zero local install - the DJ just enables Live Playlists in Serato and makes the page
// Public. Delayed (~10s poll) + text-only (artist/title, no deck/BPM), so it ranks below
// the local + real-time Serato sources.
//
// Staleness caveat (user pain point): the live page keeps showing the LAST session's
// tracks when the DJ isn't broadcasting. We therefore emit ONLY when the parsed track
// CHANGES - never re-assert a stale track as fresh now-playing - and surface a
// live-vs-idle status via the logbus + LastStatus() for the UI/ctl.
package seratolivesrc

import (
	"context"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

const (
	confidence      = 0.55             // remote scrape, delayed + text-only; below file-tail seratosrc (0.8)
	defaultInterval = 10 * time.Second // poll cadence (configurable, floored/capped in New)
	minInterval     = 3 * time.Second  // don't hammer serato.com
	maxInterval     = 5 * time.Minute  //
	httpTimeout     = 15 * time.Second // per-request ceiling (bounded network work)
	maxBody         = 4 << 20          // 4 MiB read cap on the page body
	userAgent       = "rave-mate/1.0 (+https://rave.page)"
	// idleAfter marks the source "idle" in status if no track CHANGE for this long while
	// still polling OK - the page is likely showing a stale past session.
	idleAfter = 5 * time.Minute
)

// tracknameRE matches each <div class="playlist-trackname">…</div>; class may carry extra
// classes and any attr order. Inner text is captured non-greedily up to the first </div>.
var tracknameRE = regexp.MustCompile(`(?is)<div[^>]*\bclass="[^"]*\bplaylist-trackname\b[^"]*"[^>]*>(.*?)</div>`)

// tagRE strips any inner markup (e.g. a nested <span>) from a captured trackname.
var tagRE = regexp.MustCompile(`(?s)<[^>]*>`)

// wsRE collapses whitespace runs (incl. newlines) to a single space.
var wsRE = regexp.MustCompile(`\s+`)

// Config controls the source.
type Config struct {
	URL      string        // full /live URL or a bare Serato username
	Interval time.Duration // poll cadence (0 = default; clamped to [minInterval,maxInterval])
}

// Source polls a Serato Live Playlist page and emits master now-playing.
type Source struct {
	log      *logbus.Bus
	url      string
	interval time.Duration
	client   *http.Client

	mu        sync.Mutex
	last      string    // last-emitted "artist - title" (dedupe key; "" = nothing emitted yet)
	changedAt time.Time // when the track last CHANGED (= last-updated time for status)
	status    string    // human status for UI/ctl: "off"|"waiting"|"live"|"idle"|"private"|"error: …"
}

// New builds the source. rawURL is a full https://serato.com/playlists/<user>/live URL
// or a bare username (expanded to that URL). interval 0 = default.
func New(log *logbus.Bus, cfg Config) *Source {
	iv := cfg.Interval
	if iv <= 0 {
		iv = defaultInterval
	}
	if iv < minInterval {
		iv = minInterval
	}
	if iv > maxInterval {
		iv = maxInterval
	}
	return &Source{
		log:      log,
		url:      NormalizeURL(cfg.URL),
		interval: iv,
		client:   &http.Client{Timeout: httpTimeout},
		status:   "off",
	}
}

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceSeratoLive }

// Capabilities implements session.Source: master title/artist + play state (text-only).
func (s *Source) Capabilities() []session.Capability {
	return []session.Capability{
		{Scope: session.ScopeMaster, Fields: []string{session.FieldTitle, session.FieldArtist, session.FieldIsPlaying}},
	}
}

// LastStatus returns the current live/idle/error status for the UI/ctl (thread-safe).
func (s *Source) LastStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Start polls the live page until ctx is cancelled. No-op (blocks) if no URL configured.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	if s.url == "" {
		s.setStatus("off")
		s.log.Warn(session.SourceSeratoLive, "no playlist URL configured", nil)
		<-ctx.Done()
		return nil
	}
	s.setStatus("waiting")
	s.log.Info(session.SourceSeratoLive, "polling live playlist", map[string]any{"url": s.url, "intervalSec": int(s.interval / time.Second)})
	t := time.NewTicker(s.interval)
	defer t.Stop()
	s.poll(ctx, emit) // immediate first poll (don't wait a full interval)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.poll(ctx, emit)
		}
	}
}

// poll fetches + parses the page once and emits only when the current track CHANGED.
func (s *Source) poll(ctx context.Context, emit func(session.Observation)) {
	body, err := s.fetch(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down - not an error
		}
		if s.setStatus("error: " + err.Error()) {
			s.log.Debug(session.SourceSeratoLive, "fetch failed", map[string]any{"err": err.Error()})
		}
		return // retry next tick; never crash the source
	}
	artist, title, raw, ok := parseCurrentTrack(body)
	if !ok {
		// No trackname divs = empty or Private page. Don't emit; log the transition only.
		if s.setStatus("private") {
			s.log.Debug(session.SourceSeratoLive, "no tracks on page (Private, or not live yet?)", map[string]any{"url": s.url})
		}
		return
	}
	s.mu.Lock()
	changed := raw != s.last
	if changed {
		s.last = raw
		s.changedAt = time.Now()
	}
	stale := time.Since(s.changedAt) > idleAfter
	s.mu.Unlock()

	if !changed {
		// Same track as last poll: the page may just be showing a past session. Never
		// re-emit a stale track as fresh now-playing; only update the status.
		if stale {
			s.setStatus("idle")
		}
		return
	}
	s.setStatus("live")
	fields := map[string]any{session.FieldTitle: title, session.FieldIsPlaying: true}
	if artist != "" {
		fields[session.FieldArtist] = artist
	}
	// Loaded=true: a track change is a new-now-playing boundary (full scope replacement).
	emit(session.Observation{
		Source:     session.SourceSeratoLive,
		Scope:      session.Scope{Kind: session.ScopeMaster},
		Fields:     fields,
		Confidence: confidence,
		Loaded:     true,
	})
	s.log.Info(session.SourceSeratoLive, "now playing", map[string]any{"artist": artist, "title": title})
}

// fetch GETs the page with a real UA + bounded body read.
func (s *Source) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", &httpError{code: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// setStatus stores the status and reports whether it changed (callers gate
// per-tick logs on the transition so a Private/idle page doesn't spam).
func (s *Source) setStatus(v string) bool {
	s.mu.Lock()
	changed := s.status != v
	s.status = v
	s.mu.Unlock()
	return changed
}

// httpError carries a non-200 status for the status line.
type httpError struct{ code int }

func (e *httpError) Error() string { return "HTTP " + strconv.Itoa(e.code) }

// parseCurrentTrack extracts the CURRENT track: the LAST <div class="playlist-trackname">
// on the page (Serato appends played tracks top-to-bottom; the newest is last). Splits the
// text on the FIRST " - " → artist / title. No hyphen = whole string is the title. Returns
// ok=false when the page has no trackname divs (empty / Private page). raw = the normalized
// full trackname (dedupe key).
func parseCurrentTrack(pageHTML string) (artist, title, raw string, ok bool) {
	m := tracknameRE.FindAllStringSubmatch(pageHTML, -1)
	if len(m) == 0 {
		return "", "", "", false
	}
	inner := m[len(m)-1][1]                   // last occurrence = current track
	inner = tagRE.ReplaceAllString(inner, "") // strip nested markup
	inner = html.UnescapeString(inner)        // decode &amp; &#39; etc.
	inner = strings.TrimSpace(wsRE.ReplaceAllString(inner, " "))
	if inner == "" {
		return "", "", "", false
	}
	raw = inner
	if i := strings.Index(inner, " - "); i >= 0 {
		artist = strings.TrimSpace(inner[:i])
		title = strings.TrimSpace(inner[i+3:])
		if title == "" { // trailing " - " with nothing after → treat whole as title
			artist, title = "", inner
		}
	} else {
		title = inner
	}
	return artist, title, raw, true
}

// NormalizeURL turns a bare username or a serato.com playlist URL into the canonical /live
// URL. Empty input → "". A username (no scheme/host) → https://serato.com/playlists/<u>/live.
// A serato.com URL missing the trailing /live gets it appended.
func NormalizeURL(in string) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return ""
	}
	low := strings.ToLower(in)
	if strings.Contains(low, "serato.com") || strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		u := strings.TrimRight(in, "/")
		if !strings.HasSuffix(strings.ToLower(u), "/live") {
			u += "/live"
		}
		return u
	}
	// bare username
	return "https://serato.com/playlists/" + strings.Trim(in, "/") + "/live"
}
