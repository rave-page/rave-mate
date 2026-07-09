package webui

import (
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/musiclib"
)

// Backup management: list/locate/open/restore library backups (Traktor collection snapshots under
// <config>/library-backups) AND rave-mate's own settings (config.json snapshots under
// <config>/settings-backups). Restores overwrite the live file, so the current version is always
// backed up first + a confirm is required. Never deletes the user's real DJ data.

func init() {
	onExact("lib-backups", func(u *UI, _ actMsg) { u.libBackupsModal() })
	onExact("lib-bk-create", func(u *UI, _ actMsg) { u.libBackupTraktor() })
	onExact("lib-bk-settings", func(u *UI, _ actMsg) { u.libBackupSettings() })
	onPrefix("lib-bk-open:", func(u *UI, m actMsg) { _ = openURL(m.arg("lib-bk-open:")) })
	onPrefix("lib-bk-restore:", func(u *UI, m actMsg) { u.libBackupRestoreConfirm(m.arg("lib-bk-restore:"), false) })
	onPrefix("lib-bk-restore-do:", func(u *UI, m actMsg) { u.libBackupRestore(m.arg("lib-bk-restore-do:")) })
	onPrefix("lib-bk-setrestore:", func(u *UI, m actMsg) { u.libBackupRestoreConfirm(m.arg("lib-bk-setrestore:"), true) })
	onPrefix("lib-bk-setrestore-do:", func(u *UI, m actMsg) { u.libBackupRestoreSettings(m.arg("lib-bk-setrestore-do:")) })
}

func libBackupRoot() string      { p, _ := config.DataPath("library-backups"); return p }
func settingsBackupRoot() string { p, _ := config.DataPath("settings-backups"); return p }

func (u *UI) libBackupsModal() {
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + html.EscapeString(i18n.T("library.backups.desc")) + `</p>`)
	b.WriteString(btnRow(
		btn(i18n.T("library.backups.createTraktor"), "primary", "lib-bk-create", ""),
		btn(i18n.T("library.backups.createSettings"), "outline", "lib-bk-settings", "")))

	// library backups (Traktor collection snapshot dirs)
	b.WriteString(section(i18n.T("library.backups.libraryBackups"), ""))
	backups, _ := musiclib.ListBackups(libBackupRoot())
	if len(backups) == 0 {
		b.WriteString(emptyState(i18n.T("library.backups.none")))
	} else {
		b.WriteString(`<div class="rp-card">`)
		for _, bk := range backups {
			sub := bk.When.Local().Format("2006-01-02 15:04") + " · " + humanBytes(uint64(bk.SizeBytes))
			b.WriteString(itemRow(filepath.Base(bk.Path), sub,
				btn(i18n.T("library.backups.open"), "outline", "lib-bk-open:"+bk.Path, ""),
				btn(i18n.T("library.backups.restore"), "ghost", "lib-bk-restore:"+bk.Path, "")))
		}
		b.WriteString(`</div>`)
	}

	// rave-mate settings backups
	b.WriteString(section(i18n.T("library.backups.settingsBackups"), ""))
	sb := listSettingsBackups()
	if len(sb) == 0 {
		b.WriteString(emptyState(i18n.T("library.backups.none")))
	} else {
		b.WriteString(`<div class="rp-card">`)
		for _, f := range sb {
			sub := f.when.Local().Format("2006-01-02 15:04") + " · " + humanBytes(uint64(f.size))
			b.WriteString(itemRow(filepath.Base(f.path), sub,
				btn(i18n.T("library.backups.open"), "outline", "lib-bk-open:"+filepath.Dir(f.path), ""),
				btn(i18n.T("library.backups.restore"), "ghost", "lib-bk-setrestore:"+f.path, "")))
		}
		b.WriteString(`</div>`)
	}
	u.openModal(modal(i18n.T("library.backups.title"), b.String(), ""))
}

// libBackupTraktor snapshots the live Traktor collection into <config>/library-backups.
func (u *UI) libBackupTraktor() {
	u.bg(func() {
		installs, err := musiclib.DiscoverTraktor()
		if err != nil || len(installs) == 0 || installs[0].Collection == "" {
			u.toast(i18n.T("library.toast.noTraktorBackup"))
			return
		}
		bk, berr := musiclib.BackupCollection(installs[0], libBackupRoot())
		if berr != nil {
			u.toast(i18n.T("library.backups.createFailed") + berr.Error())
			return
		}
		u.toast(i18n.T("library.backups.created") + ": " + filepath.Base(bk.Path))
		u.libBackupsModal()
	})
}

// libBackupSettings copies the live config.json into <config>/settings-backups.
func (u *UI) libBackupSettings() {
	src, err := config.DataPath("config.json")
	if err != nil {
		u.toast(i18n.T("library.backups.createFailed") + err.Error())
		return
	}
	dir := settingsBackupRoot()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		u.toast(i18n.T("library.backups.createFailed") + err.Error())
		return
	}
	dst := filepath.Join(dir, "config-"+time.Now().Format("20060102-150405")+".json")
	if err := copyFileWeb(src, dst); err != nil {
		u.toast(i18n.T("library.backups.createFailed") + err.Error())
		return
	}
	u.toast(i18n.T("library.backups.created") + ": " + filepath.Base(dst))
	u.libBackupsModal()
}

// libBackupRestoreConfirm asks before overwriting a live file. settings=true → config.json restore.
func (u *UI) libBackupRestoreConfirm(path string, settings bool) {
	msg := i18n.T("library.backups.restoreConfirm")
	doAct := "lib-bk-restore-do:" + path
	if settings {
		msg = i18n.T("library.backups.restoreConfirmSettings")
		doAct = "lib-bk-setrestore-do:" + path
	}
	body := `<p class=page-sub>` + html.EscapeString(msg) + `</p>` +
		btnRow(btn(i18n.T("library.backups.restore"), "destructive", doAct, ""), btn(i18n.T("common.cancel"), "outline", "lib-backups", ""))
	u.openModal(modal(i18n.T("library.backups.restore"), body, ""))
}

// libBackupRestore copies a Traktor backup's collection.nml back over the live collection, backing
// the current one up first. The backup dir + all source files are never modified.
func (u *UI) libBackupRestore(backupDir string) {
	u.closeModal()
	u.bg(func() {
		nml := findNML(backupDir)
		if nml == "" {
			u.toast(i18n.T("library.backups.restoreFailed") + "no .nml in backup")
			return
		}
		installs, err := musiclib.DiscoverTraktor()
		if err != nil || len(installs) == 0 || installs[0].Collection == "" {
			u.toast(i18n.T("library.backups.noDest"))
			return
		}
		dest := installs[0].Collection
		// safety: snapshot the current live collection before overwriting it
		if _, berr := musiclib.BackupCollection(installs[0], libBackupRoot()); berr != nil {
			u.toast(i18n.T("library.backups.restoreFailed") + berr.Error())
			return
		}
		if err := copyFileWeb(nml, dest); err != nil {
			u.toast(i18n.T("library.backups.restoreFailed") + err.Error())
			return
		}
		u.toast(i18n.T("library.backups.restored"))
	})
}

// libBackupRestoreSettings copies a settings backup over the live config.json (applied on restart).
func (u *UI) libBackupRestoreSettings(path string) {
	u.closeModal()
	dst, err := config.DataPath("config.json")
	if err != nil {
		u.toast(i18n.T("library.backups.restoreFailed") + err.Error())
		return
	}
	// back the current config up first so the restore itself is reversible
	if bdir := settingsBackupRoot(); bdir != "" {
		_ = os.MkdirAll(bdir, 0o755)
		_ = copyFileWeb(dst, filepath.Join(bdir, "config-preRestore-"+time.Now().Format("20060102-150405")+".json"))
	}
	if err := copyFileWeb(path, dst); err != nil {
		u.toast(i18n.T("library.backups.restoreFailed") + err.Error())
		return
	}
	u.toast(i18n.T("library.backups.settingsRestored"))
}

// ── helpers ──

type settingsBackup struct {
	path string
	when time.Time
	size int64
}

func listSettingsBackups() []settingsBackup {
	dir := settingsBackupRoot()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []settingsBackup
	for _, e := range ents {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "config-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, settingsBackup{filepath.Join(dir, e.Name()), fi.ModTime(), fi.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].when.After(out[j].when) })
	return out
}

// findNML returns the first .nml directly under dir ("" if none).
func findNML(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".nml") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// copyFileWeb stream-copies src→dst (atomic-ish via temp+rename), fsync'd.
func copyFileWeb(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
