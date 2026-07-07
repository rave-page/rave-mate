package virtualdjsrc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rave.page/mate/internal/session"
)

const (
	netCtlConfidence = 0.85
	netCtlPoll       = 500 * time.Millisecond
	netCtlBackoff    = 5 * time.Second
	netCtlTimeout    = 800 * time.Millisecond
	defaultNetCtlURL = "http://127.0.0.1:8082" // VirtualDJ Network Control plugin default
)

// netCtlTarget maps a vdjscript deck reference to the scope its readings populate.
type netCtlTarget struct {
	ref   string
	scope session.Scope
}

// runNetCtl polls the Network Control plugin until ctx is cancelled. A connection error =
// "plugin not present": log once, back off (don't spam).
func (s *Source) runNetCtl(ctx context.Context, emit func(session.Observation)) {
	base := strings.TrimRight(s.cfg.NetCtlURL, "/")
	if base == "" {
		base = defaultNetCtlURL
	}
	client := &http.Client{Timeout: netCtlTimeout}
	targets := []netCtlTarget{
		{ref: "master", scope: session.Scope{Kind: session.ScopeMaster}},
		{ref: "1", scope: session.Scope{Kind: session.ScopeDeck, ID: "A"}},
		{ref: "2", scope: session.Scope{Kind: session.ScopeDeck, ID: "B"}},
	}
	last := map[string]string{} // scope key → "artist|title" (the Loaded boundary)
	down := false
	t := time.NewTicker(netCtlPoll)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		pollErr := false
		for _, tg := range targets {
			obs, changed, err := s.pollNetCtl(ctx, client, base, tg, last)
			if err != nil {
				pollErr = true
				break
			}
			if obs != nil {
				obs.Loaded = changed
				emit(*obs)
			}
		}

		if pollErr {
			if !down {
				down = true
				s.log.Info(netCtlTag, "Network Control plugin not reachable; backing off", map[string]any{"url": base})
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(netCtlBackoff):
			}
			continue
		}
		if down {
			down = false
			s.log.Info(netCtlTag, "Network Control plugin reachable", map[string]any{"url": base})
		}
	}
}

// pollNetCtl runs the 5 queries for one target. Returns the observation (nil if no fields),
// whether the track changed (Loaded boundary), and the first transport error.
func (s *Source) pollNetCtl(ctx context.Context, c *http.Client, base string, tg netCtlTarget, last map[string]string) (*session.Observation, bool, error) {
	at, err := s.queryNetCtl(ctx, c, base, "deck "+tg.ref+" get_artist_title")
	if err != nil {
		return nil, false, err
	}
	bpmStr, err := s.queryNetCtl(ctx, c, base, "deck "+tg.ref+" get_bpm")
	if err != nil {
		return nil, false, err
	}
	keyStr, err := s.queryNetCtl(ctx, c, base, "deck "+tg.ref+" get_key")
	if err != nil {
		return nil, false, err
	}
	timeStr, err := s.queryNetCtl(ctx, c, base, "deck "+tg.ref+" get_time elapsed")
	if err != nil {
		return nil, false, err
	}
	playStr, err := s.queryNetCtl(ctx, c, base, "deck "+tg.ref+" is playing")
	if err != nil {
		return nil, false, err
	}

	artist, title := splitArtistTitle(at)
	fields := map[string]any{session.FieldIsPlaying: parseBool(playStr)}
	if title != "" {
		fields[session.FieldTitle] = title
	}
	if artist != "" {
		fields[session.FieldArtist] = artist
	}
	if bpm := parseFloat(bpmStr); bpm > 0 { // plugin reports real bpm (not the db.xml seconds-per-beat)
		fields[session.FieldBPM] = bpm
	}
	if k := strings.TrimSpace(keyStr); k != "" {
		fields[session.FieldKey] = k
	}
	if el := parseFloat(timeStr); el > 0 { // get_time returns milliseconds
		fields[session.FieldElapsedTime] = el / 1000.0
	}

	changed := false
	if title != "" || artist != "" {
		sig := artist + "|" + title
		if last[tg.scope.Key()] != sig {
			changed = true
			last[tg.scope.Key()] = sig
		}
	}
	return &session.Observation{
		Source:     session.SourceVDJNetCtl,
		Scope:      tg.scope,
		Fields:     fields,
		Confidence: netCtlConfidence,
	}, changed, nil
}

// queryNetCtl GETs {base}/query?script=<url-encoded vdjscript> and returns the plain-text body.
func (s *Source) queryNetCtl(ctx context.Context, c *http.Client, base, script string) (string, error) {
	u := base + "/query?script=" + url.QueryEscape(script)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if s.cfg.NetCtlAuth != "" {
		req.Header.Set("Authorization", s.cfg.NetCtlAuth)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("netctl status %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(body)), nil
}
