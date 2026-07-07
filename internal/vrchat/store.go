package vrchat

import (
	"encoding/json"
	"os"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/shared/secureseal"
)

// persistFile is the sealed VRChat session blob under the config dir (DPAPI on
// Windows). Mirrors internal/auth: never plaintext on disk, never logged.
const persistFile = "vrchat.bin"

// persistedSession is what survives a restart - session cookies only, no creds.
type persistedSession struct {
	Auth        string `json:"auth"`
	TwoFactor   string `json:"twoFactor,omitempty"`
	UserID      string `json:"userId,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

// sessionStore seals/loads the VRChat session at rest.
type sessionStore struct{ log *logbus.Bus }

// load returns the persisted session (zero value if absent/unreadable).
func (s *sessionStore) load() persistedSession {
	p, err := config.DataPath(persistFile)
	if err != nil {
		return persistedSession{}
	}
	blob, err := os.ReadFile(p)
	if err != nil {
		return persistedSession{}
	}
	clear, err := secureseal.Unseal(blob)
	if err != nil {
		s.log.Warn(source, "could not decrypt persisted session", map[string]any{"error": err.Error()})
		return persistedSession{}
	}
	var ps persistedSession
	if json.Unmarshal(clear, &ps) != nil {
		return persistedSession{}
	}
	return ps
}

// save seals + atomically writes the session. No OS secret store → not persisted.
func (s *sessionStore) save(ps persistedSession) {
	if ps.Auth == "" {
		return
	}
	raw, err := json.Marshal(ps)
	if err != nil {
		return
	}
	sealed, err := secureseal.Seal(raw)
	if err != nil {
		s.log.Warn(source, "session not persisted (no secure store)", map[string]any{"error": err.Error()})
		return
	}
	p, err := config.DataPath(persistFile)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, sealed, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// clear deletes the persisted session.
func (s *sessionStore) clear() {
	if p, err := config.DataPath(persistFile); err == nil {
		_ = os.Remove(p)
	}
}
