package billing

import (
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

// ErrInvalidBindingScope identifies a missing or malformed
// composition-time economics identity.
var ErrInvalidBindingScope = errors.New("billing: invalid binding scope")

// BindingScope carries the composition-time economics identity owned by the
// binding: the trusted store and the frozen tariff and policy snapshots the
// host binding translates into quote, admission, credit-screen, and terminal
// DTOs. Adapters never invent these identities; the binding declares them.
type BindingScope struct {
	StoreID string                      `json:"store_id"`
	Tariff  economics.RatingSnapshotRef `json:"tariff"`
	Policy  economics.PolicySnapshotRef `json:"policy"`
}

// Validate requires the store identity and the frozen snapshot references.
func (s BindingScope) Validate() error {
	if err := validateRef("binding store_id", s.StoreID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBindingScope, err)
	}
	if err := validateSnapshotRef("binding tariff", s.Tariff.ID, s.Tariff.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBindingScope, err)
	}
	if err := validateSnapshotRef("binding policy", s.Policy.ID, s.Policy.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBindingScope, err)
	}
	if err := validateRef("binding policy_id", s.Policy.PolicyID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBindingScope, err)
	}
	return nil
}

// Clone returns an independent copy of the scope.
func (s BindingScope) Clone() BindingScope { return s }
