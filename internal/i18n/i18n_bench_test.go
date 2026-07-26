package i18n

import (
	"strconv"
	"testing"
)

// This package had NO benchmarks, and every claim about "i18n is expensive on the render path"
// was therefore unfalsifiable - the same gap B0 closed for the Zig bridge. A settings state build
// resolves ~400 keys, a whole render pass many more, so the per-call cost is the number that
// matters, and so is whether it SCALES (the render path is serialized on the act worker, but
// tick/probe goroutines resolve strings too).
//
// Run: GOWORK=off go test -count=2 ./internal/i18n -run '^$' -bench . -benchmem

func BenchmarkT(b *testing.B) {
	SetLocale("en")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if T("settings.label.uninstall") == "" {
			b.Fatal("empty")
		}
	}
}

// BenchmarkTMiss: a key the active locale lacks costs a second map lookup (active → en).
func BenchmarkTMissActive(b *testing.B) {
	SetLocale("de")
	defer SetLocale("en")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = T("nope.no.such.key.at.all")
	}
}

func BenchmarkTInterpolate(b *testing.B) {
	SetLocale("en")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = T("placeholder.comingSoon", A{"name": "Peers"})
	}
}

func BenchmarkTn(b *testing.B) {
	SetLocale("en")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Tn("twitch.bitsCount", i%5)
	}
}

// BenchmarkTParallel is the load-bearing one: load() takes the EXCLUSIVE lock on every call just
// to read a bool, so concurrent resolvers serialize on a writer even though nothing writes.
func BenchmarkTParallel(b *testing.B) {
	SetLocale("en")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = T("settings.label.uninstall")
		}
	})
}

// BenchmarkLookupOnly isolates the actual work (RLock + map lookups) from the load() guard, so the
// guard's share is visible rather than inferred.
func BenchmarkLookupOnly(b *testing.B) {
	SetLocale("en")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, ok := lookup("settings.label.uninstall"); !ok {
			b.Fatal("miss")
		}
	}
}

// BenchmarkTSweep resolves a spread of real keys, so the map isn't answering from one hot line.
func BenchmarkTSweep(b *testing.B) {
	SetLocale("en")
	load()
	mu.RLock()
	keys := make([]string, 0, len(catalog["en"]))
	for k := range catalog["en"] {
		keys = append(keys, k)
	}
	mu.RUnlock()
	if len(keys) < 100 {
		b.Skip("catalog too small")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = T(keys[i%len(keys)])
	}
}

// BenchmarkTStateBuildShape approximates one settings-tab state build: ~400 resolutions, a few
// with interpolation, in one pass - the shape render_settings.go actually has.
func BenchmarkTStateBuildShape(b *testing.B) {
	SetLocale("en")
	load()
	mu.RLock()
	keys := make([]string, 0, 400)
	for k := range catalog["en"] {
		keys = append(keys, k)
		if len(keys) == 400 {
			break
		}
	}
	mu.RUnlock()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, k := range keys {
			if j%40 == 0 {
				_ = T(k, A{"name": strconv.Itoa(j)})
				continue
			}
			_ = T(k)
		}
	}
}
