package auth

import (
	"encoding/json"
	"os"
	"sync"
)

// persistFile is the encrypted token blob under the config dir. Encryption is
// platform-provided (Windows DPAPI, user-scoped); see secureSeal/secureUnseal.
const persistFile = "auth.bin"

// tokenStore holds the access + refresh tokens in memory and, where the platform offers
// an OS-backed secret API AND a dataPath is supplied, persists them encrypted at rest so a
// restart stays signed in. Tokens are never written in plaintext and never logged.
type tokenStore struct {
	log      Logger
	dataPath DataPath
	mu       sync.RWMutex
	acc      string
	ref      string
}

type persisted struct {
	Token   string `json:"token"`
	Refresh string `json:"refresh"`
}

func newTokenStore(log Logger, dataPath DataPath) *tokenStore {
	return &tokenStore{log: log, dataPath: dataPath}
}

func (s *tokenStore) token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.acc
}

func (s *tokenStore) refresh() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ref
}

func (s *tokenStore) set(access, refresh string) {
	s.mu.Lock()
	s.acc, s.ref = access, refresh
	s.mu.Unlock()
	s.persist()
}

func (s *tokenStore) clear() {
	s.mu.Lock()
	s.acc, s.ref = "", ""
	s.mu.Unlock()
	if s.dataPath == nil {
		return
	}
	if p, err := s.dataPath(persistFile); err == nil {
		_ = os.Remove(p)
	}
}

// load reads + decrypts the persisted token into memory and returns the access token
// (empty if absent/unreadable/in-memory-only). Called once at startup.
func (s *tokenStore) load() string {
	if s.dataPath == nil {
		return ""
	}
	p, err := s.dataPath(persistFile)
	if err != nil {
		return ""
	}
	blob, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	clear, err := secureUnseal(blob)
	if err != nil {
		s.log.Warn(source, "could not decrypt persisted session", map[string]any{"error": err.Error()})
		return ""
	}
	var pt persisted
	if err := json.Unmarshal(clear, &pt); err != nil {
		return ""
	}
	s.mu.Lock()
	s.acc, s.ref = pt.Token, pt.Refresh
	s.mu.Unlock()
	return pt.Token
}

func (s *tokenStore) persist() {
	if s.dataPath == nil {
		return // in-memory only (host didn't supply a config dir)
	}
	s.mu.RLock()
	pt := persisted{Token: s.acc, Refresh: s.ref}
	s.mu.RUnlock()
	if pt.Token == "" {
		return
	}
	raw, err := json.Marshal(pt)
	if err != nil {
		return
	}
	sealed, err := secureSeal(raw)
	if err != nil {
		// No OS secret backend (or it failed): don't fall back to plaintext on disk.
		s.log.Warn(source, "session not persisted (no secure store)", map[string]any{"error": err.Error()})
		return
	}
	p, err := s.dataPath(persistFile)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}
