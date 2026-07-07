// Package mpvembed hosts mpv's video output INSIDE the app window instead of a separate popout.
//
// mpv keeps its full GPU decode→present path (decoding frames into Fyne ourselves stutters - a
// proven dead end). To place that GPU surface inline in the app, we create a native child window
// parented into the Fyne main window and pass its handle to mpv via `--wid`; mpv reparents its
// output under the host and fills it. The host is then moved/resized to track a Fyne placeholder
// widget's on-screen rect (see internal/ui player embed) and hidden when off-screen.
//
// Windows-only: `--wid` foreign-window embedding is a Windows/X11 feature and the child-window
// plumbing here is Win32. On other platforms Supported() is false and the caller falls back to the
// popout window - never a crash.
package mpvembed
