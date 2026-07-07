package auth

// At-rest sealing lives in rave.page/mate/internal/shared/secureseal (shared with identity). These
// unexported aliases keep store.go terse; tokens are never persisted in plaintext -
// secureUnseal/secureSeal returning ErrNoSecureStore means "don't persist".

import "rave.page/mate/internal/shared/secureseal"

var (
	secureSeal   = secureseal.Seal
	secureUnseal = secureseal.Unseal
)
