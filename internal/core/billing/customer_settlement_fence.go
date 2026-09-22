package billing

import (
	"context"
	"fmt"
	"strings"
)

// Customer settlement posting-time ownership fence for Task 17.3 B2b1
// (Migration Strategy step 6, customer_call_settlement only).
//
// Before any customer monetary effect, the canonical
// CustomerSettlementSourceKey is bound to exactly one owner/marker epoch via
// B1 pins. V1 default/shadow auto-acquires V1. Draining posts only previously
// classified V1 pins with matching claim metadata/epoch. v2_active fails V1
// closed (including waking leases) and requires a V2 pin/admission. Pin
// completion and journal/balance/unit/exposure/terminal outcome commit in the
// same DB transaction (store layer); exact retry returns the existing outcome
// with a completed pin; conflicting replay fails. No public money
// option/global/second writer lives here; provider/adjustment posting is out
// of scope for B2b1.
//
// This file holds small consumer-owned contracts only: no SQL, no provider
// SDKs, no globals/DI.

// CutoverClaimMetadataProvider is the narrow B2a claim port B2b workers
// consume to pass claim-time owner/epoch to posting-time validation without
// reread-only TOCTOU. DurableStore implements it; core workers type-assert to
// it.
type CutoverClaimMetadataProvider interface {
	GetCutoverClaimMetadata(ctx context.Context, kind PostingOperationKind, operationKey string) (CutoverClaimMetadata, error)
}

// ResolveCustomerSettlementOwner returns the effective pin owner for one
// customer settlement. Empty preserves the legacy V1 default.
func ResolveCustomerSettlementOwner(input ApplyCallBillingInput) (string, error) {
	owner := strings.TrimSpace(input.PostingOwner)
	if owner == "" && input.Claim != nil {
		owner = strings.TrimSpace(input.Claim.Owner)
	}
	if owner == "" {
		return PostingOwnerV1, nil
	}
	if owner != PostingOwnerV1 && owner != PostingOwnerV2 {
		return "", fmt.Errorf("%w: %w: unknown customer settlement owner %q", ErrPostingOwnershipInvalid, ErrInvalidRecord, owner)
	}
	if input.Claim != nil && strings.TrimSpace(input.Claim.Owner) != "" && input.Claim.Owner != owner {
		return "", fmt.Errorf("%w: claim owner %q differs from posting owner %q", ErrPostingOwnershipConflict, input.Claim.Owner, owner)
	}
	return owner, nil
}

// ValidateCustomerSettlementClaim checks narrow B2a claim metadata for one
// customer settlement against its canonical identity. It does not consult the
// current marker; the store compares claim epoch to the durable marker to
// fence stale workers.
func ValidateCustomerSettlementClaim(claim CutoverClaimMetadata, accountID string, callID BillingCallID) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if claim.Kind != PostingOperationCustomerSettlement {
		return fmt.Errorf("%w: %w: customer settlement claim kind %q is not %q",
			ErrPostingOwnershipInvalid, ErrInvalidRecord, string(claim.Kind), string(PostingOperationCustomerSettlement))
	}
	wantKey, err := CustomerPostingOperationKey(accountID, callID)
	if err != nil {
		return err
	}
	if claim.OperationKey != wantKey {
		return fmt.Errorf("%w: claim operation key %q does not match canonical %q",
			ErrPostingOwnershipConflict, claim.OperationKey, wantKey)
	}
	if claim.AccountID != accountID {
		return fmt.Errorf("%w: claim account %q differs from settlement account %q",
			ErrPostingOwnershipConflict, claim.AccountID, accountID)
	}
	if claim.CallID != callID {
		return fmt.Errorf("%w: claim call %q differs from settlement call %q",
			ErrPostingOwnershipConflict, claim.CallID.String(), callID.String())
	}
	return nil
}

// ValidateCustomerSettlementOperationKind fails closed when the settlement
// operation kind is not the customer settlement namespace. Provider and
// adjustment posting are later phases and must not flow through this fence.
// The zero-charge repair kind (ALegRepairKind, "customer_no_charge_repair")
// shares the same customer settlement pin identity (same sourceKey/call) and
// is therefore allowed: it closes exposure and marks processed with zero
// money, never a second monetary authority.
func ValidateCustomerSettlementOperationKind(operationKind string) error {
	kind := strings.TrimSpace(operationKind)
	if kind == "" {
		return nil
	}
	if kind != string(PostingOperationCustomerSettlement) && kind != ALegRepairKind {
		return fmt.Errorf("%w: %w: customer settlement fence rejects operation kind %q",
			ErrPostingOwnershipInvalid, ErrInvalidRecord, operationKind)
	}
	return nil
}
