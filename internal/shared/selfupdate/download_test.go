package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// tuned returns an updater against srv with test-speed download dials.
func tuned(srv *httptest.Server, retries int) *Updater {
	u := New(srv.URL, 1, "")
	u.dlStall = 300 * time.Millisecond
	u.dlBackoff = 10 * time.Millisecond
	u.dlRetries = retries
	return u
}

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// reqLog records download requests (Range/If-Range) thread-safely.
type reqLog struct {
	mu   sync.Mutex
	rngs []string // Range header per request ("" = none)
	ifr  []string
}

func (l *reqLog) add(r *http.Request) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rngs = append(l.rngs, r.Header.Get("Range"))
	l.ifr = append(l.ifr, r.Header.Get("If-Range"))
	return len(l.rngs) - 1
}

func (l *reqLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.rngs)
}

// assertProgressMonotonic fails if any reported done value decreases.
func assertProgressMonotonic(t *testing.T, prog []int64) {
	t.Helper()
	for i := 1; i < len(prog); i++ {
		if prog[i] < prog[i-1] {
			t.Fatalf("progress went backwards at %d: %v -> %v", i, prog[i-1], prog[i])
		}
	}
}

// TestDownloadSlowButFlowing: body drips slowly but steadily far past any per-window stall
// budget - a slow-but-flowing transfer must complete (the old Client.Timeout killed it).
func TestDownloadSlowButFlowing(t *testing.T) {
	payload := []byte(strings.Repeat("rave-slow-bytes-", 64)) // 1 KiB
	const chunks = 16
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		fl := w.(http.Flusher)
		per := len(payload) / chunks
		for i := 0; i < chunks; i++ {
			end := (i + 1) * per
			if i == chunks-1 {
				end = len(payload)
			}
			_, _ = w.Write(payload[i*per : end])
			fl.Flush()
			time.Sleep(70 * time.Millisecond) // < stall(300ms) per gap; total ~1.1s >> stall
		}
	}))
	t.Cleanup(srv.Close)

	u := tuned(srv, 0)
	dst := t.TempDir() + "/out.bin"
	var prog []int64
	start := time.Now()
	if err := u.downloadURL(context.Background(), srv.URL+"/rave-mate.exe", shaOf(payload), dst,
		func(done, total int64) { prog = append(prog, done) }); err != nil {
		t.Fatalf("slow-but-flowing download failed: %v", err)
	}
	if el := time.Since(start); el < 900*time.Millisecond {
		t.Fatalf("test not slow enough to prove anything (took %v)", el)
	}
	if got, _ := fileSHA(dst); got != shaOf(payload) {
		t.Fatalf("content mismatch after slow download")
	}
	assertProgressMonotonic(t, prog)
}

// TestDownloadResumesAfterCut: connection dropped at 40% → retry resumes with
// Range: bytes=<offset>- + If-Range, and the assembled file is byte-identical.
func TestDownloadResumesAfterCut(t *testing.T) {
	payload := []byte(strings.Repeat("0123456789", 10)) // 100 bytes
	cut := 40
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := log.add(r)
		w.Header().Set("ETag", `"v1"`)
		if i == 0 { // declare full length, send 40 bytes, drop the connection
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			_, _ = w.Write(payload[:cut])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(payload)-1, len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[cut:])
	}))
	t.Cleanup(srv.Close)

	u := tuned(srv, 3)
	dst := t.TempDir() + "/out.bin"
	var prog []int64
	if err := u.downloadURL(context.Background(), srv.URL+"/rave-mate.exe", shaOf(payload), dst,
		func(done, total int64) { prog = append(prog, done) }); err != nil {
		t.Fatalf("resumed download failed: %v", err)
	}
	if got, _ := fileSHA(dst); got != shaOf(payload) {
		t.Fatal("assembled file differs from payload")
	}
	if n := log.count(); n != 2 {
		t.Fatalf("want 2 requests (cut + resume), got %d", n)
	}
	if log.rngs[1] != fmt.Sprintf("bytes=%d-", cut) {
		t.Fatalf("resume Range = %q, want bytes=%d-", log.rngs[1], cut)
	}
	if log.ifr[1] != `"v1"` {
		t.Fatalf("resume If-Range = %q, want the first response's ETag", log.ifr[1])
	}
	assertProgressMonotonic(t, prog)
}

// TestDownloadStallWatchdog: server sends headers + a few bytes then hangs → the watchdog
// aborts within budget and the retry succeeds.
func TestDownloadStallWatchdog(t *testing.T) {
	payload := []byte(strings.Repeat("abcde", 40)) // 200 bytes
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if log.add(r) == 0 {
			_, _ = w.Write(payload[:5])
			w.(http.Flusher).Flush()
			<-r.Context().Done() // hang until the client aborts
			return
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	u := tuned(srv, 2)
	dst := t.TempDir() + "/out.bin"
	start := time.Now()
	if err := u.downloadURL(context.Background(), srv.URL+"/rave-mate.exe", shaOf(payload), dst, nil); err != nil {
		t.Fatalf("download after stall retry failed: %v", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("watchdog too slow: %v (stall budget 300ms)", el)
	}
	if n := log.count(); n != 2 {
		t.Fatalf("want 2 requests (stalled + retry), got %d", n)
	}
	if got, _ := fileSHA(dst); got != shaOf(payload) {
		t.Fatal("content mismatch after stall retry")
	}
}

// TestDownloadValidatorChangeRestartsClean: content changed between attempts (different ETag)
// → If-Range resume gets a full 200 → clean restart from zero, final file = the NEW content,
// progress never goes backwards.
func TestDownloadValidatorChangeRestartsClean(t *testing.T) {
	old := []byte(strings.Repeat("OLD-BYTES-", 10)) // 100 bytes
	fresh := []byte(strings.Repeat("NEW-CONTENT-BYTES-!!", 10))
	cut := 40
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if log.add(r) == 0 {
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(old)))
			_, _ = w.Write(old[:cut])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		// Validator changed: honor If-Range semantics - full 200 with the new content.
		w.Header().Set("ETag", `"v2"`)
		_, _ = w.Write(fresh)
	}))
	t.Cleanup(srv.Close)

	u := tuned(srv, 3)
	dst := t.TempDir() + "/out.bin"
	var prog []int64
	if err := u.downloadURL(context.Background(), srv.URL+"/rave-mate.exe", shaOf(fresh), dst,
		func(done, total int64) { prog = append(prog, done) }); err != nil {
		t.Fatalf("validator-change download failed: %v", err)
	}
	if got, _ := fileSHA(dst); got != shaOf(fresh) {
		t.Fatal("final file is not the new content")
	}
	if log.ifr[1] != `"v1"` {
		t.Fatalf("resume If-Range = %q, want old validator", log.ifr[1])
	}
	assertProgressMonotonic(t, prog)
}

// TestDownloadCorruptedResumeRetriesFromZero: a resume that serves corrupted range bytes fails
// final verification → the partial is deleted and ONE clean restart from zero succeeds.
func TestDownloadCorruptedResumeRetriesFromZero(t *testing.T) {
	payload := []byte(strings.Repeat("0123456789", 10)) // 100 bytes
	cut := 40
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch log.add(r) {
		case 0:
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			_, _ = w.Write(payload[:cut])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		case 1: // resume answered with CORRUPTED bytes for the requested range
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(strings.Repeat("X", len(payload)-cut)))
		default: // clean restart: full correct body
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write(payload)
		}
	}))
	t.Cleanup(srv.Close)

	u := tuned(srv, 3)
	dst := t.TempDir() + "/out.bin"
	if err := u.downloadURL(context.Background(), srv.URL+"/rave-mate.exe", shaOf(payload), dst, nil); err != nil {
		t.Fatalf("corrupted-resume recovery failed: %v", err)
	}
	if got, _ := fileSHA(dst); got != shaOf(payload) {
		t.Fatal("final file corrupt after clean-restart retry")
	}
	if n := log.count(); n != 3 {
		t.Fatalf("want 3 requests (cut, corrupted resume, clean restart), got %d", n)
	}
	if log.rngs[2] != "" {
		t.Fatalf("clean restart must not send Range, got %q", log.rngs[2])
	}
}

// TestDownloadClientErrorNoRetry: a 4xx is permanent - exactly one request, no retries.
func TestDownloadClientErrorNoRetry(t *testing.T) {
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	u := tuned(srv, 3)
	err := u.downloadURL(context.Background(), srv.URL+"/rave-mate.exe", testSHA, t.TempDir()+"/out.bin", nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want 404 failure, got %v", err)
	}
	if n := log.count(); n != 1 {
		t.Fatalf("4xx must not retry: got %d requests", n)
	}
}

// TestDownloadServerErrorRetries: 5xx is transient - retried, then succeeds.
func TestDownloadServerErrorRetries(t *testing.T) {
	payload := []byte("rave-bytes-after-5xx")
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if log.add(r) == 0 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)

	u := tuned(srv, 2)
	dst := t.TempDir() + "/out.bin"
	if err := u.downloadURL(context.Background(), srv.URL+"/rave-mate.exe", shaOf(payload), dst, nil); err != nil {
		t.Fatalf("5xx retry failed: %v", err)
	}
	if n := log.count(); n != 2 {
		t.Fatalf("want 2 requests (5xx + retry), got %d", n)
	}
}

// TestDownloadStallErrorMessage: exhausting retries on a stalled transfer reports what
// happened and how far it got - not a bare context error.
func TestDownloadStallErrorMessage(t *testing.T) {
	payload := []byte(strings.Repeat("z", 100))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload[:40])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	u := tuned(srv, 1)
	err := u.downloadURL(context.Background(), srv.URL+"/rave-mate.exe", shaOf(payload), t.TempDir()+"/out.bin", nil)
	if err == nil {
		t.Fatal("want failure after exhausted retries")
	}
	for _, want := range []string{"gave up after 1 retries", "40%", "stalled"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "Client.Timeout") {
		t.Fatalf("bare context/timeout error leaked: %q", err.Error())
	}
}

// TestDownloadRangeIgnoredRestartsClean: a server that ignores Range (200) → restart from
// zero, final file still correct.
func TestDownloadRangeIgnoredRestartsClean(t *testing.T) {
	payload := []byte(strings.Repeat("0123456789", 10))
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if log.add(r) == 0 {
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			_, _ = w.Write(payload[:40])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(payload) // 200 despite Range: server doesn't do ranges
	}))
	t.Cleanup(srv.Close)

	u := tuned(srv, 3)
	dst := t.TempDir() + "/out.bin"
	if err := u.downloadURL(context.Background(), srv.URL+"/rave-mate.exe", shaOf(payload), dst, nil); err != nil {
		t.Fatalf("range-ignored download failed: %v", err)
	}
	if got, _ := fileSHA(dst); got != shaOf(payload) {
		t.Fatal("content mismatch when server ignored Range")
	}
	if fi, err := os.Stat(dst); err != nil || fi.Size() != int64(len(payload)) {
		t.Fatalf("dst size = %v err=%v, want %d (no doubled bytes)", fi, err, len(payload))
	}
}

// TestDownloadRedirectRefusalNoRetry: a redirect-policy refusal is permanent - one attempt,
// no retry/backoff loop.
func TestDownloadRedirectRefusalNoRetry(t *testing.T) {
	log := &reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		http.Redirect(w, r, "https://evil.example.com/rave-mate.exe", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	u := tuned(srv, 3)
	err := u.downloadURL(context.Background(), srv.URL+"/rave-mate.exe", testSHA, t.TempDir()+"/out.bin", nil)
	if err == nil || !strings.Contains(err.Error(), "refusing cross-origin redirect") {
		t.Fatalf("want redirect refusal, got %v", err)
	}
	if n := log.count(); n != 1 {
		t.Fatalf("policy refusal must not retry: got %d requests", n)
	}
}

func TestParseContentRange(t *testing.T) {
	cases := []struct {
		in         string
		start, tot int64
		ok         bool
	}{
		{"bytes 40-99/100", 40, 100, true},
		{"bytes 0-9/10", 0, 10, true},
		{"bytes 5-9/*", 5, 0, true},
		{"", 0, 0, false},
		{"items 0-9/10", 0, 0, false},
		{"bytes x-9/10", 0, 0, false},
		{"bytes 0-9", 0, 0, false},
	}
	for _, c := range cases {
		s, tot, ok := parseContentRange(c.in)
		if s != c.start || tot != c.tot || ok != c.ok {
			t.Fatalf("parseContentRange(%q) = (%d,%d,%v), want (%d,%d,%v)", c.in, s, tot, ok, c.start, c.tot, c.ok)
		}
	}
}
