package billing

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Renewable mandatory claim authority for Phase 17.3 F6+F8 (Migration Strategy
// step 6).
//
// Posting pin owner/identity is stable historical ownership. Its acquisition
// epoch is NOT a forever lease token. Each actual durable claim/lease
// operation atomically issues a current-marker CutoverClaimMetadata token
// bound to operation key, owner, marker version/epoch/state and the claimed
// work/lease identity:
//
//   - draining: only classified V1 pins receive renewal (stable owner, current epoch).
//   - active: V1 receives none (fenced); V2 eligible work receives V2.
//   - pre-drain (v1_active/v2_shadow): V1 receives current-epoch renewal.
//
// Workers receive the token as part of the claimed item/result, not via an
// optional lookup after claim. Posting validates exact token/current marker
// and pin owner; a stale token fences; re-claim/renew under allowed current
// state succeeds without rewriting historical owner.
//
// Operational errors, cancellation, malformed metadata and missing required
// tokens fail closed. They are never swallowed into nil/default owner.
// Completed historical replay may be read without a live lease only when the
// exact immutable operation outcome proves replay; new money always needs
// current authorization. Synchronous adjustment commands use the current
// marker lock directly and do not need a lease token.
//
// No SQL, no provider SDKs, no globals/DI, no stored contexts; small
// consumer-owned contracts only.

// ClaimedCompleteCall pairs one durably claimed complete call with its
// current-marker cutover token. The token is issued atomically with the
// claim-status transition; it is not an optional post-claim lookup.
type ClaimedCompleteCall struct {
	Call  CompleteCall
	Claim CutoverClaimMetadata
}

// Validate fails closed when the claimed call or its token is malformed or
// cross-wired.
func (c ClaimedCompleteCall) Validate() error {
	if err := c.Claim.Validate(); err != nil {
		return err
	}
	if err := ValidateCustomerSettlementClaim(c.Claim, c.Call.Closure.AccountID, c.Call.Closure.CallID); err != nil {
		return err
	}
	return nil
}

// ClaimedProviderCostWork pairs one pending B-leg with its current-marker
// cutover token. The token is issued atomically with the pending-work claim;
// it is not an optional post-list lookup.
type ClaimedProviderCostWork struct {
	Work  ProviderCostWork
	Claim CutoverClaimMetadata
}

// Validate fails closed when the work/token pair is malformed or cross-wired.
func (c ClaimedProviderCostWork) Validate() error {
	if err := c.Claim.Validate(); err != nil {
		return err
	}
	sealed, err := c.Work.Leg.Seal()
	if err != nil {
		return err
	}
	if err := ValidateProviderCostClaim(c.Claim, c.Work.AccountID, c.Work.CallID, sealed.Key); err != nil {
		return err
	}
	return nil
}

// ClaimedEconomicRevisionWork pairs one leased economic revision with its
// cutover token for monetary provider posting. Evidence-only work carries a
// nil Cutover (no monetary authority); monetary provider work carries a
// non-nil current-marker token issued atomically with the work lease.
type ClaimedEconomicRevisionWork struct {
	Work      EconomicRevisionWork
	WorkClaim EconomicRevisionWorkClaim
	Cutover   *CutoverClaimMetadata
}

// ClaimedCompleteCallClaimer is the production token-carrying claim port for
// customer settlement. Implementations must atomically claim the call and
// issue a current-marker token; they must fail closed on operational,
// cancellation, malformed or ineligible-renewal errors, never returning a
// nil/default token for draining/active pinned work.
type ClaimedCompleteCallClaimer interface {
	ClaimCompleteCallsWithCutover(ctx context.Context, limit int) ([]ClaimedCompleteCall, error)
}

// ClaimedProviderCostWorkClaimer is the production token-carrying claim port
// for legacy provider work. Implementations must atomically list eligible
// pending work and issue current-marker tokens; they must fail closed on
// operational/cancellation/malformed errors and withhold ineligible work
// (unpinned in draining, V1 in active) without inventing tokens.
type ClaimedProviderCostWorkClaimer interface {
	ClaimProviderCostWorkWithCutover(ctx context.Context, limit int) ([]ClaimedProviderCostWork, error)
}

// EconomicRevisionWorkCutoverClaimer is the production token-carrying lease
// port for monetary economic revisions. Implementations must atomically lease
// the work (owner/fence) and issue a current-marker cutover token for
// monetary provider work; evidence-only work returns a nil cutover. They must
// fail closed on operational/cancellation/malformed errors and withhold
// ineligible monetary work without consuming attempts.
type EconomicRevisionWorkCutoverClaimer interface {
	ClaimEconomicRevisionWorkWithCutover(ctx context.Context, work EconomicRevisionWork, owner string, lease time.Duration) (EconomicRevisionWorkClaim, *CutoverClaimMetadata, bool, error)
}

// CutoverClaimTokenForPinAtMarker builds the renewable current-marker token
// for one stable pin. Owner/identity come from the pin (historical ownership);
// version/epoch/state come from the current marker (lease authority). It does
// not rewrite the pin. It fails closed on malformed pins/markers.
func CutoverClaimTokenForPinAtMarker(pin PostingPin, marker AccountingCutoverMarker) (CutoverClaimMetadata, error) {
	if err := pin.Validate(); err != nil {
		return CutoverClaimMetadata{}, err
	}
	if err := marker.Validate(); err != nil {
		return CutoverClaimMetadata{}, err
	}
	tok := CutoverClaimMetadata{
		Kind:          pin.Kind,
		OperationKey:  pin.OperationKey,
		AccountID:     pin.AccountID,
		CallID:        pin.CallID,
		Owner:         pin.Owner,
		MarkerVersion: marker.Version,
		MarkerEpoch:   marker.Epoch,
		MarkerState:   marker.State,
	}
	if err := tok.Validate(); err != nil {
		return CutoverClaimMetadata{}, fmt.Errorf("%w: renewed claim token: %v", ErrCutoverCoordinatorInvalid, err)
	}
	return tok, nil
}

// CutoverClaimTokenForEconomicLease builds the mandatory current-marker token
// for one monetary economic lease. Pin owner/identity come from the pin,
// version/epoch/state come from the locked current marker snapshot, and the
// work/lease fence come from the atomically acquired lease claim. All three
// authorities commit in the same transaction; the token is never issued for a
// different marker than the lease. It fails closed on malformed inputs.
func CutoverClaimTokenForEconomicLease(pin PostingPin, marker AccountingCutoverMarker, workID string, lease EconomicRevisionWorkClaim) (CutoverClaimMetadata, error) {
	if err := pin.Validate(); err != nil {
		return CutoverClaimMetadata{}, err
	}
	if err := marker.Validate(); err != nil {
		return CutoverClaimMetadata{}, err
	}
	if strings.TrimSpace(workID) == "" {
		return CutoverClaimMetadata{}, fmt.Errorf("%w: economic lease work identity is required", ErrCutoverCoordinatorInvalid)
	}
	if strings.TrimSpace(lease.Owner) == "" || lease.Fence == 0 {
		return CutoverClaimMetadata{}, fmt.Errorf("%w: economic lease claim is required", ErrCutoverCoordinatorInvalid)
	}
	tok := CutoverClaimMetadata{
		Kind:          pin.Kind,
		OperationKey:  pin.OperationKey,
		AccountID:     pin.AccountID,
		CallID:        pin.CallID,
		Owner:         pin.Owner,
		MarkerVersion: marker.Version,
		MarkerEpoch:   marker.Epoch,
		MarkerState:   marker.State,
		WorkID:        strings.TrimSpace(workID),
		LeaseOwner:    strings.TrimSpace(lease.Owner),
		LeaseFence:    lease.Fence,
	}
	if err := tok.Validate(); err != nil {
		return CutoverClaimMetadata{}, fmt.Errorf("%w: economic lease token: %v", ErrCutoverCoordinatorInvalid, err)
	}
	return tok, nil
}

// IsCutoverTokenCurrentForMarker reports whether tok exactly matches the
// current marker authority (version/epoch/state). Owner/identity checks belong
// to the posting fence; this helper isolates the staleness comparison so
// workers and stores share one definition of "stale".
func IsCutoverTokenCurrentForMarker(tok CutoverClaimMetadata, marker AccountingCutoverMarker) bool {
	return tok.MarkerVersion == marker.Version && tok.MarkerEpoch == marker.Epoch && tok.MarkerState == marker.State
}
