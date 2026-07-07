package obs

import (
	"context"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
)

// View returns a Fyne status card for the connected OBS client.
// Displays connection state, current profile, profile list, and a validate button.
// Must be called from the UI goroutine; callbacks use fyne.Do for thread safety.
func View(c *Client) fyne.CanvasObject {
	// ── status rows ──────────────────────────────────────────────────────────
	connRow := newRow("Connection", "connected")
	profileRow := newRow("Profile", "-")
	profilesRow := newRow("Profiles", "-")
	validationOut := widget.NewLabel("")
	validationOut.Wrapping = fyne.TextWrapWord

	// populate profile info once
	debuglog.Go(nil, "obs:profiles", func() {
		cur, list, err := c.GetProfileList(context.Background())
		fyne.Do(func() {
			if err != nil {
				profileRow.set("error: " + err.Error())
				return
			}
			profileRow.set(cur)
			profilesRow.set(strings.Join(list, ", "))
		})
	})

	// ── validate button ──────────────────────────────────────────────────────
	validateBtn := widget.NewButton("Validate stream settings", func() {
		validationOut.SetText("checking…")
		debuglog.Go(nil, "obs:validate", func() {
			diffs, err := c.ValidateStreamSettings(DefaultStreamRequirements())
			fyne.Do(func() {
				if err != nil {
					validationOut.SetText("error: " + err.Error())
					return
				}
				if len(diffs) == 0 {
					validationOut.SetText("OK - settings meet requirements.")
					return
				}
				validationOut.SetText("Issues:\n• " + strings.Join(diffs, "\n• "))
			})
		})
	})

	body := container.NewVBox(
		connRow.row,
		profileRow.row,
		profilesRow.row,
		widget.NewSeparator(),
		validateBtn,
		validationOut,
	)
	return widget.NewCard("OBS", "obs-websocket v5 connection", body)
}

// statusRow is a label:value line with a live-updatable value.
// (Package-local mirror of ui.statusRow - avoids an import cycle between obs and ui.)
type statusRow struct {
	row *fyne.Container
	val *widget.Label
}

func newRow(label, value string) *statusRow {
	key := widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	val := widget.NewLabel(value)
	return &statusRow{row: container.NewHBox(key, val), val: val}
}

func (r *statusRow) set(v string) { r.val.SetText(v) }
