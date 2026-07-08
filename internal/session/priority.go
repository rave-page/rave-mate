package session

import "time"

// Source IDs - stable identifiers used across priority, capability, and provenance.
const (
	SourceTraktor    = "traktor"     // HTTP listener (QML mod / broadcast feed): richest live when present
	SourceNML        = "nml"         // history + collection: authoritative metadata, delayed live
	SourceMIDI       = "midi"        // aggregator source id (one source, both decoders) - liveness key
	SourceMIDIDenon  = "midi.denon"  // Denon HC4500 stock map: decks A/B title+artist (field provenance)
	SourceMIDICustom = "midi.custom" // our custom TSI: per-deck numeric/boolean state (field provenance)
	SourceIcecast    = "icecast"     // broadcast metadata: master title/artist
	SourceQML        = "qml"         // opt-in richer QML HTTP feed
	SourceNowPlaying = "nowplaying"  // macOS Now Playing: master title/artist
	SourceProDJLink  = "prodjlink"   // Pioneer CDJ/XDJ LAN broadcasts: live BPM/play state per deck

	// Serato - one source (liveness key); collection (database V2 + crates) + live now-playing
	// from the active History session file.
	SourceSerato = "serato"

	// VirtualDJ - one source (liveness key "virtualdj"), three live decoders distinguished by
	// provenance for field ranking: Network Control plugin (full metadata), OS2L (beat/BPM only),
	// History tracklist file (title/artist, laggy).
	SourceVirtualDJ  = "virtualdj"
	SourceVDJNetCtl  = "virtualdj.netctl"  // Network Control plugin HTTP: full live metadata
	SourceVDJOS2L    = "virtualdj.os2l"    // OS2L server: live BPM/beat only (no track text)
	SourceVDJHistory = "virtualdj.history" // tracklist file: title/artist, delayed fallback

	// Rekordbox live now-playing - one source (liveness key "rekordbox"), two decoders by
	// provenance: process-memory read (real-time) and master.db history poll (~60s lag). The
	// Pioneer-hardware path stays SourceProDJLink.
	SourceRekordbox    = "rekordbox"
	SourceRekordboxMem = "rekordbox.mem" // process-memory read: real-time deck/BPM/track
	SourceRekordboxDB  = "rekordbox.db"  // master.db djmdSongHistory poll: recently-played, laggy
)

// fieldPriority lists sources best-first per field. Missing field → defaultPriority.
// Mirrors the brainstorm: title prefers richest text source (QML/Traktor) over the
// delayed MIDI/Icecast feeds; descriptive metadata prefers NML (collection-accurate).
var fieldPriority = map[string][]string{
	FieldTitle:     {SourceQML, SourceTraktor, SourceVDJNetCtl, SourceRekordboxMem, SourceSerato, SourceMIDIDenon, SourceProDJLink, SourceRekordboxDB, SourceVDJHistory, SourceNML, SourceIcecast, SourceNowPlaying},
	FieldArtist:    {SourceQML, SourceTraktor, SourceVDJNetCtl, SourceRekordboxMem, SourceSerato, SourceMIDIDenon, SourceProDJLink, SourceRekordboxDB, SourceVDJHistory, SourceNML, SourceIcecast, SourceNowPlaying},
	FieldAlbum:     {SourceNML, SourceQML, SourceTraktor, SourceSerato, SourceRekordboxDB},
	FieldGenre:     {SourceNML, SourceQML, SourceTraktor, SourceSerato, SourceRekordboxDB},
	FieldKey:       {SourceQML, SourceTraktor, SourceVDJNetCtl, SourceRekordboxMem, SourceSerato, SourceProDJLink, SourceRekordboxDB, SourceNML, SourceMIDICustom},
	FieldBPM:       {SourceQML, SourceTraktor, SourceVDJNetCtl, SourceVDJOS2L, SourceRekordboxMem, SourceProDJLink, SourceSerato, SourceMIDICustom, SourceRekordboxDB, SourceNML},
	FieldIsPlaying: {SourceQML, SourceTraktor, SourceVDJNetCtl, SourceRekordboxMem, SourceProDJLink, SourceMIDICustom, SourceSerato},
	FieldPath:      {SourceNML, SourceQML, SourceTraktor, SourceSerato, SourceRekordboxDB},
	// Fader: the MIDI-custom map (RavePage Volume Adjust CC) reports the true channel-fader
	// POSITION; Traktor's HTTP feed only gives onAirLevel (post fader×crossfader, ~0.85 at
	// full). Prefer the real position so the meter reads 100% at 100%. onAirLevel→fader stays
	// the fallback for HTTP-only setups (lowest here).
	FieldFader: {SourceQML, SourceMIDICustom, SourceTraktor, SourceProDJLink, SourceNML},
}

// defaultPriority ranks sources for any field without an explicit table entry
// (the live numeric/boolean mixer state).
var defaultPriority = []string{SourceQML, SourceTraktor, SourceVDJNetCtl, SourceRekordboxMem, SourceProDJLink, SourceMIDICustom, SourceVDJOS2L, SourceSerato, SourceMIDIDenon, SourceRekordboxDB, SourceVDJHistory, SourceNML, SourceIcecast, SourceNowPlaying}

// rank returns the priority index of src for field (lower = wins). Unknown = lowest.
func rank(src, field string) int {
	order := fieldPriority[field]
	if order == nil {
		order = defaultPriority
	}
	for i, s := range order {
		if s == src {
			return i
		}
	}
	return len(order) + 1
}

// ttl is how long a winning field stays authoritative without a refresh before a
// lower-priority source may take it over. Text/metadata age slowly; live state fast.
func ttl(field string) time.Duration {
	switch field {
	case FieldTitle, FieldArtist, FieldAlbum, FieldGenre, FieldKey, FieldDeckType, FieldTrackLength, FieldLoadedAt, FieldPath:
		return 90 * time.Second
	case FieldIsPlaying, FieldElapsedTime, FieldCue:
		return 5 * time.Second
	case FieldBPM, FieldPhase, FieldFader, FieldEQHigh, FieldEQMid, FieldEQLow, FieldFilter:
		return 10 * time.Second
	default:
		return 30 * time.Second
	}
}
