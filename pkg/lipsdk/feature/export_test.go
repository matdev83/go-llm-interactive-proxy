package feature

// GeneratedContributionsForTest is a type alias to the internal generatedContributions type for testing.
type GeneratedContributionsForTest = generatedContributions

// GeneratedFrozenForTest is a type alias to the internal generatedFrozen type for testing.
type GeneratedFrozenForTest = generatedFrozen

// BindGeneratedAccessForTest attaches generated access closures to a Plane[T] for testing.
func BindGeneratedAccessForTest[T any](
	p Plane[T],
	contribute func(*generatedContributions, string, T) error,
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

// NewGeneratedContributionsForTest creates a new generatedContributions with a freeze snapshot function for testing.
func NewGeneratedContributionsForTest(freeze func() *generatedFrozen) *generatedContributions {
	return &generatedContributions{
		freeze: freeze,
	}
}

// NewGeneratedFrozenForTest creates a new generatedFrozen with underlying data for testing.
func NewGeneratedFrozenForTest(data any) *generatedFrozen {
	return &generatedFrozen{
		data: data,
	}
}

// GeneratedFrozenDataForTest retrieves test data stored on generatedFrozen.
func GeneratedFrozenDataForTest(gf *generatedFrozen) any {
	if gf == nil {
		return nil
	}
	return gf.data
}
