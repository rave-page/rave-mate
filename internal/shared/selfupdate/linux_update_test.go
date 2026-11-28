package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Linux-target updater tests. The selection/verify/swap logic is OS-neutral (u.goos + exePath
// seams), so these run on Windows too; Probe (exec of a staged script) and the permission-bit
// unwritable case are Linux-only.

func hexSHA(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// linuxFeed serves a signed two-target manifest: /win.exe + /linux.bin with their own bytes.
type linuxFeed struct {
	srv                *httptest.Server
	priv               ed25519.PrivateKey
	pubB64             string
	winBytes, linBytes []byte
	linSHA             string // manifest linux_sha256 ("" = real hash)
	tamper             bool   // serve a manifest that differs from the signed bytes
	cut                bool   // linux payload: send half then drop the connection
}

func newLinuxFeed(t *testing.T) *linuxFeed {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	f := &linuxFeed{priv: priv, pubB64: base64.StdEncoding.EncodeToString(pub),
		winBytes: []byte("windows-exe-v2"), linBytes: []byte("#!/bin/sh\necho 'rave-mate nightly-x (x) build 4242'\n")}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+manifestName+".sig"):
			fmt.Fprint(w, base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, f.manifest(false))))
		case strings.HasSuffix(r.URL.Path, "/"+manifestName):
			_, _ = w.Write(f.manifest(f.tamper))
		case strings.HasSuffix(r.URL.Path, "/win.exe"):
			_, _ = w.Write(f.winBytes)
		case strings.HasSuffix(r.URL.Path, "/linux.bin"):
			if f.cut {
				w.Header().Set("Content-Length", fmt.Sprint(len(f.linBytes)))
				_, _ = w.Write(f.linBytes[:len(f.linBytes)/2])
				if hj, ok := w.(http.Hijacker); ok {
					if c, _, err := hj.Hijack(); err == nil {
						_ = c.Close()
					}
				}
				return
			}
			_, _ = w.Write(f.linBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *linuxFeed) manifest(tampered bool) []byte {
	ls := f.linSHA
	if ls == "" {
		ls = hexSHA(f.linBytes)
	}
	lu := f.srv.URL + "/linux.bin"
	if tampered {
		lu = f.srv.URL + "/linux.bin?evil=1"
	}
	return fmt.Appendf(nil, `{"version":"nightly-x","build":4242,"url":%q,"sha256":%q,"linux_url":%q,"linux_sha256":%q}`,
		f.srv.URL+"/win.exe", hexSHA(f.winBytes), lu, ls)
}

// updater returns a linux-target updater whose "running exe" is a temp file holding old bytes.
func (f *linuxFeed) updater(t *testing.T, dir string) (*Updater, string) {
	t.Helper()
	exe := filepath.Join(dir, "rave-mate")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := New(f.srv.URL, 4000, f.pubB64)
	u.goos = "linux"
	u.exePath = func() (string, error) { return exe, nil }
	u.dlRetries, u.dlBackoff = 0, time.Millisecond
	return u, exe
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// Manifest selects the Linux asset (linux_url/linux_sha256), stages it as exe.new, 0755.
func TestLinuxManifestSelectsLinuxAsset(t *testing.T) {
	f := newLinuxFeed(t)
	u, exe := f.updater(t, t.TempDir())
	rel, avail, err := u.Available(context.Background())
	if err != nil || !avail {
		t.Fatalf("available: %v %v", avail, err)
	}
	st, err := u.Download(context.Background(), rel, nil)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if st.tmp != exe+".new" || mustRead(t, st.tmp) != string(f.linBytes) {
		t.Fatalf("staged %s = %q, want the linux payload", st.tmp, mustRead(t, st.tmp))
	}
	if mustRead(t, exe) != "old-binary" {
		t.Fatal("Download touched the running exe")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(st.tmp); fi.Mode().Perm() != 0o755 {
			t.Fatalf("staged mode %v, want 0755", fi.Mode().Perm())
		}
	}
	st.Discard()
	// A manifest without a Linux target is refused on Linux (no fallback to the Windows exe).
	var bare *httptest.Server
	bare = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"build":9,"url":%q,"sha256":%q}`, bare.URL+"/win.exe", testSHA)
	}))
	t.Cleanup(bare.Close)
	ub := New(bare.URL, 1, "")
	ub.goos = "linux"
	if _, err := ub.Latest(context.Background()); err == nil {
		t.Fatal("manifest without linux_url must be refused on linux")
	}
}

// sha256 mismatch refuses and leaves nothing behind; a manifest not matching its Ed25519
// signature is refused before anything in it is trusted (same trust root as Windows).
func TestLinuxShaAndSignatureMismatchRefuse(t *testing.T) {
	f := newLinuxFeed(t)
	f.linSHA = strings.Repeat("0", 64)
	dir := t.TempDir()
	u, exe := f.updater(t, dir)
	rel, _, err := u.Available(context.Background())
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	if _, err := u.Download(context.Background(), rel, nil); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	if _, err := os.Stat(exe + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched payload left behind: %v", err)
	}
	if mustRead(t, exe) != "old-binary" {
		t.Fatal("exe changed after a refused update")
	}

	f.linSHA, f.tamper = "", true
	if _, err := u.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "signature invalid") {
		t.Fatalf("want signature refusal, got %v", err)
	}
}

// An unwritable exe dir is reported as NotWritableError BEFORE any download.
func TestLinuxNotWritableBeforeDownload(t *testing.T) {
	f := newLinuxFeed(t)
	hits := 0
	inner := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/linux.bin") {
			hits++
		}
		inner.ServeHTTP(w, r)
	})
	u, _ := f.updater(t, t.TempDir())
	missing := filepath.Join(t.TempDir(), "gone", "rave-mate") // dir doesn't exist: OS-neutral unwritable
	u.exePath = func() (string, error) { return missing, nil }
	rel, _, err := u.Available(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.Download(context.Background(), rel, nil)
	var nw *NotWritableError
	if !errors.As(err, &nw) || nw.Path != missing {
		t.Fatalf("want NotWritableError for %s, got %v", missing, err)
	}
	if hits != 0 {
		t.Fatalf("downloaded %d times despite an unwritable target", hits)
	}
}

// Root-owned-install analogue: a 0555 dir is unwritable for a non-root user (Linux only).
func TestLinuxNotWritablePermissionBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits: run in the Linux container")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits: run as a non-root user")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "rave-mate")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	var nw *NotWritableError
	if err := CheckWritable(exe); !errors.As(err, &nw) {
		t.Fatalf("want NotWritableError, got %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckWritable(exe); err != nil {
		t.Fatalf("writable dir reported unwritable: %v", err)
	}
}

// Atomic swap: exe gets the new bytes, exe.prev keeps the old, exe.new is gone.
func TestLinuxInstallKeepsPrev(t *testing.T) {
	f := newLinuxFeed(t)
	u, exe := f.updater(t, t.TempDir())
	rel, _, err := u.Available(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st, err := u.Download(context.Background(), rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Install(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if mustRead(t, exe) != string(f.linBytes) || mustRead(t, exe+PrevSuffix) != "old-binary" {
		t.Fatalf("exe=%q prev=%q", mustRead(t, exe), mustRead(t, exe+PrevSuffix))
	}
	if _, err := os.Stat(exe + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staged .new still present after install")
	}
}

// An interrupted download (connection cut mid-body, then a cancelled ctx) leaves no file.
func TestLinuxInterruptedDownloadLeavesNothing(t *testing.T) {
	f := newLinuxFeed(t)
	f.cut = true
	dir := t.TempDir()
	u, exe := f.updater(t, dir)
	rel, _, err := u.Available(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(context.Background(), rel, nil); err == nil {
		t.Fatal("cut download must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := u.Download(ctx, rel, nil); err == nil {
		t.Fatal("cancelled download must fail")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() != filepath.Base(exe) {
			t.Fatalf("left behind: %s", e.Name())
		}
	}
}

// Byte cap: a payload over dlMax is refused (permanent) and removed.
func TestLinuxDownloadSizeCap(t *testing.T) {
	f := newLinuxFeed(t)
	dir := t.TempDir()
	u, exe := f.updater(t, dir)
	u.dlMax = 8
	rel, _, err := u.Available(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(context.Background(), rel, nil); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("want size-cap refusal, got %v", err)
	}
	if _, err := os.Stat(exe + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversized payload left behind")
	}
}

// Rollback guard: a new build that never confirms is rolled back to .prev and pinned bad after
// MaxUnconfirmedBoots; a confirmed build clears the attempt.
func TestGuardRollbackRestoresPrevAndPins(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rave-mate")
	if err := os.WriteFile(exe, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe+PrevSuffix, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := NewGuard(exe)
	if err := g.Mark(100, 101); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= MaxUnconfirmedBoots; i++ {
		if act, err := g.Boot(101); act != BootNormal || err != nil {
			t.Fatalf("boot %d: %v %v", i, act, err)
		}
	}
	act, err := g.Boot(101)
	if act != BootRolledBack || err != nil {
		t.Fatalf("boot past the limit: %v %v, want rollback", act, err)
	}
	if mustRead(t, exe) != "old" || !g.IsBad(101) || g.Pending() != nil {
		t.Fatalf("exe=%q bad=%v pending=%v", mustRead(t, exe), g.IsBad(101), g.Pending())
	}
	if rb := g.LastRollback(); rb == nil || rb.To != 101 || rb.From != 100 {
		t.Fatalf("last rollback = %+v", rb)
	}
	if act, _ := g.Boot(100); act != BootNormal { // the restored old build boots normally
		t.Fatal("restored build must boot normally")
	}

	// Confirm path: healthy new build clears the attempt, nothing pinned.
	if err := g.Mark(100, 102); err != nil {
		t.Fatal(err)
	}
	if act, _ := g.Boot(102); act != BootNormal {
		t.Fatal("first boot must be normal")
	}
	if ok, err := g.Confirm(102); !ok || err != nil {
		t.Fatalf("confirm: %v %v", ok, err)
	}
	if g.Pending() != nil || g.IsBad(102) {
		t.Fatal("confirmed build must clear pending and stay unpinned")
	}
	// Operator replaced the binary by hand: a pending attempt for another build is dropped.
	if err := g.Mark(100, 103); err != nil {
		t.Fatal(err)
	}
	if act, _ := g.Boot(999); act != BootNormal || g.Pending() != nil || g.IsBad(103) {
		t.Fatal("foreign build must drop the attempt without pinning")
	}
}

// Probe runs the staged binary's `version` and requires the manifest build (Linux: needs exec).
func TestProbeRequiresBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec of a staged shell script: run in the Linux container")
	}
	dir := t.TempDir()
	mk := func(body string) *Staged {
		p := filepath.Join(dir, fmt.Sprintf("probe-%d", time.Now().UnixNano()))
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return &Staged{tmp: p}
	}
	if err := mk(`echo "rave-mate nightly-x (x) build 4242"`).Probe(context.Background(), 4242); err != nil {
		t.Fatalf("good probe: %v", err)
	}
	if err := mk(`echo "rave-mate nightly-x (x) build 4241"`).Probe(context.Background(), 4242); err == nil {
		t.Fatal("wrong build must fail the probe")
	}
	if err := mk(`echo "error while loading shared libraries" >&2; exit 127`).Probe(context.Background(), 4242); err == nil {
		t.Fatal("loader failure must fail the probe")
	}
}
