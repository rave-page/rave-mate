package twitch

import (
	"sync"
	"time"
)

// ChatRate is a bounded sliding 60-second window of chat-message counts: one bucket per wall-clock
// second, 60 buckets, fixed memory, no timestamp list. It feeds the Live strip's "N msg/min" for
// this instance's chat AND a paired peer's (both arrive through the same bus tap in the Twitch
// proxy). Alerts are not counted - the rate answers "is chat moving", not "are alerts firing".
// The zero value is ready to use.
type ChatRate struct {
	mu      sync.Mutex
	counts  [60]uint32
	seconds [60]int64 // the unix second each bucket currently holds (0 = never used)
}

// Add records one chat message seen at now.
func (r *ChatRate) Add(now time.Time) {
	s := now.Unix()
	if s < 0 {
		return
	}
	i := int(s % 60)
	r.mu.Lock()
	if r.seconds[i] != s { // the slot holds an older minute's second - recycle it
		r.seconds[i], r.counts[i] = s, 0
	}
	r.counts[i]++
	r.mu.Unlock()
}

// PerMinute sums the messages seen in the 60 seconds ending at now (now's own second included).
func (r *ChatRate) PerMinute(now time.Time) int {
	s := now.Unix()
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for i := range r.counts {
		if sec := r.seconds[i]; sec > s-60 && sec <= s {
			n += int(r.counts[i])
		}
	}
	return n
}
