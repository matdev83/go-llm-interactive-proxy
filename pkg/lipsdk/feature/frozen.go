package feature

// FrozenPlaneSet holds an immutable collection of composed feature plane values.
type FrozenPlaneSet struct {
	values     map[string]any
	identities map[string]string
	frozen     *generatedFrozen
}

// Get retrieves the typed value for plane p from the frozen set.
// If the plane was not contributed or is absent, the zero value of P is returned.
func Get[P any](s FrozenPlaneSet, p Plane[P]) P {
	if p.generated.get != nil && s.frozen != nil {
		return p.generated.get(s.frozen)
	}
	if s.values != nil {
		if val, ok := s.values[p.ID]; ok {
			if typed, ok := val.(P); ok {
				return typed
			}
		}
	}
	var zero P
	return zero
}

// FrozenIdentity retrieves the validated identity associated with an exclusive
// or replace-by-identity plane in the frozen set, if present.
func FrozenIdentity[P any](s FrozenPlaneSet, p Plane[P]) (string, bool) {
	if p.generated.identity != nil && s.frozen != nil {
		return p.generated.identity(s.frozen)
	}
	if s.identities != nil {
		id, ok := s.identities[p.ID]
		return id, ok
	}
	return "", false
}
