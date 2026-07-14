// Package vrcperm publishes VRChat world feeds as GitHub gists: permission
// lists (user + group-role entries, roles expanded server-side because Udon has
// no runtime group API - see .devnotes/WORLD_INTEGRATIONS_RESEARCH.md) and display
// channels (posters / events / now-playing). Worlds poll the gist raw URLs via
// VRC string loading (gist.githubusercontent.com is allowlisted).
package vrcperm

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/github"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/matebridge"
	"rave.page/mate/internal/vrchat"
)

const source = "worldsync"

// GistStore is the gist surface we need (satisfied by *github.Gists).
type GistStore interface {
	Create(ctx context.Context, desc string, files map[string]string, public bool) (*github.Gist, error)
	Update(ctx context.Context, id, desc string, files map[string]string) (*github.Gist, error)
}

// MemberSource resolves group members (satisfied by *vrchat.Client).
type MemberSource interface {
	GroupMembers(ctx context.Context, groupID, roleID string, offset, n int) ([]vrchat.GroupMember, error)
}

// Event is one upcoming-events row (dates pre-formatted by the feed).
type Event struct {
	Title  string   `json:"title"`
	Date   string   `json:"date"`
	Venue  string   `json:"venue,omitempty"`
	Lineup []string `json:"lineup,omitempty"`
}

// NowPlaying is the live card payload. MUST be fed from the session layer's
// redacted output (ID-redaction feature), never raw deck state.
type NowPlaying struct {
	Live   bool   `json:"live"`
	DJ     string `json:"dj,omitempty"`
	Artist string `json:"artist,omitempty"`
	Track  string `json:"track,omitempty"`
	Link   string `json:"link,omitempty"`
	Img    string `json:"img,omitempty"`
}

// PubStatus is the last publish outcome for one target (UI surface).
type PubStatus struct {
	URL     string    // world-facing raw URL ("" until first publish)
	HTMLURL string    // gist page
	When    time.Time // last successful write
	Skipped bool      // last run was a no-change skip
	Err     string    // last error ("" = ok)
}

// SeqCounter issues persisted, strictly-increasing per-module seq numbers - the world's SEQ-GATE
// (satisfied by *gistseq.Counter). A seq is consumed only on an actual gist write (diff-only), so
// unchanged content never advances it. Nil disables enveloped writes.
type SeqCounter interface {
	Next(module string) int64
	Peek(module string) int64
}

// PointerProvider builds the current rave.live/pointer instance link (instanceOwnerName + operator
// table + join affordance) from rave-mate's live VRChat session + location. ok=false => skip the
// pointer publish this pass (signed out / no known instance).
type PointerProvider func() (matebridge.PointerModule, bool)

// Service owns expansion + publishing + the periodic refresher.
type Service struct {
	log     *logbus.Bus
	cfg     func() *config.WorldSyncFeature
	save    func()              // persist config (created gist ids)
	gists   func() GistStore    // nil while GitHub unlinked
	owner   func() string       // github login ("" while unlinked)
	members func() MemberSource // nil while VRChat unlinked
	events  func(context.Context) []Event
	nowPlay func() NowPlaying
	seq     SeqCounter // nil => enveloped module + roster writes disabled

	pagePause time.Duration // pause between member pages (API etiquette)

	mu       sync.Mutex
	lastHash map[string]string   // target key → published content hash
	expCache map[string][]string // groupID|roleID → last good displayNames
	status   map[string]PubStatus
	onChange []func()
	pointer  PointerProvider // late-bound (needs vrcloc, built after this service); nil => no pointer
}

// Deps wires the service; any nil func is treated as "unavailable".
type Deps struct {
	Log     *logbus.Bus
	Cfg     func() *config.WorldSyncFeature
	Save    func()
	Gists   func() GistStore
	Owner   func() string
	Members func() MemberSource
	Events  func(context.Context) []Event
	NowPlay func() NowPlaying
	Seq     SeqCounter // persisted monotonic per-module seq; nil => enveloped/roster writes off
}

// New builds the service.
func New(d Deps) *Service {
	return &Service{
		log: d.Log, cfg: d.Cfg, save: d.Save, gists: d.Gists, owner: d.Owner,
		members: d.Members, events: d.Events, nowPlay: d.NowPlay, seq: d.Seq,
		pagePause: time.Second,
		lastHash:  map[string]string{}, expCache: map[string][]string{}, status: map[string]PubStatus{},
	}
}

// SetPointerProvider late-binds the pointer builder (it needs the location timeline, constructed
// after this service). Nil clears it. Publishing still requires WorldSync.PointerOn + GitHub linked.
func (s *Service) SetPointerProvider(fn PointerProvider) {
	s.mu.Lock()
	s.pointer = fn
	s.mu.Unlock()
}

// OnChange registers a status hook (UI refresh), called after each publish pass.
func (s *Service) OnChange(fn func()) {
	s.mu.Lock()
	s.onChange = append(s.onChange, fn)
	s.mu.Unlock()
}

func (s *Service) notify() {
	s.mu.Lock()
	fns := append([]func(){}, s.onChange...)
	s.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Status returns the last publish outcome for a target key ("list:<id>",
// "posters", "events", "nowplaying").
func (s *Service) Status(key string) PubStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status[key]
}

// Ready reports whether publishing is possible (enabled + GitHub linked).
func (s *Service) Ready() bool {
	return s.cfg().Enabled && s.gists() != nil && s.owner() != ""
}

// Run is the periodic refresher: lists + posters + events on the configured
// interval (± jitter), now-playing on its own faster cadence. Diff-only writes
// keep GitHub usage minimal. Blocks until ctx is done.
func (s *Service) Run(ctx context.Context) {
	slow := time.NewTimer(jitter(s.cfg().ResolvedRefresh()))
	fast := time.NewTicker(s.cfg().ResolvedNowPlayingEvery())
	defer slow.Stop()
	defer fast.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-slow.C:
			if s.Ready() {
				s.RefreshAll(ctx)
			}
			slow.Reset(jitter(s.cfg().ResolvedRefresh()))
		case <-fast.C:
			if s.Ready() && s.cfg().NowPlayingOn {
				s.PublishNowPlaying(ctx)
				s.notify()
			}
		}
	}
}

// RefreshAll publishes every enabled target once (manual "Publish now" + refresher).
func (s *Service) RefreshAll(ctx context.Context) {
	f := s.cfg()
	for i := range f.Lists {
		s.PublishList(ctx, &f.Lists[i])
	}
	if f.PostersOn {
		s.PublishPosters(ctx)
	}
	if f.EventsOn {
		s.PublishEvents(ctx)
	}
	if f.NowPlayingOn {
		s.PublishNowPlaying(ctx)
	}
	s.maybePublishPointer(ctx) // rave.live/pointer instance link (enveloped; own enablement flag)
	s.notify()
}

// jitter returns d ± 10% (spreads API load across instances/restarts).
func jitter(d time.Duration) time.Duration {
	f := 0.9 + 0.2*rand.Float64() //nolint:gosec // schedule spread, not crypto
	return time.Duration(float64(d) * f)
}
