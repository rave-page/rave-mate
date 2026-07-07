package session

// Canonical field names - identical to the keys Traktor POSTs (electron/src/main/traktor.ts)
// and the web overlay reads (NowPlayingLive.tsx). Sources MUST normalize to these exact,
// case-sensitive names so a Traktor-only setup is byte-identical on the ingest wire and
// other sources fill the same vocabulary for extra decks/fields.
const (
	// deck
	FieldTitle       = "title"
	FieldArtist      = "artist"
	FieldAlbum       = "album"
	FieldGenre       = "genre"
	FieldBPM         = "bpm" // also a master field
	FieldKey         = "key"
	FieldIsPlaying   = "isPlaying"
	FieldElapsedTime = "elapsedTime" // seconds
	FieldTrackLength = "trackLength" // seconds
	FieldDeckType    = "deckType"
	FieldLoadedAt    = "loadedAt" // unix millis
	FieldPath        = "path"     // local file path of the loaded track (art/lookups); collection-derived
	FieldLabel       = "label"    // record label (source passthrough; redacted for ID-marked tracks)

	// channel
	FieldFader  = "fader" // 0..1
	FieldEQHigh = "eqHigh"
	FieldEQMid  = "eqMid"
	FieldEQLow  = "eqLow"
	FieldFilter = "filter"
	FieldCue    = "cue"

	// master
	FieldPhase = "phase"
)

// MetadataFields are the slow-changing descriptive fields NML/collection enrichment owns.
var MetadataFields = []string{FieldAlbum, FieldGenre, FieldKey}
