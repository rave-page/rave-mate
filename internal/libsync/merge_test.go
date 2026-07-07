package libsync

import (
	"testing"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

func cand(app string, t musiclib.Track) libdb.SourcedTrack {
	return libdb.SourcedTrack{App: app, Track: t}
}

// field-priority rule wins over the default order.
func TestMergeCanonicalFieldSource(t *testing.T) {
	tr := musiclib.Track{
		Artist: "A", Title: "T", DurationSec: 200,
		Genre: "Techno", Beatgrid: []musiclib.GridMarker{{PositionMs: 0, BPM: 128}},
	}
	rb := musiclib.Track{
		Artist: "A", Title: "T", DurationSec: 200,
		Genre: "House", Beatgrid: []musiclib.GridMarker{{PositionMs: 10, BPM: 130}},
	}
	cands := []libdb.SourcedTrack{cand("traktor", tr), cand("rekordbox", rb)}

	// beatgrid forced from traktor; genre forced from traktor.
	got := MergeCanonical(cands, map[string]string{"beatgrid": "traktor", "genre": "traktor"})
	if len(got.Beatgrid) != 1 || got.Beatgrid[0].BPM != 128 {
		t.Errorf("beatgrid: want traktor's (128), got %+v", got.Beatgrid)
	}
	if got.Genre != "Techno" {
		t.Errorf("genre: want Techno (traktor), got %q", got.Genre)
	}

	// No rule → default order prefers rekordbox.
	got2 := MergeCanonical(cands, nil)
	if got2.Genre != "House" {
		t.Errorf("default genre: want House (rekordbox first), got %q", got2.Genre)
	}
	if got2.Beatgrid[0].BPM != 130 {
		t.Errorf("default beatgrid: want rekordbox (130), got %v", got2.Beatgrid[0].BPM)
	}
}

// a field only one source has is taken from that source regardless of the rule.
func TestMergeCanonicalFallback(t *testing.T) {
	tr := musiclib.Track{Artist: "A", Title: "T", DurationSec: 100, Genre: "Trance"}
	rb := musiclib.Track{Artist: "A", Title: "T", DurationSec: 100} // no genre
	cands := []libdb.SourcedTrack{cand("traktor", tr), cand("rekordbox", rb)}

	got := MergeCanonical(cands, map[string]string{"genre": "rekordbox"})
	if got.Genre != "Trance" {
		t.Errorf("fallback genre: want Trance (only traktor has it), got %q", got.Genre)
	}
}

// longest known duration wins.
func TestMergeCanonicalDuration(t *testing.T) {
	a := musiclib.Track{Artist: "A", Title: "T", DurationSec: 0}
	b := musiclib.Track{Artist: "A", Title: "T", DurationSec: 321}
	got := MergeCanonical([]libdb.SourcedTrack{cand("traktor", a), cand("rekordbox", b)}, nil)
	if got.DurationSec != 321 {
		t.Errorf("duration: want 321, got %v", got.DurationSec)
	}
}
