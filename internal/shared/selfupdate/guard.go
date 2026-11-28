package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// Unattended-update safety net (Linux headless sender: nobody in front of it).
//
//	pre-swap   Staged.Probe: the NEW binary must run `version` and report the expected build
//	           (catches loader/glibc/missing-lib/init-panic failures before anything is touched).
//	swap       Guard.Mark(from,to) → Staged.Install (exe.prev kept) → re-exec.
//	post-swap  every boot calls Guard.Boot: the new build gets MaxUnconfirmedBoots tries to reach
//	           Guard.Confirm (app healthy for a while); the next boot restores exe.prev, pins the
//	           build bad (never retried) and re-execs the old binary.
//
// State is one small JSON file beside the exe (".<name>.update.json"): only written after a
// swap, which already required that directory to be writable.

// MaxUnconfirmedBoots is how many boots a freshly swapped build gets to Confirm before rollback.
// 2 = one unlucky restart (operator, unrelated crash) is tolerated; a crash loop is not.
const MaxUnconfirmedBoots = 2

// maxBadPins bounds the pinned-bad list (drop-oldest): build numbers only ever grow.
const maxBadPins = 32

// probeTimeout bounds the staged binary's `version` run.
const probeTimeout = 20 * time.Second

// Attempt is one swap from build From to build To.
type Attempt struct {
	From  int       `json:"from"`
	To    int       `json:"to"`
	Boots int       `json:"boots"`
	At    time.Time `json:"at"`
}

type guardState struct {
	Pending  *Attempt `json:"pending,omitempty"`
	Bad      []int    `json:"bad,omitempty"`
	Rollback *Attempt `json:"rollback,omitempty"` // last rollback, for status/log
}

// BootAction is Guard.Boot's verdict.
type BootAction int

const (
	BootNormal     BootAction = iota // carry on
	BootRolledBack                   // exe now holds the previous build: re-exec it
)

// Guard is the rollback guard for one executable path.
type Guard struct {
	exe  string
	path string
}

// NewGuard binds a guard to exe (symlinks resolved, see ExePath).
func NewGuard(exe string) *Guard {
	return &Guard{exe: exe, path: filepath.Join(filepath.Dir(exe), "."+filepath.Base(exe)+".update.json")}
}

func (g *Guard) load() guardState {
	var st guardState
	if b, err := os.ReadFile(g.path); err == nil {
		_ = json.Unmarshal(b, &st) // corrupt = empty state: never blocks a boot
	}
	return st
}

// save writes state atomically (temp + rename in the same dir).
func (g *Guard) save(st guardState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := g.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, g.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Mark records a swap about to happen (call BEFORE Staged.Install).
func (g *Guard) Mark(from, to int) error {
	st := g.load()
	st.Pending = &Attempt{From: from, To: to, At: time.Now().UTC()}
	return g.save(st)
}

// Clear drops a pending attempt (the swap never landed).
func (g *Guard) Clear() error {
	st := g.load()
	if st.Pending == nil {
		return nil
	}
	st.Pending = nil
	return g.save(st)
}

// IsBad reports whether build was pinned bad by a failed probe or rollback.
func (g *Guard) IsBad(build int) bool {
	for _, b := range g.load().Bad {
		if b == build {
			return true
		}
	}
	return false
}

// PinBad pins build as bad: the updater never offers it again.
func (g *Guard) PinBad(build int) error {
	st := g.load()
	pinBad(&st, build)
	return g.save(st)
}

func pinBad(st *guardState, build int) {
	for _, b := range st.Bad {
		if b == build {
			return
		}
	}
	st.Bad = append(st.Bad, build)
	if len(st.Bad) > maxBadPins {
		st.Bad = st.Bad[len(st.Bad)-maxBadPins:]
	}
}

// Pending returns the unconfirmed attempt, nil when none.
func (g *Guard) Pending() *Attempt { return g.load().Pending }

// LastRollback returns the most recent rollback, nil when none.
func (g *Guard) LastRollback() *Attempt { return g.load().Rollback }

// Boot runs once per process start, before the app: counts boots of a freshly swapped build and
// rolls back once it exceeds MaxUnconfirmedBoots without a Confirm. A pending attempt for another
// build (operator replaced the binary by hand) is dropped, nothing pinned.
func (g *Guard) Boot(cur int) (BootAction, error) {
	st := g.load()
	if st.Pending == nil {
		return BootNormal, nil
	}
	if cur != st.Pending.To {
		st.Pending = nil
		return BootNormal, g.save(st)
	}
	st.Pending.Boots++
	if st.Pending.Boots <= MaxUnconfirmedBoots {
		return BootNormal, g.save(st)
	}
	if err := g.rollback(st); err != nil {
		return BootNormal, err
	}
	return BootRolledBack, nil
}

// Confirm marks the running build healthy: clears a pending attempt for cur. Reports whether an
// attempt was confirmed.
func (g *Guard) Confirm(cur int) (bool, error) {
	st := g.load()
	if st.Pending == nil || st.Pending.To != cur {
		return false, nil
	}
	st.Pending = nil
	return true, g.save(st)
}

// RollbackNow restores exe.prev over exe and pins the pending build bad (e.g. the re-exec of the
// new binary itself failed). Errors when there is no pending attempt.
func (g *Guard) RollbackNow() error {
	st := g.load()
	if st.Pending == nil {
		return errors.New("no pending update to roll back")
	}
	return g.rollback(st)
}

// rollback: pin + clear FIRST (persisted), then rename exe.prev over exe - a crash between the two
// can never re-trigger the swap, and a missing .prev still leaves the build pinned.
func (g *Guard) rollback(st guardState) error {
	att := *st.Pending
	pinBad(&st, att.To)
	st.Pending = nil
	st.Rollback = &Attempt{From: att.From, To: att.To, Boots: att.Boots, At: time.Now().UTC()}
	if err := g.save(st); err != nil {
		return err
	}
	if err := os.Rename(g.exe+PrevSuffix, g.exe); err != nil {
		return fmt.Errorf("restore %s: %w", g.exe+PrevSuffix, err)
	}
	syncDir(filepath.Dir(g.exe))
	return nil
}

var probeBuildRe = regexp.MustCompile(`\bbuild (\d+)\b`)

// Probe runs the staged binary's `version` subcommand (bounded) and requires it to report build
// want. The new binary's dynamic loader, glibc symbol versions, package inits and stamp all run
// here - a nightly that cannot start on this host is refused BEFORE it replaces the running one.
func (s *Staged) Probe(ctx context.Context, want int) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var out capBuf
	cmd := exec.CommandContext(ctx, s.tmp, "version")
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	text := bytes.TrimSpace(out.b)
	if err != nil {
		return fmt.Errorf("probe %s: %w (output %q)", filepath.Base(s.tmp), err, text)
	}
	m := probeBuildRe.FindSubmatch(text)
	if m == nil {
		return fmt.Errorf("probe: new binary reports no build (output %q)", text)
	}
	if got, _ := strconv.Atoi(string(m[1])); got != want {
		return fmt.Errorf("probe: new binary reports build %d, manifest says %d", got, want)
	}
	return nil
}

// capBuf keeps the first 4 KiB written (bounded probe output), discarding the rest.
type capBuf struct{ b []byte }

func (c *capBuf) Write(p []byte) (int, error) {
	if room := 4096 - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
