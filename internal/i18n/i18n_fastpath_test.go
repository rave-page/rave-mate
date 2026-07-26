package i18n

import (
	"sync"
	"testing"
)

// The read fast path (lock-free snapshots published under mu) has to keep two properties the
// RLock version gave for free: a locale switch is visible to the NEXT resolution, and a resolver
// racing a switch still returns a real translation - never "", never a torn mix of catalogs.

func TestLocaleSwitchIsImmediatelyVisible(t *testing.T) {
	const key = "settings.label.uninstall"
	defer SetLocale("en")

	SetLocale("en")
	en := T(key)
	if en == "" || en == key {
		t.Fatalf("en lookup failed: %q", en)
	}
	if got := Current(); got != "en" {
		t.Fatalf("Current() = %q after SetLocale(en)", got)
	}
	SetLocale("de")
	de := T(key)
	if de == "" || de == key {
		t.Fatalf("de lookup failed: %q", de)
	}
	if got := Current(); got != "de" {
		t.Fatalf("Current() = %q after SetLocale(de)", got)
	}
	if de == en {
		t.Skip("de and en carry the same value for this key - pick another to prove the switch")
	}
	SetLocale("en")
	if got := T(key); got != en {
		t.Fatalf("switching back gave %q, want %q", got, en)
	}
}

// TestConcurrentSwitchKeepsResolving: run with -race. Every resolution must come from SOME
// installed catalog; a snapshot that could be read half-updated would show up as the key itself
// (miss in both maps) or an empty string.
func TestConcurrentSwitchKeepsResolving(t *testing.T) {
	const key = "settings.label.uninstall"
	defer SetLocale("en")

	want := map[string]bool{}
	for _, l := range Available() {
		SetLocale(l.Code)
		want[T(key)] = true
	}
	SetLocale("en")
	if len(want) < 2 {
		t.Skip("need at least two distinct translations")
	}

	codes := Available()
	stop := make(chan struct{})
	var switcher, readers sync.WaitGroup
	switcher.Add(1)
	go func() { // keeps switching for as long as the readers run - not a moment less
		defer switcher.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			SetLocale(codes[i%len(codes)].Code)
		}
	}()
	for g := 0; g < 8; g++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 20000; i++ {
				v := T(key)
				if v == "" || v == key {
					t.Errorf("resolution failed mid-switch: %q", v)
					return
				}
				if !want[v] {
					t.Errorf("resolution returned an unknown value: %q", v)
					return
				}
			}
		}()
	}
	readers.Wait() // the switch storm has to overlap the reads, so stop only after they finish
	close(stop)
	switcher.Wait()
}

// TestLoadIsIdempotent pins that the sync.Once guard did not change load's contract: repeated
// calls are safe and the catalogs stay populated.
func TestLoadIsIdempotent(t *testing.T) {
	load()
	load()
	mu.RLock()
	n := len(catalog)
	mu.RUnlock()
	if n == 0 {
		t.Fatal("no catalogs after load")
	}
	if p := fastEn.Load(); p == nil || *p == nil || len((*p)) == 0 {
		t.Fatal("en snapshot not published by load")
	}
}
