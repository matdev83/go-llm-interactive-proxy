package billingcompose

import (
	"fmt"
	"slices"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// PutOperatorCostSelectionPolicy freezes one versioned operator cost selection
// policy in the catalog. Exact replay is idempotent; a changed payload under an
// existing identity is rejected.
func (c *SnapshotCatalog) PutOperatorCostSelectionPolicy(policy billing.OperatorCostSelectionPolicy) error {
	if c == nil {
		return errNilSnapshotCatalog
	}
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("billingcompose: operator cost selection policy: %w", err)
	}
	key := keyOf(policy.Ref)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.selectionPolicies[key]; ok {
		if operatorCostSelectionPolicyReplayEqual(existing, policy) {
			return nil
		}
		return ErrSnapshotImmutable
	}
	c.selectionPolicies[key] = policy.Clone()
	return nil
}

// OperatorCostSelectionPolicy returns the frozen policy for ref, or
// ErrSnapshotNotFound. The returned value is caller-owned.
func (c *SnapshotCatalog) OperatorCostSelectionPolicy(ref billing.VersionRef) (billing.OperatorCostSelectionPolicy, error) {
	if c == nil {
		return billing.OperatorCostSelectionPolicy{}, errNilSnapshotCatalog
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	policy, ok := c.selectionPolicies[keyOf(ref)]
	if !ok {
		return billing.OperatorCostSelectionPolicy{}, fmt.Errorf("%w: operator cost selection policy", ErrSnapshotNotFound)
	}
	return policy.Clone(), nil
}

func operatorCostSelectionPolicyReplayEqual(a, b billing.OperatorCostSelectionPolicy) bool {
	return a.Version == b.Version &&
		keyOf(a.Ref) == keyOf(b.Ref) &&
		slices.Equal(a.Rules, b.Rules) &&
		slices.Equal(a.KnownZeroProvenance, b.KnownZeroProvenance)
}
