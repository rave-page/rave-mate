package vrcperm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"rave.page/mate/internal/config"
)

const (
	pageSize   = 100
	maxMembers = 5000 // safety cap per group-role entry
)

// ExpandList resolves a list's entries to displayNames. User entries are direct;
// group-role entries expand to current members via the Groups API (paginated,
// paced). On expansion failure the entry's last good expansion is reused and an
// aggregate error is returned alongside the (partial) result - callers publish
// what they have rather than emptying a live allowlist.
func (s *Service) ExpandList(ctx context.Context, l *config.PermList) ([]string, error) {
	var names []string
	var errs []error
	for i := range l.Entries {
		e := &l.Entries[i]
		switch e.Kind {
		case config.PermEntryUser:
			names = append(names, e.Display)
		case config.PermEntryGroupRole:
			got, err := s.expandGroupRole(ctx, e)
			names = append(names, got...)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s/%s: %w", e.GroupName, e.RoleName, err))
			}
		}
	}
	return names, errors.Join(errs...)
}

// expandGroupRole pages members of one group(+role) to displayNames. Falls back
// to the cached expansion on error (visibility loss must not wipe a world list).
func (s *Service) expandGroupRole(ctx context.Context, e *config.PermEntry) ([]string, error) {
	key := e.GroupID + "|" + e.RoleID
	src := s.members()
	if src == nil {
		return s.cached(key), fmt.Errorf("VRChat not linked")
	}
	var names []string
	for offset := 0; offset < maxMembers; offset += pageSize {
		page, err := src.GroupMembers(ctx, e.GroupID, e.RoleID, offset, pageSize)
		if err != nil {
			return s.cached(key), err
		}
		for _, m := range page {
			if m.User.DisplayName != "" {
				names = append(names, m.User.DisplayName)
			}
		}
		if len(page) < pageSize {
			break
		}
		if s.pagePause > 0 {
			select {
			case <-ctx.Done():
				return s.cached(key), ctx.Err()
			case <-time.After(s.pagePause):
			}
		}
	}
	s.mu.Lock()
	s.expCache[key] = append([]string(nil), names...)
	s.mu.Unlock()
	return names, nil
}

// cached returns the last good expansion for a group|role key (nil if none).
func (s *Service) cached(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.expCache[key]...)
}
