package ui

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/cuesheet"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/session/sinks/recorder"
)

// traktorHistoryDir resolves the Traktor History folder (config override, else newest install).
func (u *UI) traktorHistoryDir() string {
	if u.svc.Cfg != nil && u.svc.Cfg.Features.NML.HistoryDir != "" {
		return u.svc.Cfg.Features.NML.HistoryDir
	}
	if installs, err := musiclib.DiscoverTraktor(); err == nil && len(installs) > 0 {
		return installs[0].HistoryDir
	}
	return ""
}

// historyResolver bridges the library DB (path → collection metadata) for reconciliation.
func (u *UI) historyResolver() recorder.HistoryResolver {
	return func(path string) (recorder.HistoryMeta, bool) {
		if u.svc.Lib == nil {
			return recorder.HistoryMeta{}, false
		}
		t, ok, _ := u.svc.Lib.TrackByPath(path)
		if !ok {
			return recorder.HistoryMeta{}, false
		}
		return recorder.HistoryMeta{Title: t.Title, Artist: t.Artist, Album: t.Album, Key: t.Key, BPM: t.BPM}, true
	}
}

// recorderView is the Recordings cockpit: live status hero (recording / set capture / OBS),
// a now-playing strip with the confirm countdown, and a sets list + detail split (tracklist,
// linked captures with playback, fingerprinting, export).
type recorderView struct {
	u   *UI
	rec *recorder.Recorder

	recs  []recorder.Recording            // newest first; live set (if any) at index 0
	caps  map[string][]libdb.SetRecording // recording id → linked captures
	loose []libdb.SetRecording            // finished captures with no recording link
	selID string

	list   *widget.List
	detail *fyne.Container

	// hero badges
	recDot, capDot, obsDot *canvas.Text
	recLbl, capLbl, obsLbl *widget.Label
	finishBtn              *kitButton

	// now-playing strip
	npTitle *widget.Label
	npMeta  *widget.Label
	npState *widget.Label
	npBar   *widget.ProgressBar
}

// buildRecorder builds the Recordings cockpit tab.
func (u *UI) buildRecorder() fyne.CanvasObject {
	v := &recorderView{u: u, rec: u.svc.Recorder, caps: map[string][]libdb.SetRecording{}}

	// ── hero: title + live status badges ──
	v.recDot, v.recLbl = newBadge()
	v.capDot, v.capLbl = newBadge()
	v.obsDot, v.obsLbl = newBadge()
	v.finishBtn = newKitButtonWithIcon("Finish set", theme.MediaStopIcon(), func() {
		v.rec.StopRecording()
		v.refreshAll()
	})
	v.finishBtn.SetVariant(kitBtnBrand)
	v.finishBtn.Disable()

	badges := WrapActions(
		badgeBox("REC", v.recDot, v.recLbl),
		badgeBox("CAPTURE", v.capDot, v.capLbl),
		badgeBox("OBS", v.obsDot, v.obsLbl),
	)
	hero := container.NewVBox(
		container.NewBorder(nil, nil,
			container.NewVBox(smallCaps("PUBLISH"), boldLabel("Sets & recordings")),
			container.NewVBox(v.finishBtn)),
		badges,
	)

	// ── now-playing strip ──
	v.npTitle = boldLabel("Nothing audible")
	v.npMeta = mutedInline("")
	v.npState = mutedInline("")
	v.npBar = widget.NewProgressBar()
	v.npBar.Hide()
	npCard := widget.NewCard("", "", container.NewVBox(
		smallCaps("NOW PLAYING"),
		newKitCopyableLabel("track", v.npTitle), // right-click → copy the track line
		v.npMeta, v.npState, v.npBar,
	))

	// ── sets list (left) + detail (right) ──
	v.list = widget.NewList(
		func() int { return len(v.recs) },
		func() fyne.CanvasObject {
			title := boldLabel("")
			title.Truncation = fyne.TextTruncateEllipsis
			meta := mutedInline("")
			meta.Truncation = fyne.TextTruncateEllipsis
			return container.NewVBox(title, meta)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < 0 || id >= len(v.recs) {
				return
			}
			box := o.(*fyne.Container)
			box.Objects[0].(*widget.Label).SetText(v.listTitle(v.recs[id]))
			box.Objects[1].(*widget.Label).SetText(v.listMeta(v.recs[id]))
		},
	)
	v.list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(v.recs) {
			v.selID = v.recs[id].ID
			v.refreshDetail()
		}
	}
	// Detail hosts one child set by refreshDetail (a Border: fixed header + tabbed body whose
	// tabs scroll internally - so a long tracklist never pushes the captures/actions off-screen).
	v.detail = container.NewStack()
	listHead := container.NewVBox(smallCaps("SETS"), widget.NewSeparator())
	left := container.NewBorder(listHead, nil, nil, nil, v.list)
	split := container.New(newAdaptiveSplit(0.40), left, shrinkWidth(320, v.detail))

	v.refreshAll()

	// Live updates: recorder broadcasts on every change; a 1s tick drives elapsed/countdown
	// + the capture/OBS badges (those have no push channel into this view).
	ch, unsub := v.rec.Subscribe()
	u.closers = append(u.closers, unsub)
	goUI("recorder", func() {
		for range ch {
			fyne.Do(v.refreshAll)
		}
	})
	tick := time.NewTicker(time.Second)
	u.closers = append(u.closers, tick.Stop)
	goUI("recorder-tick", func() {
		for range tick.C {
			fyne.Do(v.refreshLive)
		}
	})
	u.recorderRefresh = v.refreshAll

	head := container.NewVBox(hero, npCard, widget.NewSeparator())
	return container.NewBorder(head, nil, nil, nil, split)
}

// ── live state (hero badges + now-playing strip) ─────────────────────────────

// newBadge returns a colored status dot + value label. No truncation - inside an HBox a
// truncated label's min width is ~one glyph, so it would render as "…"; WrapActions wraps
// whole badges to the next row instead.
func newBadge() (*canvas.Text, *widget.Label) {
	dot := canvas.NewText("●", colMuted)
	dot.TextSize = 13
	return dot, mutedInline("-")
}

// badgeBox lays out one "KEY ● value" hero badge.
func badgeBox(key string, dot *canvas.Text, lbl *widget.Label) fyne.CanvasObject {
	return container.NewHBox(smallCaps(key), container.NewCenter(dot), lbl)
}

func setBadge(dot *canvas.Text, c color.Color, lbl *widget.Label, text string) {
	dot.Color = c
	dot.Refresh()
	lbl.SetText(text)
}

// refreshLive updates the hero badges + now-playing strip (1s tick + every recorder change).
func (v *recorderView) refreshLive() {
	cfg := v.u.svc.Cfg
	active := v.rec.Active()

	// REC badge + finish button.
	switch {
	case cfg != nil && !cfg.Features.Recorder.Enabled:
		setBadge(v.recDot, colMuted, v.recLbl, "recorder off")
		v.finishBtn.Disable()
	case active != nil:
		dur := time.Since(active.StartedAt).Truncate(time.Second)
		setBadge(v.recDot, colBrandBase, v.recLbl, fmt.Sprintf("%s · %d tracks · %s", orName(active.Name), len(active.Tracks), dur))
		v.finishBtn.Enable()
	default:
		setBadge(v.recDot, colMuted, v.recLbl, "armed - waiting for the first track")
		v.finishBtn.Disable()
	}

	// CAPTURE badge (Icecast set-capture receiver).
	switch {
	case v.u.svc.SetCapture == nil || (cfg != nil && !cfg.Features.SetCapture.Enabled):
		setBadge(v.capDot, colMuted, v.capLbl, "off")
	default:
		st := v.u.svc.SetCapture.Snapshot()
		switch {
		case st.Connected:
			setBadge(v.capDot, colBrandMint, v.capLbl,
				fmt.Sprintf("capturing %s · %s", strings.ToUpper(st.Format), humanBytes(st.Bytes)))
		case st.Reconnecting:
			setBadge(v.capDot, colBrandAmber, v.capLbl, "reconnecting…")
		case st.Listening:
			setBadge(v.capDot, colForeground, v.capLbl, "listening "+st.Addr)
		default:
			setBadge(v.capDot, colBrandAmber, v.capLbl, "not listening")
		}
	}

	// OBS badge.
	switch {
	case v.u.svc.OBS == nil || (cfg != nil && !cfg.Features.OBS.Enabled):
		setBadge(v.obsDot, colMuted, v.obsLbl, "off")
	default:
		st := v.u.svc.OBS.Status()
		switch {
		case st.Recording:
			dur := time.Since(st.RecStartedAt).Truncate(time.Second)
			setBadge(v.obsDot, colBrandBase, v.obsLbl, "recording · "+dur.String())
		case st.Connected:
			setBadge(v.obsDot, colBrandMint, v.obsLbl, "connected")
		default:
			setBadge(v.obsDot, colBrandAmber, v.obsLbl, "not connected")
		}
	}

	// Now-playing strip: pending candidate (confirm countdown) or current confirmed track.
	if p, ok := v.rec.Pending(); ok {
		v.npTitle.SetText(orTrack(trackLine(p.Track)))
		setOrHide(v.npMeta, trackMetaLine(p.Track))
		left := max(time.Until(p.ConfirmAt).Truncate(time.Second), 0)
		window := p.ConfirmAt.Sub(p.FirstSeen)
		setOrHide(v.npState, fmt.Sprintf("confirming - commits to the tracklist in %s", left))
		if window > 0 {
			v.npBar.SetValue(1 - float64(left)/float64(window))
		}
		v.npBar.Show()
		return
	}
	v.npBar.Hide()
	if cur, ok := currentTrack(active); ok {
		v.npTitle.SetText(orTrack(trackLine(cur)))
		setOrHide(v.npMeta, trackMetaLine(cur))
		setOrHide(v.npState, fmt.Sprintf("✓ track %d in the tracklist · playing %s", len(active.Tracks), time.Since(cur.StartedAt).Truncate(time.Second)))
		return
	}
	v.npTitle.SetText("Nothing audible")
	setOrHide(v.npMeta, "")
	setOrHide(v.npState, "")
}

// setOrHide sets a label's text, hiding it entirely when empty (no blank line in the VBox).
func setOrHide(l *widget.Label, text string) {
	l.SetText(text)
	if text == "" {
		l.Hide()
	} else {
		l.Show()
	}
}

// currentTrack returns the still-playing confirmed track (last one, no end time yet).
func currentTrack(r *recorder.Recording) (recorder.Track, bool) {
	if r == nil || len(r.Tracks) == 0 {
		return recorder.Track{}, false
	}
	t := r.Tracks[len(r.Tracks)-1]
	return t, t.EndedAt.IsZero()
}

// trackMetaLine renders "deck A · 128.0 BPM · 8A · via traktor" (only what's known).
func trackMetaLine(t recorder.Track) string {
	var parts []string
	if t.Deck != "" {
		parts = append(parts, "deck "+t.Deck)
	}
	if t.BPM > 0 {
		parts = append(parts, fmt.Sprintf("%.1f BPM", t.BPM))
	}
	if t.Key != "" {
		parts = append(parts, t.Key)
	}
	if t.TitleSource != "" {
		parts = append(parts, "via "+t.TitleSource)
	}
	return strings.Join(parts, " · ")
}

func orTrack(s string) string {
	if strings.TrimSpace(s) == "" {
		return "Untitled track"
	}
	return s
}

// ── sets list + detail ────────────────────────────────────────────────────────

// refreshAll re-pulls recordings + captures and repaints everything.
func (v *recorderView) refreshAll() {
	v.refreshLive()
	v.recs = v.rec.List()
	if a := v.rec.Active(); a != nil {
		// List() includes the active set's persisted snapshot; replace it with the live copy
		// (fresher) and pin it first.
		recs := make([]recorder.Recording, 0, len(v.recs)+1)
		recs = append(recs, *a)
		for _, r := range v.recs {
			if r.ID != a.ID {
				recs = append(recs, r)
			}
		}
		v.recs = recs
	}
	v.caps = map[string][]libdb.SetRecording{}
	v.loose = nil
	if all, err := v.u.svc.Lib.ListSetRecordings(300); err == nil {
		for _, s := range all {
			if s.RecordingID != "" {
				v.caps[s.RecordingID] = append(v.caps[s.RecordingID], s)
			} else if !s.EndedAt.IsZero() {
				v.loose = append(v.loose, s)
			}
		}
	}
	v.list.Refresh()
	// Nothing picked (or the picked set was deleted) → select the newest so the detail
	// pane is never empty/stale. Select() won't re-fire OnSelected for an already-selected
	// row, so refreshDetail runs explicitly below either way.
	if len(v.recs) > 0 {
		if _, ok := v.byID(v.selID); !ok {
			v.selID = v.recs[0].ID
			v.list.Select(0)
		}
	}
	v.refreshDetail()
}

// byID finds a recording in the current pull.
func (v *recorderView) byID(id string) (*recorder.Recording, bool) {
	for i := range v.recs {
		if v.recs[i].ID == id {
			return &v.recs[i], true
		}
	}
	return nil, false
}

func (v *recorderView) listTitle(r recorder.Recording) string {
	name := orName(r.Name)
	if r.EndedAt.IsZero() {
		return "⏺ " + name
	}
	return name
}

func (v *recorderView) listMeta(r recorder.Recording) string {
	parts := []string{r.StartedAt.Local().Format("2006-01-02 15:04"), fmt.Sprintf("%d tracks", len(r.Tracks))}
	if !r.EndedAt.IsZero() {
		parts = append(parts, r.EndedAt.Sub(r.StartedAt).Truncate(time.Minute).String())
	} else {
		parts = append(parts, "live")
	}
	for _, s := range v.caps[r.ID] {
		if s.Kind == libdb.SetKindOBS {
			parts = append(parts, "video")
		} else {
			parts = append(parts, "audio")
		}
	}
	if !r.ReconciledAt.IsZero() {
		parts = append(parts, "matched ✓")
	}
	return strings.Join(parts, " · ")
}

// setDetail swaps the detail pane's single child.
func (v *recorderView) setDetail(o fyne.CanvasObject) {
	v.detail.Objects = []fyne.CanvasObject{o}
	v.detail.Refresh()
}

// refreshDetail repaints the right-hand pane for the selected set: a fixed header (name + meta +
// actions) over a Captures/Tracklist tab pair, so the common path (playback) is one click away and
// a long tracklist scrolls inside its own tab instead of pushing everything off-screen.
func (v *recorderView) refreshDetail() {
	sel, _ := v.byID(v.selID)
	if sel == nil {
		body := container.NewVBox(
			smallCaps("SELECTED SET"),
			mutedLabel("Select a set to see its captures, tracklist and export options."),
		)
		if loose := v.looseCapturesSection(); loose != nil {
			body.Add(loose)
		}
		v.setDetail(container.NewVScroll(body))
		return
	}
	r := *sel

	header := container.NewVBox(
		smallCaps("SELECTED SET"),
		kitSelectable(boldLabel(orName(r.Name))), // static per rebuild → true drag-select
		kitSelectable(mutedLabel(v.listMeta(r))),
		v.detailActions(r),
		widget.NewSeparator(),
	)

	// Captures tab (default - playback is the thing you reach for most).
	sets := v.caps[r.ID]
	capBox := container.NewVBox()
	if len(sets) == 0 {
		capBox.Add(mutedLabel("No captured media linked. Broadcast to the set-capture receiver or record in OBS while a set runs."))
	}
	for _, s := range sets {
		capBox.Add(v.captureBlock(r, s))
	}
	if loose := v.looseCapturesSection(); loose != nil {
		capBox.Add(loose)
	}

	rows := v.trackRows(r)
	tabs := container.NewAppTabs(
		container.NewTabItem(fmt.Sprintf("Captures (%d)", len(sets)), container.NewVScroll(capBox)),
		container.NewTabItem(fmt.Sprintf("Tracklist (%d)", len(rows)), v.trackListWidget(rows)),
	)
	tabs.SetTabLocation(container.TabLocationTop)
	v.setDetail(container.NewBorder(header, nil, nil, nil, tabs))
}

// detailActions builds the Export / Match-history / Delete row for the selected set.
func (v *recorderView) detailActions(r recorder.Recording) fyne.CanvasObject {
	export := newKitButtonWithIcon("Export", theme.DocumentSaveIcon(), func() { v.u.exportRecordingDialog(r) })
	actions := []fyne.CanvasObject{export}
	if !r.EndedAt.IsZero() {
		match := newKitButtonWithIcon("Match history", theme.ViewRefreshIcon(), func() { v.u.reconcileRecording(r) })
		actions = append(actions, match)
		del := newKitButtonWithIcon("Delete", theme.DeleteIcon(), func() { v.deleteRecording(r) })
		actions = append(actions, del)
	}
	return WrapActions(actions...)
}

// trackRow is one tracklist entry with its offset into the recording (label = "Artist - Title").
type trackRow struct {
	offset time.Duration
	label  string
}

// trackRows builds the tracklist for a set. Positions come from the live MIDI/session tracklist
// (track.StartedAt − set start); if that's empty it falls back to the .cue sidecar next to a
// linked capture (cue INDEX offsets) - both are deterministic record-time sources, no fingerprint.
func (v *recorderView) trackRows(r recorder.Recording) []trackRow {
	if len(r.Tracks) > 0 {
		rows := make([]trackRow, len(r.Tracks))
		for i, t := range r.Tracks {
			rows[i] = trackRow{offset: t.StartedAt.Sub(r.StartedAt), label: orTrack(trackLine(t))}
		}
		return rows
	}
	for _, s := range v.caps[r.ID] {
		cuePath := strings.TrimSuffix(s.Path, filepath.Ext(s.Path)) + ".cue"
		sh, err := cuesheet.ParseFile(cuePath)
		if err != nil || len(sh.Tracks) == 0 {
			continue
		}
		rows := make([]trackRow, len(sh.Tracks))
		for i, t := range sh.Tracks {
			lbl := t.Title
			if t.Performer != "" {
				lbl = t.Performer + " - " + t.Title
			}
			rows[i] = trackRow{offset: t.Start, label: orTrack(lbl)}
		}
		return rows
	}
	return nil
}

// trackListWidget renders the tracklist as a virtualized list (compact rows, internal scroll) so
// a long set doesn't blow up the layout. Empty → a hint.
func (v *recorderView) trackListWidget(rows []trackRow) fyne.CanvasObject {
	if len(rows) == 0 {
		return container.NewVScroll(container.NewVBox(mutedLabel("No tracks yet - they're added live as each track is confirmed, or read from the set's .cue.")))
	}
	return widget.NewList(
		func() int { return len(rows) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i < 0 || i >= len(rows) {
				return
			}
			o.(*widget.Label).SetText(fmt.Sprintf("%d.  [%s]  %s", i+1, fmtClock(rows[i].offset.Seconds()), rows[i].label))
		},
	)
}

// looseCapturesSection renders finished captures with no recording link (e.g. OBS recorded while
// nothing played) so they stay discoverable + deletable. nil when there are none.
func (v *recorderView) looseCapturesSection() fyne.CanvasObject {
	if len(v.loose) == 0 {
		return nil
	}
	box := container.NewVBox(
		widget.NewSeparator(),
		smallCaps(fmt.Sprintf("UNLINKED CAPTURES (%d)", len(v.loose))),
		mutedLabel("Captured media that overlaps no recorded set."),
	)
	for _, s := range v.loose {
		box.Add(v.captureBlock(recorder.Recording{}, s))
	}
	return box
}

// captureBlock renders one linked capture: caption + playback (audio) or open (video) +
// per-track jumps + fingerprint + remove.
func (v *recorderView) captureBlock(r recorder.Recording, set libdb.SetRecording) fyne.CanvasObject {
	kindLbl := "Broadcast audio"
	if set.Kind == libdb.SetKindOBS {
		kindLbl = "OBS recording"
	}
	capParts := []string{kindLbl, strings.ToUpper(set.Format)}
	if set.Bytes > 0 {
		capParts = append(capParts, humanBytes(set.Bytes))
	}
	capParts = append(capParts, filepath.Base(set.Path))
	// right-click → copy the capture's FULL path (display shows the base name)
	caption := newKitCopyable("path", mutedLabel(strings.Join(capParts, " · ")),
		func() string { return set.Path })

	remove := newKitButtonWithIcon("Remove", theme.DeleteIcon(), func() { v.deleteCapture(set) })

	box := container.NewVBox(caption)
	if !fileExists(set.Path) {
		box.Add(mutedLabel("File missing on disk."))
		box.Add(WrapActions(remove))
		return box
	}

	reveal := newKitButtonWithIcon("Show in folder", theme.FolderOpenIcon(), func() { revealFile(set.Path) })
	switch {
	case set.Kind == libdb.SetKindOBS || !isPlayable(set.Path):
		// Video (or undecodable audio): the native ffmpeg-decode player (cue/track-aware), with an
		// OS-player fallback for anything it can't handle.
		title := "Player - " + orName(r.Name)
		play := newKitButtonWithIcon("Play", theme.MediaPlayIcon(), func() {
			v.u.openPlayerModal(title, set.Path, v.playerMarkers(r, set), v.trimHints(r, set))
		})
		play.SetVariant(kitBtnBrand)
		openExt := newKitButtonWithIcon("Open externally", theme.MediaPlayIcon(), func() { openFile(set.Path) })
		box.Add(WrapActions(play, openExt, reveal, remove))
	default:
		box.Add(v.audioTransport(r, set))
		// Trim / edit modal: waveform + native transport + auto-trim (to tracks / detected music) + export.
		edit := newKitButtonWithIcon("Trim / edit…", theme.ContentCutIcon(), func() {
			v.u.openAudioEditModal("Edit - "+orName(r.Name), set.Path, v.playerMarkers(r, set), v.trimHints(r, set))
		})
		box.Add(WrapActions(edit, reveal, remove))
	}
	return box
}

// trimHints builds the auto-trim bounds for a set: first track start + last track end, anchored to
// the CAPTURE start (set.StartedAt) like playerMarkers. durSec stays 0 - the player supplies the
// real duration for silence detection. Unknown bounds are −1 (button hidden / no-op).
func (v *recorderView) trimHints(r recorder.Recording, set libdb.SetRecording) trimTarget {
	t := newTrimTarget(orName(r.Name))
	if len(r.Tracks) == 0 || set.StartedAt.IsZero() {
		return t
	}
	t.firstTrackSec = max(r.Tracks[0].StartedAt.Sub(set.StartedAt).Seconds(), 0)
	var lastEnd time.Time
	for _, tr := range r.Tracks {
		if tr.EndedAt.After(lastEnd) {
			lastEnd = tr.EndedAt
		}
	}
	if !lastEnd.IsZero() {
		t.lastTrackEndSec = max(lastEnd.Sub(set.StartedAt).Seconds(), 0)
	}
	// Last fader-down = the true set end (survives the DJ talking after the mix). Only when it lands
	// inside the capture (after the file's t0); otherwise leave unknown (button hidden).
	if !r.LastFaderAt.IsZero() && r.LastFaderAt.After(set.StartedAt) {
		t.lastFaderSec = r.LastFaderAt.Sub(set.StartedAt).Seconds()
	}
	return t
}

// playerMarkers maps a recording's tracklist to VIDEO player jump points. These anchor to the CAPTURE
// file's own start (set.StartedAt), NOT the recorder set start: OBS began recording at a different
// wall-clock than the recorder's first confirmed track, so offsetting from the set start (trackRows)
// shifted every marker by that constant. Audio playback already anchors to the capture (audioTransport);
// this mirrors it. Falls back to trackRows (cue sidecar / no set start) when there's no live tracklist.
func (v *recorderView) playerMarkers(r recorder.Recording, set libdb.SetRecording) []playerMarker {
	if len(r.Tracks) > 0 && !set.StartedAt.IsZero() {
		ms := make([]playerMarker, len(r.Tracks))
		for i, t := range r.Tracks {
			off := t.StartedAt.Sub(set.StartedAt)
			if off < 0 {
				off = 0
			}
			ms[i] = playerMarker{offset: off, label: orTrack(trackLine(t))}
		}
		return ms
	}
	rows := v.trackRows(r)
	if len(rows) == 0 {
		return nil
	}
	ms := make([]playerMarker, len(rows))
	for i, row := range rows {
		ms[i] = playerMarker(row)
	}
	return ms
}

// audioTransport renders in-app playback for a captured audio file: transport + seek bar +
// per-track jump buttons (offset = track.StartedAt − capture.StartedAt). Mirrors the studio
// media player's transport wiring.
func (v *recorderView) audioTransport(r recorder.Recording, set libdb.SetRecording) fyne.CanvasObject {
	u := v.u
	player := u.setAudioPlayer()
	path := set.Path
	// mutedInline (no wrap): as the Border's right cell a wrapping label collapses to ~1 char
	// wide and stacks the clock vertically, ballooning the transport row.
	timeLbl := mutedInline("0:00 / 0:00")
	seek := widget.NewSlider(0, 1)
	seek.Step = 0.1
	seek.Disable()
	var lastInput time.Time

	var playBtn *kitButton
	toPlay := func() { playBtn.SetText("Play"); playBtn.SetIcon(theme.MediaPlayIcon()) }
	toPause := func() { playBtn.SetText("Pause"); playBtn.SetIcon(theme.MediaPauseIcon()) }

	onEnd := func() {
		toPlay()
		seek.SetValue(0)
		seek.Disable()
		timeLbl.SetText("0:00 / 0:00")
	}
	onTick := func(cur, total float64) {
		if total <= 0 {
			return
		}
		if seek.Max != total {
			seek.Max = total
		}
		if time.Since(lastInput) > 600*time.Millisecond {
			seek.SetValue(cur)
		}
		timeLbl.SetText(fmtClock(cur) + " / " + fmtClock(total))
	}
	seek.OnChanged = func(float64) { lastInput = time.Now() }
	seek.OnChangeEnded = func(val float64) {
		if player.state().path == path {
			player.seek(val)
		}
	}

	playFrom := func(offsetSec float64) {
		player.attachUI(onTick, onEnd)
		seek.Enable()
		toPause()
		// Decode init (Host.Call) + the initial seek run off the UI thread so a track-jump never
		// freezes the app; the transport reflects the real state on the next position tick.
		debuglog.Go(u.svc.Log, "recorder-play", func() {
			if err := player.play(path); err != nil {
				fyne.Do(func() { onEnd(); u.Notify("rave-mate", "Play failed: "+err.Error()) })
				return
			}
			if offsetSec > 0 {
				player.seek(offsetSec)
			}
		})
	}

	playBtn = newKitButtonWithIcon("Play", theme.MediaPlayIcon(), func() {
		st := player.state()
		if st.path == path && st.playing { // toggle pause when this set is playing
			if player.togglePause() {
				toPlay()
			} else {
				toPause()
			}
			return
		}
		playFrom(0)
	})
	playBtn.SetVariant(kitBtnBrand)
	stopBtn := newKitButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
		if player.state().path == path {
			player.stop()
		}
		onEnd()
	})

	// Reflect live state if this set is the one currently playing.
	if st := player.state(); st.path == path && st.playing {
		player.attachUI(onTick, onEnd)
		seek.Enable()
		if st.total > 0 {
			seek.Max = st.total
			seek.SetValue(st.cur)
			timeLbl.SetText(fmtClock(st.cur) + " / " + fmtClock(st.total))
		}
		if st.paused {
			toPlay()
		} else {
			toPause()
		}
	}

	transport := container.NewBorder(nil, nil, container.NewHBox(playBtn, stopBtn), timeLbl, seek)
	box := container.NewVBox(transport)

	// Per-track jump buttons (seek to each track's offset into the captured audio).
	if len(r.Tracks) > 0 {
		box.Add(mutedLabel("Jump to a track:"))
		jumps := container.NewVBox()
		for i, t := range r.Tracks {
			off := max(t.StartedAt.Sub(set.StartedAt).Seconds(), 0)
			label := fmt.Sprintf("%d. [%s] %s", i+1, fmtClock(off), orTrack(trackLine(t)))
			offCopy := off
			jumps.Add(newKitButton(label, func() { playFrom(offCopy) }))
		}
		box.Add(jumps)
	}
	return box
}

// ── destructive actions ───────────────────────────────────────────────────────

// deleteRecording confirms + removes a set; optionally also its captured files on disk.
func (v *recorderView) deleteRecording(r recorder.Recording) {
	sets := v.caps[r.ID]
	msg := widget.NewLabel("Delete \"" + orName(r.Name) + "\"? This can't be undone.")
	msg.Wrapping = fyne.TextWrapWord
	content := container.NewVBox(msg)
	var alsoFiles *widget.Check
	if len(sets) > 0 {
		alsoFiles = widget.NewCheck(fmt.Sprintf("Also delete %d captured file(s) from disk", len(sets)), nil)
		content.Add(alsoFiles)
	}
	dialog.ShowCustomConfirm("Delete set", "Delete", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		if err := v.rec.Delete(r.ID); err != nil {
			v.u.Notify("rave-mate", "Delete failed: "+err.Error())
			return
		}
		for _, s := range sets {
			if alsoFiles != nil && alsoFiles.Checked {
				if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
					v.u.Notify("rave-mate", "Couldn't delete "+filepath.Base(s.Path)+": "+err.Error())
				}
				_ = v.u.svc.Lib.DeleteSetRecording(s.ID)
			} else {
				_ = v.u.svc.Lib.RelinkSetRecording(s.ID, "") // keep the file; unlink the dead set
			}
		}
		v.selID = ""
		v.refreshAll()
	}, v.u.win)
}

// deleteCapture confirms + removes one capture row, optionally with its file.
func (v *recorderView) deleteCapture(set libdb.SetRecording) {
	alsoFile := widget.NewCheck("Also delete the file from disk", nil)
	msg := widget.NewLabel("Remove the capture \"" + filepath.Base(set.Path) + "\" from the library?")
	msg.Wrapping = fyne.TextWrapWord
	dialog.ShowCustomConfirm("Remove capture", "Remove", "Cancel", container.NewVBox(msg, alsoFile), func(ok bool) {
		if !ok {
			return
		}
		if alsoFile.Checked {
			if err := os.Remove(set.Path); err != nil && !os.IsNotExist(err) {
				v.u.Notify("rave-mate", "Couldn't delete file: "+err.Error())
			}
		}
		if err := v.u.svc.Lib.DeleteSetRecording(set.ID); err != nil {
			v.u.Notify("rave-mate", "Remove failed: "+err.Error())
		}
		v.refreshAll()
	}, v.u.win)
}

// ── shared helpers (reconcile, export) ────────────────────────────────────────

// trackLine renders "Artist - Title" (or just the title) for a recorded track.
func trackLine(t recorder.Track) string {
	if t.Artist != "" {
		return t.Artist + " - " + t.Title
	}
	return t.Title
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// reconcileRecording matches a finished recording to the best-overlapping Traktor history
// session (off-thread) and replaces its tracklist with the authoritative, timestamped one.
func (u *UI) reconcileRecording(r recorder.Recording) {
	histDir := u.traktorHistoryDir()
	if histDir == "" {
		u.Notify("rave-mate", "No Traktor History folder found - can't match.")
		return
	}
	debuglog.Go(u.svc.Log, "reconcile", func() {
		rec, err := u.svc.Recorder.ReconcileWithHistory(r.ID, histDir, u.historyResolver())
		fyne.Do(func() {
			if err != nil {
				u.Notify("rave-mate", "Match failed: "+err.Error())
				return
			}
			u.Notify("rave-mate", fmt.Sprintf("Matched %d tracks from Traktor history.", len(rec.Tracks)))
			u.RefreshRecordings()
		})
	})
}

// exportRecordingDialog asks for a format + save location, then writes the tracklist.
func (u *UI) exportRecordingDialog(r recorder.Recording) {
	format := widget.NewRadioGroup([]string{"Text (.txt)", "CSV (.csv)", "JSON (.json)"}, nil)
	format.SetSelected("Text (.txt)")
	dialog.ShowCustomConfirm("Export tracklist", "Save", "Cancel", format, func(ok bool) {
		if !ok {
			return
		}
		fmtKey, ext := recorder.FormatText, "txt"
		switch format.Selected {
		case "CSV (.csv)":
			fmtKey, ext = recorder.FormatCSV, "csv"
		case "JSON (.json)":
			fmtKey, ext = recorder.FormatJSON, "json"
		}
		content, err := u.svc.Recorder.Export(r.ID, fmtKey)
		if err != nil {
			u.Notify("rave-mate", "Export failed: "+err.Error())
			return
		}
		save := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
			if err != nil || w == nil {
				return
			}
			defer func() { _ = w.Close() }()
			if _, err := w.Write([]byte(content)); err != nil {
				u.Notify("rave-mate", "Write failed: "+err.Error())
			}
		}, u.win)
		save.SetFileName(sanitizeFileName(orName(r.Name)) + "." + ext)
		save.SetFilter(storage.NewExtensionFileFilter([]string{"." + ext}))
		save.Show()
	}, u.win)
}

// rebuildRecorderTab replaces the Publish tab content (full rebuild - used when the
// in-place refresh hook isn't available).
func (u *UI) rebuildRecorderTab() {
	if u.tabs == nil {
		return
	}
	for _, it := range u.tabs.Items {
		if it.Text == "Publish" {
			it.Content = u.buildRecorder()
			u.tabs.Refresh()
			return
		}
	}
}

// RefreshRecordings refreshes the Recordings cockpit from any goroutine (e.g. after a
// background auto-reconcile or a remote change). Safe to call off the UI thread.
func (u *UI) RefreshRecordings() {
	fyne.Do(func() {
		if u.recorderRefresh != nil {
			u.recorderRefresh()
			return
		}
		u.rebuildRecorderTab()
	})
}

func orName(name string) string {
	if name == "" {
		return "Live set"
	}
	return name
}

func sanitizeFileName(s string) string {
	repl := func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		}
		return r
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		out = append(out, repl(r))
	}
	return string(out)
}
