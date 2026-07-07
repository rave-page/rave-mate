package vrcperm

import (
	"encoding/json"
	"testing"

	"rave.page/mate/internal/config"
)

func TestSourcesJSON(t *testing.T) {
	f := &config.WorldSyncFeature{
		Enabled: true,
		Lists: []config.PermList{
			{ID: "l1", Name: "VIPs", GistID: "gA"},
			{ID: "l2", Name: "unpublished"}, // no gist yet → omitted
		},
		EventsGistID:     "gE",
		NowPlayingGistID: "gN",
	}
	s, _ := newTestService(f, &fakeGists{}, &fakeMembers{})
	var doc struct {
		Sources []Source `json:"sources"`
	}
	if err := json.Unmarshal(s.SourcesJSON(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(doc.Sources) != 3 {
		t.Fatalf("sources = %+v", doc.Sources)
	}
	if doc.Sources[0].Kind != "perm" || doc.Sources[0].Name != "VIPs" ||
		doc.Sources[0].URL != "https://gist.githubusercontent.com/octo/gA/raw/allow.txt" ||
		doc.Sources[0].JSONURL != "https://gist.githubusercontent.com/octo/gA/raw/allow.json" {
		t.Fatalf("perm source = %+v", doc.Sources[0])
	}
	if doc.Sources[1].Kind != "events" || doc.Sources[2].Kind != "nowplaying" {
		t.Fatalf("channel order = %+v", doc.Sources)
	}
}
