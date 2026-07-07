// Package tagsync writes a library track's DJ-software analysis into its real file tag,
// recording a revertible before/after snapshot in the library DB. It writes the
// analysis/curation fields the DJ software produced - BPM, key, genre, comment - and
// deliberately leaves title/artist/album untouched (the file already carries those, and
// the collection derived them from the file). Every write is undoable via Revert.
package tagsync

import (
	"encoding/json"
	"errors"
	"strconv"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/tagwrite"
)

// tagFieldColumn maps a tagwrite field to its change_log/tracks column name.
var tagFieldColumn = map[string]string{
	tagwrite.FieldBPM:     "bpm",
	tagwrite.FieldKey:     "key",
	tagwrite.FieldGenre:   "genre",
	tagwrite.FieldComment: "comment",
}

// ErrUnsupported is returned for a file format tagwrite can't write yet (m4a/wav/aiff/…).
var ErrUnsupported = errors.New("tag write unsupported for this format")

// desiredFromTrack builds the tag set to write from a track's analysis (non-empty only).
// Fields: BPM, key, genre, comment - analysis/curation only; title/artist/album untouched.
func desiredFromTrack(t musiclib.Track) tagwrite.Tags {
	d := tagwrite.Tags{}
	if t.BPM > 0 {
		d[tagwrite.FieldBPM] = strconv.FormatFloat(t.BPM, 'f', -1, 64)
	}
	if t.Key != "" {
		d[tagwrite.FieldKey] = t.Key
	}
	if t.Genre != "" {
		d[tagwrite.FieldGenre] = t.Genre
	}
	if t.Comment != "" {
		d[tagwrite.FieldComment] = t.Comment
	}
	return d
}

// Apply writes the track's analysis into its file and records a revertible edit. Returns
// the fields written. A no-op (empty map) if the track has no analysis to write.
func Apply(db *libdb.DB, t musiclib.Track) (tagwrite.Tags, error) {
	if !tagwrite.Supported(t.Path) {
		return nil, ErrUnsupported
	}
	desired := desiredFromTrack(t)
	if len(desired) == 0 {
		return desired, nil
	}
	// Snapshot the current value of exactly the fields we're about to write (default "" so a
	// revert clears a field that wasn't present before).
	cur, err := tagwrite.Read(t.Path)
	if err != nil {
		return nil, err
	}
	before := map[string]string{}
	for f := range desired {
		before[f] = cur[f]
	}
	if err := tagwrite.Write(t.Path, desired); err != nil {
		return nil, err
	}
	if db != nil {
		if _, err := db.RecordTagEdit(t.Path, before, desired); err != nil {
			return desired, err // file written, but history failed - surface it
		}
		// Mirror the curation into the unified change history (origin tagsync) so a sync
		// can propagate these fields and the timeline is complete. tag_edits remains the
		// file-level revert; this is the library-state record.
		_ = db.AppendChanges(tagsyncEvents(t, before, desired))
	}
	return desired, nil
}

// tagsyncEvents builds change_log "set" events for the fields a tag write changed.
func tagsyncEvents(t musiclib.Track, before, after tagwrite.Tags) []libdb.ChangeEvent {
	hash := libdb.TrackHash(t.Artist, t.Title, t.DurationSec)
	var evs []libdb.ChangeEvent
	for f, newV := range after {
		col, ok := tagFieldColumn[f]
		if !ok {
			continue
		}
		oldJSON, _ := json.Marshal(before[f])
		newJSON, _ := json.Marshal(newV)
		evs = append(evs, libdb.ChangeEvent{
			TrackHash: hash, Path: t.Path, Field: col, Op: "set",
			OldValue: string(oldJSON), NewValue: string(newJSON), Origin: "tagsync",
		})
	}
	return evs
}

// Revert restores the most recent (non-reverted) tag write for path from its snapshot.
func Revert(db *libdb.DB, path string) error {
	if db == nil {
		return errors.New("no library db")
	}
	edit, ok, err := db.LatestTagEdit(path)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("no tag edit to revert for this file")
	}
	if err := tagwrite.Write(path, edit.Before); err != nil {
		return err
	}
	return db.MarkReverted(edit.ID)
}
