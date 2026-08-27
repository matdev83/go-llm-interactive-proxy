package feature

import "maps"

// FrozenPlaneSet holds an immutable collection of composed feature plane values.
type FrozenPlaneSet struct {
	// TEST-ONLY: values and identities maps are test-only fallback storage in task 2.2.
	// Task 2.3 replaces them with typed struct storage and ordinal dispatch (zero maps, zero type assertions).
	values     map[string]any
	identities map[string]string
	frozen     *generatedFrozen
}

// cloneSlice returns a copy of slice s with its own backing array,
// preserving nil vs non-nil empty slice semantics.
func cloneSlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	return append(make([]T, 0, len(s)), s...)
}

// Get retrieves the typed value for plane p from the frozen set.
// For planes bound to generated storage, Get dispatches directly via generated.get with zero map lookups,
// zero reflection, and zero type assertions on the request path.
// If the plane was not contributed or is absent, the zero value of P is returned.
func Get[P any](s FrozenPlaneSet, p Plane[P]) P {
	if p.generated.get != nil {
		if s.frozen != nil {
			return p.generated.get(s.frozen)
		}
		var zero P
		return zero
	}
	// TEST-ONLY: replaced by generated typed storage in task 2.3; no production plane will use this path.
	if s.values != nil {
		if val, ok := s.values[p.ID]; ok {
			if typed, ok := val.(P); ok {
				return cloneValue(typed)
			}
		}
	}
	var zero P
	return zero
}

// FrozenIdentity retrieves the validated identity associated with an exclusive
// or replace-by-identity plane in the frozen set, if present.
// For planes bound to generated storage, FrozenIdentity dispatches directly via generated.identity
// with zero map lookups on the request path.
func FrozenIdentity[P any](s FrozenPlaneSet, p Plane[P]) (string, bool) {
	if p.generated.identity != nil {
		if s.frozen != nil {
			return p.generated.identity(s.frozen)
		}
		return "", false
	}
	// TEST-ONLY: replaced by generated typed storage in task 2.3; no production plane will use this path.
	if s.identities != nil {
		id, ok := s.identities[p.ID]
		return id, ok
	}
	return "", false
}

// IsZero reports whether s is an uninitialized, zero-value FrozenPlaneSet.
func (s FrozenPlaneSet) IsZero() bool {
	return s.frozen == nil && s.values == nil && s.identities == nil
}

// ToContributions reconstructs a mutable ContributionSet from the frozen snapshot.
func (s FrozenPlaneSet) ToContributions() *ContributionSet {
	if s.IsZero() {
		return NewContributionSet()
	}
	valuesCopy := make(map[string]any, len(s.values))
	for k, v := range s.values {
		valuesCopy[k] = cloneAny(v)
	}
	identitiesCopy := make(map[string]string, len(s.identities))
	maps.Copy(identitiesCopy, s.identities)

	var gen *generatedContributions
	if s.frozen != nil {
		gen = s.frozen.toContributions()
	} else {
		gen = newGeneratedContributions()
	}
	return &ContributionSet{
		values:     valuesCopy,
		identities: identitiesCopy,
		pluginIDs:  make(map[string]string),
		generated:  gen,
	}
}

// ContributionSetFromFrozen reconstructs a mutable ContributionSet from a FrozenPlaneSet.
func ContributionSetFromFrozen(s FrozenPlaneSet) *ContributionSet {
	return s.ToContributions()
}
