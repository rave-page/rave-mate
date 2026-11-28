package videoshare

// frame.go - a sender's frame counter, read straight from Spout 2.007's named semaphore
// "<sender>_Count_Semaphore" whose COUNT is the sender's frame number (SpoutFrameCount.cpp: created
// with count 1, Wait(0) + Release(2) per sent frame). No SpoutLibrary.dll call: the SPOUTLIBRARY
// GetSenderFrame slot is vtable-skewed against the shipped DLL and returned junk (see
// .devnotes/SPOUT_RECV_BLACK_P0.md); the semaphore is the SDK's own source of truth, and the
// zero-copy encoder child (native/zigenc cap.FrameCounter) reads the same object.
//
// Absent (registry "Framecount" off, pre-2.007 or non-SDK sender) = ok false; consumers fail OPEN
// (treat every tick as new) rather than gate on a missing signal.

// SenderFrame reports a sender's frame counter. fps is always 0: the caller derives the rate from
// two readings (the counter is the only trustworthy input).
func SenderFrame(name string) (frame int64, fps float64, ok bool) {
	f, got := senderFrameCount(name)
	return f, 0, got
}

// FrameCountUsable reports whether a sender publishes a usable (non-negative) counter.
func FrameCountUsable(frame int64, ok bool) bool { return ok && frame >= 0 }
