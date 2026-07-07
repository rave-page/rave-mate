package vrcperm

import "encoding/json"

// Source is one world-facing feed the Unity plugin can wire into a prefab/component.
type Source struct {
	Kind    string `json:"kind"` // "perm" | "posters" | "events" | "nowplaying"
	Name    string `json:"name,omitempty"`
	URL     string `json:"url"`               // main file (perm: allow.txt newline format)
	JSONURL string `json:"jsonUrl,omitempty"` // perm lists: allow.json envelope
}

// SourcesJSON renders the plugin handoff document (written into the Unity project
// as Assets/rave.page/WorldSync/sources.json). Only published targets are listed.
func (s *Service) SourcesJSON() []byte {
	f := s.cfg()
	var out struct {
		Sources []Source `json:"sources"`
	}
	for i := range f.Lists {
		l := &f.Lists[i]
		if url := s.RawURLFor(l.GistID, FileNames); url != "" {
			out.Sources = append(out.Sources, Source{
				Kind: "perm", Name: l.Name, URL: url,
				JSONURL: s.RawURLFor(l.GistID, FileJSON),
			})
		}
	}
	if url := s.RawURLFor(f.PostersGistID, FilePosters); url != "" {
		out.Sources = append(out.Sources, Source{Kind: "posters", URL: url})
	}
	if url := s.RawURLFor(f.EventsGistID, FileEvents); url != "" {
		out.Sources = append(out.Sources, Source{Kind: "events", URL: url})
	}
	if url := s.RawURLFor(f.NowPlayingGistID, FileNowPlaying); url != "" {
		out.Sources = append(out.Sources, Source{Kind: "nowplaying", URL: url})
	}
	if out.Sources == nil {
		out.Sources = []Source{}
	}
	raw, _ := json.MarshalIndent(out, "", " ")
	return append(raw, '\n')
}
