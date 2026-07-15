package matebridge

import (
	"fmt"
	"hash/fnv"
)

// Access is the hosted per-group permissions module (schema rave.live/access@1, module key
// "access"), the richer replacement for the flat allow.txt list for worlds on the group model.
// The WORLD cannot read its instance/group id (Udon SDK limit), so a group is selected by a
// SECRET CODE typed on the keypad; the gist carries only the code's HASH (never plaintext).
// Default resolution is against Global (union of every group's allow-list + non-group entries) so
// the instance is usable on open, then narrows to a group's own {rules,users} once its code is
// entered. See .devnotes/HOSTED_ACCESS_CONTRACT.md (the cross-repo source of truth mirrored in
// Udon + TS) - change this + that doc together.

// AccessSchemaVersion is the AccessModule.V payload version (distinct from the envelope
// ContractVersion; the world detects the new format by the presence of the "global" key).
const AccessSchemaVersion = 1

// AccessModule is the inner "access" payload. Global is the permissive-on-open default; Groups are
// the code-selectable overrides. Back-compat: a world seeing a plain newline list (no "global"
// key) still loads it as a global-only allow-list.
type AccessModule struct {
	V      int           `json:"v"`
	Global AccessScope   `json:"global"`
	Groups []AccessGroup `json:"groups"`
}

// AccessRules are the non-user grant toggles evaluated on top of the display-name allow-list.
type AccessRules struct {
	InstanceOwner bool `json:"instanceOwner"`
	Master        bool `json:"master"`
	Everyone      bool `json:"everyone"`
}

// AccessScope is one resolvable permission set: rule toggles + a display-name allow-list.
type AccessScope struct {
	Rules AccessRules `json:"rules"`
	Users []string    `json:"users"`
}

// AccessGroup is one code-selectable group. CodeHash is AccessCodeHash(secret code) - the code is
// the membership proxy (VRChat group membership is uncheckable in-world). Rules+Users REPLACE
// Global when this group is active. ID + Instances are rave-mate/rave.page bookkeeping the world
// IGNORES (the world never reads a group/instance id at runtime).
type AccessGroup struct {
	ID        string      `json:"id,omitempty"`
	Name      string      `json:"name"`
	CodeHash  string      `json:"codeHash"`
	Rules     AccessRules `json:"rules"`
	Users     []string    `json:"users"`
	Instances []string    `json:"instances,omitempty"`
}

// AccessCodeHash is the shared secret-code hash: FNV-1a 64-bit over the code's UTF-8 bytes,
// lowercase 16-hex. MUST match the world's UdonSharp implementation byte-for-byte (the contract
// pins the test vectors). SOFT gate only - the fn is datamine-readable and public-gist codes are
// brute-forceable; it gates group selection + casual access, not sensitive management perms.
func AccessCodeHash(code string) string {
	h := fnv.New64a() // offset 0xcbf29ce484222325, prime 0x100000001b3
	_, _ = h.Write([]byte(code))
	return fmt.Sprintf("%016x", h.Sum64())
}
