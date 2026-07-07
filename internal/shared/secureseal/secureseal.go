// Package secureseal provides OS-backed at-rest sealing of small secrets (auth tokens,
// the node identity key). Windows uses DPAPI (CurrentUser); platforms without an OS
// secret API return ErrNoSecureStore so callers decide whether to skip persistence
// (tokens) or fall back to a 0600 file (re-pairable identity key). macOS Keychain /
// libsecret backends are a documented follow-up - see SUPPLY_CHAIN.md.
package secureseal

import "errors"

// ErrNoSecureStore signals the absence of an OS-backed secret API on this platform.
var ErrNoSecureStore = errors.New("no OS secure store on this platform")

// Available reports whether Seal/Unseal use a real OS secret store on this platform.
func Available() bool { return available }
