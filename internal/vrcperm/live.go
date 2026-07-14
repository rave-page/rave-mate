package vrcperm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"rave.page/mate/internal/github"
	"rave.page/mate/internal/matebridge"
)

// This file adds the rave.live/* gist envelope writer (Channel 2 of the world-bridge contract) on
// top of the flat allow.txt/posters.json feeds. rave-mate is the SOLE writer. Every enveloped write
// is diff-only on the INNER payload (seq/updatedAt excluded from the hash) so unchanged data never
// bumps the seq - preserving the world's strictly-increasing SEQ-GATE across restarts (the seq comes
// from a persisted SeqCounter, keyed per module). See docs/WORLD_BRIDGE_CONTRACT.md.

// PublishPointer writes the rave.live/pointer single-module gist (the instance link: who opened the
// instance + operator table + off-world join affordance). No-op (records an error) when GitHub isn't
// linked or no seq counter is wired. Stamps ConfigURL to the config-module gist when one exists.
func (s *Service) PublishPointer(ctx context.Context, p matebridge.PointerModule) {
	f := s.cfg()
	if f.ConfigGistID != "" && p.ConfigURL == "" {
		p.ConfigURL = github.RawURL(s.owner(), f.ConfigGistID, matebridge.ModuleConfig+".json")
	}
	s.publishModule(ctx, "pointer", matebridge.SchemaPointer, matebridge.ModulePointer, p, &f.PointerGistID, "rave-mate world pointer")
}

// PublishConfig writes the rave.live/config single-module gist (the active group's dotted-key ->
// JSON-string settings).
func (s *Service) PublishConfig(ctx context.Context, c matebridge.ConfigModule) {
	f := s.cfg()
	s.publishModule(ctx, "config", matebridge.SchemaConfig, matebridge.ModuleConfig, c, &f.ConfigGistID, "rave-mate world config")
}

// PublishPerformers writes the rave.live/performers single-module gist (the Twitch who-is-live
// roster). rave-mate decides live off-world + scrubs before writing (the world only plays sanctioned
// stream URLs).
func (s *Service) PublishPerformers(ctx context.Context, m matebridge.PerformersLiveModule) {
	f := s.cfg()
	s.publishModule(ctx, "performers", matebridge.SchemaPerformers, matebridge.ModulePerformers, m, &f.PerformersGistID, "rave-mate world performers")
}

// maybePublishPointer publishes the instance pointer when enabled + a provider is set (called from
// RefreshAll). Kept unexported so the refresher owns the cadence.
func (s *Service) maybePublishPointer(ctx context.Context) {
	f := s.cfg()
	if !f.PointerOn {
		return
	}
	s.mu.Lock()
	fn := s.pointer
	s.mu.Unlock()
	if fn == nil {
		return
	}
	p, ok := fn()
	if !ok {
		return
	}
	s.PublishPointer(ctx, p)
}

// publishModule writes one enveloped single-module gist, diff-only on the inner payload. On an
// actual write it issues the next per-module seq (strictly increasing), wraps the payload in the
// common envelope, and persists the gist id. mainFile is "<moduleKey>.json".
func (s *Service) publishModule(ctx context.Context, key, schema, moduleKey string, payload any, gistID *string, desc string) {
	if s.gists() == nil || s.owner() == "" {
		s.setErr(key, "GitHub not linked")
		return
	}
	if s.seq == nil {
		s.setErr(key, "seq counter unavailable")
		return
	}
	inner, err := json.Marshal(payload)
	if err != nil {
		s.setErr(key, err.Error())
		return
	}
	// Hash the INNER payload only: seq + updatedAt change every write, so hashing the envelope would
	// defeat diff-only and bump the seq forever.
	h := hashBytes(inner)
	s.mu.Lock()
	unchanged := s.lastHash[key] == h && *gistID != ""
	s.mu.Unlock()
	mainFile := moduleKey + ".json"
	if unchanged {
		s.mu.Lock()
		st := s.status[key]
		st.Skipped, st.Err = true, ""
		s.status[key] = st
		s.mu.Unlock()
		return
	}

	seq := s.seq.Next(key)
	env, err := matebridge.MarshalSingle(schema, seq, time.Now().UTC().Format(time.RFC3339), moduleKey, inner)
	if err != nil {
		s.setErr(key, err.Error())
		return
	}
	files := map[string]string{mainFile: string(env)}
	g, err := s.writeGist(ctx, *gistID, desc, files)
	if err != nil {
		s.setErr(key, err.Error())
		return
	}
	if g.ID != *gistID {
		*gistID = g.ID
		if s.save != nil {
			s.save()
		}
	}
	s.mu.Lock()
	s.lastHash[key] = h
	s.status[key] = PubStatus{URL: github.RawURL(s.owner(), g.ID, mainFile), HTMLURL: g.HTMLURL, When: time.Now()}
	s.mu.Unlock()
	s.log.Info(source, "published", map[string]any{"target": key, "seq": seq})
}

// PublishRoster publishes an editor-handed display-name roster (POST /v1/worldsync/gist) as a gist
// and returns the world-facing URLs + the assigned seq. The gist carries the FLAT allow.txt +
// allow.json (RaveAccessControl / VideoTXL consume the flat list - it is NOT enveloped); seq is
// provenance/ordering for the editor. Diff-only: an unchanged roster returns the current gist + last
// seq without a write. rave-mate owns the GitHub token; the editor never does.
func (s *Service) PublishRoster(ctx context.Context, kind, name string, names []string) (gistID, rawURL, jsonURL string, seq int64, err error) {
	if kind == "" {
		kind = "perm"
	}
	if kind != "perm" {
		return "", "", "", 0, fmt.Errorf("unsupported roster kind %q", kind)
	}
	owner := s.owner()
	if s.gists() == nil || owner == "" {
		return "", "", "", 0, fmt.Errorf("GitHub not linked")
	}
	if s.seq == nil {
		return "", "", "", 0, fmt.Errorf("seq counter unavailable")
	}
	slug := rosterSlug(name)
	key := "roster:" + slug
	files := map[string]string{FileNames: FormatNames(names), FileJSON: FormatJSON(name, names)}
	h := hashFiles(files)

	f := s.cfg()
	if f.RosterGists == nil {
		f.RosterGists = map[string]string{}
	}
	id := f.RosterGists[slug]

	s.mu.Lock()
	unchanged := s.lastHash[key] == h && id != ""
	s.mu.Unlock()
	if unchanged {
		return id, github.RawURL(owner, id, FileNames), github.RawURL(owner, id, FileJSON), s.seq.Peek(key), nil
	}

	g, werr := s.writeGist(ctx, id, "rave-mate world roster: "+name, files)
	if werr != nil {
		s.setErr(key, werr.Error())
		return "", "", "", 0, werr
	}
	seq = s.seq.Next(key)
	f.RosterGists[slug] = g.ID
	if s.save != nil {
		s.save()
	}
	s.mu.Lock()
	s.lastHash[key] = h
	s.status[key] = PubStatus{URL: github.RawURL(owner, g.ID, FileNames), HTMLURL: g.HTMLURL, When: time.Now()}
	s.mu.Unlock()
	s.log.Info(source, "published roster", map[string]any{"slug": slug, "seq": seq, "count": len(names)})
	return g.ID, github.RawURL(owner, g.ID, FileNames), github.RawURL(owner, g.ID, FileJSON), seq, nil
}

// hashBytes digests a single blob (envelope inner payload).
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// rosterSlug lowercases name to a stable [a-z0-9-] slug so re-publishing the same-named roster
// reuses its gist (the world bakes the raw URL at author time - a new gist per publish would break
// it). Empty/all-illegal input => "roster".
func rosterSlug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "roster"
	}
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-")
	}
	return out
}
