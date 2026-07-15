package session

// Central ID-mark redaction (leak prevention for unreleased tracks). The Merger keeps RAW
// source data internally (library matching, art keys stay possible upstream) and redacts
// at its two output boundaries - Snapshot() and emitted Updates - so EVERY consumer
// (overlays, stream publisher, now-playing file, recorder tracklist, Publish UI, VR
// overlays via the PNG/overlay renderers) inherits it with no per-sink work.
//
// A marked track shows title "ID"; artist and label/album are blanked unless the mark
// allows them; the file path is blanked too (filenames routinely embed "Artist - Title").

// Mark mirrors idmark.Mark: what a marked track may still show.
type Mark struct {
	ShowArtist bool
	ShowLabel  bool
}

// RedactFunc reports whether a track file path is ID-marked. Wired from internal/idmark
// at app start; nil = no redaction.
type RedactFunc func(path string) (Mark, bool)

// RedactedTitle replaces a marked track's title everywhere.
const RedactedTitle = "ID"

// SetRedactor installs the ID-mark lookup applied to every outbound Snapshot/Update.
func (m *Merger) SetRedactor(fn RedactFunc) {
	m.mu.Lock()
	m.redact = fn
	m.mu.Unlock()
}

// redactableScope: track identity lives on deck/master/nowPlaying scopes (channels are
// mixer values only).
func redactableScope(k ScopeKind) bool {
	return k == ScopeDeck || k == ScopeMaster || k == ScopeNowPlaying
}

// redactedValue returns the replacement for one field under a mark (hit=false ⇒ keep).
func redactedValue(field string, m Mark) (any, bool) {
	switch field {
	case FieldTitle:
		return RedactedTitle, true
	case FieldArtist:
		if m.ShowArtist {
			return nil, false
		}
		return "", true
	case FieldAlbum, FieldLabel:
		if m.ShowLabel {
			return nil, false
		}
		return "", true
	case FieldPath:
		return "", true
	}
	return nil, false
}

// redactSnapshot builds the redacted view of a raw (unredacted) UnifiedState. Top-level maps are
// fresh (a consumer adding/removing a scope can't corrupt the shared raw memo); per-scope field
// maps are SHARED with raw when not redacted and COPIED when a mark applies (so raw stays pristine).
// Channels are mixer-only (never redactable); decks + master carry track identity.
func redactSnapshot(raw UnifiedState, fn RedactFunc) UnifiedState {
	out := UnifiedState{
		Decks:    make(map[string]map[string]FieldValue, len(raw.Decks)),
		Channels: make(map[string]map[string]FieldValue, len(raw.Channels)),
		Master:   raw.Master,
	}
	for id, fv := range raw.Channels {
		out.Channels[id] = fv
	}
	if fn == nil {
		for id, fv := range raw.Decks {
			out.Decks[id] = fv
		}
		return out
	}
	for id, fv := range raw.Decks {
		out.Decks[id] = redactScopeShared(fv, fn)
	}
	out.Master = redactScopeShared(raw.Master, fn)
	return out
}

// redactScopeShared returns fv unchanged (shared) when its track isn't marked; otherwise a redacted
// COPY (fv and its FieldValues are never mutated).
func redactScopeShared(fv map[string]FieldValue, fn RedactFunc) map[string]FieldValue {
	path, _ := pathOf(fv)
	if path == "" {
		return fv
	}
	mark, ok := fn(path)
	if !ok {
		return fv
	}
	out := make(map[string]FieldValue, len(fv))
	for f, h := range fv {
		if nv, hit := redactedValue(f, mark); hit {
			h.Value = nv
		}
		out[f] = h
	}
	return out
}

func pathOf(fv map[string]FieldValue) (string, bool) {
	h, ok := fv[FieldPath]
	if !ok {
		return "", false
	}
	s, ok := h.Value.(string)
	return s, ok
}

// redactUpdate returns u with State redacted when its scope's track is marked. The path is
// taken from the update itself, else from the scope's current raw winner.
func (m *Merger) redactUpdate(u Update) Update {
	if !redactableScope(u.Scope.Kind) || len(u.State) == 0 {
		return u
	}
	m.mu.RLock()
	fn := m.redact
	path := ""
	if fn != nil {
		if p, ok := u.State[FieldPath].(string); ok && p != "" {
			path = p
		} else if scope := m.fields[u.Scope.Key()]; scope != nil {
			if p, ok := scope[FieldPath].value.(string); ok {
				path = p
			}
		}
	}
	m.mu.RUnlock()
	if fn == nil || path == "" {
		return u
	}
	mark, ok := fn(path)
	if !ok {
		return u
	}
	st := make(map[string]any, len(u.State))
	for f, v := range u.State {
		if nv, hit := redactedValue(f, mark); hit {
			st[f] = nv
		} else {
			st[f] = v
		}
	}
	u.State = st
	return u
}
