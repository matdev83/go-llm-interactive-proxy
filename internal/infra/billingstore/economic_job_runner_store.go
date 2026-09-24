package billingstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
)

var (
	_ billing.EconomicRevisionDependencyOutputReader = (*DurableStore)(nil)
	_ billing.EconomicRevisionReconciliationStore    = (*DurableStore)(nil)
)

// LoadEconomicRevisionDependencyOutput reads one immutable dependency rating
// output by its exact revision valuation identity. A missing output is the
// retryable dependency_pending condition, not a zero valuation.
func (s *DurableStore) LoadEconomicRevisionDependencyOutput(ctx context.Context, dependency billing.EconomicJobDependency) (economics.Valuation, error) {
	if err := s.validateContext(ctx); err != nil {
		return economics.Valuation{}, err
	}
	identity, err := dependency.OutputIdentity()
	if err != nil {
		return economics.Valuation{}, err
	}
	valuation, err := s.GetValuation(ctx, identity.ValuationKey(), economics.ValuationVersionV2)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return economics.Valuation{}, fmt.Errorf("%w: %q", billing.ErrEconomicRevisionDependencyOutputMissing, identity.Key())
		}
		return economics.Valuation{}, err
	}
	return valuation, nil
}

// AppendEconomicRevisionReconciliation persists the immutable reconciliation
// output of dependency-anchored provider-queue work. The operation is
// idempotent for an exact replay and fails closed with
// ErrEconomicRevisionConflict for a changed payload under the same identity.
// No account, journal, exposure or financial head row is touched.
func (s *DurableStore) AppendEconomicRevisionReconciliation(ctx context.Context, work billing.EconomicRevisionWork, reconciliation billing.EconomicReconciliation) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	normalizedWork, err := work.Normalize()
	if err != nil {
		return err
	}
	if normalizedWork.Kind != billing.EconomicWorkKindReconciliation {
		return fmt.Errorf("%w: reconciliation output requires reconciliation work", billing.ErrInvalidEconomicRevision)
	}
	identity, err := normalizedWork.Identity()
	if err != nil {
		return err
	}
	normalized, err := reconciliation.Normalize()
	if err != nil {
		return err
	}
	if normalized.ID != identity.ReconciliationKey() {
		return fmt.Errorf("%w: reconciliation id got=%q want=%q", billing.ErrEconomicRevisionInputMismatch, normalized.ID, identity.ReconciliationKey())
	}
	if normalized.Version != identity.EvidenceRevision {
		return fmt.Errorf("%w: reconciliation version got=%d want=%d", billing.ErrEconomicRevisionInputMismatch, normalized.Version, identity.EvidenceRevision)
	}
	if !sameEconomicSubject(normalized.Subject, normalizedWork.Subject) {
		return fmt.Errorf("%w: reconciliation subject", billing.ErrEconomicRevisionSubjectMismatch)
	}
	if normalized.Basis != normalizedWork.Input.Basis {
		return fmt.Errorf("%w: reconciliation basis got=%q want=%q", billing.ErrEconomicRevisionBasisMismatch, normalized.Basis, normalizedWork.Input.Basis)
	}
	if normalized.InputSetHash != normalizedWork.InputSetHash {
		return fmt.Errorf("%w: reconciliation hash got=%q want=%q", billing.ErrEconomicRevisionInputMismatch, normalized.InputSetHash, normalizedWork.InputSetHash)
	}
	record := ReconciliationRecord{
		ID: normalized.ID, Version: normalized.Version, Subject: normalized.Subject,
		Scope: normalized.Scope, Basis: normalized.Basis, InputSetHash: normalized.InputSetHash,
		LocalInputHash: normalized.LocalInputHash, ProviderInputHash: normalized.ProviderInputHash,
		PolicyID: normalized.PolicyID, PolicyVersion: normalized.PolicyVersion,
		ResultJSON: append(json.RawMessage(nil), normalized.ResultJSON...), CreatedAt: normalized.CreatedAt,
	}
	if err := s.AppendReconciliation(ctx, record); err != nil {
		if errors.Is(err, ErrIdentityConflict) {
			return fmt.Errorf("%w: reconciliation: %v", billing.ErrEconomicRevisionConflict, err)
		}
		return err
	}
	return nil
}

// HasEconomicRevisionReconciliation probes whether the immutable
// reconciliation output for a dependency-anchored revision already exists. It
// lets an interrupted worker finish the claim without recomputing.
func (s *DurableStore) HasEconomicRevisionReconciliation(ctx context.Context, identity billing.EconomicRevisionIdentity) (bool, error) {
	if err := s.validateContext(ctx); err != nil {
		return false, err
	}
	validated := identity
	if err := validated.Validate(); err != nil {
		return false, err
	}
	var count int
	if err := s.db.NewRaw(`SELECT COUNT(1) FROM billing_reconciliations WHERE store_id = ? AND reconciliation_id = ? AND reconciliation_version = ?`,
		s.storeID, validated.ReconciliationKey(), int64(validated.EvidenceRevision)).Scan(ctx, &count); err != nil {
		return false, fmt.Errorf("billingstore: probe economic revision reconciliation: %w", err)
	}
	return count != 0, nil
}
