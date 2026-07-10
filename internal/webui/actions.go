package webui

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/obscontrol"
)

// Parameterized action handlers (dispatched from onAction by act-prefix). Long/blocking backend
// calls run off-thread + toast; snapshot-based tabs re-render on the next livePush tick.

func (u *UI) bg(fn func()) { go fn() }

// actx returns a 30s-bounded ctx also cancelled by Stop(), so in-flight bg work (and its
// post-destroy Dispatch evals) dies with the window.
func (u *UI) actx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() { // watcher exits with the ctx (callers defer cancel)
		select {
		case <-u.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// stopped reports Stop() ran - bg completions skip DOM patches on a destroyed webview.
func (u *UI) stopped() bool {
	select {
	case <-u.stop:
		return true
	default:
		return false
	}
}

// actBusy guards slow one-shot bg actions (ffmpeg stop, bbolt fsync): one in flight per key,
// repeats dropped. Package-level like cfgJobs - one webview UI per process.
var (
	actBusyMu sync.Mutex
	actBusy   = map[string]bool{}
)

// actStart marks key in-flight; false if already running.
func actStart(key string) bool {
	actBusyMu.Lock()
	defer actBusyMu.Unlock()
	if actBusy[key] {
		return false
	}
	actBusy[key] = true
	return true
}

func actEnd(key string) {
	actBusyMu.Lock()
	delete(actBusy, key)
	actBusyMu.Unlock()
}

// pendingAct disables the control bound to act until the completion re-patch rebuilds it -
// immediate feedback while a slow bg job runs.
func (u *UI) pendingAct(act string) {
	u.eval("var b=document.querySelector(" + jsQuote("[data-act="+jsQuote(act)+"]") + ");if(b)b.disabled=true")
}

func (u *UI) logErr(what string, err error) {
	if err != nil && u.log != nil {
		u.log.Error("webui", what, map[string]any{"error": err.Error()})
	}
}

func (u *UI) launchAppGroup(id string) {
	if u.svc.AppGroups == nil {
		return
	}
	u.toast(i18n.T("actions.toast.launchingGroup"))
	u.bg(func() {
		_, _, err := u.svc.AppGroups.LaunchGroup(id)
		u.logErr("launch group", err)
	})
}

// autoToggle flips an automation's enabled flag. Save fsyncs bbolt - off the actWorker.
func (u *UI) autoToggle(id string, on bool) {
	if u.svc.Automations == nil || !actStart("auto:"+id) {
		return
	}
	u.pendingAct("auto-toggle:" + id)
	u.bg(func() {
		defer actEnd("auto:" + id)
		for _, a := range u.svc.Automations.List() {
			if a.ID == id {
				a.Enabled = on
				_, err := u.svc.Automations.Save(a)
				u.logErr("automation save", err)
				break
			}
		}
		if !u.stopped() {
			u.patchMain()
		}
	})
}

// autoDelete removes an automation. Delete fsyncs bbolt - off the actWorker.
func (u *UI) autoDelete(id string) {
	if u.svc.Automations == nil || !actStart("auto:"+id) {
		return
	}
	u.pendingAct("auto-del:" + id)
	u.bg(func() {
		defer actEnd("auto:" + id)
		u.logErr("automation delete", u.svc.Automations.Delete(id))
		if u.stopped() {
			return
		}
		u.patchMain()
		u.toast(i18n.T("actions.toast.automationDeleted"))
	})
}

func (u *UI) peerConnect(node string) {
	if u.svc.Peers == nil || u.svc.Discovery == nil {
		return
	}
	for _, p := range u.svc.Discovery.Peers() {
		if p.NodeID == node {
			u.svc.Peers.Connect(p)
			u.toast(i18n.T("actions.toast.connecting"))
			return
		}
	}
}

func (u *UI) peerForget(node string) {
	if u.svc.Peers != nil {
		u.svc.Peers.Forget(node)
	}
	u.patchMain()
}

func (u *UI) peerSAS(node string, ok bool) {
	if u.svc.Peers != nil {
		u.svc.Peers.ConfirmSAS(node, ok)
	}
	u.patchMain()
}

func (u *UI) mediaReceive(arg string) {
	if u.svc.MediaRoutes == nil {
		return
	}
	peer, src, found := strings.Cut(arg, "\x1f")
	if !found {
		return
	}
	u.toast(i18n.T("actions.toast.startingReceive"))
	u.bg(func() {
		_, err := u.svc.MediaRoutes.StartReceive(peer, src)
		u.logErr("media receive", err)
	})
}

func (u *UI) mediaStop(session string) {
	if u.svc.MediaRoutes != nil {
		u.svc.MediaRoutes.StopReceive(session)
	}
	u.patchMain()
}

func (u *UI) xferAccept(id string, ok bool) {
	if u.svc.FileXfer != nil {
		u.svc.FileXfer.Accept(id, ok)
	}
	u.patchMain()
}

func (u *UI) xferCancel(id string) {
	if u.svc.FileXfer != nil {
		u.svc.FileXfer.Cancel(id)
	}
	u.patchMain()
}

// recFinish closes the recorder set (two bbolt fsyncs) - off the actWorker.
func (u *UI) recFinish() {
	if u.svc.Recorder == nil || !actStart("rec-finish") {
		return
	}
	u.pendingAct("rec-finish")
	u.bg(func() {
		defer actEnd("rec-finish")
		u.svc.Recorder.StopRecording()
		if u.stopped() {
			return
		}
		u.patchMain()
		u.toast(i18n.T("actions.toast.setFinished"))
	})
}

func (u *UI) vrcStatus(form string) {
	if u.svc.Vrchat == nil {
		return
	}
	m := parseForm(form)
	u.toast(i18n.T("actions.toast.updatingVrcStatus"))
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		_, err := u.svc.Vrchat.UpdateStatus(ctx, m["status"], m["desc"])
		u.logErr("vrchat status", err)
	})
}

func (u *UI) vrcBio(form string) {
	if u.svc.Vrchat == nil {
		return
	}
	m := parseForm(form)
	u.toast(i18n.T("actions.toast.updatingVrcBio"))
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		_, err := u.svc.Vrchat.UpdateBio(ctx, m["bio"], nil)
		u.logErr("vrchat bio", err)
	})
}

func (u *UI) wsPublish(kind, key string) {
	s := u.svc.WorldSync
	if s == nil {
		return
	}
	u.toast(i18n.T("actions.toast.publishingGithub"))
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		switch kind {
		case "posters":
			s.PublishPosters(ctx)
		case "events":
			s.PublishEvents(ctx)
		case "nowplaying":
			s.PublishNowPlaying(ctx)
		case "list":
			lists := u.svc.Cfg.Features.WorldSync.Lists
			for i := range lists {
				if lists[i].Name == key {
					s.PublishList(ctx, &lists[i])
					return
				}
			}
		}
	})
}

// ── Live transport ──

// streamPause toggles StreamBridge.PauseLiveSignal (private / non-DJ streams). Server-authoritative
// flip: the switch is a momentary signal, so we invert the persisted flag rather than trust the
// round-tripped checkbox state, then re-render from config (the switch always mirrors the flag).
// Auto-live itself is OBS-driven in the daemon; the driver's 3s reconcile ends a live broadcast once
// paused. on is ignored (kept for the dispatch signature).
func (u *UI) streamPause(_ bool) {
	if u.svc.Cfg == nil {
		return
	}
	now := !u.svc.Cfg.Features.StreamBridge.PauseLiveSignal
	u.svc.Cfg.Features.StreamBridge.PauseLiveSignal = now
	u.saveCfg()
	u.eval("window.__patch('live-transport'," + jsQuote(u.liveTransportHTML()) + ")")
	key := "actions.toast.liveResumed"
	if now {
		key = "actions.toast.livePaused"
	}
	u.toast(i18n.T(key))
}

// arecToggle starts/stops manual audio capture. Stop waits on ffmpeg's graceful exit (≤6s) +
// finalize - off the actWorker; button stays disabled until the completion re-patch.
func (u *UI) arecToggle() {
	if u.svc.AudioRec == nil || !actStart("arec-toggle") {
		return
	}
	u.pendingAct("arec-toggle")
	u.bg(func() {
		defer actEnd("arec-toggle")
		var err error
		if u.svc.AudioRec.Status().Recording {
			err = u.svc.AudioRec.StopManual()
		} else {
			err = u.svc.AudioRec.StartManual()
		}
		u.logErr("audio record", err)
		if !u.stopped() {
			u.eval("window.__patch('live-transport'," + jsQuote(u.liveTransportHTML()) + ")")
		}
	})
}

func (u *UI) tcStart() {
	if u.svc.Timecode != nil {
		u.bg(func() { u.logErr("tc start", u.svc.Timecode.StartClock()) })
	}
}

func (u *UI) tcStop() {
	if u.svc.Timecode != nil {
		u.svc.Timecode.StopClock()
	}
}

func (u *UI) obsCmd(id, kind string) {
	if u.svc.OBSControl == nil {
		return
	}
	action := obscontrol.ActStreamToggle
	if kind == "record" {
		action = obscontrol.ActRecordToggle
	}
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		u.logErr("obs cmd", u.svc.OBSControl.Command(ctx, obscontrol.Cmd{Target: id, Action: action}))
	})
}

func parseForm(form string) map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal([]byte(form), &m)
	return m
}
