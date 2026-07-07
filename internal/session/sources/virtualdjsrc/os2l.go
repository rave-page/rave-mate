package virtualdjsrc

import (
	"context"
	"encoding/json"
	"net"
	"strconv"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/session"
)

const os2lConfidence = 0.7

// os2lMsg is one OS2L stream object. Only beat carries data we use (bpm). cmd/btn frames are
// metadata-free → ignored, not errors. (Schema: os2l.org / LordVonAdel/os2l.)
type os2lMsg struct {
	Evt    string  `json:"evt"`
	Change bool    `json:"change"`
	Pos    int     `json:"pos"`
	Bpm    float64 `json:"bpm"`
}

// runOS2L binds the OS2L TCP listener (port 0 → OS-assigned), advertises it via mDNS so
// VirtualDJ auto-discovers + connects, and handles each connection. BPM/beat only.
func (s *Source) runOS2L(ctx context.Context, emit func(session.Observation)) {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp4", ":"+strconv.Itoa(s.cfg.OS2LPort))
	if err != nil {
		s.log.Warn(os2lTag, "OS2L listen failed", map[string]any{"error": err.Error()})
		return
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	s.log.Info(os2lTag, "OS2L server listening", map[string]any{"port": port})

	debuglog.Go(s.log, os2lTag, func() { advertiseOS2L(ctx, s.log, port) })
	debuglog.Go(s.log, os2lTag, func() { <-ctx.Done(); _ = ln.Close() }) // unblock Accept on shutdown

	for {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return // listener closed (ctx done) or fatal accept error
		}
		c := conn
		debuglog.Go(s.log, os2lTag, func() { s.handleOS2L(ctx, c, emit) })
	}
}

// handleOS2L drives one VirtualDJ connection. The server MUST speak first - VirtualDJ only
// starts sending beat events after the server emits a message. Objects may be concatenated
// on the stream, so a json.Decoder loop reads successive values.
func (s *Source) handleOS2L(ctx context.Context, conn net.Conn, emit func(session.Observation)) {
	defer func() { _ = conn.Close() }()
	debuglog.Go(s.log, os2lTag, func() { <-ctx.Done(); _ = conn.Close() }) // unblock blocked Read
	s.log.Info(os2lTag, "OS2L client connected", map[string]any{"remote": conn.RemoteAddr().String()})

	// Prime VirtualDJ: it withholds beat events until the server has spoken.
	if _, err := conn.Write([]byte(`{"evt":"feedback","name":"ping","state":"on"}` + "\n")); err != nil {
		return
	}

	dec := json.NewDecoder(conn)
	for {
		var m os2lMsg
		if err := dec.Decode(&m); err != nil {
			return // EOF / closed / malformed framing
		}
		if m.Evt != "beat" || m.Bpm <= 0 { // cmd/btn carry no metadata; skip
			continue
		}
		emit(session.Observation{
			Source:     session.SourceVDJOS2L,
			Scope:      session.Scope{Kind: session.ScopeMaster},
			Fields:     map[string]any{session.FieldBPM: m.Bpm},
			Confidence: os2lConfidence,
		})
	}
}
