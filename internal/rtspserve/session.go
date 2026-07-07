package rtspserve

// Per-connection RTSP handling. Only RTP/AVP/TCP interleaved transport is offered - that
// is what rtspt:// (VRChat AVPro) speaks, and it needs no UDP hole management on the LAN.

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
)

const rtspTimeout = 90 * time.Second // read deadline; clients keep-alive via GET_PARAMETER

type rtspRequest struct {
	method string
	target string
	header map[string]string // lower-cased keys
}

// readRequest parses one RTSP request (request line + headers + optional body, discarded).
func readRequest(br *bufio.Reader) (*rtspRequest, error) {
	line, err := readLine(br)
	if err != nil {
		return nil, err
	}
	if line == "" { // tolerate stray CRLF between requests
		line, err = readLine(br)
		if err != nil {
			return nil, err
		}
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 3 || !strings.HasPrefix(parts[2], "RTSP/") {
		return nil, fmt.Errorf("bad request line %q", line)
	}
	req := &rtspRequest{method: strings.ToUpper(parts[0]), target: parts[1], header: map[string]string{}}
	for {
		h, err := readLine(br)
		if err != nil {
			return nil, err
		}
		if h == "" {
			break
		}
		if i := strings.IndexByte(h, ':'); i > 0 {
			req.header[strings.ToLower(strings.TrimSpace(h[:i]))] = strings.TrimSpace(h[i+1:])
		}
	}
	if n, _ := strconv.Atoi(req.header["content-length"]); n > 0 && n < 1<<20 {
		if _, err := io.CopyN(io.Discard, br, int64(n)); err != nil {
			return nil, err
		}
	}
	return req, nil
}

func readLine(br *bufio.Reader) (string, error) {
	s, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// connWriter serializes response + RTP writes on one connection.
type connWriter struct {
	c  net.Conn
	mu chan struct{} // 1-slot semaphore (allows deadline set + write as one unit)
}

func newConnWriter(c net.Conn) *connWriter {
	w := &connWriter{c: c, mu: make(chan struct{}, 1)}
	w.mu <- struct{}{}
	return w
}

func (w *connWriter) write(b []byte) error {
	<-w.mu
	defer func() { w.mu <- struct{}{} }()
	_ = w.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := w.c.Write(b)
	return err
}

// respond writes an RTSP response echoing CSeq.
func (w *connWriter) respond(cseq, status string, extra []string, body string) error {
	var b strings.Builder
	b.WriteString("RTSP/1.0 " + status + "\r\n")
	if cseq != "" {
		b.WriteString("CSeq: " + cseq + "\r\n")
	}
	b.WriteString("Server: rave-mate\r\n")
	for _, h := range extra {
		b.WriteString(h + "\r\n")
	}
	if body != "" {
		b.WriteString("Content-Length: " + strconv.Itoa(len(body)) + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(body)
	return w.write([]byte(b.String()))
}

// pathOf extracts the path component of an RTSP target ("*" and bare paths tolerated).
func pathOf(target string) string {
	if u, err := url.Parse(target); err == nil && u.Path != "" {
		return strings.TrimSuffix(u.Path, "/")
	}
	return target
}

// handleConn serves one RTSP client connection.
func (s *Server) handleConn(ctx context.Context, c net.Conn, cfg config.RTSPServeFeature, fd *feed) {
	defer func() { _ = c.Close() }()
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Unblock a parked read on stop/writer-death: closing the conn is the only interrupt.
	debuglog.Go(s.log, source, func() { <-connCtx.Done(); _ = c.Close() })
	w := newConnWriter(c)
	br := bufio.NewReader(c)
	sessionID := fmt.Sprintf("%08x", rand.Uint32())
	wantPath := cfg.ResolvedPath()
	playing := false

	for {
		// Pre-play: idle clients time out. While streaming, dead peers surface as RTP
		// write errors instead - players that never send keep-alives stay connected.
		dl := rtspTimeout
		if playing {
			dl = 24 * time.Hour
		}
		_ = c.SetReadDeadline(time.Now().Add(dl))
		req, err := readRequest(br)
		if err != nil {
			return // client gone / timed out
		}
		cseq := req.header["cseq"]
		switch req.method {
		case "OPTIONS":
			_ = w.respond(cseq, "200 OK", []string{"Public: OPTIONS, DESCRIBE, SETUP, PLAY, TEARDOWN, GET_PARAMETER, SET_PARAMETER"}, "")
		case "DESCRIBE":
			if p := pathOf(req.target); p != "" && p != "*" && p != wantPath {
				_ = w.respond(cseq, "404 Not Found", nil, "")
				continue
			}
			select {
			case <-fd.ready:
			case <-time.After(5 * time.Second):
				_ = w.respond(cseq, "503 Service Unavailable", nil, "")
				continue
			case <-connCtx.Done():
				return
			}
			sps, pps := fd.params()
			body := sdp(sps, pps)
			_ = w.respond(cseq, "200 OK", []string{
				"Content-Base: " + req.target + "/",
				"Content-Type: application/sdp",
			}, body)
		case "SETUP":
			tr := req.header["transport"]
			if !strings.Contains(tr, "TCP") && !strings.Contains(tr, "interleaved") {
				_ = w.respond(cseq, "461 Unsupported Transport", nil, "")
				continue
			}
			_ = w.respond(cseq, "200 OK", []string{
				"Transport: RTP/AVP/TCP;unicast;interleaved=0-1",
				"Session: " + sessionID + ";timeout=90",
			}, "")
		case "PLAY":
			_ = w.respond(cseq, "200 OK", []string{"Session: " + sessionID, "Range: npt=now-"}, "")
			if !playing {
				playing = true
				s.mu.Lock()
				s.clients++
				s.mu.Unlock()
				debuglog.Go(s.log, source, func() {
					s.streamTo(connCtx, w, fd, cfg.ResolvedFPS())
					s.mu.Lock()
					s.clients--
					s.mu.Unlock()
					cancel() // stream writer died → tear the conn down
				})
			}
		case "TEARDOWN":
			_ = w.respond(cseq, "200 OK", []string{"Session: " + sessionID}, "")
			return
		case "GET_PARAMETER", "SET_PARAMETER":
			_ = w.respond(cseq, "200 OK", []string{"Session: " + sessionID}, "")
		default:
			_ = w.respond(cseq, "501 Not Implemented", nil, "")
		}
	}
}

// streamTo pushes the live feed to one playing client as interleaved RTP (channel 0).
// Joins at an IDR; SPS/PPS are re-sent in-band before every keyframe.
func (s *Server) streamTo(ctx context.Context, w *connWriter, fd *feed, fps int) {
	sub := fd.subscribe()
	defer fd.unsubscribe(sub)
	seq := uint16(rand.Uint32())
	ts := rand.Uint32()
	ssrc := rand.Uint32()
	tsStep := uint32(90000 / fps)
	for {
		select {
		case <-ctx.Done():
			return
		case au := <-sub.ch:
			nals := au.nals
			if au.key {
				if sps, pps := fd.params(); sps != nil && pps != nil {
					nals = append([][]byte{sps, pps}, nals...)
				}
			}
			for _, p := range payloadize(nals, rtpMaxPayload) {
				pkt := buildRTP(p, seq, ts, ssrc)
				seq++
				if err := w.write(pkt); err != nil {
					return
				}
			}
			ts += tsStep
		}
	}
}

// buildRTP frames one payload as $-interleaved RTP: 4-byte interleave header + 12-byte
// RTP header (V=2, PT=96) + payload.
func buildRTP(p rtpPayload, seq uint16, ts, ssrc uint32) []byte {
	n := 12 + len(p.data)
	pkt := make([]byte, 4+n)
	pkt[0] = '$'
	pkt[1] = 0 // channel 0 = RTP
	binary.BigEndian.PutUint16(pkt[2:], uint16(n))
	pkt[4] = 0x80 // V=2
	pkt[5] = 96
	if p.marker {
		pkt[5] |= 0x80
	}
	binary.BigEndian.PutUint16(pkt[6:], seq)
	binary.BigEndian.PutUint32(pkt[8:], ts)
	binary.BigEndian.PutUint32(pkt[12:], ssrc)
	copy(pkt[16:], p.data)
	return pkt
}
