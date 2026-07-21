package recorder

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"time"

	"rave.page/mate/internal/session"
)

// Fader-history start-time reconstruction: the exact mechanism for aligning a tracklist
// to its capture. The capture's audible start (silence probe) anchors 0:00; each track's
// start is its deck's first fader-up from there on. Cue previews that never went on air
// during the capture are removed. Sources: the recording's own OnAirLog (always-on going
// forward) or the raw Traktor payload log (Features.Traktor.LogPayloads, default on).

// DeckEvent is one transport/fader observation for a deck, in log order.
type DeckEvent struct {
	At      time.Time
	Deck    string
	Playing *bool    // deck isPlaying transition
	Fader   *float64 // deck's channel post-fader level (onAirLevel, 0..1)
}

// PlanFaderFix reconstructs start times from recorded fader history. Only the tracks
// whose recorded start predates the audible moment are re-timed - once the mix is live,
// recorded starts are already real. For each pre-audio track, in order: start = the first
// instant ≥ the previous one where ITS deck is playing with the fader up, searched up to
// the first post-audio track (their real plays all happen in that window); no such moment
// = a cue preview that never made the capture → removed. The first surviving track opens
// the file at exactly the audible start. ok=false when the history doesn't cover the
// audible window (wrong night, logging off) - callers fall back to PlanTimeFix.
func PlanFaderFix(rec Recording, capStart, capEnd time.Time, leading time.Duration, evs []DeckEvent) (TimeFix, bool) {
	if capStart.IsZero() || len(rec.Tracks) == 0 || rec.EndedAt.IsZero() || len(evs) == 0 {
		return TimeFix{}, false
	}
	if leading < 0 {
		leading = 0
	}
	audio := capStart.Add(leading)
	if !capEnd.IsZero() && !audio.Before(capEnd) {
		return TimeFix{}, false
	}
	if !audio.Before(rec.EndedAt) {
		return TimeFix{}, false
	}
	if evs[0].At.After(audio) || evs[len(evs)-1].At.Before(audio) {
		return TimeFix{}, false // history doesn't span the audible moment
	}

	// Pre-audio tracks + the search bound: the first post-audio track's recorded start.
	pre := 0
	for pre < len(rec.Tracks) && rec.Tracks[pre].StartedAt.Before(audio) {
		pre++
	}
	bound := rec.EndedAt
	if pre < len(rec.Tracks) {
		bound = rec.Tracks[pre].StartedAt.Add(30 * time.Second)
	}
	// Decks with no fader feed in this history fail open (playing = on air) - a deck fed
	// by a source without channel data must not get its real tracks removed.
	hasFader := map[string]bool{}
	for _, e := range evs {
		if e.Fader != nil {
			hasFader[e.Deck] = true
		}
	}

	fix := TimeFix{NewStart: audio, TrackStarts: map[int]time.Time{}, Opener: -1}
	cursor := capStart
	for i := 0; i < pre; i++ {
		t := rec.Tracks[i]
		r, found := firstOnAir(evs, t.Deck, hasFader[t.Deck], cursor, bound)
		if !found {
			fix.RemoveTracks = append(fix.RemoveTracks, i)
			continue
		}
		if fix.Opener < 0 {
			fix.Opener = i
			r = audio // the opener starts where the audio starts
		}
		if !r.Equal(t.StartedAt) {
			fix.TrackStarts[i] = r
		}
		cursor = r
	}
	if fix.Opener < 0 && pre > 0 {
		return TimeFix{}, false // every pre-audio track unplaceable - distrust this history
	}
	if audio.Equal(rec.StartedAt) && len(fix.TrackStarts) == 0 && len(fix.RemoveTracks) == 0 {
		return TimeFix{}, false
	}
	return fix, true
}

// firstOnAir returns the first instant in [from, until] where deck is playing with the
// fader above the on-air threshold (withFader=false: playing alone counts). State carried
// from events before the window counts at `from` itself.
func firstOnAir(evs []DeckEvent, deck string, withFader bool, from, until time.Time) (time.Time, bool) {
	playing, fader := false, 0.0
	onAir := func() bool { return playing && (!withFader || fader > session.OnAirFaderThreshold) }
	i := 0
	for ; i < len(evs) && !evs[i].At.After(from); i++ {
		if e := evs[i]; e.Deck == deck {
			if e.Playing != nil {
				playing = *e.Playing
			}
			if e.Fader != nil {
				fader = *e.Fader
			}
		}
	}
	if onAir() {
		return from, true
	}
	for ; i < len(evs); i++ {
		e := evs[i]
		if e.At.After(until) {
			break
		}
		if e.Deck != deck {
			continue
		}
		if e.Playing != nil {
			playing = *e.Playing
		}
		if e.Fader != nil {
			fader = *e.Fader
		}
		if onAir() {
			return e.At, true
		}
	}
	return time.Time{}, false
}

// ParseTraktorPayloadLog extracts deck transport + channel fader events from the raw
// Traktor payload jsonl between from and to (window-bounded: the file grows for months).
// Channel numbers map to decks Traktor-default (1→A … 4→D); malformed lines are skipped.
func ParseTraktorPayloadLog(r io.Reader, from, to time.Time) []DeckEvent {
	chDeck := map[string]string{"1": "A", "2": "B", "3": "C", "4": "D"}
	var out []DeckEvent
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var row struct {
			TS      time.Time `json:"ts"`
			URL     string    `json:"url"`
			Payload struct {
				IsPlaying  *bool    `json:"isPlaying"`
				OnAirLevel *float64 `json:"onAirLevel"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil || row.TS.Before(from) || row.TS.After(to) {
			continue
		}
		switch {
		case strings.HasPrefix(row.URL, "/updateDeck/") && row.Payload.IsPlaying != nil:
			out = append(out, DeckEvent{At: row.TS, Deck: strings.TrimPrefix(row.URL, "/updateDeck/"), Playing: row.Payload.IsPlaying})
		case strings.HasPrefix(row.URL, "/updateChannel/") && row.Payload.OnAirLevel != nil:
			if d := chDeck[strings.TrimPrefix(row.URL, "/updateChannel/")]; d != "" {
				out = append(out, DeckEvent{At: row.TS, Deck: d, Fader: row.Payload.OnAirLevel})
			}
		}
	}
	return out
}

// FaderEventsFromOnAirLog converts a recording's own on-air log (measured threshold
// crossings) into replayable deck events.
func FaderEventsFromOnAirLog(log []OnAirEvent) []DeckEvent {
	playing := true
	out := make([]DeckEvent, 0, len(log))
	for _, e := range log {
		f := 0.0
		if e.Up {
			f = 1.0
		}
		fv := f
		out = append(out, DeckEvent{At: e.At, Deck: e.Deck, Playing: &playing, Fader: &fv})
	}
	return out
}
