package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/elevate"
	"rave.page/mate/internal/traktorqml"
)

// traktorQmlCard installs/removes the Traktor api-client QML mod - the richest live feed
// (full deck metadata → localhost:8080). Writes under Program Files, so Apply/Revert run via
// an elevated child (UAC). Patches D2.qml in place (reversible, update-safe). See traktorqml.
func (u *UI) traktorQmlCard() fyne.CanvasObject {
	status := widget.NewLabel("checking…")
	status.Wrapping = fyne.TextWrapWord
	st := u.newStatus(nil) // pushed by refresh (Traktor probe is async)
	applyBtn := widget.NewButtonWithIcon("Install / Re-apply", theme.DownloadIcon(), nil)
	revertBtn := widget.NewButtonWithIcon("Remove", theme.DeleteIcon(), nil)

	var refresh func()
	run := func(action string) {
		applyBtn.Disable()
		revertBtn.Disable()
		status.SetText("Working… approve the UAC prompt - and keep Traktor closed.")
		go func() {
			defer debuglog.Recover(u.svc.Log, "traktor-qml-ui", false)
			err := u.runQMLElevated(action)
			fyne.Do(func() {
				if err != nil {
					u.Notify("rave-mate", "QML feed "+action+" failed: "+err.Error())
				} else {
					u.Notify("rave-mate", "QML feed "+action+" ok.")
				}
				refresh()
			})
		}()
	}
	applyBtn.OnTapped = func() { run("apply") }
	revertBtn.OnTapped = func() { run("revert") }

	refresh = func() {
		go func() {
			defer debuglog.Recover(u.svc.Log, "traktor-qml-status", false)
			in, ok, err := traktorqml.Newest()
			var s traktorqml.Status
			if ok {
				s = traktorqml.Probe(in)
			}
			fyne.Do(func() {
				switch {
				case err != nil:
					status.SetText("Error: " + err.Error())
					st.set(colBrandAmber, "probe error")
					applyBtn.Disable()
					revertBtn.Disable()
				case !ok:
					status.SetText("No Traktor Pro install found under Program Files.")
					st.set(colMuted, "no Traktor install")
					applyBtn.Disable()
					revertBtn.Disable()
				case s.Healthy:
					status.SetText(fmt.Sprintf("Installed on Traktor %s - streaming live deck data to :8080.", in.Version))
					st.set(colBrandMint, "installed · Traktor "+in.Version)
					applyBtn.Enable()
					revertBtn.Enable()
				case s.Patched || s.ApiPresent:
					status.SetText(fmt.Sprintf("Partially installed on Traktor %s (patched=%v, Api=%v) - likely a Traktor update reverted it. Re-apply to fix.", in.Version, s.Patched, s.ApiPresent))
					st.set(colBrandAmber, "partially installed - re-apply")
					applyBtn.Enable()
					revertBtn.Enable()
				default:
					status.SetText(fmt.Sprintf("Not installed on Traktor %s. Install for the richest live feed.", in.Version))
					st.set(colMuted, "not installed")
					applyBtn.Enable()
					revertBtn.Disable()
				}
			})
		}()
	}
	refresh()

	return featureCard("Traktor QML feed (advanced)",
		"Richest live data - patches Traktor's D2 QML so it streams full deck data (title/artist/BPM/key/cues/position) to rave-mate on localhost:8080. Needs admin (UAC) and Traktor closed; backs up D2.qml, is reversible, and survives Traktor updates (Re-apply patches the new file in place rather than overwriting it).",
		nil, st, status, container.NewHBox(applyBtn, revertBtn))
}

// runQMLElevated runs `rave-mate traktor-qml <action>` elevated (UAC) and reads the child's
// error from a result file. Returns nil on success.
func (u *UI) runQMLElevated(action string) error {
	tmp, err := os.CreateTemp("", "ravqml-*.txt")
	if err != nil {
		return err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(path) }()

	code, err := elevate.RunSelfElevated([]string{"traktor-qml", action, "--result", path})
	if err != nil {
		if errors.Is(err, elevate.ErrDeclined) {
			return errors.New("elevation declined (UAC)")
		}
		return err
	}
	msg, _ := os.ReadFile(path)
	if m := strings.TrimSpace(string(msg)); m != "" {
		return errors.New(m)
	}
	if code != 0 {
		return fmt.Errorf("elevated helper exited %d", code)
	}
	return nil
}
