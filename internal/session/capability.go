package session

import "slices"

// Capability declares which fields a Source can provide for a scope kind and (optionally)
// specific IDs - e.g. Denon → decks A,B title/artist. IDs nil means all IDs of the kind.
type Capability struct {
	Scope  ScopeKind `json:"scope"`
	IDs    []string  `json:"ids,omitempty"`
	Fields []string  `json:"fields"`
}

// Provides reports whether the capability covers (scope, id, field).
func (c Capability) Provides(scope ScopeKind, id, field string) bool {
	if c.Scope != scope {
		return false
	}
	if len(c.IDs) > 0 && !slices.Contains(c.IDs, id) {
		return false
	}
	return slices.Contains(c.Fields, field)
}
