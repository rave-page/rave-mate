//go:build !windows

package i18n

// osLocale returns the OS UI locale from env vars (Unix). "" → caller uses en.
func osLocale() string { return envLocale() }
