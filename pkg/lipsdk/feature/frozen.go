package feature

import (
	"fmt"
	"maps"
)

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

// materializeRequestSlice evaluates a slice request materializer, cloning the resulting slice
// to guarantee isolation of the frozen request view.
func materializeRequestSlice[T any](
	source []T,
	materialize func([]T) []T,
) []T {
	if materialize == nil {
		return cloneSlice(source)
	}
	return cloneSlice(materialize(source))
}

// Get retrieves the typed value for plane p from the frozen set.
// For planes bound to generated storage, Get dispatches directly via generated.get with zero map lookups,
// zero reflection, and zero type assertions on the request path.
// If the plane was not contributed or is absent, the zero value of P is returned.
func Get[P any](s FrozenPlaneSet, p Plane[P]) P {
	if p.generated.get != nil && s.frozen != nil {
		return p.generated.get(s.frozen)
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

// FreezeRequestPlanes materializes request-scoped feature planes into an immutable FrozenPlaneSet.
// Planes with a declared RequestMaterializer (e.g. sorted execution planes) are materialized
// once at snapshot construction; all other planes are preserved in their frozen order.
func FreezeRequestPlanes(in FrozenPlaneSet) FrozenPlaneSet {
	if in.IsZero() {
		return FrozenPlaneSet{}
	}
	if in.frozen != nil {
		return FrozenPlaneSet{
			frozen: in.frozen.freezeRequest(),
		}
	}
	cset := in.ToContributions()
	frozen := cset.Freeze()
	return FreezeRequestPlanes(frozen)
}

// FrozenIdentity retrieves the validated identity associated with an exclusive
// or replace-by-identity plane in the frozen set, if present.
// For planes bound to generated storage, FrozenIdentity dispatches directly via generated.identity
// with zero map lookups on the request path.
func FrozenIdentity[P any](s FrozenPlaneSet, p Plane[P]) (string, bool) {
	if p.generated.identity != nil && s.frozen != nil {
		return p.generated.identity(s.frozen)
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

// ContributeCandidateTo contributes candidate plane values in s into dst under the given source and contributor ID.
func (s FrozenPlaneSet) ContributeCandidateTo(dst *ContributionSet, source SourceKind, contributorID string) error {
	if s.IsZero() || dst == nil {
		return nil
	}
	staged := dst.Clone()
	if s.frozen != nil && staged.generated != nil {
		if err := s.frozen.contributeCandidateTo(staged.generated, source, contributorID); err != nil {
			return err
		}
		if s.identities != nil && staged.identities != nil {
			maps.Copy(staged.identities, s.identities)
		}
	} else {
		// Test-only map-backed storage fallback:
		if err := contributeCandidateMapTo(s.values, staged, source, contributorID); err != nil {
			return err
		}
	}
	*dst = *staged
	return nil
}

// Clone returns a deep copy of the FrozenPlaneSet with independent slice backing arrays and map copies.
func (s FrozenPlaneSet) Clone() FrozenPlaneSet {
	if s.IsZero() {
		return FrozenPlaneSet{}
	}
	var valuesCopy map[string]any
	if s.values != nil {
		valuesCopy = make(map[string]any, len(s.values))
		for k, v := range s.values {
			valuesCopy[k] = cloneAny(v)
		}
	}
	var identitiesCopy map[string]string
	if s.identities != nil {
		identitiesCopy = make(map[string]string, len(s.identities))
		maps.Copy(identitiesCopy, s.identities)
	}
	var genFrozen *generatedFrozen
	if s.frozen != nil {
		genFrozen = s.frozen.clone()
	}
	return FrozenPlaneSet{
		values:     valuesCopy,
		identities: identitiesCopy,
		frozen:     genFrozen,
	}
}

// validateStored checks that all planes in s conform to their declared plane validation rules
// without replaying into a ContributionSet.
func (s FrozenPlaneSet) validateStored() error {
	if s.IsZero() {
		return nil
	}
	if s.frozen != nil {
		return s.frozen.validate()
	}
	return validateAllPlanesMap(s.values, s.identities)
}

// Validate checks that all planes in s conform to their declared plane validation rules.
func (s FrozenPlaneSet) Validate() error {
	return s.validateStored()
}

// ReplayTo replays all planes in s into dst under SourceFeature with contributorID transactionally.
// If any plane fails validation or combination, dst is left unmodified (fail-before-mutate).
func (s FrozenPlaneSet) ReplayTo(dst *ContributionSet, contributorID string) error {
	return s.ReplaySourceTo(dst, SourceFeature, contributorID)
}

// ReplaySourceTo replays all planes in s into dst under source with contributorID transactionally.
// If any plane fails validation or combination, dst is left unmodified (fail-before-mutate).
func (s FrozenPlaneSet) ReplaySourceTo(dst *ContributionSet, source SourceKind, contributorID string) error {
	if s.IsZero() || dst == nil {
		return nil
	}
	if contributorID == "" {
		contributorID = "feature"
	}
	if s.frozen != nil {
		if planeID, ok := s.frozen.hasIdentityReplayRule(source, CombReplaceByIdentity); ok {
			return &AttributedError{
				PluginID: contributorID,
				PlaneID:  planeID,
				Err:      fmt.Errorf("%w: source %s requires identity-aware binder operation", ErrUnsupportedReplaySource, source),
			}
		}
	} else if s.values != nil {
		if planeID, ok := mapHasIdentityReplayRule(s.values, source, CombReplaceByIdentity); ok {
			return &AttributedError{
				PluginID: contributorID,
				PlaneID:  planeID,
				Err:      fmt.Errorf("%w: source %s requires identity-aware binder operation", ErrUnsupportedReplaySource, source),
			}
		}
	}
	if err := s.validateStored(); err != nil {
		return attributeReplayValidationError(err, contributorID)
	}
	staged := dst.Clone()
	if s.frozen != nil && staged.generated != nil {
		if err := s.frozen.replayAllPlanesTo(staged.generated, source, contributorID); err != nil {
			return err
		}
	} else {
		if err := replayAllPlanesMapTo(s.values, s.identities, staged, source, contributorID); err != nil {
			return err
		}
	}
	*dst = *staged
	return nil
}
