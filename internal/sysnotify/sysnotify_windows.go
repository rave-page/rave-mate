//go:build windows

package sysnotify

import (
	"context"
	"os"
	"os/exec"
	"time"

	"rave.page/mate/internal/sysexec"
)

// PowerShell WinRT toast - the fallback used only when no native override (tray balloon) is
// registered. NOTE: a Windows toast fired without a registered AppUserModelID (AUMID) may silently
// not display; the tray balloon path wired via SetNative is the reliable one. Title/body come in
// through env vars (RM_NOTIFY_*), never string-interpolated into the script, to avoid injection.
const psToast = `try {
[Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime] | Out-Null
$tpl=[Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$txt=$tpl.GetElementsByTagName('text')
$txt.Item(0).AppendChild($tpl.CreateTextNode($env:RM_NOTIFY_TITLE)) | Out-Null
$txt.Item(1).AppendChild($tpl.CreateTextNode($env:RM_NOTIFY_BODY)) | Out-Null
$n=[Windows.UI.Notifications.ToastNotification]::new($tpl)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('rave-mate').Show($n)
} catch {}`

// osSend fires a hidden, low-priority PowerShell toast (best-effort, ~5s cap).
func osSend(title, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", psToast)
	c.Env = append(os.Environ(), "RM_NOTIFY_TITLE="+title, "RM_NOTIFY_BODY="+body)
	sysexec.Hide(c)
	sysexec.LowPriority(c)
	return c.Run()
}
