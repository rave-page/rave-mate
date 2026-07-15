package app

import (
	"context"
	"net/url"
	"strings"

	"rave.page/mate/internal/config"
	ghlink "rave.page/mate/internal/github"
	"rave.page/mate/internal/matebridge"
	"rave.page/mate/internal/matepreset"
	"rave.page/mate/internal/vrchat"
	"rave.page/mate/internal/vrcloc"
	"rave.page/mate/internal/vrcperm"
)

// This file wires the EDIT-TIME loopback bridge (Channel 1 of the world-bridge contract): adapters
// over the live VRChat session + WorldSync gist publisher + a file-backed preset store, all bound to
// 127.0.0.1 behind a rave-mate-minted bearer. Every gateway acts ONLY on rave-mate's local sanctioned
// surface; no caller credential is ever accepted. See docs/WORLD_BRIDGE_CONTRACT.md.
//
// Enablement: the bridge runs whenever the WorldSync feature is enabled - it is the edit-time half of
// the same world-integration feature (the runtime gist half is WorldSync's existing refresher). No
// separate toggle (one flag = one feature); a gateway further gates itself live via Available():
// vrchat needs a signed-in session, worldsync needs GitHub linked.

// ── Directory gateway (VRChat friends/groups/resolve over the sealed local session) ──────────────

type directoryGateway struct {
	mgr     *vrchat.Manager
	enabled func() bool
}

// Available: advertise + serve vrchat routes only while the feature is on AND a session is signed in.
func (g *directoryGateway) Available() bool {
	return g.enabled != nil && g.enabled() && g.mgr.State().LoggedIn
}

func (g *directoryGateway) Friends(ctx context.Context, offset, n int, offline bool) ([]matebridge.Friend, error) {
	fs, err := g.mgr.Client().Friends(ctx, offset, n, offline)
	if err != nil {
		return nil, err
	}
	out := make([]matebridge.Friend, 0, len(fs))
	for _, f := range fs {
		out = append(out, matebridge.Friend{ID: f.ID, DisplayName: f.DisplayName, Status: f.Status, Online: onlineFromStatus(f.Status)})
	}
	return out, nil
}

// onlineFromStatus maps VRChat presence to a boolean so the editor need not know VRChat's status
// vocabulary. VRChat "offline"/"" => offline; every settable presence ("active","join me","ask me",
// "busy") => online. (The friends list only returns non-offline friends unless offline=true is asked,
// so this is the authoritative reduction of that vocabulary.)
func onlineFromStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s != "" && s != "offline"
}

func (g *directoryGateway) Groups(ctx context.Context) ([]matebridge.Group, error) {
	id := g.mgr.CurrentUserID()
	if id == "" {
		return nil, vrchat.ErrUnauthorized
	}
	gs, err := g.mgr.Client().UserGroups(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]matebridge.Group, 0, len(gs))
	for _, gr := range gs {
		out = append(out, matebridge.Group{ID: gr.EffectiveID(), Name: gr.Name, ShortCode: gr.ShortCode, MemberCount: gr.MemberCount})
	}
	return out, nil
}

// GroupMembers returns ONE page (the editor paginates). partial=false for a clean page; a private
// group / hidden members surface as an upstream error (the handler answers 502, the editor keeps
// last-good), not a silent under-list.
func (g *directoryGateway) GroupMembers(ctx context.Context, groupID, roleID string, offset, n int) ([]matebridge.GroupMember, bool, error) {
	ms, err := g.mgr.Client().GroupMembers(ctx, groupID, roleID, offset, n)
	if err != nil {
		return nil, false, err
	}
	out := make([]matebridge.GroupMember, 0, len(ms))
	for _, m := range ms {
		out = append(out, matebridge.GroupMember{ID: m.UserID, DisplayName: m.User.DisplayName, RoleIDs: m.RoleIDs})
	}
	return out, false, nil
}

// Resolve maps stable ids -> current display names, best-effort PER ID: an unresolvable id (deleted /
// hidden / lookup error) yields DisplayName:"" so the editor keeps the id and retries, never failing
// the whole batch. usr_ -> user lookup, grp_ -> group lookup; anything else is returned empty.
func (g *directoryGateway) Resolve(ctx context.Context, ids []string) ([]matebridge.Resolved, error) {
	out := make([]matebridge.Resolved, 0, len(ids))
	for _, id := range ids {
		switch {
		case strings.HasPrefix(id, "usr_"):
			name, err := g.mgr.Client().UserDisplayName(ctx, id)
			if err != nil {
				name = ""
			}
			out = append(out, matebridge.Resolved{ID: id, DisplayName: name, Kind: "user"})
		case strings.HasPrefix(id, "grp_"):
			name, err := g.mgr.Client().GroupName(ctx, id)
			if err != nil {
				name = ""
			}
			out = append(out, matebridge.Resolved{ID: id, DisplayName: name, Kind: "group"})
		default:
			out = append(out, matebridge.Resolved{ID: id, DisplayName: "", Kind: ""})
		}
	}
	return out, nil
}

// ── Settings gateway (project config rave-mate changed while Unity was closed) ────────────────────

type settingsGateway struct {
	cfg   func() *config.WorldSyncFeature
	owner func() string
	seq   gistseqPeeker
}

// gistseqPeeker is the subset of the seq counter the settings gateway reads (seq high-water per
// module) - kept an interface so the gateway doesn't own the counter.
type gistseqPeeker interface {
	Peek(module string) int64
}

// Available: settings read only local config, always serviceable.
func (g *settingsGateway) Available() bool { return true }

// Settings reports the module gist raw URLs to stamp + a seq that advances whenever rave-mate
// republishes a module (max of the per-module seqs) so a returning editor detects the change. The
// projectId is advisory (single project per rave-mate); configValues/rebuildScopes are reserved for
// future author-facing settings and empty for now.
func (g *settingsGateway) Settings(_ context.Context, _ string) (*matebridge.Settings, error) {
	f := g.cfg()
	owner := g.owner()
	var urls []string
	var seqs []int64
	// Mode-agnostic: prefer the persisted LiveModules pointer (both publish modes write it); fall
	// back to the direct-mode owner+gist derivation before the first LiveModules record. Same for
	// seq - the persisted value (server-owned in hosted mode) else the local gistseq high-water.
	add := func(key, gistID, moduleKey string) {
		lm := f.LiveModules[key]
		raw := lm.RawURL
		if raw == "" && owner != "" && gistID != "" {
			raw = ghlink.RawURL(owner, gistID, moduleKey+".json")
		}
		if raw != "" {
			urls = append(urls, raw)
		}
		s := lm.Seq
		if s == 0 && g.seq != nil {
			s = g.seq.Peek(key)
		}
		seqs = append(seqs, s)
	}
	add("pointer", f.PointerGistID, matebridge.ModulePointer)
	add("config", f.ConfigGistID, matebridge.ModuleConfig)
	add("performers", f.PerformersGistID, matebridge.ModulePerformers)

	seq := maxSeq(seqs...)
	return &matebridge.Settings{
		ContractVersion: matebridge.ContractVersion,
		Seq:             seq,
		UpdatedAt:       "",
		ModuleURLs:      urls,
		ConfigValues:    map[string]string{},
		RebuildScopes:   []string{},
	}, nil
}

// RebuildSignals has no source yet (rave-mate edits nothing that requires a re-bake) - a clean empty
// poll. The capability stays advertised (settings routes are live); the editor just sees no signals.
func (g *settingsGateway) RebuildSignals(_ context.Context, _ int64) (int64, []matebridge.RebuildSignal, error) {
	return 0, nil, nil
}

func maxSeq(vs ...int64) int64 {
	var m int64
	for _, v := range vs {
		if v > m {
			m = v
		}
	}
	return m
}

// ── Roster gateway (editor hands a resolved roster; rave-mate publishes the gist) ────────────────

type rosterGateway struct{ svc *vrcperm.Service }

// Available: publishing needs the WorldSync feature enabled + GitHub linked.
func (g *rosterGateway) Available() bool { return g.svc.Ready() }

func (g *rosterGateway) PublishRoster(ctx context.Context, kind, name string, names []string) (string, string, string, int64, error) {
	return g.svc.PublishRoster(ctx, kind, name, names)
}

// ── construction ─────────────────────────────────────────────────────────────────────────────────

// editorBridgeDeps wires newEditorBridge. cfg/owner feed the settings gateway (WorldSync config +
// GitHub login); vrcEnabled gates the vrchat capability; seq is the shared gist SEQ-GATE counter
// (also feeding the preset store) and its Peek surfaces the settings seq.
type editorBridgeDeps struct {
	VRChat     *vrchat.Manager
	WorldSync  *vrcperm.Service
	Cfg        func() *config.WorldSyncFeature
	Owner      func() string
	VRCEnabled func() bool
	Seq        interface {
		matepreset.SeqCounter
		gistseqPeeker
	}
	Version string
}

// newEditorBridge builds the loopback server for the Unity editor. Gateways self-gate via
// Available(); the discovery file is written 0600 on Start, removed on Stop. Returns nil only if the
// config dir is unresolved (no discovery target) - callers treat nil as "bridge disabled".
func newEditorBridge(d editorBridgeDeps) *matebridge.Server {
	discPath, err := config.DataPath(matebridge.DiscoveryFile)
	if err != nil {
		return nil
	}
	presetDir, err := config.DataPath("mate-presets")
	if err != nil {
		return nil
	}
	return matebridge.New(matebridge.Options{
		Directory:     &directoryGateway{mgr: d.VRChat, enabled: d.VRCEnabled},
		Presets:       matepreset.NewStore(presetDir, d.Seq),
		Settings:      &settingsGateway{cfg: d.Cfg, owner: d.Owner, seq: d.Seq},
		Roster:        &rosterGateway{svc: d.WorldSync},
		Version:       d.Version,
		DiscoveryPath: discPath,
	})
}

// pointerProvider builds the rave.live/pointer instance link from the live VRChat session + location:
// instanceOwnerName = the signed-in account (the operator that opened the instance); byOperator seeds
// that operator at priority 10 so operator-presence resolution works out of the box; activeGroup* +
// joinInfo come from the current location when known. ok=false when signed out (nothing to link).
func pointerProvider(vrcMgr *vrchat.Manager, current func() (vrcloc.Location, bool)) vrcperm.PointerProvider {
	return func() (matebridge.PointerModule, bool) {
		st := vrcMgr.State()
		if !st.LoggedIn || st.DisplayName == "" {
			return matebridge.PointerModule{}, false
		}
		p := matebridge.PointerModule{
			Default:           "main",
			InstanceOwnerName: st.DisplayName,
			ByOperator:        []matebridge.OperatorRef{{Operator: st.DisplayName, Profile: "main", Priority: 10}},
		}
		if loc, ok := current(); ok {
			p.ActiveGroupID = loc.GroupID
			p.ActiveGroupName = loc.GroupName
			p.JoinInfo = joinInfoFor(loc)
		}
		return p, true
	}
}

// joinInfoFor builds the DISPLAY-ONLY off-world join affordance (Udon cannot join an instance). The
// deep link is best-effort from world+instance ids; empty when unknown.
func joinInfoFor(loc vrcloc.Location) matebridge.JoinInfo {
	if loc.WorldID == "" {
		return matebridge.JoinInfo{}
	}
	target := loc.WorldID
	if loc.InstanceID != "" {
		target += ":" + loc.InstanceID
	}
	return matebridge.JoinInfo{
		DeepLink: "vrchat://launch?ref=rave.page&id=" + url.QueryEscape(target),
		Label:    "Join the set",
	}
}
