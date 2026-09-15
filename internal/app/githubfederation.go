package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	ghlink "rave.page/mate/internal/github"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/peerfed"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/vrcperm"
)

// peerGistStore is the federated vrcperm.GistStore (mirror of peerVrcMembers for gists).
var _ vrcperm.GistStore = (*peerGistStore)(nil)

// ghServer adapts the local GitHub link (auth + gist client) to remotectl.GitHubServer so a
// paired peer can publish world feeds through this box's token. Auth-free by construction: only
// state (LocalSignedIn/Login) + gist CRUD are exposed; the token stays in *github.Auth.
type ghServer struct {
	*ghlink.Auth
	*ghlink.Gists
}

// peerGistStore implements vrcperm.GistStore over remotectl: gist writes run on the serving
// peer (its token, its account) so an unlinked instance's World Sync publishes as if linked
// locally. Mirrors peerVrcMembers. The serving peer's node id is set by the federation watcher
// on arm and cleared on disarm; the token never crosses the link.
type peerGistStore struct {
	endpoint func() *remotectl.Endpoint // late-bound: remotectl is constructed after vrcperm

	mu     sync.Mutex
	nodeID string
}

// setNode points the store at the serving peer (watcher arm; "" on disarm).
func (s *peerGistStore) setNode(id string) {
	s.mu.Lock()
	s.nodeID = id
	s.mu.Unlock()
}

func (s *peerGistStore) client() (*remotectl.Client, error) {
	e := s.endpoint()
	s.mu.Lock()
	id := s.nodeID
	s.mu.Unlock()
	if e == nil || id == "" {
		return nil, fmt.Errorf("github federation: no serving peer")
	}
	return remotectl.NewClient(e, id), nil
}

// Create publishes a new gist through the serving peer.
func (s *peerGistStore) Create(ctx context.Context, desc string, files map[string]string, public bool) (*ghlink.Gist, error) {
	cli, err := s.client()
	if err != nil {
		return nil, err
	}
	return cli.GhGistCreate(ctx, desc, files, public)
}

// Update patches a gist through the serving peer.
func (s *peerGistStore) Update(ctx context.Context, id, desc string, files map[string]string) (*ghlink.Gist, error) {
	cli, err := s.client()
	if err != nil {
		return nil, err
	}
	return cli.GhGistUpdate(ctx, id, desc, files)
}

// runGitHubFederationWatcher arms/disarms World-Sync (GitHub) federation: with no LOCAL link,
// the first paired instance holding one serves gist publishing as if linked locally. A local
// link always wins; the serving peer vanishing/unlinking disarms. Shared peerfed loop; probes
// github.state (which never carries the token). Mirrors the VRChat watcher's direct probing.
func runGitHubFederationWatcher(ctx context.Context, log *logbus.Bus, auth *ghlink.Auth, store *peerGistStore,
	peers *peerlink.Manager, endpoint func() *remotectl.Endpoint) {
	peerfed.Watcher[remotectl.GitHubStateResult]{
		Interval: 30 * time.Second,
		LocalActive: func() bool {
			return auth.LocalSignedIn()
		},
		Peers: func() []peerfed.Peer {
			if endpoint() == nil || peers == nil {
				return nil
			}
			return connectedPeers(peers)
		},
		Probe: func(pctx context.Context, nodeID string) (remotectl.GitHubStateResult, bool, error) {
			e := endpoint()
			if e == nil {
				return remotectl.GitHubStateResult{}, false, nil
			}
			st, err := remotectl.NewClient(e, nodeID).GitHubState(pctx)
			return st, st.SignedIn, err
		},
		Arm: func(nodeID, name string, st remotectl.GitHubStateResult) {
			auth.SetFederated(st.Login, name)
			store.setNode(nodeID)
			log.Info("worldsync", "github federation armed - gists served by peer", map[string]any{"via": name})
		},
		Disarm: func() {
			auth.ClearFederated()
			store.setNode("")
		},
	}.Run(ctx)
}
