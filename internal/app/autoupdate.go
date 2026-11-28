package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/shared/selfupdate"
	"rave.page/mate/internal/updater"
	"rave.page/mate/internal/version"
)

// Unattended self-update for the headless Linux service (the `sus` sender). Windows keeps its
// user-driven flow (webui nav/tray/settings + ctl SELF-UPDATE). See docs/dev/RELEASING.md
// "Linux unattended update".

// NoAutoUpdateEnv disables the unattended updater (e.g. Environment= in the systemd unit).
const NoAutoUpdateEnv = "RAVE_MATE_NO_AUTOUPDATE"

// updateHealthWindow: a freshly swapped build must stay up this long after its ctl socket serves
// before Guard.Confirm accepts it; dying earlier counts toward the rollback.
const updateHealthWindow = 30 * time.Second

// reexecBackstop: if the clean shutdown wedges after a swap, exec anyway (CLOEXEC closes our
// fds; feature children see their stdio EOF and exit).
const reexecBackstop = 60 * time.Second

var reexecExe atomic.Pointer[string]

// ReexecTarget returns the swapped executable main must exec after Run returns ("" = plain exit).
func ReexecTarget() string {
	if p := reexecExe.Load(); p != nil {
		return *p
	}
	return ""
}

// autoUpdateOffReason returns why the unattended updater does not run here ("" = it runs).
func autoUpdateOffReason(serviceMode bool) string {
	switch {
	case runtime.GOOS != "linux":
		return "" // not applicable: no status token off Linux
	case !serviceMode:
		return "windowed (use the in-app update flow)"
	case os.Getenv(NoAutoUpdateEnv) != "":
		return "disabled by " + NoAutoUpdateEnv
	case version.FeedURL == "":
		return "unstamped build (no feed)"
	}
	return ""
}

// updateBootGuard runs the rollback guard once per PRIMARY start (a second launch must not count
// as a boot of the new build). Returns the exe to re-exec when it just rolled back to .prev.
// Logbus isn't up yet: stderr (journald) carries it; confirmUpdateHealth re-logs it on the bus.
func updateBootGuard() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	exe, err := selfupdate.ExePath()
	if err != nil {
		return ""
	}
	selfupdate.CleanupOld() // a .new left by a download this process's predecessor died in
	act, err := selfupdate.NewGuard(exe).Boot(version.BuildNum())
	if err != nil {
		fmt.Fprintln(os.Stderr, "rave-mate: update guard:", err)
	}
	if act == selfupdate.BootRolledBack {
		fmt.Fprintf(os.Stderr, "rave-mate: build %s never became healthy - restored %s, re-executing the previous build\n", version.Build, exe+selfupdate.PrevSuffix)
		return exe
	}
	return ""
}

// startAutoUpdate starts the unattended loop (headless Linux). quit cancels the app run context;
// busy reports a live-set reason to defer the swap. Returns nil when not applicable.
func startAutoUpdate(ctx context.Context, log *logbus.Bus, quit context.CancelFunc, busy func() string) *updater.Auto {
	exe, err := selfupdate.ExePath()
	if err != nil {
		log.Warn("update", "unattended updater off: "+err.Error(), nil)
		return nil
	}
	g := selfupdate.NewGuard(exe)
	a := updater.NewAuto(updater.AutoConfig{
		Feed:    updater.WrapAutoFeed(selfupdate.New(version.FeedURL, version.BuildNum(), version.UpdatePubKey)),
		Guard:   g,
		Current: version.BuildNum(),
		Log:     log,
		Busy:    busy,
		Restart: func(rel *selfupdate.Release) {
			reexecExe.Store(&exe)
			time.AfterFunc(reexecBackstop, func() {
				log.Warn("update", "clean shutdown wedged - re-exec backstop", map[string]any{"build": rel.Build})
				if err := selfupdate.Reexec(exe); err != nil {
					log.Error("update", "re-exec backstop failed", map[string]any{"error": err.Error()})
				}
			})
			quit()
		},
	})
	log.Info("update", "unattended updater armed", map[string]any{"exe": exe, "feed": version.FeedURL, "build": version.BuildNum()})
	debuglog.Go(log, "autoupdate", func() { a.Run(ctx) })
	return a
}

// confirmUpdateHealth confirms a freshly swapped build once the app has served for
// updateHealthWindow (the rollback guard's health signal), and logs a rollback that brought us
// here. No-op when nothing is pending.
func confirmUpdateHealth(ctx context.Context, log *logbus.Bus) {
	exe, err := selfupdate.ExePath()
	if err != nil {
		return
	}
	g := selfupdate.NewGuard(exe)
	if rb := g.LastRollback(); rb != nil && time.Since(rb.At) < 10*time.Minute {
		log.Warn("update", fmt.Sprintf("rolled back: build %d never became healthy (%d boots) - running build %d, %d pinned bad", rb.To, rb.Boots, rb.From, rb.To), nil)
	}
	if g.Pending() == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(updateHealthWindow):
	}
	ok, err := g.Confirm(version.BuildNum())
	switch {
	case err != nil:
		log.Warn("update", "health confirm failed", map[string]any{"error": err.Error()})
	case ok:
		log.Info("update", "update confirmed healthy", map[string]any{"build": version.BuildNum()})
	}
}

// applyAndReexec is the Linux one-shot apply (ctl SELF-UPDATE / peer remote update): download +
// verify → probe → guard mark → swap → clean quit → main re-execs (same PID). Pinned-bad builds
// are refused; failures log and leave the running build untouched.
func (c *appControl) applyAndReexec(rel *selfupdate.Release, u *selfupdate.Updater) {
	exe, err := selfupdate.ExePath()
	if err != nil {
		c.log.Warn("update", "apply failed", map[string]any{"error": err.Error()})
		return
	}
	g := selfupdate.NewGuard(exe)
	if g.IsBad(rel.Build) {
		c.log.Warn("update", "apply refused: build pinned bad", map[string]any{"build": rel.Build})
		return
	}
	dctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	st, err := u.Download(dctx, rel, nil)
	cancel()
	if err != nil {
		c.log.Warn("update", "apply failed", map[string]any{"build": rel.Build, "error": err.Error(), "fix": notWritableHint(err)})
		return
	}
	if err := st.Probe(context.Background(), rel.Build); err != nil {
		st.Discard()
		_ = g.PinBad(rel.Build)
		c.log.Warn("update", "apply refused: new binary failed its start probe - build pinned bad", map[string]any{"build": rel.Build, "error": err.Error()})
		return
	}
	if err := g.Mark(version.BuildNum(), rel.Build); err != nil {
		st.Discard()
		c.log.Warn("update", "apply aborted: cannot write rollback marker", map[string]any{"error": err.Error()})
		return
	}
	if err := st.Install(); err != nil {
		_ = g.Clear()
		c.log.Warn("update", "install failed", map[string]any{"error": err.Error()})
		return
	}
	c.log.Info("update", "installed - restarting into the new build", map[string]any{"build": rel.Build})
	reexecExe.Store(&exe)
	c.Quit()
}

// notWritableHint returns the operator fix for a NotWritableError ("" otherwise).
func notWritableHint(err error) string {
	var nw *selfupdate.NotWritableError
	if errors.As(err, &nw) {
		return updater.NotWritableFix
	}
	return ""
}

// updateBusy is the live-set gate for the unattended swap: any media route (send OR receive -
// a peer consuming this sender, or this node consuming one) or a live stream-bridge session
// defers it. A merely connected peer with no route does not. Snapshot reads only, never blocks.
func updateBusy(media medialink.MediaControl, pub *featurehost.StreamProxy) string {
	if media != nil {
		if rs := media.Stats(); len(rs) > 0 {
			return fmt.Sprintf("%d media route(s) active (%s %s)", len(rs), rs[0].Direction, rs[0].Peer)
		}
	}
	if pub != nil && pub.Status().IsLive {
		return "stream bridge live"
	}
	return ""
}

// updateStatusToken renders the update state for ctl STATUS ("" = nothing to show).
func (c *appControl) updateStatusToken() string {
	switch {
	case c.autoUpd != nil:
		return c.autoUpd.Status().String()
	case c.autoUpdOff != "":
		return "off(" + c.autoUpdOff + ")"
	}
	return ""
}
