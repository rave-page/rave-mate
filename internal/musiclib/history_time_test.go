package musiclib

import (
	"testing"
	"time"
)

func TestDecodeHistoryTime(t *testing.T) {
	// Real values from history_2026y06m04d_01h26m44s.nml (set ran late June 3).
	got := decodeHistoryTime(132777475, 80690)
	want := time.Date(2026, 6, 3, 22, 24, 50, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("decodeHistoryTime(132777475, 80690) = %s, want %s", got, want)
	}
}

func TestDecodeHistoryTime_zeroAndInvalid(t *testing.T) {
	if !decodeHistoryTime(0, 1234).IsZero() {
		t.Error("startDate 0 must yield zero time")
	}
	if !decodeHistoryTime(0xFFFFFFFF, 0).IsZero() {
		t.Error("implausible date must yield zero time")
	}
}
