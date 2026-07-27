package videoshare

// frame.go - a sender's frame counter: PERMANENTLY UNAVAILABLE on this SDK pairing.
//
// Increment 3 added this over SPOUTLIBRARY's GetSenderFrame and measured it returning constant junk
// (a fixed negative number, with GetSenderFps reporting the MONITOR's refresh rate). The black-frame
// P0 found the reason: the vendored SpoutLibrary.h and the shipped SpoutLibrary.dll disagree about
// vtable slot order across a window covering the whole receiver block, so GetSenderFrame was never
// actually being called. See .devnotes/SPOUT_RECV_BLACK_P0.md.
//
// The implementation is gone rather than "fixed": nothing in that window can be trusted, and a
// plausible-looking number is worse than no number. The declaration stays so callers compile and get
// an honest "unavailable" instead of junk. Restoring it needs a header that MATCHES the DLL - a
// supply-chain change (SUPPLY_CHAIN.md), not a code change.

// SenderFrame reports a sender's frame counter. ok is ALWAYS false on this SDK pairing; consumers
// must fail OPEN (treat every tick as new) rather than gate on a missing signal.
func SenderFrame(string) (frame int64, fps float64, ok bool) { return 0, 0, false }

// FrameCountUsable reports whether a sender publishes a usable (non-negative) counter.
func FrameCountUsable(frame int64, ok bool) bool { return ok && frame >= 0 }
