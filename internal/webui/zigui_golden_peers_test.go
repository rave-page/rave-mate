//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Peers golden gate: Zig renderer must be BYTE-IDENTICAL to the Go renderer for
// representative states. Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

func peerSel(id, label, cur string, opts ...[2]string) selState {
	s := selState{ID: id, Label: label, CurLabel: cur, Rows: []selRow{}}
	for _, o := range opts {
		s.Rows = append(s.Rows, selRow{Val: o[0], Label: o[1], Cur: o[0] == cur})
	}
	if len(s.Rows) == 0 {
		s.CurLabel = "(select…)"
	}
	return s
}

// peersFixtures: unavailable, empty, gated, populated, escaping edge, long values, unicode.
func peersFixtures() map[string]peersSt {
	base := func() peersSt {
		return peersSt{
			Title: "Peers", Sub: "Pair another rave-mate on this LAN", Available: true,
			Unavailable: "Peer link unavailable",
			Body: peersBodySt{
				Strip:           "0 connected · 0 found · node ab12cd34ef…",
				ConnsTitle:      "Connections",
				Conns:           peerListSt{Empty: "No active connections"},
				MediaTitle:      "Media plane",
				CamTitle:        "Webcam",
				XferTitle:       "File transfer",
				NetTitle:        "On this network",
				Discovered:      peerListSt{Empty: "Searching for peers…"},
				RememberedTitle: "Remembered (offline)",
				Remembered:      peerListSt{Empty: "None"},
			},
		}
	}

	unavailable := peersSt{Title: "Peers", Sub: "ignored", Available: false,
		Unavailable: "Peer link unavailable"}

	// available, every optional section hidden, all three lists empty
	empty := base()

	// cam feature off, no transfers, discovery off, media plane with zero routes
	gated := base()
	gated.Body.Discovered = peerListSt{Empty: "Peer discovery is off"}
	gated.Body.Media = peerMediaSt{
		Show: true, ClockLine: "Clock tier 3 · acquiring",
		NoRoutes: "No active media routes",
	}
	gated.Body.Cam = peerCamSt{Show: true, Gated: true, GateHint: "Webcam is switched off in Settings"}
	gated.Body.Xfer = peerXferSt{
		Show: true, None: true, NoneHint: "No transfers yet",
		Settings: xferSetSt{
			Show:      true,
			Enabled:   newToggle("Receive files", "peers-xfer-enabled", false),
			AcceptLbl: "Accept", Mode: "ask", AskLbl: "Ask", AutoLbl: "Automatic",
			Dir:        newField("Save to", "peers-xfer-dir", "", "text"),
			DefaultDir: "Default: C:\\Users\\dj\\Downloads",
		},
	}

	populated := base()
	populated.Body.Strip = "2 connected · 3 found · node ab12cd34ef…"
	populated.Body.Banner = peerBannerSt{
		Show: true, Text: "Controlling Studio PC",
		Btn: uiBtn{Label: "Stop controlling", Variant: "warn", Act: "peers-control:node-1", Val: "0"},
	}
	populated.Body.Conns = peerListSt{
		Empty: "No active connections",
		Rows: []peerRowSt{
			{Dot: "success", Name: "Studio PC", Sub: "connected · paired",
				Btns: []uiBtn{
					{Label: "Stop control", Variant: "warn", Act: "peers-control:node-1", Val: "0"},
					{Label: "Forget", Variant: "ghost", Act: "peer-forget:node-1"},
				},
				Decks: []peerDeckSt{
					{Audible: true, Line: "Deck A · Artist - Title  (128 BPM)"},
					{Line: "Deck B · Other - Track"},
				}},
			{Name: "Laptop", Sub: "pairing code 421 907",
				Btns: []uiBtn{
					{Label: "Matches", Variant: "go", Act: "peer-sas:node-2", Val: "1"},
					{Label: "Doesn't match", Variant: "destructive", Act: "peer-sas:node-2", Val: "0"},
				}},
			{Dot: "warning", Name: "node-3abc…", Sub: "connecting",
				Btns: []uiBtn{{Label: "Forget", Variant: "ghost", Act: "peer-forget:node-3"}}},
		},
	}
	populated.Body.Media = peerMediaSt{
		Show:      true,
		ClockLine: "Clock tier 1 · locked · offset +0.42 ms",
		SyncLines: []string{"Studio PC +0.42 ms · rtt 1.20 ms · locked", "Laptop −12.3 ms · rtt 4.10 ms · acquiring"},
		HasTC:     true, TCLine: "TC master: this instance · 01:02:03:04 @30",
		NoRoutes:  "No active media routes",
		RoutesHdr: "Routes: 2",
		Routes: []peerRouteSt{
			{Title: "Sending to Studio PC · stream 1 · 1204 frames · 12.4 MB",
				Detail: "loss 0 · 0.00% · jitter 0.31 ms · retx 2 · pli 0",
				Pipe:   "h264_nvenc tier 2 · 6.2 Mbps · kf 40 · out 30.0 fps · cuda"},
			{Title: "Receiving from Laptop · stream 2 · 900 frames · 8.1 MB",
				Detail: "loss 3 · recovered 3 · jitter 0.90 ms · p50 12.3 ms · p95 41.0 ms · nack 5"},
		},
		Recv: peerRecvSt{
			Show: true, Head: "Receive video",
			Rows: []peerRecvRowSt{
				{Mark: "◂ ", Line: "cam @ Studio PC",
					Btn: uiBtn{Label: "Stop", Variant: "destructive", Act: "media-stop:sess-1"}},
				{Line: "screen @ Laptop · 1920x1080@60",
					Btn: uiBtn{Label: "Receive", Variant: "go", Act: "media-recv:node-2\x1fdesc-9"}},
			},
		},
	}
	populated.Body.Cam = peerCamSt{
		Show: true, Empty: "No camera instances",
		Nodes: []camNodeSt{
			{Name: "This instance", RefreshAct: "peers-cam-refresh:node-1", Status: "Live 1280x720 @ 30",
				Dev:    peerSel("peers-cam-device-node-1", "Device", "C920", [2]string{"C920", "C920"}, [2]string{"OBS Virtual Camera", "OBS Virtual Camera"}),
				Mode:   peerSel("peers-cam-mode-node-1", "Size / FPS", "1280x720 @ 30", [2]string{"1280x720 @ 30", "1280x720 @ 30"}, [2]string{"1920x1080 @ 30", "1920x1080 @ 30"}),
				Start:  uiBtn{Label: "Stop", Variant: "warn", Act: "peers-cam-start:node-1", Val: "stop"},
				Sender: "rave-cam", SenderLine: "Spout sender: rave-cam", PropsHdr: "Lens / image",
				Props: []camPropSt{
					{Label: "Zoom", MinS: "0", MaxS: "500", StepS: "5", ValS: "120",
						Act: "peers-cam-prop:node-1\x1fzoom", AutoAct: "peers-cam-auto:node-1\x1fzoom", AutoLbl: "Auto"},
					{Label: "Exposure", MinS: "-11", MaxS: "1", StepS: "1", ValS: "-6",
						Act: "peers-cam-prop:node-1\x1fexposure", Disabled: true, CanAuto: true, Auto: true,
						AutoAct: "peers-cam-auto:node-1\x1fexposure", AutoLbl: "Auto"},
				}},
			{Name: "Paired: Studio PC", RefreshAct: "peers-cam-refresh:node-2", Status: "No camera selected",
				Dev:      peerSel("peers-cam-device-node-2", "Device", "", [2]string{"", "Select a camera…"}),
				Mode:     peerSel("peers-cam-mode-node-2", "Size / FPS", "", [2]string{"", "size @ fps"}),
				Start:    uiBtn{Label: "Start", Variant: "go", Act: "peers-cam-start:node-2", Val: "start"},
				PropsHdr: "Lens / image"},
		},
	}
	populated.Body.Xfer = peerXferSt{
		Show: true, NoneHint: "No transfers yet",
		Settings: xferSetSt{
			Show:      true,
			Enabled:   newToggle("Receive files", "peers-xfer-enabled", true),
			AcceptLbl: "Accept", Mode: "auto", AskLbl: "Ask", AutoLbl: "Automatic",
			Dir:        newField("Save to", "peers-xfer-dir", "D:\\incoming", "text"),
			DefaultDir: "Default: C:\\Users\\dj\\Downloads",
		},
		Pend: []xferPendSt{
			{Line: "Studio PC wants to send set.wav (2 files, 1.2 GB)",
				Btns: []uiBtn{
					{Label: "Accept", Variant: "go", Act: "xfer-accept:t1", Val: "1"},
					{Label: "Decline", Variant: "ghost", Act: "xfer-accept:t1", Val: "0"},
				}},
		},
		Rows: []xferProgSt{
			{Title: "⇩ set.wav from Studio PC", Bar: true, BarPct: progressPct(0.4237),
				BarCap: "4.2 MB / 10.0 MB · 1.1 MB/s",
				Btn:    uiBtn{Label: "Cancel", Variant: "ghost", Act: "xfer-cancel:t2"}},
			{Title: "⇧ mix.flac to Laptop", SubText: "Waiting for the peer…",
				Btn: uiBtn{Label: "Cancel", Variant: "ghost", Act: "xfer-cancel:t3"}},
			{Title: "⇧ stalled.wav to Laptop", SubText: "Interrupted - retrying (write: disk full)",
				Btn: uiBtn{Label: "Cancel", Variant: "ghost", Act: "xfer-cancel:t4"}},
			{Title: "⇩ done.wav from Studio PC", SubText: "2 files · 10.0 MB",
				IsBadge: true, Badge: "Done", BadgeVar: "success"},
			{Title: "⇩ bad.wav from Studio PC", SubText: "Failed: connection reset",
				IsBadge: true, Badge: "Error", BadgeVar: "error"},
			{Title: "⇧ gone.wav to Laptop", SubText: "Canceled",
				IsBadge: true, Badge: "canceled", BadgeVar: "secondary"},
			{Title: "⇩ zero.wav from Laptop", Bar: true, BarPct: progressPct(0),
				BarCap: "0 B / 0 B · 0 B/s",
				Btn:    uiBtn{Label: "Cancel", Variant: "ghost", Act: "xfer-cancel:t5"}},
		},
	}
	populated.Body.Discovered = peerListSt{
		Empty: "Searching for peers…",
		Rows: []peerRowSt{
			{Name: "Booth Mac", Sub: "192.168.10.42:47631",
				Btns: []uiBtn{{Label: "Pair", Variant: "primary", Act: "peer-connect:node-4"}}},
			{Name: "node-5abcd…", Sub: "[fe80::1%eth0]:47632",
				Btns: []uiBtn{{Label: "Connect", Variant: "outline", Act: "peer-connect:node-5"}}},
		},
	}
	populated.Body.Remembered = peerListSt{
		Empty: "None",
		Rows: []peerRowSt{
			{Dot: "muted", Name: "Old rig", Sub: "offline",
				Btns: []uiBtn{{Label: "Forget", Variant: "ghost", Act: "peer-forget:node-6"}}},
		},
	}

	escaping := base()
	escaping.Title = `Pe&ers <"live">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.Body.Strip = `2 & <3> "found" 'x'`
	escaping.Body.ConnsTitle = `C&onns<">`
	escaping.Body.MediaTitle = `M&edia'<>`
	escaping.Body.CamTitle = `C&am "cam"`
	escaping.Body.XferTitle = `X&fer<'>`
	escaping.Body.NetTitle = `N&et"<>`
	escaping.Body.RememberedTitle = `R&em'<">`
	escaping.Body.Banner = peerBannerSt{
		Show: true, Text: `Controlling A&B <"peer'>`,
		Btn: uiBtn{Label: `St&op "it"`, Variant: "warn", Act: `peers-control:n&"1'<>`, Val: "0"},
	}
	escaping.Body.Conns = peerListSt{
		Empty: `no &<conns>"`,
		Rows: []peerRowSt{
			{Dot: "success", Name: `A&B <"quoted'>`, Sub: `st&ate"<>'`,
				Btns:  []uiBtn{{Label: `F&orget "x"`, Variant: "ghost", Act: `peer-forget:n&"1'<>`}},
				Decks: []peerDeckSt{{Audible: true, Line: `Deck A · A&rtist - T"itle<>'`}}},
		},
	}
	escaping.Body.Media = peerMediaSt{
		Show: true, ClockLine: `Clock &tier <1> "locked"`,
		SyncLines: []string{`A&B +0.42 ms 'ok'`},
		HasTC:     true, TCLine: `TC &master "x"<>`,
		NoRoutes:  `no &routes"`,
		RoutesHdr: `R&outes: <1>`,
		Routes: []peerRouteSt{
			{Title: `→ A&B "s1"`, Detail: `d&etail<'>`, Pipe: `p&ipe "hw"<>`},
		},
		Recv: peerRecvSt{
			Show: true, Head: `R&ecv "video"`,
			Rows: []peerRecvRowSt{
				{Mark: "◂ ", Line: `c&am @ A"B'<>`,
					Btn: uiBtn{Label: `St&op'`, Variant: "destructive", Act: `media-stop:s&"1'<>`}},
				{Line: `s&rc @ A"B`, Btn: uiBtn{Label: `R&ecv"`, Variant: "go", Act: "media-recv:n&2\x1fd\"9'<>"}},
			},
		},
	}
	escaping.Body.Cam = peerCamSt{
		Show: true, Empty: `no &cams"`,
		Nodes: []camNodeSt{
			{Name: `Th&is "instance"'<>`, RefreshAct: `peers-cam-refresh:n&"1`, Status: `st&atus <"live">`,
				Dev:    peerSel(`peers-cam-device-n-1`, `D&evice"`, `C&920"`, [2]string{`C&920"`, `C&920"`}),
				Mode:   peerSel(`peers-cam-mode-n-1`, `S&ize'`, `1280x720 @ 30`, [2]string{`1280x720 @ 30`, `1280x720 @ 30`}),
				Start:  uiBtn{Label: `St&art"`, Variant: "go", Act: `peers-cam-start:n&"1`, Val: "start"},
				Sender: `se&nder <"x">`, SenderLine: `Spout sender: se&nder <"x">`, PropsHdr: `L&ens "image"`,
				Props: []camPropSt{
					{Label: `Z&oom "x"<>`, MinS: "-100", MaxS: "100", StepS: "1", ValS: "-25",
						Act: "peers-cam-prop:n&1\x1fz\"m'<>", Disabled: true, CanAuto: true, Auto: true,
						AutoAct: "peers-cam-auto:n&1\x1fz\"m'<>", AutoLbl: `A&uto"`},
				}},
		},
	}
	escaping.Body.Xfer = peerXferSt{
		Show: true, NoneHint: `no &transfers"`,
		Settings: xferSetSt{
			Show:      true,
			Enabled:   uiToggle{Label: `R&eceive "files"`, DL: `r&eceive "files"`, Act: "peers-xfer-enabled", On: true},
			AcceptLbl: `A&ccept"`, Mode: "ask", AskLbl: `A&sk'`, AutoLbl: `A&uto<">`,
			Dir:        uiField{Label: `S&ave to"`, DL: `s&ave to"`, Act: "peers-xfer-dir", Value: `D:\p&th<"x">`, Type: "text"},
			DefaultDir: `Default: C:\U&sers\"dj"`,
		},
		Pend: []xferPendSt{
			{Line: `A&B wants to send "s'et".wav`,
				Btns: []uiBtn{
					{Label: `A&ccept"`, Variant: "go", Act: `xfer-accept:t&"1'<>`, Val: "1"},
					{Label: `D&ecline'`, Variant: "ghost", Act: `xfer-accept:t&"1'<>`, Val: "0"},
				}},
		},
		Rows: []xferProgSt{
			{Title: `⇩ s&et "1".wav<>`, Bar: true, BarPct: progressPct(0.5), BarCap: `4.2 MB / 10.0 MB · 1.1 MB/s &"'<>`,
				Btn: uiBtn{Label: `C&ancel"`, Variant: "ghost", Act: `xfer-cancel:t&"2'<>`}},
			{Title: `⇧ b&ad.wav'`, SubText: `Failed: c&onn "reset"<>`,
				IsBadge: true, Badge: `E&rror"`, BadgeVar: "error"},
		},
	}
	escaping.Body.Discovered = peerListSt{
		Empty: `s&earching"`,
		Rows: []peerRowSt{
			{Name: `B&ooth <"Mac">`, Sub: `192.168.10.42:47631 &"'`,
				Btns: []uiBtn{{Label: `P&air"`, Variant: "primary", Act: `peer-connect:n&"4'<>`}}},
		},
	}
	escaping.Body.Remembered = peerListSt{
		Empty: `n&one"`,
		Rows: []peerRowSt{
			{Dot: "muted", Name: `O&ld "rig"`, Sub: `o&ffline'`,
				Btns: []uiBtn{{Label: `F&orget<>`, Variant: "ghost", Act: `peer-forget:n&"6'<>`}}},
		},
	}

	long := base()
	longS := strings.Repeat("very-long-", 120)
	long.Body.Strip = longS
	long.Body.Conns = peerListSt{
		Empty: longS,
		Rows: []peerRowSt{
			{Dot: "success", Name: longS, Sub: strings.Repeat("state-", 200),
				Btns:  []uiBtn{{Label: longS, Variant: "ghost", Act: strings.Repeat("peer-forget:n", 100)}},
				Decks: []peerDeckSt{{Audible: false, Line: longS}}},
		},
	}
	long.Body.Media = peerMediaSt{
		Show: true, ClockLine: longS,
		SyncLines: []string{longS, strings.Repeat("s", 900)},
		HasTC:     true, TCLine: longS,
		NoRoutes:  longS,
		RoutesHdr: longS,
		Routes:    []peerRouteSt{{Title: longS, Detail: strings.Repeat("d", 800), Pipe: longS}},
		Recv: peerRecvSt{Show: true, Head: longS, Rows: []peerRecvRowSt{
			{Mark: "◂ ", Line: longS, Btn: uiBtn{Label: longS, Variant: "destructive", Act: strings.Repeat("media-stop:s", 80)}},
		}},
	}
	long.Body.Cam = peerCamSt{Show: true, Empty: longS, Nodes: []camNodeSt{
		{Name: longS, RefreshAct: strings.Repeat("peers-cam-refresh:n", 40), Status: longS,
			Dev:    peerSel("peers-cam-device-n-long", longS, longS, [2]string{longS, longS}),
			Mode:   peerSel("peers-cam-mode-n-long", longS, "", [2]string{"", longS}),
			Start:  uiBtn{Label: longS, Variant: "go", Act: strings.Repeat("peers-cam-start:n", 40), Val: "start"},
			Sender: longS, SenderLine: longS, PropsHdr: longS,
			Props: []camPropSt{
				{Label: longS, MinS: "-2147483648", MaxS: "2147483647", StepS: "1", ValS: "0",
					Act: strings.Repeat("peers-cam-prop:n", 40), CanAuto: true, AutoAct: strings.Repeat("peers-cam-auto:n", 40), AutoLbl: longS},
			}},
	}}
	long.Body.Xfer = peerXferSt{Show: true, NoneHint: longS,
		Settings: xferSetSt{Show: true,
			Enabled:   newToggle(longS, "peers-xfer-enabled", true),
			AcceptLbl: longS, Mode: "auto", AskLbl: longS, AutoLbl: longS,
			Dir:        newField(longS, "peers-xfer-dir", strings.Repeat("d/", 400), "text"),
			DefaultDir: longS,
		},
		Pend: []xferPendSt{{Line: longS, Btns: []uiBtn{{Label: longS, Variant: "go", Act: strings.Repeat("xfer-accept:t", 60), Val: "1"}}}},
		Rows: []xferProgSt{{Title: longS, Bar: true, BarPct: progressPct(0.999999), BarCap: longS,
			Btn: uiBtn{Label: longS, Variant: "ghost", Act: strings.Repeat("xfer-cancel:t", 60)}}},
	}
	long.Body.Discovered = peerListSt{Empty: longS, Rows: []peerRowSt{
		{Name: longS, Sub: strings.Repeat("[fe80::1%eth0]:47632 ", 50),
			Btns: []uiBtn{{Label: longS, Variant: "primary", Act: strings.Repeat("peer-connect:n", 80)}}},
	}}
	long.Body.Remembered = peerListSt{Empty: longS}

	unicode := base()
	unicode.Title = "ピア 🎧"
	unicode.Sub = "größer Пиры"
	unicode.Body.Strip = "2 подключено · 3 найдено · узел ab12cd34ef…"
	unicode.Body.ConnsTitle = "Соединения"
	unicode.Body.MediaTitle = "メディア"
	unicode.Body.CamTitle = "Камера"
	unicode.Body.XferTitle = "ファイル転送"
	unicode.Body.NetTitle = "У цій мережі"
	unicode.Body.RememberedTitle = "запомненные"
	unicode.Body.Banner = peerBannerSt{Show: true, Text: "Управление Студія 🎛️",
		Btn: uiBtn{Label: "止める", Variant: "warn", Act: "peers-control:узел☂", Val: "0"}}
	unicode.Body.Conns = peerListSt{Empty: "нет соединений", Rows: []peerRowSt{
		{Dot: "success", Name: "Студія 中文 🎛️", Sub: "подключено · спарено",
			Btns:  []uiBtn{{Label: "забути", Variant: "ghost", Act: "peer-forget:узел☂"}},
			Decks: []peerDeckSt{{Audible: true, Line: "Дек A · Артист - Трек  (128 BPM)"}}},
	}}
	unicode.Body.Media = peerMediaSt{Show: true,
		ClockLine: "Часы уровень 1 · захвачено · смещение −0.42 ms",
		SyncLines: []string{"Студія +0.42 ms · rtt 1.20 ms · захвачено"},
		HasTC:     true, TCLine: "TC мастер: この端末 · 01:02:03:04 @29.97df",
		NoRoutes: "нет активных маршрутов", RoutesHdr: "Маршруты: 1",
		Routes: []peerRouteSt{{Title: "Отправка → Студія", Detail: "потери 0 · дрожание 0.31 ms", Pipe: "h264_nvenc уровень 2 · 6.2 Mbps"}},
		Recv: peerRecvSt{Show: true, Head: "Приём видео", Rows: []peerRecvRowSt{
			{Mark: "◂ ", Line: "камера @ Студія", Btn: uiBtn{Label: "止める", Variant: "destructive", Act: "media-stop:сесія☂"}},
		}},
	}
	unicode.Body.Cam = peerCamSt{Show: true, Empty: "нет камер", Nodes: []camNodeSt{
		{Name: "Этот экземпляр", RefreshAct: "peers-cam-refresh:узел☂", Status: "В эфире 1280x720 @ 30",
			Dev:    peerSel("peers-cam-device-node-u", "Устройство", "Камера☂", [2]string{"Камера☂", "Камера☂"}),
			Mode:   peerSel("peers-cam-mode-node-u", "サイズ / FPS", "1280x720 @ 30", [2]string{"1280x720 @ 30", "1280x720 @ 30"}),
			Start:  uiBtn{Label: "остановить", Variant: "warn", Act: "peers-cam-start:узел☂", Val: "stop"},
			Sender: "rave-камера", SenderLine: "Spout sender: rave-камера", PropsHdr: "Объектив / изображение",
			Props: []camPropSt{{Label: "Зум 🔍", MinS: "0", MaxS: "500", StepS: "5", ValS: "120",
				Act: "peers-cam-prop:узел☂\x1fзум", CanAuto: true, AutoAct: "peers-cam-auto:узел☂\x1fзум", AutoLbl: "Авто"}}},
	}}
	unicode.Body.Xfer = peerXferSt{Show: true, NoneHint: "переводов пока нет",
		Settings: xferSetSt{Show: true,
			Enabled:   newToggle("Приймати файли", "peers-xfer-enabled", true),
			AcceptLbl: "受け入れる", Mode: "auto", AskLbl: "спросить", AutoLbl: "автоматически",
			Dir:        newField("Сохранить в", "peers-xfer-dir", "D:\\Музыка\\входящие", "text"),
			DefaultDir: "По умолчанию: C:\\Загрузки",
		},
		Rows: []xferProgSt{{Title: "⇩ сет.wav от Студія", Bar: true, BarPct: progressPct(0.25),
			BarCap: "4.2 МБ / 10.0 МБ · 1.1 МБ/с", Btn: uiBtn{Label: "Скасувати", Variant: "ghost", Act: "xfer-cancel:т☂"}}},
	}
	unicode.Body.Discovered = peerListSt{Empty: "поиск…", Rows: []peerRowSt{
		{Name: "Буд 麦克", Sub: "192.168.10.42:47631", Btns: []uiBtn{{Label: "Спарувати", Variant: "primary", Act: "peer-connect:узел☂2"}}},
	}}
	unicode.Body.Remembered = peerListSt{Empty: "нет", Rows: []peerRowSt{
		{Dot: "muted", Name: "Старий риг", Sub: "офлайн", Btns: []uiBtn{{Label: "забути", Variant: "ghost", Act: "peer-forget:узел☂3"}}},
	}}

	return map[string]peersSt{
		"unavailable": unavailable,
		"empty":       empty,
		"gated":       gated,
		"populated":   populated,
		"escaping":    escaping,
		"long":        long,
		"unicode":     unicode,
	}
}

func TestZigPeersGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range peersFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderPeers(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", peersHTML(st), zig)

			bjs := stateJSON(st.Body)
			if bjs == nil {
				t.Fatal("body marshal failed")
			}
			zigBody, ok := zigui.RenderPeersBody(bjs)
			if !ok {
				t.Fatal("zig body render failed")
			}
			assertBytesEqual(t, "body", peersBodyHTML(st.Body), zigBody)
		})
	}
}
