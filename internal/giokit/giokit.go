// Package giokit is the Gio (gioui.org) port of the rave.page corporate identity - the
// porting unit for the incremental Fyne→Gio migration (media/streams/graphics surfaces
// first; see GIO_MIGRATION.md). It provides the brand theme, DENSITY-FIRST widgets
// (~24–26px controls, 4–6px padding, 12–13sp text - the point of the migration), a
// window host that runs a Gio event loop off the main thread, and a control-plane
// Registry so ctl snapshot/tap parity can be wired for Gio surfaces.
//
// Gio needs no cgo on Windows (pure-Go d3d11 backend), so giokit compiles in every
// build flavor including the workers.
package giokit
