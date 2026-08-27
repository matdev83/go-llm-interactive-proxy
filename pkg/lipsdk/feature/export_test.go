package feature

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
