package billing

import (
	"context"
	"fmt"
	"strings"
)

// Provider charge/payable posting-time ownership fence for Task 17.3 B2b2
// (Migration Strategy step 6, provider_charge only).
//
// Before any provider monetary effect (legacy provider_call_cogs journal,
// revision head/exclusion/journal, posting/execution fences, work completion),
// the canonical ProviderCostSourceKey lineage is bound to exactly one
// owner/marker epoch via B1 pins. V1 default/shadow auto-acquires V1.
// Draining posts only previously classified V1 pins with matching claim
// metadata/epoch. v2_active fails V1 closed (including waking leases) and
// requires a V2 pin/admission. Pin completion and provider effects commit in
// the same DB transaction (store layer); exact retry returns the existing
// outcome with a completed pin; conflicting replay fails. Exact nonpayable
// exclusions also complete the pin. No public money option/global/second
// writer lives here; adjustment posting is out of scope for B2b2.
//
// This file holds small consumer-owned contracts only: no SQL, no provider
// SDKs, no globals/DI.

// ResolveProviderCostOwner returns the effective pin owner for one legacy
// provider posting. Empty preserves the legacy V1 default.
func ResolveProviderCostOwner(input ApplyProviderCostInput) (string, error) {
	owner := strings.TrimSpace(input.PostingOwner)
	if owner == "" && input.Claim != nil {
		owner = strings.TrimSpace(input.Claim.Owner)
	}
	if owner == "" {
		return PostingOwnerV1, nil
	}
	if owner != PostingOwnerV1 && owner != PostingOwnerV2 {
		return "", fmt.Errorf("%w: %w: unknown provider charge owner %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, owner)
	}
	if input.Claim != nil && strings.TrimSpace(input.Claim.Owner) != "" && input.Claim.Owner != owner {
		return "", fmt.Errorf("%w: claim owner %q differs from posting owner %q", ErrPostingOwnershipConflict, input.Claim.Owner, owner)
	}
	return owner, nil
}

// ResolveProviderRevisionOwner returns the effective pin owner for one
// provider revision posting. Empty preserves the legacy V1 default.
func ResolveProviderRevisionOwner(input ProviderCostRevisionInput) (string, error) {
	owner := strings.TrimSpace(input.PostingOwner)
	if owner == "" && input.Claim != nil {
		owner = strings.TrimSpace(input.Claim.Owner)
	}
	if owner == "" {
		return PostingOwnerV1, nil
	}
	if owner != PostingOwnerV1 && owner != PostingOwnerV2 {
		return "", fmt.Errorf("%w: %w: unknown provider revision owner %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, owner)
	}
	if input.Claim != nil && strings.TrimSpace(input.Claim.Owner) != "" && input.Claim.Owner != owner {
		return "", fmt.Errorf("%w: claim owner %q differs from posting owner %q", ErrPostingOwnershipConflict, input.Claim.Owner, owner)
	}
	return owner, nil
}

// ValidateProviderCostClaim checks narrow B2a claim metadata for one legacy
// provider posting against its canonical identity (ProviderCostSourceKey of
// the sealed leg key). It does not consult the current marker; the store
// compares claim epoch to the durable marker to fence stale workers.
func ValidateProviderCostClaim(claim CutoverClaimMetadata, accountID string, callID BillingCallID, legKey string) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if claim.Kind != PostingOperationProviderCharge {
		return fmt.Errorf("%w: %w: provider charge claim kind %q is not %q",
			ErrPostingOwnershipInvalid, ErrInvalidRecord, string(claim.Kind), string(PostingOperationProviderCharge))
	}
	wantKey, err := ProviderCostSourceKey(legKey)
	if err != nil {
		return err
	}
	if claim.OperationKey != wantKey {
		return fmt.Errorf("%w: claim operation key %q does not match canonical %q",
			ErrPostingOwnershipConflict, claim.OperationKey, wantKey)
	}
	if claim.AccountID != accountID {
		return fmt.Errorf("%w: claim account %q differs from provider account %q",
			ErrPostingOwnershipConflict, claim.AccountID, accountID)
	}
	if claim.CallID != callID {
		return fmt.Errorf("%w: claim call %q differs from provider call %q",
			ErrPostingOwnershipConflict, claim.CallID.String(), callID.String())
	}
	return nil
}

// ValidateProviderRevisionClaim checks narrow B2a claim metadata for one
// provider revision posting against its canonical revision-specific identity
// derived from the immutable revision (ProviderRevisionPostingOperationKey).
// It does not consult the current marker; the store compares claim epoch to
// the durable marker to fence stale workers. F6: token operation key must
// bind the revision-specific pin, not merely B-leg lineage.
func ValidateProviderRevisionClaim(claim CutoverClaimMetadata, input ProviderCostRevisionInput) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if claim.Kind != PostingOperationProviderCharge {
		return fmt.Errorf("%w: %w: provider revision claim kind %q is not %q",
			ErrPostingOwnershipInvalid, ErrInvalidRecord, string(claim.Kind), string(PostingOperationProviderCharge))
	}
	normalized, err := input.Normalize()
	if err != nil {
		return err
	}
	storeID := strings.TrimSpace(normalized.Subject.StoreID)
	if storeID == "" {
		return fmt.Errorf("%w: %w: revision subject store scope is required for claim identity", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	wantKey, err := ProviderRevisionPostingOperationKey(normalized)
	if err != nil {
		return err
	}
	if claim.OperationKey != wantKey {
		return fmt.Errorf("%w: claim operation key %q does not match canonical %q",
			ErrPostingOwnershipConflict, claim.OperationKey, wantKey)
	}
	if claim.AccountID != normalized.AccountID {
		return fmt.Errorf("%w: claim account %q differs from revision account %q",
			ErrPostingOwnershipConflict, claim.AccountID, normalized.AccountID)
	}
	if claim.CallID != normalized.CallID {
		return fmt.Errorf("%w: claim call %q differs from revision call %q",
			ErrPostingOwnershipConflict, claim.CallID.String(), normalized.CallID.String())
	}
	return nil
}

// ValidateProviderChargeOperationKind fails closed when a provider operation
// kind is not the provider charge namespace. Customer and adjustment posting
// must not flow through this fence.
func ValidateProviderChargeOperationKind(operationKind string) error {
	kind := strings.TrimSpace(operationKind)
	if kind == "" {
		return nil
	}
	if kind != string(PostingOperationProviderCharge) {
		return fmt.Errorf("%w: %w: provider charge fence rejects operation kind %q",
			ErrPostingOwnershipInvalid, ErrInvalidRecord, operationKind)
	}
	return nil
}

// ProviderChargeClaimInput carries the resolved owner/claim pair a worker
// passes to the durable posting seam.
type ProviderChargeClaimInput struct {
	Owner string
	Claim *CutoverClaimMetadata
}

// ProviderCostWorkClaimStore is the narrow claim port workers consume to pass
// claim-time owner/epoch to posting-time validation. DurableStore implements
// it via GetCutoverClaimMetadata; test doubles may implement it explicitly.
// Production workers must be constructed with a non-nil claim provider (see
// NewCallProviderCostWorkerWithClaim); the legacy constructor remains
// test-only.
type ProviderCostWorkClaimStore interface {
	GetCutoverClaimMetadata(ctx context.Context, kind PostingOperationKind, operationKey string) (CutoverClaimMetadata, error)
}
