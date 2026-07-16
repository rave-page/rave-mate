package vrcperm

import (
	"context"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/github"
	"rave.page/mate/internal/matebridge"
)

// This file is the live-module publisher seam. Payload building stays shared in live.go
// (PublishPointer/PublishConfig/PublishPerformers build the module structs); only the
// wrap+seq+write step differs by mode:
//   - direct  - rave-mate envelopes the payload (MarshalSingle), issues the seq (gistseq), and
//     writes the gist with the user's own token. rave-mate owns everything.
//   - hosted  - rave-mate sends the RAW payload to rave.page's worldlive API; the SERVER
//     envelopes it + owns the seq + writes the gist under rave.page's service account.
// Both persist the mode-agnostic LiveModules pointer (raw URL + seq) so the rest of the app is
// mode-blind. See docs/WORLD_BRIDGE_CONTRACT.md.

// moduleResult is the outcome of writing one live module.
type moduleResult struct {
	RawURL  string // stable world-facing raw URL (baked into the world)
	HTMLURL string // gist/admin page ("" in hosted mode)
	Seq     int64  // world SEQ-GATE value
	GistID  string // provenance
}

// publisher writes one enveloped live module and reports its world-facing pointer. The
// orchestrator (Service.publishModule) owns the diff-only skip, LiveModules persistence, and
// status; a publisher only readies + writes.
type publisher interface {
	// ready reports whether a publish can proceed in this mode (links/config present).
	ready() (ok bool, reason string)
	// published reports whether key already has a persisted pointer (diff-only needs a prior write).
	published(key string) bool
	// write publishes moduleKey's inner payload; returns the stable raw URL + seq.
	write(ctx context.Context, key, schema, moduleKey, desc string, inner []byte) (moduleResult, error)
}

// HostedClient publishes a raw module payload via rave.page's worldlive API (server owns the
// envelope + seq). Satisfied by an app-side adapter over internal/api. Nil => hosted mode
// unavailable.
type HostedClient interface {
	// Ready reports whether a hosted publish can proceed (signed in + world id configured).
	Ready() (ok bool, reason string)
	// PublishModule PUTs moduleKey's raw payload; returns the stable raw URL, gist id, server seq.
	PublishModule(ctx context.Context, moduleKey string, payload []byte) (rawURL, gistID string, seq int64, err error)
}

// mode returns the effective publish mode (config-driven; default direct).
func (s *Service) mode() string { return s.cfg().ResolvedPublishMode() }

// publisher returns the mode's publisher. A hosted-configured feature with no client wired
// returns a hostedPublisher whose ready() surfaces the misconfig (never a silent direct write).
func (s *Service) publisher() publisher {
	if s.mode() == config.WorldSyncModeHosted {
		return &hostedPublisher{s: s}
	}
	return &directPublisher{s: s}
}

// gistIDPtr maps an internal target key to its persisted gist-id field (direct mode).
func (s *Service) gistIDPtr(key string) *string {
	f := s.cfg()
	switch key {
	case "pointer":
		return &f.PointerGistID
	case "config":
		return &f.ConfigGistID
	case "performers":
		return &f.PerformersGistID
	case "access":
		return &f.AccessGistID
	}
	return nil
}

// liveModule returns the mode-agnostic published pointer for key (zero value if never published).
func (s *Service) liveModule(key string) config.LiveModulePub { return s.cfg().LiveModules[key] }

// recordLiveModule persists key's world-facing pointer + saves config (one write covers both a
// new gist id mutated in-place by the direct writer AND the LiveModules entry).
func (s *Service) recordLiveModule(key string, res moduleResult) {
	f := s.cfg()
	if f.LiveModules == nil {
		f.LiveModules = map[string]config.LiveModulePub{}
	}
	f.LiveModules[key] = config.LiveModulePub{RawURL: res.RawURL, Seq: res.Seq, GistID: res.GistID}
	if s.save != nil {
		s.save()
	}
}

// ── direct publisher (user-token gist; rave-mate envelopes + owns seq) ───────────────────────────

type directPublisher struct{ s *Service }

func (p *directPublisher) ready() (bool, string) {
	if p.s.gists() == nil || p.s.owner() == "" {
		return false, "GitHub not linked"
	}
	if p.s.seq == nil {
		return false, "seq counter unavailable"
	}
	return true, ""
}

func (p *directPublisher) published(key string) bool {
	id := p.s.gistIDPtr(key)
	return id != nil && *id != ""
}

func (p *directPublisher) write(ctx context.Context, key, schema, moduleKey, desc string, inner []byte) (moduleResult, error) {
	// seq issued only here (after the orchestrator's diff-only skip), so unchanged content never
	// advances it - preserving the world's strictly-increasing SEQ-GATE.
	seq := p.s.seq.Next(key)
	env, err := matebridge.MarshalSingle(schema, seq, time.Now().UTC().Format(time.RFC3339), moduleKey, inner)
	if err != nil {
		return moduleResult{}, err
	}
	mainFile := moduleKey + ".json"
	gistID := p.s.gistIDPtr(key)
	prev := ""
	if gistID != nil {
		prev = *gistID
	}
	g, err := p.s.writeGist(ctx, prev, desc, map[string]string{mainFile: string(env)})
	if err != nil {
		return moduleResult{}, err
	}
	if gistID != nil {
		*gistID = g.ID // persisted by recordLiveModule's save (cfg pointer mutated in place)
	}
	return moduleResult{
		RawURL:  github.RawURL(p.s.owner(), g.ID, mainFile),
		HTMLURL: g.HTMLURL,
		Seq:     seq,
		GistID:  g.ID,
	}, nil
}

// ── hosted publisher (rave.page worldlive API; server envelopes + owns seq) ───────────────────────

type hostedPublisher struct{ s *Service }

func (p *hostedPublisher) ready() (bool, string) {
	if p.s.hosted == nil {
		return false, "hosted publish unavailable (no rave.page client)"
	}
	return p.s.hosted.Ready()
}

func (p *hostedPublisher) published(key string) bool { return p.s.liveModule(key).RawURL != "" }

func (p *hostedPublisher) write(ctx context.Context, key, _, moduleKey, _ string, inner []byte) (moduleResult, error) {
	// Send the RAW payload only: the server owns the envelope + seq. moduleKey is the frozen
	// module key the worldlive API keys on (pointer|config|performersLive|…).
	rawURL, gistID, seq, err := p.s.hosted.PublishModule(ctx, moduleKey, inner)
	if err != nil {
		return moduleResult{}, err
	}
	return moduleResult{RawURL: rawURL, Seq: seq, GistID: gistID}, nil
}
