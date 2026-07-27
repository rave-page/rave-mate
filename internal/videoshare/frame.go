package videoshare

// frame.go - a sender's FRAME COUNTER, read metadata-only (zigmedia increment 3).
//
// Spout bumps a sender's frame counter inside SendTexture/SendImage, so a counter that does not
// move means the sender published nothing new. A zero-copy capture session paces BLIND (design §6)
// and re-encodes whatever the texture currently holds, which on a static sender is a duplicate
// frame; this is the cheap signal that tells the two apart. Reading it costs one shared-memory
// read - no OpenGL context, no receiver binding, no pixel transfer - which is exactly why it does
// not re-introduce the readback object the zero-copy path removed.

// SenderFrame returns a sender's frame counter and its measured fps.
//
// ok=false = no backend / unknown sender. A frame counter < 0 means the SENDING app disabled frame
// counting (Spout's SetFrameCount(false) / DisableFrameCount), i.e. the signal is unavailable and
// every consumer must fail OPEN - treat every tick as new, never freeze a stream because a
// counter is missing.
func SenderFrame(name string) (frame int64, fps float64, ok bool) { return senderFrame(name) }

// FrameCountUsable reports whether a sender publishes a usable (non-negative, moving) counter.
func FrameCountUsable(frame int64, ok bool) bool { return ok && frame >= 0 }
