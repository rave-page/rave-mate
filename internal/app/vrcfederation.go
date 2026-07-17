package app

import (
	"context"
	"fmt"
	"sync"
	"time"

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
