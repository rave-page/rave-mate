package auth

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// registerSchemes are the product URL schemes the apps claim in the OS on install.
// ravepage:// is the canonical grant scheme; rave:// is the product deeplink scheme.
var registerSchemes = []string{"ravepage", "rave"}

// acceptSchemes are the schemes we parse from an inbound deeplink: the registered
// product schemes + the legacy "dymattic" scheme older API bridge pages may still
// emit. Accepted so legacy links keep working, but no longer registered on new installs.
var acceptSchemes = []string{"ravepage", "rave", "dymattic"}

// callback is the parsed auth deeplink payload.
type callback struct {
	Code    string // one-time grant code (?grant= or legacy ?code=)
	API     string // optional API base the browser used (?api=)
	Err     string // OAuth error code (?error=)
	ErrDesc string // OAuth error description (?error_description=)
}

// reSingleSlash matches a scheme with a single slash (ravepage:/auth/...) some shells
// pass, so we can normalize it to the double-slash form before url.Parse.
var reSingleSlash = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+\-.]*):/(?:/)?`)

// parseCallback validates + parses a custom-scheme auth callback URL. It accepts both
// registered schemes, the /auth/callback and /callback paths, and grant|code params.
func parseCallback(raw string) (callback, error) {
	raw = strings.TrimSpace(raw)
	norm := reSingleSlash.ReplaceAllString(raw, "$1://")
	u, err := url.Parse(norm)
	if err != nil {
		return callback{}, fmt.Errorf("parse %q: %w", raw, err)
	}
	scheme := strings.ToLower(strings.TrimSuffix(u.Scheme, ":"))
	if !accepts(scheme) {
		return callback{}, fmt.Errorf("unrecognized scheme %q", scheme)
	}
	// Host or path may carry the route depending on slash form: ravepage://auth/callback
	// parses host="auth", path="/callback"; normalize to a single path string.
	route := strings.TrimSuffix("/"+strings.Trim(u.Host+u.Path, "/"), "/")
	if route != "/auth/callback" && route != "/callback" {
		return callback{}, fmt.Errorf("unexpected route %q", route)
	}
	q := u.Query()
	code := q.Get("grant")
	if code == "" {
		code = q.Get("code")
	}
	return callback{
		Code:    code,
		API:     q.Get("api"),
		Err:     q.Get("error"),
		ErrDesc: q.Get("error_description"),
	}, nil
}

func accepts(scheme string) bool {
	return slices.Contains(acceptSchemes, scheme)
}

// IsDeepLink reports whether arg looks like one of our auth deeplinks (cheap prefix
// check used by an entrypoint to route a launch arg).
func IsDeepLink(arg string) bool {
	low := strings.ToLower(strings.TrimSpace(arg))
	for _, s := range acceptSchemes {
		if strings.HasPrefix(low, s+":") {
			return true
		}
	}
	return false
}
