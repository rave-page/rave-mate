package recorder

import "time"

// TimeFix is a planned start-time correction: a rebased set start + per-track clamps.
// Pure data - PlanTimeFix computes it, the UI previews it, ApplyTimeFix commits it.
type TimeFix struct {
	NewStart    time.Time
	TrackStarts map[int]time.Time // track index → corrected absolute start
}

// PlanTimeFix aligns a finished recording's timeline to its captured audio file. The
// audible start of the capture is capStart + leading (silence probed from the file): the
// set start and track 1 move there, and any track that "started" earlier (deck looping /
// cueing before the mix went live) clamps up to it - so a first track looped for 20 min
// no longer offsets every exported start time.
//
// Track 2's start bounds the correction: probed silence reaching past it means the probe
// measured the wrong thing (threshold too low, wrong capture), so fall back to the raw
// capture start; if even that is at/past track 2, this capture can't align the set →
// ok=false. ok=false also for live/empty sets and no-op plans.
func PlanTimeFix(rec Recording, capStart time.Time, leading time.Duration) (TimeFix, bool) {
	if capStart.IsZero() || len(rec.Tracks) == 0 || rec.EndedAt.IsZero() {
		return TimeFix{}, false
	}
	if leading < 0 {
		leading = 0
	}
	audio := capStart.Add(leading)
	if len(rec.Tracks) >= 2 {
		t2 := rec.Tracks[1].StartedAt
		if !audio.Before(t2) {
			audio = capStart
		}
		if !audio.Before(t2) {
			return TimeFix{}, false
		}
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
