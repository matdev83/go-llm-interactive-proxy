package billingstore

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/uptrace/bun"
)

// economicClaimGateAllows reports whether one economic queue claim may proceed
// under the current cutover marker. Evidence-only work (customer rating,
// reconciliation, shadow, no-adapter queues) is always operable and never
// consults pins. Monetary provider work:
//
//   - v1_active/v2_shadow (or absent marker): allowed (legacy, posting
//     auto-acquires V1 at posting time).
//   - v1_draining: allowed only with a classified V1 pin (pre-boundary work
//     claimable with metadata and can finish; new V1 was fenced at enqueue).
//     Unpinned monetary work skips without consuming an attempt until
//     classification pins it.
//   - v2_active: V1 (including waking leases) fenced/skipped; explicit V2
//     monetary work allowed (posting acquires V2 pins, authorized).
//
// Skips return (false, nil) so workers poll without error spam; operational
// failures return errors. Pin epoch staleness is fenced at posting time via
// claim metadata vs marker/pin comparison (renewable authority through
// classification refresh + fresh GetCutoverClaimMetadata fetch), not here.
func (s *DurableStore) economicClaimGateAllows(ctx context.Context, tx bun.IDB, work billing.EconomicRevisionWork, state economicRevisionWorkStateRow) (bool, error) {
	normalized, err := work.Normalize()
	if err != nil {
		return false, err
	}
	// R2 authoritative intent: monetary shape plus mutable delivery state
	// decides. Payload EvidenceOnly/owner alone never decides; upgraded
	// shadow (payload evidence-only, state monetary) is monetary here.
	shape := billing.IsMonetaryEconomicShape(normalized)
	if !shape {
		return true, nil
	}
	authoritativeMonetary := state.ProviderPosting == 1
	if !authoritativeMonetary {
		// Monetary shape but evidence delivery (shadow/pure, state 0):
		// always operable, never pinned/fenced.
		return true, nil
	}
	authoritative := billing.OverlayAuthoritativeEconomicWork(normalized, true, state.PostingOwner)
	marker, err := s.GetAccountingCutover(ctx)
	if err != nil {
		if errors.Is(err, billing.ErrAccountingCutoverNotFound) {
			return true, nil
		}
		return false, err
	}
	switch marker.State {
	case billing.AccountingCutoverV1Active, billing.AccountingCutoverV2Shadow:
		return true, nil
	case billing.AccountingCutoverV1Draining:
		_, _, pinKey, err := billing.MonetaryEconomicPostingKey(s.storeID, authoritative)
		if err != nil {
			return false, nil
		}
		var pin billing.PostingPin
		if tx != nil {
			row, found, lerr := s.loadPostingOwnershipPin(ctx, tx, billing.PostingOperationProviderCharge, pinKey)
			if lerr != nil || !found {
				return false, nil
			}
			pin, err = postingOwnershipRowToPin(row)
			if err != nil {
				return false, nil
			}
		} else {
			pin, err = s.getPostingPinCommitted(ctx, pinKey)
			if err != nil {
				return false, nil
			}
		}
		if pin.Owner != billing.PostingOwnerV1 {
			return false, nil
		}
		return true, nil
	case billing.AccountingCutoverV2Active:
		owner := state.PostingOwner
		if owner == "" {
			// Legacy-inferred V1 monetary work.
			return false, nil
		}
		if owner == billing.PostingOwnerV2 {
			if !billing.IsV2NewWorkAuthorized(marker.State) {
				return false, nil
			}
			return true, nil
		}
		return false, nil
	default:
		return false, nil
	}
}

func (s *DurableStore) getPostingPinCommitted(ctx context.Context, pinKey string) (billing.PostingPin, error) {
	return s.GetPostingPin(ctx, billing.PostingOperationProviderCharge, pinKey)
}
