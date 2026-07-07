package vrcperm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/github"
)

// PublishList expands + publishes one permission list. Diff-only: unchanged
// content writes nothing. Creates the gist on first publish (persists GistID).
func (s *Service) PublishList(ctx context.Context, l *config.PermList) {
	key := "list:" + l.ID
	names, expErr := s.ExpandList(ctx, l)
	if expErr != nil && len(names) == 0 {
		// Nothing expandable and no cache - keep the last published state.
		s.setErr(key, fmt.Sprintf("expand: %v", expErr))
		return
	}
	files := map[string]string{
		FileNames: FormatNames(names),
		FileJSON:  FormatJSON(l.Name, names),
	}
	desc := "rave-mate world permission list: " + l.Name
	s.publish(ctx, key, &l.GistID, desc, files, FileNames)
	if expErr != nil {
		// Published from partial/cached expansion - surface the degradation.
		s.appendErr(key, fmt.Sprintf("partial expand: %v", expErr))
	}
}

// PublishPosters publishes the poster-billboard channel.
func (s *Service) PublishPosters(ctx context.Context) {
	f := s.cfg()
	files := map[string]string{FilePosters: PostersJSON(f.Posters)}
	s.publish(ctx, "posters", &f.PostersGistID, "rave-mate world posters", files, FilePosters)
}

// PublishEvents publishes the upcoming-events channel.
func (s *Service) PublishEvents(ctx context.Context) {
	f := s.cfg()
	if s.events == nil {
		s.setErr("events", "no events feed wired")
		return
	}
	evs := s.events(ctx)
	files := map[string]string{FileEvents: EventsJSON(evs)}
	s.publish(ctx, "events", &f.EventsGistID, "rave-mate world events", files, FileEvents)
}

// PublishNowPlaying publishes the live now-playing card (redacted session output).
func (s *Service) PublishNowPlaying(ctx context.Context) {
	f := s.cfg()
	if s.nowPlay == nil {
		s.setErr("nowplaying", "no now-playing feed wired")
		return
	}
	np := s.nowPlay()
	if np.Link == "" {
		np.Link = f.NowPlayingLink
	}
	if np.Img == "" {
		np.Img = f.NowPlayingImg
	}
	files := map[string]string{FileNowPlaying: NowPlayingJSON(np)}
	s.publish(ctx, "nowplaying", &f.NowPlayingGistID, "rave-mate now playing", files, FileNowPlaying)
}

// RawURLFor returns the world-facing raw URL for a target's main file
// ("" until publishable). Latest-revision gist raw URLs are CDN-cached ~5 min.
func (s *Service) RawURLFor(gistID, file string) string {
	owner := s.owner()
	if owner == "" || gistID == "" {
		return ""
	}
	return github.RawURL(owner, gistID, file)
}

// publish writes files to the target gist when content changed; creates the
// gist when id is empty (or update 404s after a relink) and persists the id.
func (s *Service) publish(ctx context.Context, key string, gistID *string, desc string, files map[string]string, mainFile string) {
	store := s.gists()
	if store == nil || s.owner() == "" {
		s.setErr(key, "GitHub not linked")
		return
	}
	h := hashFiles(files)
	s.mu.Lock()
	unchanged := s.lastHash[key] == h && *gistID != ""
	s.mu.Unlock()
	if unchanged {
		s.mu.Lock()
		st := s.status[key]
		st.Skipped, st.Err = true, ""
		s.status[key] = st
		s.mu.Unlock()
		return
	}

	var g *github.Gist
	var err error
	if *gistID == "" {
		g, err = store.Create(ctx, desc, files, false) // secret gist: unlisted, URL-readable
	} else {
		g, err = store.Update(ctx, *gistID, desc, files)
		if err != nil && strings.Contains(err.Error(), "404") {
			// Stale id (deleted gist / relinked account) - self-heal with a new gist.
			g, err = store.Create(ctx, desc, files, false)
		}
	}
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
	s.status[key] = PubStatus{
		URL: github.RawURL(s.owner(), g.ID, mainFile), HTMLURL: g.HTMLURL, When: time.Now(),
	}
	s.mu.Unlock()
	s.log.Info(source, "published", map[string]any{"target": key})
}

// setErr records a failed pass (keeps prior URL/When for the UI).
func (s *Service) setErr(key, msg string) {
	s.mu.Lock()
	st := s.status[key]
	st.Err, st.Skipped = msg, false
	s.status[key] = st
	s.mu.Unlock()
	s.log.Warn(source, "publish failed", map[string]any{"target": key, "error": msg})
}

// appendErr adds a warning to an otherwise successful pass.
func (s *Service) appendErr(key, msg string) {
	s.mu.Lock()
	st := s.status[key]
	st.Err = msg
	s.status[key] = st
	s.mu.Unlock()
}

// hashFiles digests file names + contents (deterministic order).
func hashFiles(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write([]byte(files[n]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
