//go:build !windows

package auth

// RegisterScheme is a no-op off Windows for now. Linux needs a .desktop file with a
// MimeType=x-scheme-handler/ravepage entry + update-desktop-database; macOS needs the
// scheme in Info.plist (CFBundleURLTypes). Both are documented follow-ups.
func (m *Manager) RegisterScheme() error {
	m.log.Info(source, "scheme registration not implemented on this platform", nil)
	return nil
}
