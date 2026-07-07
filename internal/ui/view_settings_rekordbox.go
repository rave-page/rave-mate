package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/rekordboxdb"
	"rave.page/mate/internal/session"
)

// proDjLinkCard toggles the passive Pro DJ Link listener (Pioneer CDJ/XDJ now-playing).
func (u *UI) proDjLinkCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.ProDJLink
	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled {
			s.set(colMuted, "off")
			return
		}
		src, ok := u.sourceInfo(session.SourceProDJLink)
		switch {
		case ok && src.Receiving:
			s.set(colBrandMint, "receiving CDJ status")
		case ok && src.Running:
			s.set(colBrandMint, "listening :50002")
		default:
			s.set(colBrandAmber, "not running")
		}
	})
	body := container.NewVBox(
		mutedLabel("Passively reads Pioneer CDJ/XDJ status broadcasts on the LAN (UDP 50002) - live BPM + play state per deck. No virtual device is announced (read-only). If it can't start, Rekordbox itself may be holding the port - close it."),
	)
	return featureCard("Pro DJ Link (Pioneer CDJ/XDJ)", "Live now-playing from networked Pioneer players.", u.sessionToggle(&f.Enabled), st, body)
}

// rekordboxKeyCard manages the master.db SQLCipher key for newer Rekordbox (6.6.5+/7) and
// offers a one-click extraction via pyrekordbox, plus the manual commands + docs link.
func (u *UI) rekordboxKeyCard() fyne.CanvasObject {
	keyEntry := newEntry()
	keyEntry.SetPlaceHolder("paste your master.db key (long hex string)")
	keyEntry.Password = true

	// One-time decrypt probe (cheap: page-1 only) so the status reflects whether the DB
	// actually unlocks - the default key already works for most current Rekordbox installs.
	var probe atomic.Int32 // 0 unknown, 1 unlocked, 2 locked
	dbs := rekordboxdb.DiscoverRekordboxMasterDB()
	runProbe := func() {
		if len(dbs) == 0 {
			return
		}
		go func() {
			defer debuglog.Recover(u.svc.Log, "rbkey-probe", false)
			if err := rekordboxdb.Probe(dbs[0]); err == nil {
				probe.Store(1)
			} else {
				probe.Store(2)
			}
		}()
	}
	runProbe()
	st := u.newStatus(func(s *cardStatus) {
		switch {
		case len(dbs) == 0:
			s.set(colMuted, "no master.db found")
		case probe.Load() == 1:
			s.set(colBrandMint, "unlocked ✓")
		case probe.Load() == 2:
			s.set(colBrandAmber, "needs key (RB version)")
		default:
			s.set(colMuted, "checking…")
		}
	})

	save := widget.NewButtonWithIcon("Save key", theme.DocumentSaveIcon(), func() {
		if err := rekordboxdb.SaveKey(keyEntry.Text); err != nil {
			u.Notify("rave-mate", "Save key: "+err.Error())
			return
		}
		u.Notify("rave-mate", "Rekordbox key saved.")
		probe.Store(0)
		runProbe()
	})
	save.Importance = widget.HighImportance

	test := widget.NewButtonWithIcon("Test", theme.ConfirmIcon(), func() {
		go func() {
			defer debuglog.Recover(u.svc.Log, "rbkey-test", false)
			dbs := rekordboxdb.DiscoverRekordboxMasterDB()
			if len(dbs) == 0 {
				u.Notify("rave-mate", "No master.db found.")
				return
			}
			lib, err := rekordboxdb.Open(dbs[0], "")
			if err != nil {
				u.Notify("rave-mate", "master.db: "+err.Error())
				return
			}
			u.Notify("rave-mate", fmt.Sprintf("master.db OK - %d tracks, %d sessions.", len(lib.Tracks), len(lib.Sessions)))
		}()
	})

	auto := widget.NewButtonWithIcon("Extract with pyrekordbox", theme.SearchIcon(), func() {
		go func() {
			defer debuglog.Recover(u.svc.Log, "rbkey-extract", false)
			u.Notify("rave-mate", "Running pyrekordbox to extract the key…")
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			key, err := extractRekordboxKey(ctx, u.svc.Log)
			if err != nil {
				u.Notify("rave-mate", "pyrekordbox: "+err.Error())
				return
			}
			if err := rekordboxdb.SaveKey(key); err != nil {
				u.Notify("rave-mate", "Save: "+err.Error())
				return
			}
			fyne.Do(func() { keyEntry.SetText(key) })
			probe.Store(0)
			runProbe()
			u.Notify("rave-mate", "Key extracted + saved. Click Test, or re-import master.db.")
		}()
	})

	cmds := "pip install pyrekordbox\n" +
		"python -c \"from pyrekordbox.db6.database import deobfuscate, BLOB; print(deobfuscate(BLOB))\""
	cmdLbl := widget.NewLabelWithStyle(cmds, fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	cmdLbl.Wrapping = fyne.TextWrapWord
	copyBtn := widget.NewButtonWithIcon("Copy commands", theme.ContentCopyIcon(), func() {
		fyne.CurrentApp().Clipboard().SetContent(cmds)
		u.Notify("rave-mate", "Commands copied.")
	})
	docs := widget.NewHyperlink("pyrekordbox docs", mustURL("https://pyrekordbox.readthedocs.io/en/stable/installation.html"))

	body := container.NewVBox(
		mutedLabel("master.db is the live Rekordbox library + play history (with timestamps). The built-in key unlocks most current Rekordbox installs automatically - click Test to check. Only supply a key if it shows “needs key”."),
		mutedLabel("If a future Rekordbox rotates its key: “Extract with pyrekordbox” installs + runs the tool to recover it (needs Python 3.8+). Or run it manually + paste the key:"),
		cmdLbl,
		container.NewHBox(auto, copyBtn, docs),
		container.NewBorder(nil, nil, nil, container.NewHBox(save, test), keyEntry),
		mutedLabel("The key is stored owner-only at <config>/rave-mate/rekordbox.key. You can also set the RAVE_REKORDBOX_KEY env var."),
	)
	return featureCard("Rekordbox master.db key", "Unlock the live Rekordbox library + play history.", nil, st, body)
}

// keyExtractSnippet recovers the master.db key WITHOUT SQLCipher: pyrekordbox's deobfuscate(BLOB)
// returns the bundled key directly (newer pyrekordbox carries the current key). Falls back to
// opening the DB (needs sqlcipher3) for older pyrekordbox.
const keyExtractSnippet = `import sys
key=""
try:
    from pyrekordbox.db6.database import deobfuscate, BLOB
    key=deobfuscate(BLOB) or ""
except Exception as e:
    sys.stderr.write("deobfuscate: "+repr(e)+"\n")
if not key:
    for cls in ("Rekordbox6Database","MasterDatabase"):
        if key: break
        try:
            m=__import__("pyrekordbox",fromlist=[cls]); key=getattr(getattr(m,cls)(),"key","") or ""
        except Exception as e:
            sys.stderr.write(cls+": "+repr(e)+"\n")
sys.stdout.write((key or "").strip())`

// extractRekordboxKey finds a local Python, ensures pyrekordbox is installed, and runs it to
// recover the master.db key. Every step is logged (so "check logs" is useful).
func extractRekordboxKey(ctx context.Context, log *logbus.Bus) (string, error) {
	py := findPython()
	if py == "" {
		log.Warn("rbkey", "no python interpreter found on PATH", nil)
		return "", fmt.Errorf("Python not found - install Python 3.8+ (python.org), reopen rave-mate, then retry")
	}
	log.Info("rbkey", "using python", map[string]any{"exe": py})

	run := func(args ...string) (string, string, error) {
		cmd := exec.CommandContext(ctx, py, args...)
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return strings.TrimSpace(out.String()), strings.TrimSpace(errb.String()), err
	}

	extract := func() (string, error) {
		out, errb, err := run("-c", keyExtractSnippet)
		if key := strings.TrimSpace(out); len(key) >= 16 {
			return key, nil
		}
		log.Warn("rbkey", "extract returned no key", map[string]any{"stderr": tail(errb, 400), "err": errStr(err)})
		return "", fmt.Errorf("no key")
	}

	if key, err := extract(); err == nil {
		log.Info("rbkey", "key extracted", map[string]any{"len": len(key)})
		return key, nil
	}
	// pyrekordbox likely missing → install it, then retry once.
	log.Info("rbkey", "installing pyrekordbox", nil)
	if out, errb, err := run("-m", "pip", "install", "--user", "pyrekordbox"); err != nil {
		log.Warn("rbkey", "pip install failed", map[string]any{"stderr": tail(errb+out, 400), "err": errStr(err)})
		return "", fmt.Errorf("couldn't install pyrekordbox (pip). Run manually: %s -m pip install pyrekordbox", py)
	}
	key, err := extract()
	if err != nil {
		return "", fmt.Errorf("pyrekordbox installed but no key returned - see logs")
	}
	log.Info("rbkey", "key extracted after install", map[string]any{"len": len(key)})
	return key, nil
}

// findPython returns a usable Python interpreter, preferring the py launcher + real installs
// over the Microsoft Store stub (which just opens the Store and never runs).
func findPython() string {
	for _, c := range []string{"py", "python", "python3"} {
		p, err := exec.LookPath(c)
		if err != nil || isStoreStub(p) {
			continue
		}
		return p
	}
	return ""
}

func isStoreStub(p string) bool {
	return strings.Contains(strings.ToLower(p), `microsoft\windowsapps`)
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
