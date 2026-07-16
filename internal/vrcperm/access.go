package vrcperm

import (
	"context"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/matebridge"
)

// This file composes + publishes the rave.live/access module (hosted per-group permissions; see
// .devnotes/HOSTED_ACCESS_CONTRACT.md). It EXTENDS the enveloped live-module path in live.go/
// publisher.go (same diff-only, SEQ-GATE, direct-or-hosted seam as pointer/config/performers) - the
// flat allow.txt PermList feeds (publish.go) stay untouched for back-compat.

// PublishAccess composes the access module (global union + per-group code hashes) from config and
// publishes it through the mode-agnostic live-module seam (direct gist or hosted worldlive API).
// Diff-only on the inner payload; secret codes never leave local config (only their hash ships).
func (s *Service) PublishAccess(ctx context.Context) {
	m := buildAccessModule(s.cfg())
	s.publishModule(ctx, "access", matebridge.SchemaAccess, matebridge.ModuleAccess, m, "rave-mate world access")
}

// buildAccessModule maps the persisted access config to the wire AccessModule. Global is COMPOSED
// automatically = the base rules + the union of the non-group allow-list and every group's users
// (deduped + sorted for stable diff-only output). Each group's secret code is hashed here (FNV-1a);
// the plaintext never reaches the payload.
func buildAccessModule(f *config.WorldSyncFeature) matebridge.AccessModule {
	groups := make([]matebridge.AccessGroup, 0, len(f.AccessGroups))
	union := append([]string(nil), f.AccessUsers...)
	for i := range f.AccessGroups {
		g := &f.AccessGroups[i]
		grp := matebridge.AccessGroup{
			ID:       g.ID,
			Name:     g.Name,
			CodeHash: matebridge.AccessCodeHash(g.Code),
			Rules:    toWireRules(g.Rules),
			Users:    orEmpty(dedupSort(g.Users)),
		}
		if len(g.Instances) > 0 {
			grp.Instances = append([]string(nil), g.Instances...)
		}
		groups = append(groups, grp)
		union = append(union, g.Users...)
	}
	return matebridge.AccessModule{
		V: matebridge.AccessSchemaVersion,
		Global: matebridge.AccessScope{
			Rules: toWireRules(f.AccessRules),
			Users: orEmpty(dedupSort(union)),
		},
		Groups: groups,
	}
}

// orEmpty coerces a nil slice to a non-nil empty one so `users` always marshals as a JSON array
// ([]) - the contract shape - not null.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// toWireRules converts the persisted rule toggles to the wire shape.
func toWireRules(r config.AccessRulesConfig) matebridge.AccessRules {
	return matebridge.AccessRules{
		InstanceOwner: r.InstanceOwner,
		Master:        r.Master,
		Everyone:      r.Everyone,
	}
}
