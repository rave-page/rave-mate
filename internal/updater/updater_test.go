package updater

import (
	"context"
	"fmt"
	"testing"
	"time"

	"rave.page/mate/internal/shared/selfupdate"
)

// fakeInstaller records Install calls; err makes it fail.
type fakeInstaller struct {
	calls int
	err   error
}

func (f *fakeInstaller) Install() error { f.calls++; return f.err }

// fakeFeed scripts Available/Download outcomes.
type fakeFeed struct {
	enabled  bool
	rel      *selfupdate.Release
	avail    bool
	checkErr error
	dlErr    error
	inst     *fakeInstaller
	checks   int
}

func (f *fakeFeed) Enabled() bool { return f.enabled }
func (f *fakeFeed) Available(context.Context) (*selfupdate.Release, bool, error) {
	f.checks++
	return f.rel, f.avail, f.checkErr
}
func (f *fakeFeed) Download(_ context.Context, _ *selfupdate.Release, onProgress func(done, total int64)) (Installer, error) {
	if onProgress != nil {
		onProgress(50, 100)
		onProgress(100, 100)
	}
	if f.dlErr != nil {
		return nil, f.dlErr
	}
	return f.inst, nil
}

func rel(v string, build int) *selfupdate.Release {
	return &selfupdate.Release{Version: v, Build: build, Notes: "notes"}
}

// notifyStore is an in-memory LastNotified/SetNotified pair (simulates the config field).
type notifyStore struct{ v string }

func (s *notifyStore) get() string  { return s.v }
func (s *notifyStore) set(v string) { s.v = v }

func newTestMgr(f *fakeFeed, store *notifyStore, notified *[]string, states *[]Status) *Manager {
	return New(Config{
		Feed:         f,
		LastNotified: store.get,
		SetNotified:  store.set,
		Notify: func(r *selfupdate.Release) {
			if notified != nil {
				*notified = append(*notified, r.Version)
			}
		},
		OnChange: func(st Status) {
			if states != nil {
				*states = append(*states, st)
			}
		},
	})
}

// TestHappyPath: idle → available (notify once) → downloading (progress) → downloaded → staged.
func TestHappyPath(t *testing.T) {
	inst := &fakeInstaller{}
	f := &fakeFeed{enabled: true, rel: rel("v2", 2), avail: true, inst: inst}
	store := &notifyStore{}
	var notified []string
	m := newTestMgr(f, store, &notified, nil)

	if m.Status().State != Idle {
		t.Fatal("want Idle before first check")
	}
	m.Check(context.Background())
	if st := m.Status(); st.State != Available || st.Rel.Version != "v2" {
		t.Fatalf("want Available v2, got %+v", st)
	}
	if len(notified) != 1 || notified[0] != "v2" || store.v != "v2" {
		t.Fatalf("want one persisted notification for v2, got %v store=%q", notified, store.v)
	}

	m.mu.Lock() // drive download synchronously (StartDownload spawns a goroutine)
	rl := m.st.Rel
	m.st.State = Downloading
	m.mu.Unlock()
	m.download(rl)
	if st := m.Status(); st.State != Downloaded || st.Progress != 1 || st.Err != "" {
		t.Fatalf("want Downloaded, got %+v", st)
	}

	m.mu.Lock()
	in := Installer(inst)
	m.inst = in
	m.mu.Unlock()
	m.install(in)
	if st := m.Status(); st.State != Staged || st.Err != "" {
		t.Fatalf("want Staged, got %+v", st)
	}
	if inst.calls != 1 {
		t.Fatalf("want 1 Install call, got %d", inst.calls)
	}
}

// TestNotifyOncePerVersionAcrossRestart: a second manager sharing the persisted store must not
// re-notify the same version; a NEWER version notifies again.
func TestNotifyOncePerVersionAcrossRestart(t *testing.T) {
	store := &notifyStore{}
	var notified []string

	f1 := &fakeFeed{enabled: true, rel: rel("v2", 2), avail: true}
	newTestMgr(f1, store, &notified, nil).Check(context.Background())
	if len(notified) != 1 {
		t.Fatalf("want first detection notified, got %v", notified)
	}

	// "Restart": fresh manager, same persisted store, same version → silent.
	f2 := &fakeFeed{enabled: true, rel: rel("v2", 2), avail: true}
	newTestMgr(f2, store, &notified, nil).Check(context.Background())
	if len(notified) != 1 {
		t.Fatalf("same version must not re-notify after restart, got %v", notified)
	}

	// Newer version → notifies once more.
	f3 := &fakeFeed{enabled: true, rel: rel("v3", 3), avail: true}
	m3 := newTestMgr(f3, store, &notified, nil)
	m3.Check(context.Background())
	m3.Check(context.Background())
	if len(notified) != 2 || notified[1] != "v3" {
		t.Fatalf("want exactly one v3 notification, got %v", notified)
	}
}

// TestCheckErrorSurfacesAndBacksOff: failures set Err, keep state, and grow nextDelay (capped).
func TestCheckErrorSurfacesAndBacksOff(t *testing.T) {
	f := &fakeFeed{enabled: true, checkErr: fmt.Errorf("manifest signature invalid (not signed by the build key)")}
	m := newTestMgr(f, &notifyStore{}, nil, nil)

	base := m.nextDelay()
	if base != DefaultInterval {
		t.Fatalf("want base interval %v, got %v", DefaultInterval, base)
	}
	for i := 1; i <= 8; i++ {
		m.Check(context.Background())
		want := DefaultInterval << min(i, maxBackoffMul)
		if got := m.nextDelay(); got != want {
			t.Fatalf("after %d fails want delay %v, got %v", i, want, got)
		}
	}
	if st := m.Status(); st.State != Idle || st.Err == "" {
		t.Fatalf("want Idle with surfaced error, got %+v", st)
	}

	// Recovery resets the backoff and clears the error.
	f.checkErr, f.avail, f.rel = nil, true, rel("v2", 2)
	m.Check(context.Background())
	if got := m.nextDelay(); got != DefaultInterval {
		t.Fatalf("want reset delay, got %v", got)
	}
	if st := m.Status(); st.State != Available || st.Err != "" {
		t.Fatalf("want clean Available after recovery, got %+v", st)
	}
}

// TestDownloadFailureNeverInstalls: a verification failure returns to Available with Err set;
// Install from that state is refused.
func TestDownloadFailureNeverInstalls(t *testing.T) {
	inst := &fakeInstaller{}
	f := &fakeFeed{enabled: true, rel: rel("v2", 2), avail: true, dlErr: fmt.Errorf("checksum mismatch: got x want y"), inst: inst}
	m := newTestMgr(f, &notifyStore{}, nil, nil)
	m.Check(context.Background())

	m.mu.Lock()
	rl := m.st.Rel
	m.st.State = Downloading
	m.mu.Unlock()
	m.download(rl)
	st := m.Status()
	if st.State != Available || st.Err == "" {
		t.Fatalf("want Available + surfaced error, got %+v", st)
	}
	m.Install() // wrong state → no-op
	if inst.calls != 0 {
		t.Fatal("Install must never run without a verified download")
	}
}

// TestCheckSkipsWhileBusyOrStaged: polls are suppressed during download/downloaded/staged.
func TestCheckSkipsWhileBusyOrStaged(t *testing.T) {
	f := &fakeFeed{enabled: true, rel: rel("v2", 2), avail: true}
	m := newTestMgr(f, &notifyStore{}, nil, nil)
	m.Check(context.Background())
	if f.checks != 1 {
		t.Fatalf("want 1 check, got %d", f.checks)
	}
	for _, s := range []State{Downloading, Downloaded, Staged} {
		m.mu.Lock()
		m.st.State = s
		m.mu.Unlock()
		m.Check(context.Background())
	}
	if f.checks != 1 {
		t.Fatalf("busy/staged states must skip polls; got %d checks", f.checks)
	}
}

// TestGuards: StartDownload outside Available and Install outside Downloaded are no-ops;
// disabled feed never polls.
func TestGuards(t *testing.T) {
	f := &fakeFeed{enabled: false}
	m := newTestMgr(f, &notifyStore{}, nil, nil)
	m.Check(context.Background())
	m.StartDownload()
	m.Install()
	if f.checks != 0 || m.Status().State != Idle {
		t.Fatalf("disabled feed must stay Idle, got %+v after %d checks", m.Status(), f.checks)
	}
	if m.Enabled() {
		t.Fatal("want disabled")
	}
}

// TestRunTickerGating: Run performs the first check after the settle delay and honors CheckNow
// kicks; stop terminates the loop.
func TestRunTickerGating(t *testing.T) {
	f := &fakeFeed{enabled: true, rel: rel("v2", 2), avail: true}
	m := New(Config{Feed: f, Interval: 20 * time.Millisecond})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { m.Run(stop); close(done) }()

	m.CheckNow() // immediate kick beats the settle delay
	deadline := time.Now().Add(2 * time.Second)
	for f.checks == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.checks == 0 {
		t.Fatal("CheckNow kick did not trigger a poll")
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// TestOnChangeEmits: state transitions emit; identical re-checks stay silent.
func TestOnChangeEmits(t *testing.T) {
	f := &fakeFeed{enabled: true, rel: rel("v2", 2), avail: true}
	var states []Status
	m := newTestMgr(f, &notifyStore{}, nil, &states)
	m.Check(context.Background())
	m.Check(context.Background()) // same release → no new emit
	if len(states) != 1 || states[0].State != Available {
		t.Fatalf("want exactly one Available emit, got %+v", states)
	}
}
