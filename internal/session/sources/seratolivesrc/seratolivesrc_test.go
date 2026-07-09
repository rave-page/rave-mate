package seratolivesrc

import "testing"

func TestParseCurrentTrack(t *testing.T) {
	tests := []struct {
		name                       string
		html                       string
		wantOK                     bool
		wantArtist, wantTitle, raw string
	}{
		{
			name: "picks last of several",
			html: `<div class="playlist-trackname">Old Artist - Old Song</div>
			       <div class="playlist-trackname">Mid Artist - Mid Song</div>
			       <div class="playlist-trackname">Now Artist - Now Song</div>`,
			wantOK: true, wantArtist: "Now Artist", wantTitle: "Now Song", raw: "Now Artist - Now Song",
		},
		{
			name:   "single track split",
			html:   `<div class="playlist-trackname">Deadmau5 - Strobe</div>`,
			wantOK: true, wantArtist: "Deadmau5", wantTitle: "Strobe", raw: "Deadmau5 - Strobe",
		},
		{
			name:   "no hyphen = whole is title",
			html:   `<div class="playlist-trackname">ID</div>`,
			wantOK: true, wantArtist: "", wantTitle: "ID", raw: "ID",
		},
		{
			name:   "html entity decode",
			html:   `<div class="playlist-trackname">Simon &amp; Garfunkel - The Boxer &#39;69</div>`,
			wantOK: true, wantArtist: "Simon & Garfunkel", wantTitle: "The Boxer '69", raw: "Simon & Garfunkel - The Boxer '69",
		},
		{
			name:   "extra classes + attr order",
			html:   `<div data-x=1 class="row playlist-trackname bold">A Guy Called Gerald - Voodoo Ray</div>`,
			wantOK: true, wantArtist: "A Guy Called Gerald", wantTitle: "Voodoo Ray", raw: "A Guy Called Gerald - Voodoo Ray",
		},
		{
			name:   "nested markup stripped + whitespace collapsed",
			html:   "<div class=\"playlist-trackname\">\n  <span>Bicep</span> - Glue\n</div>",
			wantOK: true, wantArtist: "Bicep", wantTitle: "Glue", raw: "Bicep - Glue",
		},
		{
			name:   "trailing hyphen falls back to whole",
			html:   `<div class="playlist-trackname">Just This - </div>`,
			wantOK: true, wantArtist: "", wantTitle: "Just This -", raw: "Just This -",
		},
		{
			name:   "empty/private page",
			html:   `<html><body><h1>Private playlist</h1></body></html>`,
			wantOK: false,
		},
		{
			name:   "trackname div present but empty",
			html:   `<div class="playlist-trackname">   </div>`,
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, ti, r, ok := parseCurrentTrack(tc.html)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if a != tc.wantArtist || ti != tc.wantTitle || r != tc.raw {
				t.Errorf("got artist=%q title=%q raw=%q; want artist=%q title=%q raw=%q", a, ti, r, tc.wantArtist, tc.wantTitle, tc.raw)
			}
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"djamnesia", "https://serato.com/playlists/djamnesia/live"},
		{"  djamnesia  ", "https://serato.com/playlists/djamnesia/live"},
		{"https://serato.com/playlists/djamnesia/live", "https://serato.com/playlists/djamnesia/live"},
		{"https://serato.com/playlists/djamnesia", "https://serato.com/playlists/djamnesia/live"},
		{"https://serato.com/playlists/djamnesia/", "https://serato.com/playlists/djamnesia/live"},
		{"serato.com/playlists/dj/live", "serato.com/playlists/dj/live"},
	}
	for _, tc := range tests {
		if got := NormalizeURL(tc.in); got != tc.want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
