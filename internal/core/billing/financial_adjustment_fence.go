package billing

import (
	"context"
	"fmt"
	"strings"
)

// Financial adjustment posting-time ownership fence for Task 17.3 B2b3/B2b4
// (Migration Strategy step 6, financial_adjustment only).
//
// Before any selected-cost monetary effect (journal delta, head CAS,
// adjustment/link rows), the canonical head pin
// FinancialAdjustmentPostingOperationKey(store, account, call, head, subject)
// is bound to exactly one owner/marker epoch via B1 pins. B2b4 extends the
// same kind to the remaining synchronous writers without fake B-leg/head:
// cost pass-through per-head pins
// CostPassThroughFinancialAdjustmentPostingOperationKey(store, account, call)
// and direct per-source pins
// DirectFinancialAdjustmentPostingOperationKey(store, account, source). V1
// default/shadow auto-acquires V1. Draining posts only previously classified
// V1 pins with matching claim metadata/epoch (sync: new blocked, exact
// completed replay allowed). v2_active fails V1 closed (including waking
// leases) and requires a V2 pin/admission. Pin completion and adjustment
// journal/head/linkage effects commit in the same DB transaction (store
// layer); exact retry returns the existing outcome with a completed pin;
// conflicting replay fails. Replacement revisions share one head pin
// authority: each revision keeps its own canonical operation/link identity
// while the pin completion advances to the latest outcome under the same
// owner. Exact nonpayable exclusions (NoOp) also complete the pin. No public
// money option/global/second writer lives here; customer/provider posting is
// out of scope for B2b3/B2b4.
//
// This file holds small consumer-owned contracts only: no SQL, no provider
// SDKs, no globals/DI.

// ResolveFinancialAdjustmentOwner returns the effective pin owner for one
// selected-cost adjustment. Empty preserves the legacy V1 default.
func ResolveFinancialAdjustmentOwner(input SelectedCostAdjustmentInput) (string, error) {
	owner := strings.TrimSpace(input.PostingOwner)
	if owner == "" && input.Claim != nil {
		owner = strings.TrimSpace(input.Claim.Owner)
	}
	if owner == "" {
		return PostingOwnerV1, nil
	}
	if owner != PostingOwnerV1 && owner != PostingOwnerV2 {
		return "", fmt.Errorf("%w: %w: unknown financial adjustment owner %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, owner)
	}
	if input.Claim != nil && strings.TrimSpace(input.Claim.Owner) != "" && input.Claim.Owner != owner {
		return "", fmt.Errorf("%w: claim owner %q differs from posting owner %q", ErrPostingOwnershipConflict, input.Claim.Owner, owner)
	}
	return owner, nil
}

// ResolveCostPassThroughAdjustmentOwner returns the effective pin owner for
// one synchronous cost pass-through revision. Empty preserves legacy V1.
func ResolveCostPassThroughAdjustmentOwner(input CostPassThroughRevisionInput) (string, error) {
	owner := strings.TrimSpace(input.PostingOwner)
	if owner == "" && input.Claim != nil {
		owner = strings.TrimSpace(input.Claim.Owner)
	}
	if owner == "" {
		return PostingOwnerV1, nil
	}
	if owner != PostingOwnerV1 && owner != PostingOwnerV2 {
		return "", fmt.Errorf("%w: %w: unknown cost pass-through owner %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, owner)
	}
	if input.Claim != nil && strings.TrimSpace(input.Claim.Owner) != "" && input.Claim.Owner != owner {
		return "", fmt.Errorf("%w: claim owner %q differs from posting owner %q", ErrPostingOwnershipConflict, input.Claim.Owner, owner)
	}
	return owner, nil
}

// ResolveDirectAdjustmentOwner returns the effective pin owner for one
// synchronous direct adjustment. Empty preserves legacy V1.
func ResolveDirectAdjustmentOwner(input AdjustmentInput) (string, error) {
	owner := strings.TrimSpace(input.PostingOwner)
	if owner == "" && input.Claim != nil {
		owner = strings.TrimSpace(input.Claim.Owner)
	}
	if owner == "" {
		return PostingOwnerV1, nil
	}
	if owner != PostingOwnerV1 && owner != PostingOwnerV2 {
		return "", fmt.Errorf("%w: %w: unknown direct adjustment owner %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, owner)
	}
	if input.Claim != nil && strings.TrimSpace(input.Claim.Owner) != "" && input.Claim.Owner != owner {
		return "", fmt.Errorf("%w: claim owner %q differs from posting owner %q", ErrPostingOwnershipConflict, input.Claim.Owner, owner)
	}
	return owner, nil
}

// ValidateFinancialAdjustmentClaim checks narrow B2a claim metadata for one
// selected-cost adjustment against its canonical head identity derived from
// the normalized input. It does not consult the current marker; the store
// compares claim epoch to the durable marker to fence stale workers.
func ValidateFinancialAdjustmentClaim(claim CutoverClaimMetadata, input SelectedCostAdjustmentInput) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if claim.Kind != PostingOperationFinancialAdjustment {
		return fmt.Errorf("%w: %w: financial adjustment claim kind %q is not %q",
			ErrPostingOwnershipInvalid, ErrInvalidRecord, string(claim.Kind), string(PostingOperationFinancialAdjustment))
	}
	normalized, err := input.Normalize()
	if err != nil {
		return err
	}
	storeID := strings.TrimSpace(normalized.Subject.StoreID)
	if storeID == "" {
		return fmt.Errorf("%w: %w: adjustment subject store scope is required for claim identity", ErrPostingOwnershipInvalid, ErrInvalidRecord)
	}
	wantKey, err := FinancialAdjustmentPostingOperationKey(storeID, normalized.AccountID, normalized.CallID, normalized.HeadKey, normalized.Subject)
	if err != nil {
		return err
	}
	if claim.OperationKey != wantKey {
		return fmt.Errorf("%w: claim operation key %q does not match canonical %q",
			ErrPostingOwnershipConflict, claim.OperationKey, wantKey)
	}
	if claim.AccountID != normalized.AccountID {
		return fmt.Errorf("%w: claim account %q differs from adjustment account %q",
			ErrPostingOwnershipConflict, claim.AccountID, normalized.AccountID)
	}
	if claim.CallID != normalized.CallID {
		return fmt.Errorf("%w: claim call %q differs from adjustment call %q",
			ErrPostingOwnershipConflict, claim.CallID.String(), normalized.CallID.String())
	}
	return nil
}

// ValidateCostPassThroughAdjustmentClaim checks narrow B2a claim metadata for
// one cost pass-through revision against its canonical per-head pin identity
// (store/account/call/head). It does not consult the current marker; the
// store compares claim epoch to the durable marker to fence stale workers.
func ValidateCostPassThroughAdjustmentClaim(claim CutoverClaimMetadata, storeID, accountID string, callID BillingCallID) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if claim.Kind != PostingOperationFinancialAdjustment {
		return fmt.Errorf("%w: %w: cost pass-through claim kind %q is not %q",
			ErrPostingOwnershipInvalid, ErrInvalidRecord, string(claim.Kind), string(PostingOperationFinancialAdjustment))
	}
	wantKey, err := CostPassThroughFinancialAdjustmentPostingOperationKey(storeID, accountID, callID)
	if err != nil {
		return err
	}
	if claim.OperationKey != wantKey {
		return fmt.Errorf("%w: claim operation key %q does not match canonical %q",
			ErrPostingOwnershipConflict, claim.OperationKey, wantKey)
	}
	if claim.AccountID != strings.TrimSpace(accountID) {
		return fmt.Errorf("%w: claim account %q differs from adjustment account %q",
			ErrPostingOwnershipConflict, claim.AccountID, accountID)
	}
	if claim.CallID != callID {
		return fmt.Errorf("%w: claim call %q differs from adjustment call %q",
			ErrPostingOwnershipConflict, claim.CallID.String(), callID.String())
	}
	return nil
}

// ValidateDirectAdjustmentClaim checks narrow B2a claim metadata for one
// direct adjustment against its canonical per-source pin identity
// (store/account/source). It does not consult the current marker; the store
// compares claim epoch to the durable marker to fence stale workers.
func ValidateDirectAdjustmentClaim(claim CutoverClaimMetadata, storeID, accountID, sourceKey string) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if claim.Kind != PostingOperationFinancialAdjustment {
		return fmt.Errorf("%w: %w: direct adjustment claim kind %q is not %q",
			ErrPostingOwnershipInvalid, ErrInvalidRecord, string(claim.Kind), string(PostingOperationFinancialAdjustment))
	}
	wantKey, err := DirectFinancialAdjustmentPostingOperationKey(storeID, accountID, sourceKey)
	if err != nil {
		return err
	}
	if claim.OperationKey != wantKey {
		return fmt.Errorf("%w: claim operation key %q does not match canonical %q",
			ErrPostingOwnershipConflict, claim.OperationKey, wantKey)
	}
	if claim.AccountID != strings.TrimSpace(accountID) {
		return fmt.Errorf("%w: claim account %q differs from adjustment account %q",
			ErrPostingOwnershipConflict, claim.AccountID, accountID)
	}
	// Direct claims carry no call lineage; empty CallID is canonical.
	if strings.TrimSpace(claim.CallID.String()) != "" {
		return fmt.Errorf("%w: direct adjustment claim must carry no call lineage",
			ErrPostingOwnershipConflict)
	}
	return nil
}

// ValidateFinancialAdjustmentOperationKind fails closed when an adjustment
// operation kind is not the financial adjustment namespace. Customer and
// provider posting must not flow through this fence.
func ValidateFinancialAdjustmentOperationKind(operationKind string) error {
	kind := strings.TrimSpace(operationKind)
	if kind == "" {
		return nil
	}
	if kind != string(PostingOperationFinancialAdjustment) {
		return fmt.Errorf("%w: %w: financial adjustment fence rejects operation kind %q",
			ErrPostingOwnershipInvalid, ErrInvalidRecord, operationKind)
	}
	return nil
}

// FinancialAdjustmentClaimStore is the narrow claim port workers consume to
// pass claim-time owner/epoch to posting-time validation. DurableStore
// implements it via GetCutoverClaimMetadata; test doubles may implement it
// explicitly. Selected-cost adjustments are synchronous nonqueued commands:
// they validate the current marker at execution; any future queued adjustment
// worker must be constructed with a non-nil claim provider so missing metadata
// fails fast instead of silently bypassing the fence.
type FinancialAdjustmentClaimStore interface {
	GetCutoverClaimMetadata(ctx context.Context, kind PostingOperationKind, operationKey string) (CutoverClaimMetadata, error)
}
