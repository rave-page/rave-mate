// Package logbus re-exports rave.page/mate/internal/shared/logbus so the existing
// rave.page/mate/internal/logbus import path (56+ call sites) keeps working while the
// implementation lives in shared, reused by rave-app. New code may import shared/logbus
// directly. Type aliases mean *Bus / Entry / Level interoperate transparently across the two
// import paths.
package logbus

import slog "rave.page/mate/internal/shared/logbus"

// Level + its values (aliased so comparisons + switches keep compiling).
type Level = slog.Level

const (
	Debug = slog.Debug
	Info  = slog.Info
	Warn  = slog.Warn
	Error = slog.Error
)

// Entry + Bus are the shared types.
type (
	Entry = slog.Entry
	Bus   = slog.Bus
)

// New returns a Bus retaining the last capacity entries.
func New(capacity int) *Bus { return slog.New(capacity) }
