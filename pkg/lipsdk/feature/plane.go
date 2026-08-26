package feature

import (
	"fmt"
)

// SourceKind identifies the composition source contributing to an extension plane.
type SourceKind uint8

const (
	// SourceUnknown is the uninitialized or unknown source sentinel.
	SourceUnknown SourceKind = iota
	// SourceFeature represents contributions from feature plugins.
	SourceFeature
	// SourceHost represents contributions or overlays injected by the host runtime.
	SourceHost
	// SourceGenerationBinder represents generation-scoped binder contributions.
	SourceGenerationBinder
)

func (s SourceKind) String() string {
	switch s {
	case SourceFeature:
		return "feature"
	case SourceHost:
		return "host"
	case SourceGenerationBinder:
		return "generation_binder"
	default:
		return fmt.Sprintf("SourceKind(%d)", s)
	}
}

// Multiplicity defines whether a plane accepts multiple ordered contributions or a single exclusive slot.
type Multiplicity uint8

const (
	// MultUnknown is the uninitialized multiplicity sentinel.
	MultUnknown Multiplicity = iota
	// MultOrdered allows multiple contributions combined in registration order.
	MultOrdered
	// MultExclusive permits at most one contribution, failing fast on conflict.
	MultExclusive
)

func (m Multiplicity) String() string {
	switch m {
	case MultOrdered:
		return "ordered"
	case MultExclusive:
		return "exclusive"
	default:
		return fmt.Sprintf("Multiplicity(%d)", m)
	}
}

// Combination specifies how contributions are combined for a particular source.
type Combination uint8

const (
	// CombUnsupported is the zero value and the only unsupported-source sentinel.
	CombUnsupported Combination = iota
	// CombConcatenate appends incoming contributions to existing ones.
	CombConcatenate
	// CombExclusive allows exactly one contribution and rejects conflicts.
	CombExclusive
	// CombReduce deterministically folds incoming contributions in order.
	CombReduce
	// CombReplaceByIdentity replaces matching identity entries or appends new ones.
	CombReplaceByIdentity
)

func (c Combination) String() string {
	switch c {
	case CombUnsupported:
		return "unsupported"
	case CombConcatenate:
		return "concatenate"
	case CombExclusive:
		return "exclusive"
	case CombReduce:
		return "reduce"
	case CombReplaceByIdentity:
		return "replace_by_identity"
	default:
		return fmt.Sprintf("Combination(%d)", c)
	}
}

// SourceRules defines per-source combination rules for an extension plane.
type SourceRules struct {
	Feature          Combination
	Host             Combination
	GenerationBinder Combination
}

// RuleFor returns the combination rule declared for source.
func (r SourceRules) RuleFor(source SourceKind) Combination {
	switch source {
	case SourceFeature:
		return r.Feature
	case SourceHost:
		return r.Host
	case SourceGenerationBinder:
		return r.GenerationBinder
	default:
		return CombUnsupported
	}
}

// NilPolicy defines how typed-nil interface values are handled before validation and combination.
type NilPolicy uint8

const (
	// NilNotApplicable indicates nil policy is not applicable (e.g. non-pointer/non-interface types).
	NilNotApplicable NilPolicy = iota
	// NilReject causes typed nil contributions to be rejected with an error before candidate mutation.
	NilReject
	// NilSkip causes typed nil contributions to be silently ignored/skipped.
	NilSkip
)

// DiagnosticOccupant represents an occupant in the diagnostics inventory.
type DiagnosticOccupant struct {
	Label      string
	PluginID   string
	Privileges []string
}

// PrivilegeProjection represents privilege metadata projected from a plane contribution.
type PrivilegeProjection struct {
	Flags []string
}

// DiagnosticDescriptor describes how a plane's frozen values are materialized for diagnostics.
type DiagnosticDescriptor[T any] struct {
	StageID       string
	CoalesceGroup string
	Materialize   func(T) []DiagnosticOccupant
	Privileges    func(T) PrivilegeProjection
}

type generatedContributions struct{}

type generatedFrozen struct{}

type generatedAccess[T any] struct {
	contribute func(*generatedContributions, string, T) error
	get        func(*generatedFrozen) T
	identity   func(*generatedFrozen) (string, bool)
}

// Plane declares an extension plane contract with multiplicity, per-source combination rules,
// validation, and diagnostics metadata.
type Plane[T any] struct {
	ID           string
	Multiplicity Multiplicity
	Rules        SourceRules
	NilPolicy    NilPolicy
	Validate     func(v T) error
	// Combine combines an incoming contribution with current accumulated state for a source.
	// Combiners MUST NOT mutate inputs directly on failure (fail-before-mutate).
	Combine     func(source SourceKind, current, incoming T) (T, error)
	Identity    func(v T) (string, bool)
	Diagnostics DiagnosticDescriptor[T]
	generated   generatedAccess[T]
}

// PlaneDeclaration allows non-generic validation of plane declarations across a manifest.
type PlaneDeclaration interface {
	PlaneID() string
	ValidateDeclaration() error
}

// PlaneID returns the stable ID of the plane.
func (p Plane[T]) PlaneID() string {
	return p.ID
}

// ValidateDeclaration verifies that the plane declaration is well-formed, complete,
// and that multiplicity and source rules are mutually consistent.
func (p Plane[T]) ValidateDeclaration() error {
	if p.ID == "" {
		return fmt.Errorf("%w: plane ID must not be empty", ErrInvalidPlane)
	}
	if p.Multiplicity != MultOrdered && p.Multiplicity != MultExclusive {
		return fmt.Errorf("%w: plane %q: invalid multiplicity %v", ErrInvalidPlane, p.ID, p.Multiplicity)
	}

	sources := []SourceKind{SourceFeature, SourceHost, SourceGenerationBinder}
	hasSupportedSource := false

	for _, src := range sources {
		rule := p.Rules.RuleFor(src)
		switch rule {
		case CombUnsupported:
			continue
		case CombConcatenate, CombExclusive, CombReduce, CombReplaceByIdentity:
			// valid combination rule
		default:
			return fmt.Errorf("%w: plane %q: invalid combination rule %v on source %v", ErrInvalidPlane, p.ID, rule, src)
		}
		hasSupportedSource = true

		if p.Multiplicity == MultExclusive {
			if rule == CombConcatenate {
				return fmt.Errorf("%w: plane %q: exclusive plane cannot use concatenate rule on source %v", ErrInvalidPlane, p.ID, src)
			}
			if rule == CombReduce {
				return fmt.Errorf("%w: plane %q: exclusive plane cannot use reduce rule on source %v", ErrInvalidPlane, p.ID, src)
			}
		}
		if p.Multiplicity == MultOrdered {
			if rule == CombExclusive {
				return fmt.Errorf("%w: plane %q: ordered plane cannot use exclusive rule on source %v", ErrInvalidPlane, p.ID, src)
			}
		}
		if rule == CombExclusive || rule == CombReplaceByIdentity {
			if p.Identity == nil {
				return fmt.Errorf("%w: plane %q: identity extractor required for %v rule on source %v", ErrInvalidPlane, p.ID, rule, src)
			}
		}
	}

	if !hasSupportedSource {
		return fmt.Errorf("%w: plane %q: at least one source rule must be supported", ErrInvalidPlane, p.ID)
	}

	if p.Combine == nil {
		return fmt.Errorf("%w: plane %q: Combine function must not be nil", ErrInvalidPlane, p.ID)
	}

	return nil
}

// ValidateManifest checks that a collection of plane declarations contains no duplicate IDs
// and that every plane declaration is valid.
func ValidateManifest(declarations ...PlaneDeclaration) error {
	seen := make(map[string]struct{}, len(declarations))
	for _, decl := range declarations {
		if decl == nil {
			return fmt.Errorf("%w: nil plane declaration in manifest", ErrInvalidPlane)
		}
		if err := decl.ValidateDeclaration(); err != nil {
			return err
		}
		id := decl.PlaneID()
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%w: duplicate plane ID %q in manifest", ErrInvalidPlane, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}
