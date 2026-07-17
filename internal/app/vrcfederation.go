package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/vrchat"
)

// peerVrcMembers serves group members through the first connected peer holding a live
// VRChat link (vrchat federation) - the publish-time group-role expansion keeps working
// on instances that never linked VRChat themselves. The serving peer is memoized
// (peerVrcTTL) so the per-page GroupMembers calls don't re-probe every peer each page;
// a failed call drops the memo so the next page re-discovers.
type peerVrcMembers struct {
	endpoint func() *remotectl.Endpoint // late-bound: remotectl is constructed after vrcperm
	peers    *peerlink.Manager

	mu     sync.Mutex
	nodeID string
	until  time.Time
}

const peerVrcTTL = 60 * time.Second

// pick returns a client bound to the memoized (or freshly discovered) linked peer.
func (s *peerVrcMembers) pick(ctx context.Context) (*remotectl.Client, error) {
	e := s.endpoint()
	if e == nil || s.peers == nil {
		return nil, fmt.Errorf("peer control unavailable")
	}
	s.mu.Lock()
	memo := s.nodeID
	if time.Now().After(s.until) {
		memo = ""
	}
	s.mu.Unlock()
	if memo != "" {
		return remotectl.NewClient(e, memo), nil
	}
	for _, c := range s.peers.Connections() {
		if c.Status != peerlink.StatusConnected {
			continue
		}
		cli := remotectl.NewClient(e, c.NodeID)
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st, err := cli.VrcStatus(sctx)
		cancel()
		if err != nil || !st.Linked {
			continue
		}
		s.mu.Lock()
		s.nodeID, s.until = c.NodeID, time.Now().Add(peerVrcTTL)
		s.mu.Unlock()
		return cli, nil
	}
	return nil, fmt.Errorf("no connected peer with a VRChat link")
}

func (s *peerVrcMembers) drop() {
	s.mu.Lock()
	s.nodeID = ""
	s.mu.Unlock()
}

// GroupMembers implements vrcperm.MemberSource over the federation peer.
func (s *peerVrcMembers) GroupMembers(ctx context.Context, groupID, roleID string, offset, n int) ([]vrchat.GroupMember, error) {
	cli, err := s.pick(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := cli.VrcGroupMembers(ctx, groupID, roleID, offset, n)
	if err != nil {
		s.drop() // peer gone / unlinked mid-expansion - re-discover on the next page
	}
	return rows, err
}

// ── full federation: peer-tunneled vrchat.Client + watcher ────────────────────

// vrcProxyTransport is the RoundTripper behind a federated vrchat.Client: every
// request is executed by the serving peer with ITS session (remotectl
// vrchat.proxy). Cookies never cross the link; responses are synthesized JSON.
type vrcProxyTransport struct {
	endpoint func() *remotectl.Endpoint
	nodeID   string
}

func (t *vrcProxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	e := t.endpoint()
	if e == nil {
		return nil, fmt.Errorf("vrchat federation: peer control unavailable")
	}
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	// client base = https://api.vrchat.cloud/api/1 → serving side re-joins its
	// own base, so strip the prefix down to the API-relative path (+query).
	pq := strings.TrimPrefix(req.URL.Path, "/api/1")
	if req.URL.RawQuery != "" {
		pq += "?" + req.URL.RawQuery
	}
	status, respBody, err := remotectl.NewClient(e, t.nodeID).
		VrcProxy(req.Context(), req.Method, pq, body, req.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:  h,
		Body:    io.NopCloser(bytes.NewReader(respBody)),
		Request: req,
	}, nil
}

// runVrcFederationWatcher arms/disarms full VRChat federation on the manager:
// with no LOCAL session, the first connected peer holding one serves EVERY
// feature (tabs, worlds, status edits) through a peer-tunneled client. A local
// login always wins; the serving peer vanishing/unlinking disarms. 30s cadence
// + an immediate first pass, all status probes bounded at 5s.
func runVrcFederationWatcher(ctx context.Context, log *logbus.Bus, mgr *vrchat.Manager,
	peers *peerlink.Manager, endpoint func() *remotectl.Endpoint) {
	armed := ""
	check := func() {
		if mgr.LocalState().LoggedIn {
			if armed != "" {
				armed = ""
				mgr.ClearFederated()
			}
			return
		}
		e := endpoint()
		if e == nil || peers == nil {
			return
		}
		for _, c := range peers.Connections() {
			if c.Status != peerlink.StatusConnected {
				continue
			}
			cli := remotectl.NewClient(e, c.NodeID)
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			st, err := cli.VrcStatus(sctx)
			cancel()
			if err != nil || !st.Linked {
				if armed == c.NodeID {
					armed = ""
					mgr.ClearFederated()
				}
				continue
			}
			if armed == c.NodeID {
				return // serving peer still healthy
			}
			name := strings.TrimSpace(c.Nickname)
			if name == "" && len(c.NodeID) >= 8 {
				name = c.NodeID[:8]
			}
			fed := vrchat.NewWithTransport(log, &vrcProxyTransport{endpoint: endpoint, nodeID: c.NodeID})
			mgr.SetFederated(fed, vrchat.State{UserID: st.UserID, DisplayName: st.DisplayName, Via: name})
			armed = c.NodeID
			log.Info("vrchat", "federation armed - session served by peer", map[string]any{"via": name})
			return
		}
		if armed != "" {
			armed = ""
			mgr.ClearFederated()
			log.Info("vrchat", "federation disarmed - serving peer gone", nil)
		}
	}
	check()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			check()
		}
	}
}
