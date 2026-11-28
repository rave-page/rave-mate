//go:build !windows || !encembed

package senderkeeper

// No embedded keeper in this build; exePath falls back to the sidecar beside rave-mate.exe.
var embeddedKeeper []byte
