package feature

import (
	"fmt"
	"maps"
	"reflect"
)

// ContributionSet accumulates typed, validated contributions from plugins.
type ContributionSet struct {
	// TEST-ONLY: map-backed storage is test-only interim for planes without production generated bindings.
	// Task 2.3 replaces with typed struct + ordinal dispatch (zero maps, zero reflection).
	values     map[string]any
	identities map[string]string
	pluginIDs  map[string]string
	generated  *generatedContributions
}

// NewContributionSet creates a new empty ContributionSet.
func NewContributionSet() *ContributionSet {
	return &ContributionSet{
		values:     make(map[string]any),
		identities: make(map[string]string),
		pluginIDs:  make(map[string]string),
		generated:  newGeneratedContributions(),
	}
}

// Has reports whether a contribution for planeID exists in the set.
func (s *ContributionSet) Has(planeID string) bool {
	if s == nil {
		return false
	}
	if s.pluginIDs != nil {
		if _, ok := s.pluginIDs[planeID]; ok {
			return true
		}
	}
	if s.values != nil {
		_, ok := s.values[planeID]
		return ok
	}
	return false
}

// Freeze produces an immutable FrozenPlaneSet from the accumulated contributions.
// Freeze transfers ownership of stored values to the returned FrozenPlaneSet.
// Stored mutable values (such as slices and maps) are defensively cloned so that subsequent
// mutations to the ContributionSet or source slices do not affect the frozen snapshot.
func (s *ContributionSet) Freeze() FrozenPlaneSet {
	if s == nil {
		return FrozenPlaneSet{}
	}
	valuesCopy := make(map[string]any, len(s.values))
	for k, v := range s.values {
		valuesCopy[k] = cloneAny(v)
	}
	identitiesCopy := make(map[string]string, len(s.identities))
	maps.Copy(identitiesCopy, s.identities)
	var genFrozen *generatedFrozen
	if s.generated != nil && s.generated.freeze != nil {
		genFrozen = s.generated.freeze()
	}
	return FrozenPlaneSet{
		values:     valuesCopy,
		identities: identitiesCopy,
		frozen:     genFrozen,
	}
}

// Contribute adds a typed contribution from a feature plugin to the ContributionSet.
// If any validation or combination fails, the ContributionSet is left unmodified (fail-before-mutate)
// and an AttributedError attributing the plugin ID and plane ID is returned.
func Contribute[P any](s *ContributionSet, p Plane[P], pluginID string, v P) error {
	if s == nil {
		return fmt.Errorf("feature: nil ContributionSet")
	}
	if pluginID == "" {
		return &AttributedError{
			PlaneID: p.ID,
			Err:     fmt.Errorf("%w: plugin ID must not be empty", ErrInvalidContribution),
		}
	}
	if err := p.ValidateDeclaration(); err != nil {
		return &AttributedError{
			PluginID: pluginID,
			PlaneID:  p.ID,
			Err:      err,
		}
	}

	rule := p.Rules.RuleFor(SourceFeature)
	if rule == CombUnsupported {
		return &AttributedError{
			PluginID: pluginID,
			PlaneID:  p.ID,
			Err:      fmt.Errorf("%w: source %v is not supported on plane %q", ErrUnsupportedSource, SourceFeature, p.ID),
		}
	}

	// 1. Apply NilPolicy before Validate and Combine.
	isNil := false
	if p.IsNil != nil {
		isNil = p.IsNil(v)
	} else {
		var anyVal any = v
		isNil = (anyVal == nil)
	}

	if isNil {
		switch p.NilPolicy {
		case NilReject:
			return &AttributedError{
				PluginID: pluginID,
				PlaneID:  p.ID,
				Err:      fmt.Errorf("%w: nil contribution rejected by policy on plane %q", ErrNilContribution, p.ID),
			}
		case NilSkip:
			// Omit consistently from combination and diagnostics (leave set untouched)
			return nil
		case NilNotApplicable:
			// Continue to Validate / Combine
		}
	}

	// 2. Validate incoming contribution.
	if p.Validate != nil {
		if err := p.Validate(v); err != nil {
			return &AttributedError{
				PluginID: pluginID,
				PlaneID:  p.ID,
				Err:      fmt.Errorf("%w: %w", ErrInvalidContribution, err),
			}
		}
	}

	// 3. Identity extraction and validation (if plane requires it or declares an identity extractor).
	var incomingID string
	var hasID bool
	if p.Identity != nil {
		incomingID, hasID = p.Identity(v)
		if (rule == CombExclusive || rule == CombReplaceByIdentity) && (!hasID || incomingID == "") {
			if rule == CombExclusive {
				return &AttributedError{
					PluginID: pluginID,
					PlaneID:  p.ID,
					Err:      fmt.Errorf("%w: failed to extract identity from exclusive contribution", ErrInvalidContribution),
				}
			}
			return &AttributedError{
				PluginID: pluginID,
				PlaneID:  p.ID,
				Err:      fmt.Errorf("%w: failed to extract identity from replace_by_identity contribution", ErrInvalidContribution),
			}
		}
	}

	// 4. Exclusive conflict check.
	if rule == CombExclusive {
		if existingID, occupied := s.identities[p.ID]; occupied {
			conflictErr := fmt.Errorf("%w: %q and %q", ErrExclusiveConflict, existingID, incomingID)
			return &AttributedError{
				PluginID: pluginID,
				PlaneID:  p.ID,
				Err:      conflictErr,
			}
		}
	}

	// 5. Generated storage path: pure storage, closures are NOT responsible for identity validation or conflict checks.
	if p.generated.contribute != nil && s.generated != nil {
		if err := p.generated.contribute(s.generated, pluginID, v); err != nil {
			return &AttributedError{
				PluginID: pluginID,
				PlaneID:  p.ID,
				Err:      fmt.Errorf("%w: %w", ErrInvalidContribution, err),
			}
		}
		if hasID && incomingID != "" {
			s.identities[p.ID] = incomingID
		}
		s.pluginIDs[p.ID] = pluginID
		return nil
	}

	// 6. Test-only map-backed storage path (Combine): replaced by generated typed storage in task 2.3.
	// TEST-ONLY: replaced by generated typed storage in task 2.3; no production plane will use this path.
	incoming := cloneValue(v)

	if rule == CombExclusive {
		var zero P
		combined, err := p.Combine(SourceFeature, zero, incoming)
		if err != nil {
			return &AttributedError{
				PluginID: pluginID,
				PlaneID:  p.ID,
				Err:      fmt.Errorf("%w: %w", ErrInvalidContribution, err),
			}
		}

		s.values[p.ID] = cloneValue(combined)
		s.identities[p.ID] = incomingID
		s.pluginIDs[p.ID] = pluginID
		return nil
	}

	var current P
	if existingVal, exists := s.values[p.ID]; exists {
		if typed, ok := existingVal.(P); ok {
			current = typed
		}
	}

	// Defensive copy of current before invoking Combine to ensure fail-before-mutate:
	// a fallible combiner mutating current on failure cannot corrupt the stored candidate value.
	currentCopy := cloneValue(current)
	combined, err := p.Combine(SourceFeature, currentCopy, incoming)
	if err != nil {
		return &AttributedError{
			PluginID: pluginID,
			PlaneID:  p.ID,
			Err:      fmt.Errorf("%w: %w", ErrInvalidContribution, err),
		}
	}

	s.values[p.ID] = cloneValue(combined)
	s.pluginIDs[p.ID] = pluginID
	if hasID && incomingID != "" {
		s.identities[p.ID] = incomingID
	}
	return nil
}

func cloneValue[T any](v T) T {
	var anyVal any = v
	if anyVal == nil {
		return v
	}
	cloned := cloneAny(anyVal)
	if typed, ok := cloned.(T); ok {
		return typed
	}
	return v
}

func cloneAny(v any) any {
	if v == nil {
		return nil
	}
	val := reflect.ValueOf(v)
	switch val.Kind() {
	case reflect.Slice:
		if val.IsNil() {
			return v
		}
		l := val.Len()
		c := val.Cap()
		out := reflect.MakeSlice(val.Type(), l, c)
		reflect.Copy(out, val)
		return out.Interface()
	case reflect.Map:
		if val.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(val.Type(), val.Len())
		iter := val.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), iter.Value())
		}
		return out.Interface()
	default:
		return v
	}
}
