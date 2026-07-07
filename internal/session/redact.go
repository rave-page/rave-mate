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

// redactFieldValues redacts one scope's merged view in place (Snapshot copies only).
func redactFieldValues(fv map[string]FieldValue, fn RedactFunc) {
	path, _ := pathOf(fv)
	if path == "" {
		return
	}
	mark, ok := fn(path)
	if !ok {
		return
	}
	for f, h := range fv {
		if nv, hit := redactedValue(f, mark); hit {
			h.Value = nv
			fv[f] = h
		}
	}
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
