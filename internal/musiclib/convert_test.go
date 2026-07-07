package musiclib

import (
	"bytes"
	"strings"
	"testing"
)

const sampleRekordbox = `<?xml version="1.0" encoding="UTF-8"?>
<DJ_PLAYLISTS Version="1.0.0">
<PRODUCT Name="rekordbox" Version="6.0" Company="Pioneer"/>
<COLLECTION Entries="1">
<TRACK TrackID="1" Name="Drop It" Artist="DJ Test" Composer="Hot Label" Album="EP"
 Genre="Tech House" TotalTime="312" BitRate="320" Rating="204" AverageBpm="128.00"
 Tonality="Am" Size="12582912" PlayCount="7"
 Location="file://localhost/C:/Music/DJ%20Test/Drop%20It.mp3">
<TEMPO Inizio="0.250" Bpm="128.00" Metro="4/4" Battito="1"/>
<POSITION_MARK Name="Intro" Type="0" Start="0.250" Num="0"/>
<POSITION_MARK Name="Loop1" Type="4" Start="60.0" End="64.0" Num="-1"/>
</TRACK>
</COLLECTION>
<PLAYLISTS></PLAYLISTS>
</DJ_PLAYLISTS>`

func TestParseRekordbox(t *testing.T) {
	var got []Track
	n, err := ParseRekordboxXML(strings.NewReader(sampleRekordbox), func(tr Track) { got = append(got, tr) })
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	tr := got[0]
	if tr.Title != "Drop It" || tr.Artist != "DJ Test" || tr.Label != "Hot Label" {
		t.Errorf("meta: %+v", tr)
	}
	if tr.BPM != 128 || tr.Key != "Am" || tr.Rating != 4 || tr.BitrateBps != 320000 {
		t.Errorf("bpm/key/rating/bitrate: %v %q %d %d", tr.BPM, tr.Key, tr.Rating, tr.BitrateBps)
	}
	if !strings.HasSuffix(tr.Path, "Drop It.mp3") || strings.Contains(tr.Path, "%20") {
		t.Errorf("path not decoded: %q", tr.Path)
	}
	if len(tr.Beatgrid) != 1 || tr.Beatgrid[0].PositionMs != 250 || tr.Beatgrid[0].BPM != 128 {
		t.Errorf("beatgrid: %+v", tr.Beatgrid)
	}
	if len(tr.Cues) != 2 {
		t.Fatalf("cues: %+v", tr.Cues)
	}
	if tr.Cues[0].Kind != CueHot || tr.Cues[0].StartMs != 250 {
		t.Errorf("cue0: %+v", tr.Cues[0])
	}
	if tr.Cues[1].Kind != CueLoop || tr.Cues[1].LenMs != 4000 {
		t.Errorf("cue1 (loop): %+v", tr.Cues[1])
	}
}

const sampleVirtualDJ = `<?xml version="1.0" encoding="UTF-8"?>
<VirtualDJ_Database Version="8.5">
<Song FilePath="C:\Music\x.mp3" FileSize="5242880">
<Tags Author="VA" Title="Track" Album="Comp" Genre="House" Key="Am" Bpm="0.468750" Year="2024"/>
<Infos SongLength="300.0" Bitrate="320" PlayCount="3"/>
<Poi Pos="0.5" Type="beatgrid" Bpm="0.468750"/>
<Poi Pos="30.0" Type="cue" Num="1" Name="Verse"/>
<Poi Pos="90.0" Type="loop" Num="2" Name="Build"/>
</Song>
</VirtualDJ_Database>`

func TestParseVirtualDJ(t *testing.T) {
	var got []Track
	n, err := ParseVirtualDJ(strings.NewReader(sampleVirtualDJ), func(tr Track) { got = append(got, tr) })
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	tr := got[0]
	if tr.Title != "Track" || tr.Artist != "VA" || tr.Key != "Am" {
		t.Errorf("meta: %+v", tr)
	}
	// 0.46875 s/beat → 128 BPM
	if tr.BPM < 127.9 || tr.BPM > 128.1 {
		t.Errorf("bpm decode: %v", tr.BPM)
	}
	if len(tr.Beatgrid) != 1 || tr.Beatgrid[0].BPM < 127.9 {
		t.Errorf("beatgrid: %+v", tr.Beatgrid)
	}
	if len(tr.Cues) != 2 || tr.Cues[0].Kind != CueHot || tr.Cues[1].Kind != CueLoop {
		t.Errorf("cues: %+v", tr.Cues)
	}
}

// TestRoundTrip imports Traktor → Library → exports Rekordbox → re-imports, asserting the
// key musical metadata + cues + beatgrid survive the conversion.
func TestRoundTrip(t *testing.T) {
	lib, err := Import(FormatTraktor, strings.NewReader(sampleCollection))
	if err != nil || len(lib.Tracks) != 1 {
		t.Fatalf("import traktor: %d %v", len(lib.Tracks), err)
	}
	src := lib.Tracks[0]

	var buf bytes.Buffer
	if err := Export(FormatRekordbox, lib, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "POSITION_MARK") || !strings.Contains(buf.String(), "TEMPO") {
		t.Errorf("rekordbox export missing cues/tempo:\n%s", buf.String())
	}

	rt, err := Import(FormatRekordbox, &buf)
	if err != nil || len(rt.Tracks) != 1 {
		t.Fatalf("re-import: %d %v", len(rt.Tracks), err)
	}
	got := rt.Tracks[0]
	if got.Title != src.Title || got.Artist != src.Artist || got.Key != src.Key {
		t.Errorf("meta drift: %q/%q/%q vs %q/%q/%q", got.Title, got.Artist, got.Key, src.Title, src.Artist, src.Key)
	}
	if got.BPM != src.BPM {
		t.Errorf("bpm drift: %v vs %v", got.BPM, src.BPM)
	}
	if len(got.Cues) != 1 || got.Cues[0].Kind != CueHot {
		t.Errorf("cue drift: %+v", got.Cues)
	}
	// Traktor track BPM with no explicit grid → one fallback TEMPO anchor.
	if len(got.Beatgrid) != 1 || got.Beatgrid[0].BPM != src.BPM {
		t.Errorf("beatgrid drift: %+v", got.Beatgrid)
	}
}

// TestExportTraktorNML round-trips Rekordbox → Traktor NML → re-parse.
func TestExportTraktorNML(t *testing.T) {
	lib, err := Import(FormatRekordbox, strings.NewReader(sampleRekordbox))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Export(FormatTraktor, lib, &buf); err != nil {
		t.Fatal(err)
	}
	back, err := Import(FormatTraktor, &buf)
	if err != nil || len(back.Tracks) != 1 {
		t.Fatalf("reparse nml: %d %v\n%s", len(back.Tracks), err, buf.String())
	}
	tr := back.Tracks[0]
	if tr.Title != "Drop It" || tr.BPM != 128 {
		t.Errorf("nml round-trip meta: %+v", tr)
	}
	// grid (1) + hotcue (1) + loop (1) = 3 CUE_V2 → beatgrid 1, cues 2
	if len(tr.Beatgrid) != 1 {
		t.Errorf("nml beatgrid: %+v", tr.Beatgrid)
	}
	if len(tr.Cues) < 2 {
		t.Errorf("nml cues: %+v", tr.Cues)
	}
}
