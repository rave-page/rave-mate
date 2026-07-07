package app

import (
	"os"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/shared/auth"
)

// ensureHandoffSecret writes a fresh 32-byte handoff secret (owner-only) if none exists yet.
// Authenticates the co-located token handoff: a cross-user process can't read it, so it can't
// forge the AES-GCM-sealed ADOPT-TOKEN blob. Best-effort - handoff degrades to refused.
func ensureHandoffSecret(log *logbus.Bus) {
	p, err := config.DataPath(auth.HandoffSecretFile)
	if err != nil {
		return
	}
	if _, err := os.Stat(p); err == nil {
		return // already present
	}
	secret, err := auth.NewHandoffSecret()
	if err != nil {
		return
	}
	if err := os.WriteFile(p, secret, 0o600); err != nil {
		log.Warn("app", "handoff secret not written", map[string]any{"error": err.Error()})
	}
}

// loadHandoffSecret reads the handoff secret (the rave-app sender encrypts the bundle under it).
func loadHandoffSecret() ([]byte, error) {
	p, err := config.DataPath(auth.HandoffSecretFile)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}
