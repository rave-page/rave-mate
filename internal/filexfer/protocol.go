package filexfer

import "fmt"

// Negotiation rides the EXISTING peerlink control plane (JSON on ChanBus / eventbus), like
// medialink: the sender offers, the receiver answers, the AEAD key is NEVER on the bus (both
// ends derive it from the peerlink handshake). Bulk data rides the dedicated conn (conn.go).

// Bus topics for transfer negotiation.
const (
	TopicOffer  = "file.offer"
	TopicAnswer = "file.answer"
)

// Answer reject reasons the sender maps to states (user-facing strings live UI-side).
const (
	reasonDeclined = "declined"
	reasonDisabled = "file transfer is disabled on the paired instance"
)

// Enc is the files-plane encryption preference on Offer/Answer. ABSENT ("") = ON: an older peer
// sends no field and MUST stay encrypted (this plane is default-AEAD). AEAD is dropped only when
// BOTH ends explicitly send EncOff for each other (peers.PlaneFiles).
const (
	EncOn  = "on"
	EncOff = "off"
)

// planeEncrypt resolves a transfer's files-plane encryption: default ON, plaintext only when BOTH
// ends opted out. A lone or absent opt-out keeps AEAD.
func planeEncrypt(offerEnc, answerEnc string) bool {
	return !(offerEnc == EncOff && answerEnc == EncOff)
}

// Offer announces a transfer: the receiver dials Addr and pulls. Bytes/Files are the
// manifest totals (display + sanity); the authoritative manifest crosses the AEAD conn.
type Offer struct {
	ID     string `json:"id"`
	Target string `json:"target"` // receiver node id
	Name   string `json:"name"`   // display name (base of the sent path)
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	Addr   string `json:"addr"`          // sender's transfer listener "host:port"
	Enc    string `json:"enc,omitempty"` // sender's files-plane enc pref (EncOn/EncOff; "" = ON)
}

// Answer accepts or rejects an Offer.
type Answer struct {
	ID     string `json:"id"`
	Accept bool   `json:"accept"`
	Reason string `json:"reason,omitempty"`
	Enc    string `json:"enc,omitempty"` // receiver's files-plane enc pref (EncOn/EncOff; "" = ON)
}

// Control message types on the AEAD conn (ctlMsg.T).
const (
	ctlManifest = "manifest" // sender→receiver: file list
	ctlGet      = "get"      // receiver→sender: stream file Index from Offset
	ctlHave     = "have"     // receiver→sender: file Index already complete locally
	ctlFileDone = "filedone" // sender→receiver: file Index fully sent; SHA = hex sha256
	ctlDone     = "done"     // receiver→sender: every file verified
	ctlCancel   = "cancel"   // either way: transfer canceled
	ctlErr      = "err"      // either way: protocol-fatal failure (Reason)
)

// ctlMsg is one JSON control frame on the conn (frameCtl).
type ctlMsg struct {
	T      string      `json:"t"`
	Files  []FileEntry `json:"files,omitempty"`  // manifest
	Index  int         `json:"index,omitempty"`  // get/have/filedone
	Offset int64       `json:"offset,omitempty"` // get
	SHA    string      `json:"sha,omitempty"`    // filedone (hex sha256)
	Reason string      `json:"reason,omitempty"` // cancel/err
}

// protoErr marks a protocol-fatal failure (bad hash, bad manifest, peer err frame):
// endSession settles to StateError instead of the resumable StateStalled.
type protoErr struct{ msg string }

func (e *protoErr) Error() string { return e.msg }

func protoErrf(format string, a ...any) error {
	return &protoErr{msg: fmt.Sprintf(format, a...)}
}
