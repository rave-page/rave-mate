package automation

import (
	"sync"
	"testing"
	"time"
)

// nopLogger discards all log output for tests.
type nopLogger struct{}

func (nopLogger) Info(string, string, map[string]any) {}
func (nopLogger) Warn(string, string, map[string]any) {}

func TestNextDaily(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 6, 4, 10, 30, 0, 0, loc)

	tests := []struct {
		name         string
		hour, min    int
		wantY, wantD int
		wantH, wantM int
	}{
		{"later today", 18, 0, 2026, 4, 18, 0},
		{"earlier today → tomorrow", 8, 15, 2026, 5, 8, 15},
		{"exact now → tomorrow", 10, 30, 2026, 5, 10, 30},
		{"one minute later today", 10, 31, 2026, 4, 10, 31},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nextDaily(now, tc.hour, tc.min)
			if !got.After(now) {
				t.Fatalf("next %v not after now %v", got, now)
			}
			if got.Year() != tc.wantY || got.Day() != tc.wantD || got.Hour() != tc.wantH || got.Minute() != tc.wantM {
				t.Fatalf("got %v, want %d-06-%02d %02d:%02d", got, tc.wantY, tc.wantD, tc.wantH, tc.wantM)
			}
		})
	}
}

func TestSetIntervalArmsWithoutImmediateFire(t *testing.T) {
	var mu sync.Mutex
	var fired []string
	s := NewScheduler(nopLogger{}, func(id string) {
		mu.Lock()
		fired = append(fired, id)
		mu.Unlock()
	})
	defer s.Stop()

	s.Set([]Schedule{{
		ID: "iv", Enabled: true, Kind: ScheduleInterval, IntervalMinutes: 1,
	}})

	// IntervalMinutes:1 → first tick is 1m out; nothing should fire in the next 50ms.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	n := len(fired)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("expected no immediate fire, got %d", n)
	}
}

func TestDisabledNotArmed(t *testing.T) {
	s := NewScheduler(nopLogger{}, func(string) {})
	s.Set([]Schedule{
		{ID: "off", Enabled: false, Kind: ScheduleInterval, IntervalMinutes: 1},
		{ID: "on", Enabled: true, Kind: ScheduleDaily, AtHour: 3, AtMinute: 0},
	})
	s.mu.Lock()
	n := len(s.cancels)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 armed schedule, got %d", n)
	}
	s.Stop()
}

func TestStopIdempotentAndClears(t *testing.T) {
	s := NewScheduler(nopLogger{}, func(string) {})
	s.Set([]Schedule{
		{ID: "a", Enabled: true, Kind: ScheduleInterval, IntervalMinutes: 1},
		{ID: "b", Enabled: true, Kind: ScheduleDaily, AtHour: 5, AtMinute: 30},
	})
	s.Stop()
	s.Stop() // must not panic on repeat

	s.mu.Lock()
	n := len(s.cancels)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("expected cleared state after Stop, got %d cancels", n)
	}
}

func TestSetReplacesPrevious(t *testing.T) {
	s := NewScheduler(nopLogger{}, func(string) {})
	defer s.Stop()
	s.Set([]Schedule{{ID: "1", Enabled: true, Kind: ScheduleInterval, IntervalMinutes: 1}})
	s.Set([]Schedule{
		{ID: "2", Enabled: true, Kind: ScheduleInterval, IntervalMinutes: 1},
		{ID: "3", Enabled: true, Kind: ScheduleDaily, AtHour: 1, AtMinute: 0},
	})
	s.mu.Lock()
	n := len(s.cancels)
	s.mu.Unlock()
	if n != 2 {
		t.Fatalf("expected 2 armed after replace, got %d", n)
	}
}
