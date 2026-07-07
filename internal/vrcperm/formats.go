package vrcperm

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"rave.page/mate/internal/config"
)

// List gist file names. allow.txt = newline displayNames (VideoTXL newline mode,
// ProTV/generic loaders); allow.json = envelope (VideoTXL JSON mode: array path
// "users"). No timestamps - content-stable output keeps diff-only writes quiet.
const (
	FileNames = "allow.txt"
	FileJSON  = "allow.json"

	FilePosters    = "posters.json"
	FileEvents     = "events.json"
	FileNowPlaying = "nowplaying.json"
)

// FormatNames renders sorted unique displayNames, one per line.
func FormatNames(names []string) string {
	u := dedupSort(names)
	if len(u) == 0 {
		return "\n" // empty gist files are rejected; single newline = empty list
	}
	return strings.Join(u, "\n") + "\n"
}

// FormatJSON renders the {"list":name,"users":[...]} envelope.
func FormatJSON(listName string, names []string) string {
	out := struct {
		List  string   `json:"list"`
		Users []string `json:"users"`
	}{List: listName, Users: dedupSort(names)}
	if out.Users == nil {
		out.Users = []string{}
	}
	raw, _ := json.MarshalIndent(out, "", " ")
	return string(raw) + "\n"
}

// PostersJSON renders the poster-billboard channel payload.
func PostersJSON(ps []config.WorldPoster) string {
	out := struct {
		Posters []config.WorldPoster `json:"posters"`
	}{Posters: ps}
	if out.Posters == nil {
		out.Posters = []config.WorldPoster{}
	}
	raw, _ := json.MarshalIndent(out, "", " ")
	return string(raw) + "\n"
}

// EventsJSON renders the upcoming-events channel payload.
func EventsJSON(evs []Event) string {
	out := struct {
		Events []Event `json:"events"`
	}{Events: evs}
	if out.Events == nil {
		out.Events = []Event{}
	}
	raw, _ := json.MarshalIndent(out, "", " ")
	return string(raw) + "\n"
}

// NowPlayingJSON renders the live-card channel payload.
func NowPlayingJSON(np NowPlaying) string {
	raw, _ := json.MarshalIndent(np, "", " ")
	return string(raw) + "\n"
}

// imageHostsExact / imageHostsWild are VRChat's image-loading allowlist
// (creators.vrchat.com/worlds/udon/image-loading, verified 2026-07).
// gist.githubusercontent.com and rave.page are NOT on it - poster/card images
// must live on one of these hosts.
var imageHostsExact = map[string]bool{
	"dl.dropbox.com": true, "dl.dropboxusercontent.com": true,
	"images4.imagebam.com": true, "i.ibb.co": true, "images2.imgbox.com": true,
	"i.imgur.com": true, "i.postimg.cc": true, "i.redd.it": true,
	"pbs.twimg.com": true, "assets.vrchat.com": true, "i.ytimg.com": true,
}

var imageHostsWild = []string{".disbridge.com", ".github.io", ".vrcdn.cloud"}

// ImageHostAllowed reports whether raw is an https URL on a VRC image-allowlisted host.
func ImageHostAllowed(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if imageHostsExact[h] {
		return true
	}
	for _, suf := range imageHostsWild {
		if strings.HasSuffix(h, suf) {
			return true
		}
	}
	return false
}

// dedupSort returns sorted unique non-empty names.
func dedupSort(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
