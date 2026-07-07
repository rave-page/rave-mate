package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Booked-event lookup + filename templating for the rename-from-event action and the
// studio listEvents method. Port of electron/src/main/ipc/automations.ts event matching.
// The events query (/events/?involved_user_id=…) is a freeform list the generated client
// doesn't expose cleanly, so we issue it with the stdlib over the desktop's own creds.
// TLS is verified normally - prod + development.api.rave.page both carry real certs.

// rawEvent is the loose shape of one /events/ row (snake_case API fields).
type rawEvent struct {
	ID       any    `json:"id"`
	Title    string `json:"title"`
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
	Venue    string `json:"venue_name"`
	Slug     string `json:"slug"`
}

func httpGetJSON(ctx context.Context, target *url.URL, token string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 240))
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

// fetchUserEvents resolves the caller's id via /auth/me then lists their involved events.
func fetchUserEvents(ctx context.Context, apiBaseURL, token string) ([]rawEvent, error) {
	if apiBaseURL == "" || token == "" {
		return nil, fmt.Errorf("missing apiBaseUrl/token")
	}
	base, err := url.Parse(apiBaseURL)
	if err != nil {
		return nil, err
	}
	var me struct {
		ID string `json:"id"`
	}
	if err := httpGetJSON(ctx, base.ResolveReference(&url.URL{Path: "/auth/me"}), token, &me); err != nil {
		return nil, fmt.Errorf("auth/me: %w", err)
	}
	if me.ID == "" {
		return nil, fmt.Errorf("could not resolve user id")
	}
	q := url.Values{"involved_user_id": {me.ID}, "limit": {"200"}}
	evURL := base.ResolveReference(&url.URL{Path: "/events/", RawQuery: q.Encode()})
	var raw json.RawMessage
	if err := httpGetJSON(ctx, evURL, token, &raw); err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	return decodeEventList(raw), nil
}

// decodeEventList handles both a bare array and {events|items|results|data:[…]} envelopes.
func decodeEventList(raw json.RawMessage) []rawEvent {
	var arr []rawEvent
	if json.Unmarshal(raw, &arr) == nil && arr != nil {
		return arr
	}
	var env map[string]json.RawMessage
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	for _, k := range []string{"events", "items", "results", "data"} {
		if v, ok := env[k]; ok {
			var got []rawEvent
			if json.Unmarshal(v, &got) == nil {
				return got
			}
		}
	}
	return nil
}

func (e rawEvent) idStr() string {
	switch v := e.ID.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	}
	return ""
}

// pickMatchingEvent returns the booked event whose window (±buffer) contains fileMs, closest
// start wins. nil if none.
func pickMatchingEvent(events []rawEvent, fileMs int64, bufferMinutes int) *MatchedEvent {
	bufMs := int64(math.Max(0, float64(bufferMinutes))) * 60_000
	var best *rawEvent
	var bestScore int64 = math.MaxInt64
	for i := range events {
		ev := &events[i]
		startMs, ok := parseMs(ev.StartsAt)
		if !ok {
			continue
		}
		endMs, hasEnd := parseMs(ev.EndsAt)
		upper := startMs + 4*3600_000
		if hasEnd {
			upper = endMs
		}
		lower := startMs - bufMs
		upper += bufMs
		if fileMs < lower || fileMs > upper {
			continue
		}
		score := absI64(fileMs - startMs)
		if score < bestScore {
			best, bestScore = ev, score
		}
	}
	if best == nil || best.idStr() == "" {
		return nil
	}
	return toMatched(best)
}

func toMatched(ev *rawEvent) *MatchedEvent {
	return &MatchedEvent{
		ID:        ev.idStr(),
		Title:     orStr(ev.Title, ev.idStr()),
		StartsAt:  ptrOrNil(ev.StartsAt),
		EndsAt:    ptrOrNil(ev.EndsAt),
		VenueName: ptrOrNil(ev.Venue),
		Slug:      ptrOrNil(ev.Slug),
	}
}

// ── filename templating ──────────────────────────────────────────────────────

var safeRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func slugify(in, fallback string) string {
	s := strings.Trim(safeRe.ReplaceAllString(strings.TrimSpace(in), "-"), "-")
	if s == "" {
		return fallback
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func sanitizeBasename(name string) string {
	t := strings.TrimLeft(safeRe.ReplaceAllString(name, "-"), ".-")
	if len(t) > 200 {
		t = t[:200]
	}
	if t == "" {
		return "file"
	}
	return t
}

func applyTemplate(tmpl string, date, venueSlug, eventSlug, originalBasename, ext string) string {
	r := strings.NewReplacer(
		"{YYYY-MM-DD}", date,
		"{venueSlug}", venueSlug,
		"{eventSlug}", eventSlug,
		"{originalBasename}", originalBasename,
		"{ext}", ext,
	)
	return sanitizeBasename(r.Replace(tmpl))
}

// ── small helpers ────────────────────────────────────────────────────────────

func parseMs(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		if t, err = time.Parse("2006-01-02T15:04:05", s); err != nil {
			return 0, false
		}
	}
	return t.UnixMilli(), true
}

func absI64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func orStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func ptrOrNil(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
