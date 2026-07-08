//go:build windows

package sysexec

import "golang.org/x/sys/windows"

// SetSelfBelowNormal sets THIS process's priority class: BELOW_NORMAL when below is true, NORMAL
// otherwise. Best-effort (ignores errors). The activity governor calls this so rave-mate yields the
// CPU to a live encoder / the user's foreground app whenever it isn't the thing being looked at.
// Lowering the whole process (not individual threads) covers every worker goroutine at once.
func SetSelfBelowNormal(below bool) {
	class := uint32(normalPriorityClass)
	if below {
		class = belowNormalPriorityClass
	}
	_ = windows.SetPriorityClass(windows.CurrentProcess(), class)
}
