package twitch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testChatLog(t *testing.T) *ChatLog {
	t.Helper()
	l, err := OpenChatLog(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l
}

func TestChatLogAppendRecent(t *testing.T) {
	l := testChatLog(t)
	for i := 0; i < 5; i++ {
		l.Append(Event{Kind: KindChat, UserLogin: "u", Text: fmt.Sprint("m", i), TS: int64(i)})
	}
	evs := l.Recent(3)
	if len(evs) != 3 {
		t.Fatalf("want 3, got %d", len(evs))
	}
	for i, ev := range evs { // chronological tail: m2 m3 m4
		if want := fmt.Sprint("m", i+2); ev.Text != want {
			t.Errorf("evs[%d].Text = %q, want %q", i, ev.Text, want)
		}
	}
	if got := l.Recent(100); len(got) != 5 {
		t.Errorf("recent(100) = %d, want 5", len(got))
	}
}

func TestChatLogRecentSpansDays(t *testing.T) {
	l := testChatLog(t)
	// Yesterday's file written directly (Append always targets today).
	y := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	old := `{"kind":"chat","userLogin":"u","text":"old","ts":1}` + "\n"
	if err := os.WriteFile(filepath.Join(l.dir, y+chatlogExt), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	l.Append(Event{Kind: KindFollow, UserLogin: "f", TS: 2})
	evs := l.Recent(10)
	if len(evs) != 2 || evs[0].Text != "old" || evs[1].Kind != KindFollow {
		t.Fatalf("bad span read: %+v", evs)
	}
}

func TestChatLogSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	l, err := OpenChatLog(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Append(Event{Kind: KindChat, Text: "persisted", TS: 1})
	l.Close()
	l2, err := OpenChatLog(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	evs := l2.Recent(10)
	if len(evs) != 1 || evs[0].Text != "persisted" {
		t.Fatalf("history lost across reopen: %+v", evs)
	}
}

func TestChatLogDayCapDropsNewest(t *testing.T) {
	defer func(v int64) { chatlogMaxDayBytes = v }(chatlogMaxDayBytes)
	chatlogMaxDayBytes = 200
	l := testChatLog(t)
	long := strings.Repeat("x", 60)
	for i := 0; i < 10; i++ {
		l.Append(Event{Kind: KindChat, Text: long, TS: int64(i)})
	}
	evs := l.Recent(100)
	if len(evs) == 0 || len(evs) >= 10 {
		t.Fatalf("day cap did not drop: %d rows", len(evs))
	}
	if evs[0].TS != 0 { // drop-newest: the OLDEST rows survive
		t.Errorf("first surviving TS = %d, want 0", evs[0].TS)
	}
}

func TestChatLogPruneAge(t *testing.T) {
	dir := t.TempDir()
	stale := time.Now().AddDate(0, 0, -(chatlogMaxDays + 2)).Format("2006-01-02")
	fresh := time.Now().Format("2006-01-02")
	for _, d := range []string{stale, fresh} {
		if err := os.WriteFile(filepath.Join(dir, d+chatlogExt), []byte(`{"kind":"chat","ts":1}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l, err := OpenChatLog(dir, nil) // Open prunes
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := os.Stat(filepath.Join(dir, stale+chatlogExt)); !os.IsNotExist(err) {
		t.Error("stale day file not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, fresh+chatlogExt)); err != nil {
		t.Error("fresh day file pruned")
	}
}

func TestChatLogPruneTotalSize(t *testing.T) {
	defer func(v int64) { chatlogMaxTotalBytes = v }(chatlogMaxTotalBytes)
	chatlogMaxTotalBytes = 150
	dir := t.TempDir()
	line := `{"kind":"chat","text":"` + strings.Repeat("y", 80) + `","ts":1}` + "\n"
	days := []string{
		time.Now().AddDate(0, 0, -3).Format("2006-01-02"),
		time.Now().AddDate(0, 0, -2).Format("2006-01-02"),
		time.Now().Format("2006-01-02"),
	}
	for _, d := range days {
		if err := os.WriteFile(filepath.Join(dir, d+chatlogExt), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l, err := OpenChatLog(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := os.Stat(filepath.Join(dir, days[0]+chatlogExt)); !os.IsNotExist(err) {
		t.Error("oldest file not size-pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, days[2]+chatlogExt)); err != nil {
		t.Error("newest file pruned by size cap")
	}
}
