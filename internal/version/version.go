// Package version holds the build identity, stamped at link time via -ldflags -X.
// A dev build (go run / plain go build) leaves the zero values, which disables the
// self-updater (empty FeedURL) and shows "dev" in the UI.
package version

import (
	"strconv"
	"strings"
)

var (
	// Version is the human label, e.g. "development-abc1234" or "master-abc1234".
	Version = "dev"
	// Build is the monotonic build number (GitLab CI_PIPELINE_ID). The updater
	// compares this - it's strictly increasing per project, so it orders dev
	// builds correctly where semver of a "-branch.sha" prerelease can't.
	Build = "0"
	// Commit is the short git SHA.
	Commit = ""
	// FeedURL is this build's own update feed, e.g.
	// "https://development.rave.page/app/mate/". Empty on a dev build → updater off.
	FeedURL = ""
	// UpdatePubKey is the base64 raw (32-byte) Ed25519 public key the updater uses to
	// verify the signed manifest. When set, a valid manifest signature is REQUIRED - a
	// feed-write attacker can't forge a release without the private key. Empty → signing
	// not yet provisioned, updater falls back to sha256 + same-origin only.
	UpdatePubKey = ""
	// Channel is the release channel: "nightly" | "alpha" | "beta" | "production".
	// Empty → derived from the Version branch prefix (see ResolvedChannel). Stamp
	// explicitly via -X to override (nightly.yml stamps "nightly"). Anything other
	// than "production" is a prerelease (launch warning + backup hint).
	Channel = ""
)

// ResolvedChannel returns the effective release channel. Unstamped local builds
// report "dev"; branch builds map master/main → production, staging → beta,
// anything else → alpha.
func ResolvedChannel() string {
	if Channel != "" {
		return Channel
	}
	switch {
	case Version == "" || Version == "dev":
		return "dev"
	case strings.HasPrefix(Version, "master"), strings.HasPrefix(Version, "main"):
		return "production"
	case strings.HasPrefix(Version, "staging"):
		return "beta"
	default:
		return "alpha"
	}
}

// IsPreRelease reports a non-production build (launch warning + backup hint).
func IsPreRelease() bool { return ResolvedChannel() != "production" }

// BuildNum parses Build into an int (0 on any parse error).
func BuildNum() int {
	n, _ := strconv.Atoi(Build)
	return n
}

// String is a one-line identity for logs / the `version` subcommand.
func String() string {
	s := Version
	if Commit != "" {
		s += " (" + Commit + ")"
	}
	if Build != "0" {
		s += " build " + Build
	}
	return s
}
