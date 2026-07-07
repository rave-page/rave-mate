package webui

import (
	"fmt"
	"html"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/audiorec"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/peerbridge"
	"rave.page/mate/internal/session"
)

const perfSpanWeb = 120 // 2 min at 1 Hz

// renderLive is the mid-set cockpit at parity with the Fyne Live tab: transport strip (stream /
// record / timecode), now-playing LCD, status, Decks A–D, streaming cockpit, live Network / Timing /
// System-performance graphs, and the bottom signal strip. livePush patches every live fragment.
func (u *UI) renderLive() string {
	var b strings.Builder
	b.WriteString(panel(i18n.T("live.title"), i18n.T("live.subtitle")))
	b.WriteString(`<div id=live-transport>` + u.liveTransportHTML() + `</div>`)
	b.WriteString(`<div id=live-np>` + u.nowPlayingHTML() + `</div>`)
	b.WriteString(section(i18n.T("live.status.title"), `<div id=live-status>`+u.liveStatusHTML()+`</div>`))
	b.WriteString(section(i18n.T("live.decks.title"), `<div id=live-decks>`+u.decksHTML()+`</div>`))
	if u.svc.OBSControl != nil {
		b.WriteString(section(i18n.T("live.cockpit.title"), `<div id=live-cockpit>`+u.cockpitHTML()+`</div>`))
	}
	if u.svc.AbleLink != nil {
		b.WriteString(section(i18n.T("live.ablelink.title"), `<div id=live-ablelink>`+u.ableLinkHTML()+`</div>`))
	}
	// tips live on the STATIC section titles - the well contents tick at 1 Hz.
	if u.svc.NetStats != nil {
		b.WriteString(sectionTip(i18n.T("live.network.title"), tipTopic("network-graph"), `<div id=live-net>`+u.networkHTML()+`</div>`))
		b.WriteString(sectionTip(i18n.T("live.timing.title"), tipTopic("timing-graph"), `<div id=live-tim>`+u.timingHTML()+`</div>`))
	}
	if u.svc.Perf != nil {
		b.WriteString(sectionTip(i18n.T("live.sysperf.title"), tipTopic("perf-graph"), `<div id=live-perf2>`+u.sysperfHTML()+`</div>`))
	}
	b.WriteString(`<div id=live-strip class=livestrip>` + u.liveStripHTML() + `</div>`)
	return b.String()
}

// ── transport ──

func (u *UI) liveTransportHTML() string {
	signedIn := u.svc.Auth != nil && u.svc.Auth.SignedIn()
	isLive := u.svc.Stream != nil && u.svc.Stream.Status().IsLive
	goliveDis, endDis := "", " disabled"
	if !signedIn || isLive {
		goliveDis = " disabled"
	}
	if isLive {
		endDis = ""
	}
	var b strings.Builder
	b.WriteString(`<div class=transport><span class=tlabel>` + html.EscapeString(i18n.T("live.transport.streamLabel")) + `</span>`)
	b.WriteString(`<form data-act=stream-golive><input class=field-input name=title placeholder=` + attrQ(i18n.T("live.transport.titlePlaceholder")) + ` style="width:220px" autocomplete=off>`)
	b.WriteString(`<button class="rp-btn rp-btn--go" type=submit` + goliveDis + `>` + html.EscapeString(i18n.T("live.transport.goLive")) + `</button></form>`)
	b.WriteString(`<button class="rp-btn rp-btn--destructive" data-act=stream-end` + endDis + `>` + html.EscapeString(i18n.T("live.transport.end")) + `</button>`)
	if u.svc.AudioRec != nil {
		rec := u.svc.AudioRec.Status()
		label := i18n.T("live.transport.record")
		if rec.Recording {
			label = i18n.T("player.stop")
		}
		b.WriteString(`<span class=tsep></span><span class=tlabel>` + html.EscapeString(i18n.T("live.transport.recLabel")) + `</span>`)
		b.WriteString(`<button class="rp-btn rp-btn--outline" data-act=arec-toggle>` + html.EscapeString(label) + `</button>`)
		b.WriteString(`<span class=np-artist id=live-rec-state>` + html.EscapeString(recStateText(rec)) + `</span>`)
	}
	if u.svc.Timecode != nil {
		b.WriteString(`<span class=tsep></span><span class=tlabel>` + html.EscapeString(i18n.T("live.transport.tcLabel")) + `</span>`)
		b.WriteString(`<span class=tmono id=live-tc>` + html.EscapeString(u.tcText()) + `</span>`)
		b.WriteString(`<button class="rp-btn rp-btn--go" data-act=tc-start>` + html.EscapeString(i18n.T("live.transport.start")) + `</button>`)
		b.WriteString(`<button class="rp-btn rp-btn--outline" data-act=tc-stop>` + html.EscapeString(i18n.T("live.transport.stopCaps")) + `</button>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func recStateText(s audiorec.Status) string {
	if !s.Recording {
		return ""
	}
	src := i18n.T("live.transport.recManual")
	if s.Auto {
		src = i18n.T("live.transport.recAuto")
	}
	if s.Path != "" {
		src += " · " + filepath.Base(s.Path)
	}
	return "● " + src
}

// ── ableton link ──

// ableLinkHTML renders the Link sync panel: phrase-phase bar, tempo/peers summary, a resync
// button, and (when OBS media-sync is present) the per-source chase/delay-comp readout.
func (u *UI) ableLinkHTML() string {
	st := u.svc.AbleLink.State()
	if !st.Available {
		return statusRow("warning", i18n.T("live.ablelink.backend"), i18n.T("live.ablelink.unavailable"))
	}
	quantum := int(st.Quantum)
	if quantum <= 0 {
		quantum = 16
	}
	var b strings.Builder
	beat := int(st.Phase) + 1 // 1-based beat within the phrase
	b.WriteString(progressBar(st.PhraseFraction(),
		i18n.T("live.ablelink.phraseBeat", i18n.A{"beat": fmt.Sprint(beat), "quantum": fmt.Sprint(quantum)})))
	variant, state := "success", i18n.T("live.ablelink.enabled")
	if !st.Enabled {
		variant, state = "off", i18n.T("live.ablelink.disabled")
	}
	b.WriteString(statusRow(variant, i18n.T("live.ablelink.session"),
		i18n.T("live.ablelink.summary", i18n.A{"tempo": fmt.Sprintf("%.1f", st.Tempo), "peers": fmt.Sprint(st.Peers), "state": state})))
	b.WriteString(btnRow(btn(i18n.T("settings.body.ablelink.resync"), "outline", "ablelink-resync", "")))
	// Per-source media-sync / delay-comp chase readout (mirrors the Fyne obssync view).
	if u.svc.OBSControl != nil {
		for _, ss := range u.svc.OBSControl.SyncStatuses() {
			line := ss.LastAction
			sv := "success"
			if ss.Err != "" {
				line, sv = "error: "+ss.Err, "error"
			} else if ss.Active && !ss.Playing {
				line, sv = i18n.T("live.ablelink.waiting"), "warning"
			}
			src := ss.Source
			if src == "" {
				src = "?"
			}
			b.WriteString(statusRow(sv, src, fmt.Sprintf("%s · err %+dms · %.0f corr/min", line, ss.ErrorMs, ss.CorrectionsPerMin)))
		}
	}
	return b.String()
}

// ── now playing ──

func (u *UI) nowPlayingHTML() string {
	d, ok := u.masterDeck()
	line1, line2 := i18n.T("live.nowPlaying.noTrack"), ""
	if ok {
		t := strings.TrimSpace(d.Artist)
		if t != "" && d.Title != "" {
			t += " - "
		}
		t += d.Title
		if len(t) > 40 {
			t = t[:39] + "…"
		}
		line1 = "♪ " + strings.ToUpper(t)
		line2 = i18n.T("live.nowPlaying.line2", i18n.A{"deck": d.Deck, "elapsed": mmss(d.ElapsedTime), "total": mmss(d.TrackLength)})
		if d.BPM > 0 {
			line2 += "    " + i18n.T("live.bpm", i18n.A{"bpm": fmt.Sprintf("%.1f", d.BPM)})
		}
		if d.Key != "" {
			line2 += "    " + d.Key
		}
	}
	return `<div class=lcd><div class=lcd-1 data-label="now playing" data-value="` + html.EscapeString(line1) + `">` +
		html.EscapeString(line1) + `</div><div class=lcd-2>` + html.EscapeString(line2) + `</div></div>`
}

func (u *UI) masterDeck() (session.DeckSnapshot, bool) {
	if u.svc.Session != nil {
		ov := u.svc.Session.Snapshot().BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
		for _, d := range ov.Decks {
			if d.Deck == ov.Master.Deck {
				return d, true
			}
		}
	}
	if u.svc.PeerBridge != nil {
		var best *peerbridge.RemoteState
		for _, s := range u.svc.PeerBridge.RemoteStates() {
			s := s
			if !s.NowPlaying.Playing || time.Since(s.UpdatedAt) > session.NowPlayingStaleAfter {
				continue
			}
			if best == nil || s.UpdatedAt.After(best.UpdatedAt) {
				best = &s
			}
		}
		if best != nil {
			np := best.NowPlaying
			return session.DeckSnapshot{Deck: np.Deck, Title: np.Title, Artist: np.Artist,
				BPM: np.BPM, Key: np.Key, ElapsedTime: np.Elapsed, TrackLength: np.Length, IsPlaying: np.Playing}, true
		}
	}
	return session.DeckSnapshot{}, false
}

// ── status ──

func (u *UI) liveStatusHTML() string {
	api := ""
	if u.svc.API != nil {
		api = u.svc.API.BaseURL()
	}
	acct := i18n.T("live.status.notSignedIn")
	if u.svc.Auth != nil && u.svc.Auth.SignedIn() {
		acct = i18n.T("live.status.signedIn")
	}
	trk := "-"
	if u.svc.Traktor != nil {
		port := 8080
		if u.svc.Cfg != nil {
			port = u.svc.Cfg.Features.Traktor.ResolvedPort()
		}
		switch {
		case u.svc.Cfg != nil && !u.svc.Cfg.Features.Traktor.Enabled:
			trk = i18n.T("common.disabled")
		case u.svc.Traktor.Listening():
			trk = i18n.T("live.status.trkListening", i18n.A{"port": fmt.Sprint(port)})
		default:
			trk = i18n.T("live.status.trkNotBound", i18n.A{"port": fmt.Sprint(port)})
		}
	}
	strm := i18n.T("live.status.idle")
	if u.svc.Stream != nil {
		s := u.svc.Stream.Status()
		switch {
		case s.IsLive:
			strm = i18n.T("live.status.strmLive", i18n.A{"title": s.Title, "count": fmt.Sprint(s.PendingEventCount)})
		case s.LastError != "":
			strm = i18n.T("live.status.idleError", i18n.A{"error": s.LastError})
		}
	}
	row := func(k, v string) string {
		return `<div class=st-row><span class=st-k>` + html.EscapeString(k) + `</span>` +
			`<span data-label="` + strings.ToLower(k) + `" data-value="` + html.EscapeString(v) + `">` + html.EscapeString(v) + `</span></div>`
	}
	return `<div class="rp-card">` + row(i18n.T("live.status.apiLabel"), api) + row(i18n.T("live.status.accountLabel"), acct) +
		row(i18n.T("live.status.traktorLabel"), trk) + row(i18n.T("live.status.streamLabel"), strm) + `</div>`
}

// ── decks ──

func (u *UI) decksHTML() string {
	byDeck := map[string]session.DeckSnapshot{}
	audible := ""
	if u.svc.Session != nil {
		ov := u.svc.Session.Snapshot().BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
		for _, d := range ov.Decks {
			byDeck[d.Deck] = d
		}
		audible = ov.Master.Deck
	}
	// No local deck playing → mirror all playing decks from the freshest linked peer.
	note := ""
	localLive := false
	for _, d := range byDeck {
		if d.IsPlaying {
			localLive = true
			break
		}
	}
	if !localLive && u.svc.PeerBridge != nil {
		var best *peerbridge.RemoteState
		for _, s := range u.svc.PeerBridge.RemoteStates() {
			s := s
			if time.Since(s.UpdatedAt) > session.NowPlayingStaleAfter || len(s.NowPlaying.AllDecks()) == 0 {
				continue
			}
			if best == nil || s.UpdatedAt.After(best.UpdatedAt) {
				best = &s
			}
		}
		if best != nil {
			byDeck, audible = map[string]session.DeckSnapshot{}, ""
			for _, ds := range best.NowPlaying.AllDecks() {
				byDeck[ds.Deck] = session.DeckSnapshot{Deck: ds.Deck, Title: ds.Title, Artist: ds.Artist,
					BPM: ds.BPM, Key: ds.Key, ElapsedTime: ds.Elapsed, TrackLength: ds.Length, IsPlaying: true}
				if ds.Audible {
					audible = ds.Deck
				}
			}
			note = i18n.T("live.decks.fromPeer", i18n.A{"name": peerName("", best.NodeID)})
		}
	}
	// A-D always render; extra peer deck ids append after.
	ids := []string{"A", "B", "C", "D"}
	seen := map[string]bool{"A": true, "B": true, "C": true, "D": true}
	for id := range byDeck {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids[4:])
	var b strings.Builder
	if note != "" {
		b.WriteString(`<div class=decks-note>` + html.EscapeString(note) + `</div>`)
	}
	b.WriteString(`<div class=decks-grid>`)
	for _, id := range ids {
		d, ok := byDeck[id]
		cls := "deckbig"
		title, meta := "–", "-"
		if ok {
			if cur := strings.TrimSpace(d.Artist + " - " + d.Title); cur != "-" && cur != "" {
				title = cur
			}
			meta = fmt.Sprintf("%s / %s", mmss(d.ElapsedTime), mmss(d.TrackLength))
			if d.BPM > 0 {
				meta += " · " + i18n.T("live.bpm", i18n.A{"bpm": fmt.Sprintf("%.1f", d.BPM)})
			}
			if d.Key != "" {
				meta += " · " + d.Key
			}
			if d.IsPlaying {
				cls += " deckbig--live"
			}
			if id == audible && d.IsPlaying {
				cls += " deckbig--audible"
			}
		}
		b.WriteString(`<div class="` + cls + `"><div class=deckbig-id>` + html.EscapeString(i18n.T("live.deck.name", i18n.A{"id": id})) + `</div>` +
			`<div class=deckbig-t>` + html.EscapeString(title) + `</div><div class=deckbig-m>` + html.EscapeString(meta) + `</div></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── streaming cockpit ──

func (u *UI) cockpitHTML() string {
	insts := u.svc.OBSControl.Statuses()
	if len(insts) == 0 {
		return emptyState(i18n.T("live.cockpit.empty"))
	}
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	for _, in := range insts {
		name := in.Label
		if name == "" {
			name = peerName("", in.Node)
		}
		if in.Local {
			name += " " + i18n.T("live.cockpit.thisPc")
		}
		state, sv := i18n.T("live.cockpit.offline"), "muted"
		switch {
		case in.Streaming:
			state, sv = i18n.T("live.cockpit.streaming", i18n.A{"kbps": fmt.Sprint(in.BitrateKbps)}), "error"
		case in.Recording:
			state, sv = i18n.T("live.cockpit.recording"), "error"
		case in.Reconnecting:
			state, sv = i18n.T("live.cockpit.reconnecting"), "warning"
		case in.Connected:
			state, sv = i18n.T("live.cockpit.ready"), "success"
		}
		streamLabel, recLabel := i18n.T("live.cockpit.startStream"), i18n.T("live.cockpit.startRec")
		if in.Streaming {
			streamLabel = i18n.T("live.cockpit.stopStream")
		}
		if in.Recording {
			recLabel = i18n.T("live.cockpit.stopRec")
		}
		b.WriteString(`<div class=row><span class=row-label>` + dot(sv) + ` ` + html.EscapeString(name) +
			` <span class=np-artist>` + html.EscapeString(state) + `</span></span>` +
			btnRow(btn(streamLabel, "primary", "obs-stream:"+in.ID, ""), btn(recLabel, "outline", "obs-record:"+in.ID, "")) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── graphs ──

func (u *UI) networkHTML() string {
	snap := u.svc.NetStats.Snapshot()
	graph := sparklineSVG([]sparkSeries{
		{snap.PeerIn, sparkMint, true},
		{snap.PeerOut, sparkHot, true},
		{snap.APIIn, sparkViolet, false},
		{snap.APIOut, sparkAmber, false},
	}, 600, 56)
	legend := fmt.Sprintf(`<span style="color:%s">%s</span><span style="color:%s">%s</span>`+
		`<span style="color:%s">%s</span><span style="color:%s">%s</span>`+
		`<span style="color:%s">%s</span>`,
		sparkMint, html.EscapeString(i18n.T("live.network.peerDown", i18n.A{"rate": rateStr(snap.PeerIn)})),
		sparkHot, html.EscapeString(i18n.T("live.network.upRate", i18n.A{"rate": rateStr(snap.PeerOut)})),
		sparkViolet, html.EscapeString(i18n.T("live.network.apiDown", i18n.A{"rate": rateStr(snap.APIIn)})),
		sparkAmber, html.EscapeString(i18n.T("live.network.upRate", i18n.A{"rate": rateStr(snap.APIOut)})),
		sparkMint, html.EscapeString(i18n.T("live.network.session", i18n.A{
			"down": humanBytes(snap.SessPeerIn + snap.SessAPIIn),
			"up":   humanBytes(snap.SessPeerOut + snap.SessAPIOut),
		})))
	return `<div class=gwell title=` + attrQ(i18n.T("live.network.tooltip")) + `><div class=glegend>` + legend + `</div>` + graph + `</div>`
}

func (u *UI) timingHTML() string {
	snap := u.svc.NetStats.Snapshot()
	pal := []string{sparkMint, sparkHot, sparkViolet, sparkAmber, sparkInfo}
	var series []sparkSeries
	var legend strings.Builder
	for i, r := range snap.RTT {
		c := pal[i%len(pal)]
		series = append(series, sparkSeries{r.Ms, c, false})
		ms := "-"
		if r.Has {
			ms = fmt.Sprintf("%.1fms", r.LatestMs)
		}
		fmt.Fprintf(&legend, `<span style="color:%s">%s %s</span>`, c, html.EscapeString(strings.ToUpper(r.Label)), ms)
	}
	if len(series) == 0 {
		series = []sparkSeries{{make([]float64, snap.Span), sparkMuted, false}}
		legend.WriteString(`<span>` + html.EscapeString(i18n.T("live.timing.noPeers")) + `</span>`)
	}
	return `<div class=gwell title=` + attrQ(i18n.T("live.timing.tooltip")) + `><div class=glegend>` + legend.String() + `</div>` + sparklineSVG(series, 600, 56) + `</div>`
}

func (u *UI) sysperfHTML() string {
	ss := u.svc.Perf.Snapshot()
	if len(ss) > perfSpanWeb {
		ss = ss[len(ss)-perfSpanWeb:]
	}
	appC := make([]float64, len(ss))
	sysC := make([]float64, len(ss))
	appR := make([]float64, len(ss))
	sysR := make([]float64, len(ss))
	for i, s := range ss {
		appC[i], appR[i] = s.CPUPct, s.RSSMB
		if s.SysOK {
			sysC[i], sysR[i] = s.SysCPUPct, s.SysMemUsedMB
		} else {
			sysC[i], sysR[i] = math.NaN(), math.NaN()
		}
	}
	appLbl, sysLbl := html.EscapeString(i18n.T("live.perf.app")), html.EscapeString(i18n.T("live.perf.sys"))
	cpuLeg := `<span>` + appLbl + ` -</span><span>` + sysLbl + ` -</span>`
	ramLeg := `<span>` + appLbl + ` -</span><span>` + sysLbl + ` -</span>`
	head := i18n.T("live.perf.headroom") + " -"
	if len(ss) > 0 {
		l := ss[len(ss)-1]
		sysCPU, sysRAM := "-", "-"
		if l.SysOK {
			sysCPU = fmt.Sprintf("%.0f%%", l.SysCPUPct)
			sysRAM = fmt.Sprintf("%.1f/%.1f GB", l.SysMemUsedMB/1024, l.SysMemTotalMB/1024)
			head = i18n.T("live.perf.headroomLine", i18n.A{
				"gb":  fmt.Sprintf("%.1f", (l.SysMemTotalMB-l.SysMemUsedMB)/1024),
				"cpu": fmt.Sprintf("%.0f", math.Max(0, 100-l.SysCPUPct)),
			})
		}
		cpuLeg = fmt.Sprintf(`<span style="color:%s">%s %.0f%%</span><span style="color:%s">%s %s</span>`, sparkMint, appLbl, l.CPUPct, sparkHot, sysLbl, sysCPU)
		ramLeg = fmt.Sprintf(`<span style="color:%s">%s %.0f MB</span><span style="color:%s">%s %s</span>`, sparkViolet, appLbl, l.RSSMB, sparkAmber, sysLbl, sysRAM)
	}
	cpuG := sparklineSVG([]sparkSeries{{appC, sparkMint, true}, {sysC, sparkHot, false}}, 600, 56)
	ramG := sparklineSVG([]sparkSeries{{sysR, sparkAmber, false}, {appR, sparkViolet, true}}, 600, 56)
	return `<div class=gwell title=` + attrQ(i18n.T("live.perf.tooltip")) + `><div class=glegend>` + cpuLeg + `</div>` + cpuG +
		`<div class=glegend>` + ramLeg + `</div>` + ramG +
		`<div class=glegend><span style="color:` + sparkMint + `">` + html.EscapeString(head) + `</span></div></div>`
}

// ── bottom signal strip (port of liveStatusLeft/Center/Right) ──

func (u *UI) liveStripHTML() string {
	return `<span>` + html.EscapeString(u.stripLeft()) + `</span><span>` + html.EscapeString(u.stripCenter()) +
		`</span><span>` + html.EscapeString(u.stripRight()) + `</span>`
}

func (u *UI) stripLeft() string {
	var p []string
	if u.svc.OBS != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.OBS.Enabled {
		o := u.svc.OBS.Status()
		switch {
		case o.Recording:
			p = append(p, i18n.T("live.strip.obsRec", i18n.A{"dur": time.Since(o.RecStartedAt).Truncate(time.Second).String()}))
		case o.Connected:
			p = append(p, i18n.T("live.strip.obsOk"))
		default:
			p = append(p, i18n.T("live.strip.obsOff"))
		}
	}
	if u.svc.SetCapture != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.SetCapture.Enabled {
		c := u.svc.SetCapture.Snapshot()
		switch {
		case c.Connected:
			p = append(p, i18n.T("live.strip.capBytes", i18n.A{"bytes": humanBytes(uint64(c.Bytes))}))
		case c.Reconnecting:
			p = append(p, i18n.T("live.strip.capReconnecting"))
		case c.Listening:
			p = append(p, i18n.T("live.strip.capListening"))
		}
	}
	if u.svc.AudioRec != nil && u.svc.AudioRec.Status().Recording {
		p = append(p, i18n.T("live.strip.recOn"))
	}
	return strings.Join(p, " · ")
}

func (u *UI) stripCenter() string {
	var p []string
	if u.svc.Timecode != nil {
		if tc, running := u.svc.Timecode.Now(); running {
			p = append(p, i18n.T("live.strip.tc", i18n.A{"tc": tc.String()}))
		}
	}
	if u.svc.DMX != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.DMX.Enabled {
		snap := u.svc.DMX.Status()
		receiving := false
		for _, un := range snap.Universes {
			if un.PPS > 0 {
				receiving = true
			}
		}
		switch {
		case !snap.Running:
			p = append(p, i18n.T("live.strip.dmxOff"))
		case receiving:
			p = append(p, i18n.T("live.strip.dmxActive"))
		default:
			p = append(p, i18n.T("live.strip.dmxIdle"))
		}
	}
	return strings.Join(p, " · ")
}

func (u *UI) stripRight() string {
	var p []string
	if m := u.svc.Twitch; m != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.Twitch.Enabled {
		switch {
		case m.SignedIn() && m.Self().Login != "":
			p = append(p, i18n.T("live.strip.twitchUser", i18n.A{"login": m.Self().Login}))
		case m.SignedIn():
			p = append(p, i18n.T("live.strip.twitchConnecting"))
		}
	}
	if u.svc.Perf != nil {
		if ss := u.svc.Perf.Snapshot(); len(ss) > 0 {
			last := ss[len(ss)-1]
			if last.SysOK {
				p = append(p, i18n.T("live.strip.freeResources", i18n.A{
					"cpu": fmt.Sprintf("%.0f", math.Max(0, 100-last.SysCPUPct)),
					"gb":  fmt.Sprintf("%.1f", (last.SysMemTotalMB-last.SysMemUsedMB)/1024),
				}))
			}
		}
	}
	return strings.Join(p, " · ")
}

// ── small helpers ──

func mmss(s float64) string {
	if s <= 0 {
		return "0:00"
	}
	t := int(s)
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

func lastVal(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	if v := vals[len(vals)-1]; !math.IsNaN(v) {
		return v
	}
	return 0
}

func rateStr(vals []float64) string { return humanBytes(uint64(lastVal(vals))) + "/s" }

func (u *UI) tcText() string {
	if u.svc.Timecode == nil {
		return "--:--:--:--"
	}
	tc, running := u.svc.Timecode.Now()
	if running {
		return tc.String()
	}
	return tc.String() + " ◼"
}
