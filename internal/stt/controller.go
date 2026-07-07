package stt

import (
	"context"
	"sync"
)

// Controller drives dictation from the keybinds (record/send/discard) and delivers each finished
// transcript to onText (→ Twitch chat). One session at a time. In auto-submit mode (Options.
// AutoSilence>0) a session ends itself on silence and the transcript is delivered automatically;
// in manual mode it ends on Send (or Discard drops it).
type Controller struct {
	opts   func() Options  // current settings (device/model/auto-submit) per session
	onText func(string)    // deliver a finished transcript
	onStat func(string)    // optional status line for the UI/logs (may be nil)
	bg     context.Context // lifetime ctx for sessions

	mu       sync.Mutex
	active   *Session
	lastText string       // last finished transcript (preview + clipboard); "" until first result
	onClip   func(string) // OS clipboard setter, wired from the UI (keeps this pkg UI-free); nil = unsupported
	onUpdate func(string) // fires on lastText change (desktop preview + future VR overlay); may be nil
}

// NewController builds a controller. bg bounds session lifetime (cancel on shutdown). onStat may
// be nil.
func NewController(bg context.Context, opts func() Options, onText func(string), onStat func(string)) *Controller {
	return &Controller{opts: opts, onText: onText, onStat: onStat, bg: bg}
}

// SetClipboard wires the OS clipboard setter (from the Fyne UI layer; keeps this pkg UI-free).
func (c *Controller) SetClipboard(fn func(string)) {
	c.mu.Lock()
	c.onClip = fn
	c.mu.Unlock()
}

// SetOnUpdate registers a callback fired whenever the last transcript changes (desktop preview;
// future VR overlay). Fired with "" on Clear.
func (c *Controller) SetOnUpdate(fn func(string)) {
	c.mu.Lock()
	c.onUpdate = fn
	c.mu.Unlock()
}

// LastTranscript returns the most recent finished transcript ("" if none yet) - for preview UIs.
func (c *Controller) LastTranscript() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastText
}

// setLast records the last transcript and notifies any update listener (outside the lock).
func (c *Controller) setLast(s string) {
	c.mu.Lock()
	c.lastText = s
	fn := c.onUpdate
	c.mu.Unlock()
	if fn != nil {
		fn(s)
	}
}

// CopyToClipboard copies the last transcript to the OS clipboard via the UI-wired setter. Returns
// false if there's nothing to copy or no clipboard setter is wired.
func (c *Controller) CopyToClipboard() bool {
	c.mu.Lock()
	t, fn := c.lastText, c.onClip
	c.mu.Unlock()
	if t == "" || fn == nil {
		c.stat("clipboard: nothing to copy")
		return false
	}
	fn(t)
	c.stat("copied to clipboard: " + t)
	return true
}

// SendLast posts the last transcript to chat (preview "send"). No-op if empty.
func (c *Controller) SendLast() {
	c.mu.Lock()
	t := c.lastText
	c.mu.Unlock()
	if t == "" {
		return
	}
	c.onText(t)
	c.stat("sent: " + t)
}

// Clear discards any in-flight dictation and clears the last transcript (preview "clear / retry").
// After Clear the controller is idle - re-arm with Toggle / the record keybind.
func (c *Controller) Clear() {
	c.Discard()
	c.setLast("")
}

func (c *Controller) stat(s string) {
	if c.onStat != nil {
		c.onStat(s)
	}
}

// Active reports whether a dictation is in progress.
func (c *Controller) Active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active != nil
}

// Toggle starts dictation, or - if one is running - finishes + sends it (push-to-talk toggle).
func (c *Controller) Toggle() {
	c.mu.Lock()
	running := c.active != nil
	c.mu.Unlock()
	if running {
		c.Send()
		return
	}
	c.start()
}

// Send finishes the active session and sends its transcript.
func (c *Controller) Send() {
	c.mu.Lock()
	s := c.active
	c.mu.Unlock()
	if s != nil {
		s.Stop()
	}
}

// Discard finishes the active session and drops it.
func (c *Controller) Discard() {
	c.mu.Lock()
	s := c.active
	c.mu.Unlock()
	if s != nil {
		s.Discard()
		c.stat("dictation discarded")
	}
}

// start begins a session + a watcher that delivers (or drops) the result and clears active.
func (c *Controller) start() {
	s, err := StartSession(c.bg, c.opts())
	if err != nil {
		c.stat("dictation failed: " + err.Error())
		return
	}
	c.mu.Lock()
	c.active = s
	c.mu.Unlock()
	c.stat("listening…")
	go func() {
		r := <-s.Done()
		c.mu.Lock()
		if c.active == s {
			c.active = nil
		}
		c.mu.Unlock()
		switch {
		case r.Discarded:
			return
		case r.Err != nil:
			c.stat("dictation error: " + r.Err.Error())
		case r.Text == "":
			c.stat("nothing heard")
		default:
			c.setLast(r.Text)
			c.onText(r.Text)
			c.stat("sent: " + r.Text)
		}
	}()
}
