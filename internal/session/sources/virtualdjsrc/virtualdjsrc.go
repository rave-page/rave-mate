// Package virtualdjsrc is the VirtualDJ session source. It is ONE session.Source
// (session.SourceVirtualDJ) that internally runs up to three independent live backends,
// each tagging its Observations with a distinct provenance Source so the merger ranks the
// fields separately:
//
//	Backend 1 - Network Control plugin (session.SourceVDJNetCtl, ~0.85): polls the plugin's
//	            HTTP /query?script=<vdjscript> for full live metadata (title/artist/bpm/key/play).
//	Backend 2 - OS2L server (session.SourceVDJOS2L, ~0.7): we advertise _os2l._tcp via mDNS +
//	            run a TCP listener; VirtualDJ connects and streams beat events → BPM only.
//	Backend 3 - History tracklist (session.SourceVDJHistory, ~0.5): polls the newest file in
//	            Documents\VirtualDJ\History and emits the last logged track (title/artist, laggy).
//
// Mirrors midisrc/prodjlinksrc: one source, multiple internal decoders with their own
// provenance. STDLIB ONLY - the mDNS responder (os2l.go/mdns.go) is hand-rolled.
package virtualdjsrc

import (
	"context"
	"strconv"
	"strings"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

const (
	logTag     = "virtualdj"
	netCtlTag  = "virtualdj.netctl"
	os2lTag    = "virtualdj.os2l"
	historyTag = "virtualdj.history"
)

// Config selects + parameterizes the three backends. A disabled backend has zero footprint.
type Config struct {
	NetCtl      bool   // enable the Network Control plugin HTTP poller
	NetCtlURL   string // plugin base URL (empty → defaultNetCtlURL)
	NetCtlAuth  string // optional Authorization header value
	OS2L        bool   // enable the OS2L server (mDNS advertise + TCP listen)
	OS2LPort    int    // OS2L TCP port (0 → OS-assigned, advertised via mDNS)
	Tracklist   bool   // enable the History tracklist fallback
	DatabaseDir string // VirtualDJ dir for History\ (empty → virtualdj.DefaultDir)
}

// Source is the VirtualDJ aggregate source.
type Source struct {
	log *logbus.Bus
	cfg Config
}

// New builds the source.
func New(log *logbus.Bus, cfg Config) *Source { return &Source{log: log, cfg: cfg} }

// ID implements session.Source. The per-backend provenance tags (virtualdj.netctl etc.)
// live on the observations, not here - this is the aggregator liveness key.
func (s *Source) ID() string { return session.SourceVirtualDJ }

// Capabilities implements session.Source: the union of the enabled backends' fields.
func (s *Source) Capabilities() []session.Capability {
	var caps []session.Capability
	if s.cfg.NetCtl {
		fields := []string{session.FieldTitle, session.FieldArtist, session.FieldBPM,
			session.FieldKey, session.FieldIsPlaying, session.FieldElapsedTime}
		caps = append(caps,
			session.Capability{Scope: session.ScopeMaster, Fields: fields},
			session.Capability{Scope: session.ScopeDeck, IDs: []string{"A", "B"}, Fields: fields},
		)
	}
	if s.cfg.OS2L {
		caps = append(caps, session.Capability{Scope: session.ScopeMaster, Fields: []string{session.FieldBPM}})
	}
	if s.cfg.Tracklist {
		caps = append(caps, session.Capability{Scope: session.ScopeMaster, Fields: []string{session.FieldTitle, session.FieldArtist}})
	}
	return caps
}

// Start launches each enabled backend as a guarded goroutine and blocks until ctx is done.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	started := false
	if s.cfg.NetCtl {
		started = true
		debuglog.Go(s.log, netCtlTag, func() { s.runNetCtl(ctx, emit) })
	}
	if s.cfg.OS2L {
		started = true
		debuglog.Go(s.log, os2lTag, func() { s.runOS2L(ctx, emit) })
	}
	if s.cfg.Tracklist {
		started = true
		debuglog.Go(s.log, historyTag, func() { s.runHistory(ctx, emit) })
	}
	if !started {
		s.log.Warn(logTag, "no VirtualDJ backends enabled; source idle", nil)
	}
	<-ctx.Done()
	return nil
}

// ── shared parse helpers ───────────────────────────────────────────────────────

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// splitArtistTitle parses VirtualDJ's "artist - title" on the FIRST " - ". No separator →
// the whole string is the title.
func splitArtistTitle(s string) (artist, title string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if i := strings.Index(s, " - "); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+3:])
	}
	return "", s
}
