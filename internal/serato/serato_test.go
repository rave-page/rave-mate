package serato

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// enc16 encodes s as UTF-16BE (Serato text payload).
func enc16(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.BigEndian.PutUint16(b[i*2:], c)
	}
	return b
}

// u32 encodes v as a 4-byte big-endian payload.
func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// field builds one [tag][len][payload] envelope chunk. tag may be 4 raw bytes.
func field(tag []byte, payload []byte) []byte {
	h := make([]byte, 8)
	copy(h, tag)
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

// numTag returns the 4-byte big-endian key for an adat numeric field id.
func numTag(id uint32) []byte { return u32(id) }

func TestParseDatabaseRoundTrip(t *testing.T) {
	otrk1 := field([]byte("otrk"), bytes.Join([][]byte{
		field([]byte("pfil"), enc16("Music/a.mp3")),
		field([]byte("tsng"), enc16("First Title")),
		field([]byte("tart"), enc16("Artist One")),
		field([]byte("talb"), enc16("Album One")),
		field([]byte("tgen"), enc16("Techno")),
		field([]byte("tbpm"), enc16("128.00")),
		field([]byte("tkey"), enc16("8A")),
		field([]byte("tlen"), enc16("3:30")),
	}, nil))
	otrk2 := field([]byte("otrk"), bytes.Join([][]byte{
		field([]byte("pfil"), enc16("Music/b.flac")),
		field([]byte("tsng"), enc16("Second")),
		field([]byte("tart"), enc16("Artist Two")),
		field([]byte("tbpm"), enc16("174.5")),
		field([]byte("tlen"), enc16("1:02:03")),
	}, nil))
	buf := bytes.Join([][]byte{field([]byte("vrsn"), enc16("2.0/Serato")), otrk1, otrk2}, nil)

	tracks, err := ParseDatabase(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ParseDatabase: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(tracks))
	}
	a := tracks[0]
	if a.Path != "Music/a.mp3" || a.Title != "First Title" || a.Artist != "Artist One" ||
		a.Album != "Album One" || a.Genre != "Techno" || a.Key != "8A" {
		t.Errorf("track0 text mismatch: %+v", a)
	}
	if a.BPM != 128.0 {
		t.Errorf("track0 BPM = %v, want 128", a.BPM)
	}
	if a.LengthSec != 210 {
		t.Errorf("track0 LengthSec = %d, want 210", a.LengthSec)
	}
	if tracks[1].BPM != 174.5 {
		t.Errorf("track1 BPM = %v, want 174.5", tracks[1].BPM)
	}
	if tracks[1].LengthSec != 3723 {
		t.Errorf("track1 LengthSec = %d, want 3723", tracks[1].LengthSec)
	}
}

func TestParseSessionADAT(t *testing.T) {
	adat := field([]byte("adat"), bytes.Join([][]byte{
		field(numTag(adatTitle), enc16("Live Track")),
		field(numTag(adatArtist), enc16("Live Artist")),
		field(numTag(adatPath), enc16("Music/live.mp3")),
		field(numTag(adatBPM), u32(130)),
		field(numTag(adatDeck), u32(2)),
		field(numTag(adatKey), enc16("5A")),
		field(numTag(adatPlayed), []byte{0x01}),
	}, nil))
	oent := field([]byte("oent"), adat)
	buf := bytes.Join([][]byte{field([]byte("vrsn"), enc16("1.0")), oent}, nil)

	tracks, err := ParseSession(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ParseSession: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("want 1 track, got %d", len(tracks))
	}
	got := tracks[0]
	if got.Title != "Live Track" || got.Artist != "Live Artist" || got.Path != "Music/live.mp3" || got.Key != "5A" {
		t.Errorf("session text mismatch: %+v", got)
	}
	if got.BPM != 130 || got.Deck != 2 || !got.Played {
		t.Errorf("session numeric mismatch: BPM=%v Deck=%d Played=%v", got.BPM, got.Deck, got.Played)
	}
}

// TestParseSessionPlayed4Byte covers the width-robust played flag (Serato writes the flag
// as a BE int whose width varies: a 4-byte 1 is 00 00 00 01, so reading only byte 0 misses
// it) plus starttime/endtime decode driving live-vs-idle.
func TestParseSessionPlayed4Byte(t *testing.T) {
	mk := func(title string, deck, played, start, end uint32) []byte {
		fields := [][]byte{
			field(numTag(adatTitle), enc16(title)),
			field(numTag(adatDeck), u32(deck)),
			field(numTag(adatPlayed), u32(played)), // 4-byte int, not a single byte
			field(numTag(adatStart), u32(start)),
		}
		if end != 0 {
			fields = append(fields, field(numTag(adatEnd), u32(end)))
		}
		return field([]byte("oent"), field([]byte("adat"), bytes.Join(fields, nil)))
	}
	buf := bytes.Join([][]byte{
		field([]byte("vrsn"), enc16("1.0")),
		mk("Ended", 1, 1, 1000, 1100), // played, has endtime => idle
		mk("Live", 1, 1, 1200, 0),     // played, no endtime => on the deck
	}, nil)

	tracks, err := ParseSession(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ParseSession: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("want 2, got %d", len(tracks))
	}
	if !tracks[0].Played || tracks[0].EndedAt != 1100 || tracks[0].StartedAt != 1000 {
		t.Errorf("ended entry: %+v", tracks[0])
	}
	if !tracks[1].Played || tracks[1].EndedAt != 0 || tracks[1].StartedAt != 1200 {
		t.Errorf("live entry (4-byte played must decode true, no endtime): %+v", tracks[1])
	}
}

func TestParseCrate(t *testing.T) {
	buf := bytes.Join([][]byte{
		field([]byte("vrsn"), enc16("1.0/Serato ScratchLive Crate")),
		field([]byte("otrk"), field([]byte("ptrk"), enc16("Music/a.mp3"))),
		field([]byte("otrk"), field([]byte("ptrk"), enc16("Music/b.flac"))),
	}, nil)
	c, err := ParseCrate(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ParseCrate: %v", err)
	}
	if len(c.TrackPaths) != 2 || c.TrackPaths[0] != "Music/a.mp3" || c.TrackPaths[1] != "Music/b.flac" {
		t.Errorf("crate paths mismatch: %+v", c.TrackPaths)
	}
}

func TestDecodeRejectsOverrun(t *testing.T) {
	bad := []byte("otrk")
	bad = append(bad, []byte{0x00, 0x00, 0x10, 0x00}...) // claims 4096-byte payload
	bad = append(bad, 0x01, 0x02)                        // but only 2 bytes follow
	if _, err := ParseDatabase(bytes.NewReader(bad)); err == nil {
		t.Fatal("want error on length overrun, got nil")
	}
}
