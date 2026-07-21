package recorder

import "time"

// TimeFix is a planned start-time correction: a rebased set start + per-track clamps.
// Pure data - PlanTimeFix computes it, the UI previews it, ApplyTimeFix commits it.
type TimeFix struct {
	NewStart    time.Time
	TrackStarts map[int]time.Time // track index → corrected absolute start
}

// PlanTimeFix aligns a finished recording's timeline to its captured audio file, so the
// exported offsets are relative to the uploaded mix: the audible start of the capture
// (capStart + probed leading silence) becomes 0:00. The set start moves there, track 1
// starts there, and every track whose recorded start predates it clamps up to it.
//
// Deck-play/history times BEFORE the audible start are phantoms - looping, cueing or
// prepping decks before the broadcast went live (often before the capture even began) -
// so the silence measured from the actual file outranks them; several early tracks
// clamping to 0:00 is expected, not an error. ok=false only when there is nothing to
// align to (no capture start, live/empty set, an entirely-silent capture, audio starting
// past the set end) or the plan is a no-op.
func PlanTimeFix(rec Recording, capStart, capEnd time.Time, leading time.Duration) (TimeFix, bool) {
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
	fix := TimeFix{NewStart: audio, TrackStarts: map[int]time.Time{}}
	for i, t := range rec.Tracks {
		ns := t.StartedAt
		if i == 0 || ns.Before(audio) {
			ns = audio
		}
		if !ns.Equal(t.StartedAt) {
			fix.TrackStarts[i] = ns
		}
	}
	if audio.Equal(rec.StartedAt) && len(fix.TrackStarts) == 0 {
		return TimeFix{}, false
	}
	return fix, true
}
