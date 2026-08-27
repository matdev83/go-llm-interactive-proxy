package feature

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"

// GeneratedContributionsForTest is a type alias to the internal generatedContributions type for testing.
type GeneratedContributionsForTest = generatedContributions

// GeneratedFrozenForTest is a type alias to the internal generatedFrozen type for testing.
type GeneratedFrozenForTest = generatedFrozen

// BindGeneratedAccessForTest attaches generated access closures to a Plane[T] for testing.
func BindGeneratedAccessForTest[T any](
	p Plane[T],
	contribute func(*generatedContributions, SourceKind, string, T) error,
	get func(*generatedFrozen) T,
	identity func(*generatedFrozen) (string, bool),
) Plane[T] {
	p.generated = generatedAccess[T]{
		contribute: contribute,
		get:        get,
		identity:   identity,
	}
	return p
}

// NewContributionSetWithGeneratedForTest creates a ContributionSet wrapping a generatedContributions pointer for testing.
func NewContributionSetWithGeneratedForTest(gen *generatedContributions) *ContributionSet {
	s := NewContributionSet()
	s.generated = gen
	return s
}

// NewFrozenPlaneSetWithGeneratedForTest creates a FrozenPlaneSet wrapping a generatedFrozen pointer for testing.
func NewFrozenPlaneSetWithGeneratedForTest(frozen *generatedFrozen) FrozenPlaneSet {
	return FrozenPlaneSet{
		frozen: frozen,
	}
}

// NewGeneratedContributionsForTest creates a new generatedContributions for testing.
func NewGeneratedContributionsForTest() *generatedContributions {
	return newGeneratedContributions()
}

// NewFrozenPlaneSetFromMapForTest creates a map-backed FrozenPlaneSet for testing,
// defensively cloning the input maps to ensure frozen immutability.
func NewFrozenPlaneSetFromMapForTest(values map[string]any, identities map[string]string) FrozenPlaneSet {
	var valuesCopy map[string]any
	if values != nil {
		valuesCopy = make(map[string]any, len(values))
		for k, v := range values {
			valuesCopy[k] = cloneAny(v)
		}
	}
	var identitiesCopy map[string]string
	if identities != nil {
		identitiesCopy = make(map[string]string, len(identities))
		for k, v := range identities {
			identitiesCopy[k] = v
		}
	}
	return FrozenPlaneSet{
		values:     valuesCopy,
		identities: identitiesCopy,
		frozen:     nil,
	}
}

// NewMalformedGeneratedFrozenCandidateForTest constructs a test-only generated FrozenPlaneSet
// with specific candidate request transforms and attempt transforms for transaction testing.
func NewMalformedGeneratedFrozenCandidateForTest(
	reqTr []request.Transform,
	attTr []request.AttemptTransform,
) FrozenPlaneSet {
	gf := &generatedFrozen{
		requestTransforms: cloneSlice(reqTr),
		attemptTransforms: cloneSlice(attTr),
	}
	return FrozenPlaneSet{
		frozen: gf,
	}
}
