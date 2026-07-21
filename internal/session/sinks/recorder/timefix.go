package recorder

import "time"

// TimeFix is a planned start-time correction: a rebased set start, per-track clamps and
// tracks to drop. Pure data - PlanTimeFix computes it, the UI previews it, ApplyTimeFix
// commits it. Indexes refer to the recording's CURRENT track order.
type TimeFix struct {
	NewStart     time.Time
	TrackStarts  map[int]time.Time // track index → corrected absolute start
	RemoveTracks []int             // played before the opener - not in the capture
	Opener       int               // index of the track that opens the audible recording
}

// PlanTimeFix aligns a finished recording's timeline to its captured audio file WITHOUT
// fader history (PlanFaderFix is the exact mechanism; this is the fallback), so the
// exported offsets are relative to the uploaded mix: the audible start of the capture
// (capStart + probed leading silence) becomes 0:00.
//
// Deck-play/history times BEFORE the audible start are phantoms - looping, cueing or
// prepping decks before the broadcast went live (often before the capture even began).
// The file alone cannot order them, so ONE of them is the opener (opener param; <0 =
// auto: the FIRST pre-audio track - in the prep-then-record workflow the first entry is
// the track that was sitting looped as the intended opener, later entries are cue
// previews; no pre-audio tracks → track 0). Every other pre-audio track occupies a slot
// the file doesn't have → RemoveTracks. ok=false only when there is nothing to align to
// (no capture start, live/empty set, entirely-silent capture, audio past the set end) or
// the plan is a no-op.
func PlanTimeFix(rec Recording, capStart, capEnd time.Time, leading time.Duration, opener int) (TimeFix, bool) {
	if capStart.IsZero() || len(rec.Tracks) == 0 || rec.EndedAt.IsZero() {
		return TimeFix{}, false
	}
	if leading < 0 {
		leading = 0
	}
	audio := capStart.Add(leading)
	if !capEnd.IsZero() && !audio.Before(capEnd) {
		return TimeFix{}, false // silence runs to the capture's end - nothing audible to anchor on
	}
	if !audio.Before(rec.EndedAt) {
		return TimeFix{}, false
	}
	if opener < 0 || opener >= len(rec.Tracks) {
		opener = 0
	}
	fix := TimeFix{NewStart: audio, TrackStarts: map[int]time.Time{}, Opener: opener}
	for i, t := range rec.Tracks {
		switch {
		case i == opener:
			if !t.StartedAt.Equal(audio) {
				fix.TrackStarts[i] = audio
			}
		case t.StartedAt.Before(audio):
			fix.RemoveTracks = append(fix.RemoveTracks, i)
		}
	}
	if audio.Equal(rec.StartedAt) && len(fix.TrackStarts) == 0 && len(fix.RemoveTracks) == 0 {
		return TimeFix{}, false
	}
	return fix, true
}
