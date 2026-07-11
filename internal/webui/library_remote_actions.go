package webui

// Library remote-target switch. Selecting a peer flips the shared control target: the Library
// tab becomes the live mirror of that peer (library_mirror.go), the Publish tab its remote
// cockpit. Selecting "This computer" ends the mirror session.

import "strings"

func init() {
	onPrefix("lib-target:", func(u *UI, m actMsg) { u.libSetTarget(strings.TrimPrefix(m.Act, "lib-target:")) })
}

// libSetTarget flips the shared control target and re-renders the tab (empty = this computer).
// Ends any live mirror session and resets the publish remote cache + selection so the Publish
// tab agrees on the target.
func (u *UI) libSetTarget(t string) {
	u.mirrorShutdown()
	u.mu.Lock()
	u.remoteTarget = t
	u.mu.Unlock()
	ps := u.pubR()
	ps.mu.Lock()
	ps.resetFor(t)
	ps.mu.Unlock()
	u.pubSetSel("")
	u.patchMain() // a peer target re-renders lib-body as the mirror (opens the session)
}
