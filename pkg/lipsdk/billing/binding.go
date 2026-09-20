package billing

import (
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

const (
	// BindingVersionV1 is the first version of the external billing binding.
	BindingVersionV1 uint32 = 1
)

var (
	// ErrInvalidBinding identifies a malformed binding envelope.
	ErrInvalidBinding = errors.New("billing: invalid binding")
	// ErrInvalidBindingID identifies a missing or malformed binding ID.
	ErrInvalidBindingID = errors.New("billing: invalid binding ID")
	// ErrUnsupportedBindingVersion identifies an unknown binding version.
	ErrUnsupportedBindingVersion = errors.New("billing: unsupported binding version")
	// ErrMissingCreditScreen identifies an absent credit-screen port.
	ErrMissingCreditScreen = errors.New("billing: credit screen port is required")
	// ErrMissingQuoter identifies an absent quote port.
	ErrMissingQuoter = errors.New("billing: quoter port is required")
	// ErrMissingAdmission identifies an absent exposure-admission port.
	ErrMissingAdmission = errors.New("billing: exposure admission port is required")
	// ErrMissingTerminal identifies an absent terminal handoff port.
	ErrMissingTerminal = errors.New("billing: terminal port is required")
	// ErrDuplicateMonetaryBinding rejects a second monetary binding. Ordinary
	// non-money authority registrations may coexist for distinct quotas, but
	// they must never create a second monetary authority.
	ErrDuplicateMonetaryBinding = errors.New("billing: duplicate monetary binding")
)

// Binding is the minimal typed external monetary host binding. It carries a
// stable identity and version, complete cheap-screen/quote-admit/terminal
// ports, and explicit owned-resource lifecycle registration. Public rater and
// statement/reconciliation ports stay separate in pkg/lipsdk/economics;
// workers consume them after terminal persistence, not through this binding.
type Binding struct {
	ID           string
	Version      uint32
	Scope        BindingScope
	CreditScreen CreditScreener
	Quoter       economics.Quoter
	Admission    ExposureAdmitter
	Terminal     TerminalSink
	Lifecycle    Lifecycle
}

// IsMonetary reports that the binding owns the monetary authority. It lets a
// future host composition reject a second monetary binding while ordinary
// non-money authority registrations coexist for distinct quotas.
func (b Binding) IsMonetary() bool { return true }

// Validate rejects incomplete bindings before publication: invalid ID or
// version, nil or typed-nil monetary ports, and invalid lifecycle
// declarations.
func (b Binding) Validate() error {
	if err := validateRef("binding id", b.ID, MaxBindingIDLength); err != nil {
		return fmt.Errorf("%w: %v: %w", ErrInvalidBinding, err, ErrInvalidBindingID)
	}
	if b.Version != BindingVersionV1 {
		return fmt.Errorf("%w: version %d: %w", ErrInvalidBinding, b.Version, ErrUnsupportedBindingVersion)
	}
	if err := b.Scope.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, err)
	}
	if isNilValue(b.CreditScreen) {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, ErrMissingCreditScreen)
	}
	if isNilValue(b.Quoter) {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, ErrMissingQuoter)
	}
	if isNilValue(b.Admission) {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, ErrMissingAdmission)
	}
	if isNilValue(b.Terminal) {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, ErrMissingTerminal)
	}
	if err := b.Lifecycle.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidBinding, err)
	}
	return nil
}

// ValidateBindings validates each candidate binding and enforces the
// single-monetary-authority rule: at most one monetary binding may be
// published. An empty set is valid and keeps the host non-money.
func ValidateBindings(bindings []Binding) error {
	monetary := 0
	for i, binding := range bindings {
		if err := binding.Validate(); err != nil {
			return fmt.Errorf("billing: binding %d: %w", i, err)
		}
		if binding.IsMonetary() {
			monetary++
		}
	}
	if monetary > 1 {
		return fmt.Errorf("%w: %d monetary bindings", ErrDuplicateMonetaryBinding, monetary)
	}
	return nil
}
