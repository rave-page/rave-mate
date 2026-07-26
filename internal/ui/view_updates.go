package ui

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/coord"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/shared/selfupdate"
	"rave.page/mate/internal/version"
)

// updatesCard shows the running version + a self-update control. On a dev build (no feed
// baked in) the updater is disabled and the card just reports the version.
func (u *UI) updatesCard() fyne.CanvasObject {
	cur := widget.NewLabel("Version: " + version.String())
	status := widget.NewLabel("")
	status.Wrapping = fyne.TextWrapWord
	st := u.newStatus(nil) // pushed by the check flow

	up := u.updater
	if up == nil || !up.Enabled() {
		status.SetText("This is a development build - updates are managed manually.")
		st.set(colMuted, "dev build")
		return featureCard("Updates", "", nil, st, cur, status)
	}
	st.set(colMuted, version.String())

	check := widget.NewButton("Check for updates", nil)
	check.OnTapped = func() {
		check.Disable()
		status.SetText("Checking…")
		go func() {
			defer debuglog.Recover(u.svc.Log, "update-check", false)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rel, avail, err := up.Available(ctx)
			fyne.Do(func() {
				check.Enable()
				switch {
				case err != nil:
					status.SetText("Check failed: " + err.Error())
					st.set(colBrandAmber, "check failed")
				case !avail:
					status.SetText("You're up to date.")
					st.set(colBrandMint, "up to date")
				default:
					status.SetText(fmt.Sprintf("Update available: %s - %s", rel.Version, rel.Notes))
					st.set(colBrandAmber, "update available: "+rel.Version)
					u.promptInstall(rel)
				}
			})
		}()
	}

	return featureCard("Updates", "Update rave-mate to the latest build from your branch feed.", nil, st,
		cur, status, check)
}

// promptInstall confirms, then downloads + swaps + offers a restart.
func (u *UI) promptInstall(rel *selfupdate.Release) {
	win := u.win
	body := fmt.Sprintf("Install %s (build %d)?\n\n%s", rel.Version, rel.Build, rel.Notes)
	dialog.NewConfirm("Update rave-mate", body, func(ok bool) {
		if !ok {
			return
		}
		u.runInstall(rel)
	}, win).Show()
}

// runInstall downloads the release with a progress dialog, then prompts to restart.
func (u *UI) runInstall(rel *selfupdate.Release) {
	bar := widget.NewProgressBar()
	msg := widget.NewLabel("Downloading…")
	prog := dialog.NewCustomWithoutButtons("Updating", container.NewVBox(msg, bar), u.win)
	prog.Show()

	go func() {
		defer debuglog.Recover(u.svc.Log, "update-apply", false)
		// No deadline: a total-time cap killed slow-but-flowing downloads; selfupdate's stall
		// watchdog + bounded retries end a dead transfer.
		ctx := context.Background()
		err := u.updater.Apply(ctx, rel, func(done, total int64) {
			if total <= 0 {
				return
			}
			fyne.Do(func() { bar.SetValue(float64(done) / float64(total)) })
		})
		fyne.Do(func() {
			prog.Hide()
			if err != nil {
				u.svc.Log.Warn("app", "update failed", map[string]any{"error": err.Error()})
				dialog.NewError(err, u.win).Show()
				return
			}
			u.svc.Log.Info("app", "update installed", map[string]any{"version": rel.Version, "build": rel.Build})
			dialog.NewConfirm("Update ready",
				"Update installed. Restart rave-mate now to apply?",
				func(ok bool) {
					if !ok {
						return
					}
					coord.NotifyRaveApp() // user-initiated → tell a co-located rave-app to update too
					if err := selfupdate.Relaunch(); err != nil {
						u.svc.Log.Warn("app", "relaunch failed", map[string]any{"error": err.Error()})
						dialog.NewError(err, u.win).Show()
						return
					}
					u.Stop()
				}, u.win).Show()
		})
	}()
}

// checkUpdatesInBackground polls once at startup and notifies if a newer build exists.
// Non-blocking; silent on any error (offline, feed missing).
func (u *UI) checkUpdatesInBackground() {
	if u.updater == nil || !u.updater.Enabled() {
		return
	}
	go func() {
		defer debuglog.Recover(u.svc.Log, "update-startup", false)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rel, avail, err := u.updater.Available(ctx)
		if err != nil || !avail {
			return
		}
		u.svc.Log.Info("app", "update available", map[string]any{"version": rel.Version, "build": rel.Build})
		u.Notify("rave-mate update", fmt.Sprintf("%s is available - open Settings → Updates to install.", rel.Version))
	}()
}
