package webui

// "Publish to rave.page": the confirm dialog + staged progress for uploading a finished set
// (audio + tracklist offsets + waveform + loudness) through internal/setpublish.
//
// The dialog is deliberately a consent gate, not a convenience: nothing uploads until the user
// ticks the rights confirmation, and the summary states exactly what leaves the machine first.
//
// Everything slow (manifest assembly = possible multi-minute analysis; the upload itself) runs on
// u.bg and patches the modal through its session token, so the act lane never blocks.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/setpublish"
	"rave.page/mate/internal/shared/auth"
)

const pubPublishOwner = "pub-publish"

// pubPubRepaint throttles progress repaints - a 2h set is hundreds of chunk events and each
// repaint is a full modal re-render.
const pubPubRepaint = 250 * time.Millisecond

// pubPubSt is the publish dialog's state (package-level: the webview UI is a single instance).
type pubPubSt struct {
	recID     string
	loading   bool
	man       *setpublish.SetManifest
	prev      libdb.SetPublish
	hasPrev   bool
	rights    bool
	vis       string
	running   bool
	prog      setpublish.Progress
	res       *setpublish.Result
	errMsg    string
	lastPaint time.Time
}

var (
	pubPubMu sync.Mutex
	pubPub   pubPubSt
)

func init() {
	onPrefix("pub-publish:", func(u *UI, m actMsg) { u.pubPublishOpen(m.arg("pub-publish:")) })
	onExact("pub-pub-rights", func(u *UI, m actMsg) { u.pubPubRights(m.Val) })
	onPrefix("pub-pub-vis:", func(u *UI, m actMsg) { u.pubPubVis(m.arg("pub-pub-vis:")) })
	onExact("pub-pub-go", func(u *UI, m actMsg) { u.pubPubGo() })
	onExact("pub-pub-retry", func(u *UI, m actMsg) { u.pubPubRetry() })
}

// setPublisher builds the publisher from the wired services (nil when anything is missing).
func (u *UI) setPublisher() *setpublish.Publisher {
	if u.svc.Recorder == nil || u.svc.API == nil || u.svc.Lib == nil || u.svc.Workers == nil {
		return nil
	}
	asm := setpublish.NewAssembler(u.svc.Recorder, u.svc.Lib, u.svc.Store, u.svc.Workers, u.log)
	return setpublish.NewPublisher(asm, u.svc.API, u.svc.Lib, u.svc.Workers, u.log, u.pubAuthToken)
}

func (u *UI) pubAuthToken() string {
	if u.svc.Auth == nil {
		return ""
	}
	return u.svc.Auth.Token()
}

// pubPublishOpen opens the confirm dialog and assembles the manifest off the act lane.
func (u *UI) pubPublishOpen(recID string) {
	if recID == "" {
		return
	}
	pub := u.setPublisher()
	if pub == nil {
		u.toast(i18n.T("publish.upload.unavailable"))
		return
	}
	if u.svc.Auth == nil || !u.svc.Auth.SignedIn() {
		u.toast(i18n.T("publish.upload.needAuth"))
		return
	}
	prev, hasPrev := pub.Published(recID)
	pubPubMu.Lock()
	pubPub = pubPubSt{recID: recID, loading: true, prev: prev, hasPrev: hasPrev, vis: pubDefaultVis(hasPrev, prev)}
	pubPubMu.Unlock()

	tok := u.openModalAs(pubPublishOwner, u.pubPubModalHTML())
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		man, err := pub.Preview(ctx, recID)
		pubPubMu.Lock()
		if pubPub.recID != recID {
			pubPubMu.Unlock()
			return // the user moved on to another set
		}
		pubPub.loading = false
		if err != nil {
			pubPub.errMsg = err.Error()
		} else {
			pubPub.man = &man
		}
		pubPubMu.Unlock()
		u.openModalIf(tok, u.pubPubModalHTML())
	})
}

// pubDefaultVis keeps a re-publish on whatever it was; a first publish starts unlisted (the
// least surprising default - shareable by link, not broadcast).
func pubDefaultVis(hasPrev bool, _ libdb.SetPublish) string {
	if hasPrev {
		return "unlisted"
	}
	return "unlisted"
}

func (u *UI) pubPubRights(val string) {
	pubPubMu.Lock()
	pubPub.rights = val == "true"
	pubPubMu.Unlock()
	u.openModalIfOwner(pubPublishOwner, u.pubPubModalHTML())
}

func (u *UI) pubPubVis(v string) {
	pubPubMu.Lock()
	pubPub.vis = v
	pubPubMu.Unlock()
	u.openModalIfOwner(pubPublishOwner, u.pubPubModalHTML())
}

// pubPubRetry returns a failed run to the confirm state (the manifest is still assembled).
func (u *UI) pubPubRetry() {
	pubPubMu.Lock()
	pubPub.errMsg, pubPub.res, pubPub.running = "", nil, false
	pubPub.prog = setpublish.Progress{}
	pubPubMu.Unlock()
	u.openModalIfOwner(pubPublishOwner, u.pubPubModalHTML())
}

// pubPubGo starts the publish. Guarded: no manifest, no rights tick, or a run already in
// flight all no-op.
func (u *UI) pubPubGo() {
	pub := u.setPublisher()
	if pub == nil {
		return
	}
	pubPubMu.Lock()
	if pubPub.man == nil || !pubPub.rights || pubPub.running {
		pubPubMu.Unlock()
		return
	}
	pubPub.running, pubPub.errMsg, pubPub.res = true, "", nil
	pubPub.prog = setpublish.Progress{Stage: setpublish.StagePreparing}
	req := setpublish.Request{
		RecordingID: pubPub.recID, Title: pubPub.man.Title, Visibility: pubPub.vis,
		RightsConfirmed: true,
	}
	pubPubMu.Unlock()

	tok := u.openModalAs(pubPublishOwner, u.pubPubModalHTML())
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Hour)
		defer cancel()
		res, err := pub.Publish(ctx, req, func(p setpublish.Progress) {
			pubPubMu.Lock()
			stageChanged := pubPub.prog.Stage != p.Stage
			pubPub.prog = p
			paint := stageChanged || time.Since(pubPub.lastPaint) >= pubPubRepaint
			if paint {
				pubPub.lastPaint = time.Now()
			}
			pubPubMu.Unlock()
			if paint {
				u.openModalIf(tok, u.pubPubModalHTML())
			}
		})
		pubPubMu.Lock()
		pubPub.running = false
		if err != nil {
			pubPub.errMsg = err.Error()
		} else {
			pubPub.res = &res
			pubPub.hasPrev = true
		}
		pubPubMu.Unlock()
		u.openModalIf(tok, u.pubPubModalHTML())
		if err == nil {
			u.patchMain() // the set row now shows "published"
		}
	})
}

// ── render ───────────────────────────────────────────────────────────────────

// pubPubModalHTML renders the dialog for whatever state the publish is in.
func (u *UI) pubPubModalHTML() string {
	pubPubMu.Lock()
	st := pubPub
	man := pubPub.man
	pubPubMu.Unlock()

	title := i18n.T("publish.upload.title")
	switch {
	case st.res != nil:
		return modal(title, u.pubPubDoneBody(st), btn(i18n.T("common.close"), "primary", "modal-close", ""))
	case st.running:
		return modal(title, u.pubPubProgressBody(st), "")
	case st.loading:
		return modal(title, hint("info", i18n.T("publish.upload.reading")), "")
	case st.errMsg != "" && man == nil:
		return modal(title, hint("warn", st.errMsg), "")
	}
	return modal(title, u.pubPubConfirmBody(st, man), u.pubPubFooter(st))
}

// pubPubConfirmBody is the pre-flight summary + consent gate.
func (u *UI) pubPubConfirmBody(st pubPubSt, man *setpublish.SetManifest) string {
	if man == nil {
		return hint("warn", i18n.T("publish.upload.unavailable"))
	}
	var b strings.Builder
	if st.errMsg != "" {
		b.WriteString(hint("warn", i18n.T("publish.upload.failed")+": "+st.errMsg))
	}
	if st.hasPrev {
		b.WriteString(hint("info", i18n.T("publish.upload.alreadyPublished",
			i18n.A{"when": st.prev.UploadedAt.Local().Format("2006-01-02 15:04")})))
	}

	// What actually leaves this machine.
	b.WriteString(statusRowDL("ok", i18n.T("publish.upload.file"), "publish-file", man.Audio.Name))
	b.WriteString(statusRowDL("ok", i18n.T("publish.upload.size"), "publish-size", humanBytes(uint64(man.Audio.SizeBytes))))
	b.WriteString(statusRowDL("ok", i18n.T("publish.upload.duration"), "publish-duration",
		pubClock(float64(man.DurationMs)/1000)))
	b.WriteString(statusRowDL("ok", i18n.T("publish.upload.tracks"), "publish-tracks",
		fmt.Sprint(len(man.Tracks))))
	b.WriteString(statusRowDL(pubLoudVariant(man), i18n.T("publish.upload.loudness"), "publish-loudness",
		pubLoudSummary(man)))

	for _, w := range man.Warnings {
		b.WriteString(hint("warn", w))
	}

	b.WriteString(subTabs("pub-pub-vis:", st.vis,
		[2]string{"private", i18n.T("publish.upload.visPrivate")},
		[2]string{"unlisted", i18n.T("publish.upload.visUnlisted")},
		[2]string{"public", i18n.T("publish.upload.visPublic")}))

	// Consent gate. data-label is space-free so `ctl set publish-rights true` drives it.
	b.WriteString(toggleRowDL(i18n.T("publish.upload.rights"), "publish-rights", "pub-pub-rights", st.rights))
	b.WriteString(hint("info", i18n.T("publish.upload.rightsHint")))
	b.WriteString(btnRow(btn(i18n.T("publish.upload.tos"), "ghost", "open-url", u.pubTermsURL())))
	return b.String()
}

// pubPubFooter gates the publish button on the rights tick.
func (u *UI) pubPubFooter(st pubPubSt) string {
	if st.man == nil && st.errMsg != "" {
		return ""
	}
	label := i18n.T("publish.upload.publishBtn")
	if st.hasPrev {
		label = i18n.T("publish.upload.republishBtn")
	}
	if !st.rights {
		// Visible but inert: the gate has to be legible, not hidden.
		return btnRow(btnGated(label, i18n.T("publish.upload.rightsRequired")),
			btn(i18n.T("common.cancel"), "outline", "modal-close", ""))
	}
	return btnRow(btn(label, "primary", "pub-pub-go", ""),
		btn(i18n.T("common.cancel"), "outline", "modal-close", ""))
}

// pubPubProgressBody is the staged progress view.
func (u *UI) pubPubProgressBody(st pubPubSt) string {
	p := st.prog
	var b strings.Builder
	b.WriteString(statusRowDL("live", pubStageLabel(p.Stage), "publish-stage", p.Detail))
	b.WriteString(progressBarStr(fmt.Sprintf("%.1f%%", clampPct(p.Percent)), pubPubCaption(p)))
	if p.BytesTotal > 0 {
		b.WriteString(statusRowDL("ok", i18n.T("publish.upload.sent"), "publish-sent",
			humanBytes(uint64(p.BytesSent))+" / "+humanBytes(uint64(p.BytesTotal))))
	}
	b.WriteString(hint("info", i18n.T("publish.upload.runningHint")))
	return b.String()
}

// pubPubDoneBody is the success end-state.
func (u *UI) pubPubDoneBody(st pubPubSt) string {
	r := st.res
	var b strings.Builder
	b.WriteString(hint("ok", i18n.T("publish.upload.done")))
	b.WriteString(statusRowDL("ok", i18n.T("publish.upload.recordingId"), "publish-recording", r.APIRecordingID))
	b.WriteString(statusRowDL("ok", i18n.T("publish.upload.tracks"), "publish-tracks", fmt.Sprint(r.Tracks)))
	if r.AudioReused {
		b.WriteString(hint("info", i18n.T("publish.upload.audioReused")))
	}
	if !r.WaveformSent || !r.LoudnessSent {
		b.WriteString(hint("warn", i18n.T("publish.upload.metaPartial")))
	}
	b.WriteString(btnRow(btn(i18n.T("publish.upload.viewOnline"), "explore", "open-url",
		u.pubRecordingURL(r.APIRecordingID))))
	return b.String()
}

// pubPubCaption is the bar caption: percent plus the stage's own detail.
func pubPubCaption(p setpublish.Progress) string {
	cap := fmt.Sprintf("%.0f%%", clampPct(p.Percent))
	if p.Detail != "" {
		cap += " · " + p.Detail
	}
	return cap
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func pubStageLabel(stage string) string {
	switch stage {
	case setpublish.StagePreparing:
		return i18n.T("publish.upload.stagePreparing")
	case setpublish.StageUploading:
		return i18n.T("publish.upload.stageUploading")
	case setpublish.StageProcessing:
		return i18n.T("publish.upload.stageProcessing")
	case setpublish.StagePublishing:
		return i18n.T("publish.upload.stagePublishing")
	}
	return i18n.T("publish.upload.stagePreparing")
}

// pubLoudSummary renders the integrated/true-peak summary, or the "not analysed" note.
func pubLoudSummary(man *setpublish.SetManifest) string {
	if man.Loudness == nil {
		return i18n.T("publish.upload.noLoudness")
	}
	return fmt.Sprintf("%.1f LUFS · %.1f dBTP · LRA %.1f",
		man.Loudness.IntegratedLUFS, man.Loudness.TruePeakDB, man.Loudness.LRA)
}

func pubLoudVariant(man *setpublish.SetManifest) string {
	if man.Loudness == nil {
		return "warn"
	}
	return "ok"
}

// pubTermsURL resolves the rave.page terms page for the configured API base.
func (u *UI) pubTermsURL() string {
	return u.pubWebsiteBase() + "/terms"
}

func (u *UI) pubRecordingURL(id string) string {
	return u.pubWebsiteBase() + "/recordings/" + id
}

func (u *UI) pubWebsiteBase() string {
	if u.svc.API == nil {
		return "https://rave.page"
	}
	return auth.WebsiteBase(u.svc.API.BaseURL())
}
