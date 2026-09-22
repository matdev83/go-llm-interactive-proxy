package billing

import (
	"context"
	"fmt"
	"strings"
)

// ErrProviderCostAuthority is the legacy provider-cost monetary-boundary
// classification. It aliases the revision-era sentinel so adapters can share
// one authority vocabulary while older callers retain their existing
// errors.Is behavior.
var ErrProviderCostAuthority = ErrProviderCostRevisionAuthority

// ErrProviderCostUntrusted is a descriptive compatibility alias used by
// provider-cost callers that do not distinguish legacy and revision writers.
var ErrProviderCostUntrusted = ErrProviderCostAuthority

// ProviderCostAuthorityError is the typed authority failure returned when a
// legacy provider-cost result is not provider-authoritative. It aliases the
// existing revision error type so callers can inspect either boundary using
// errors.As without introducing a second error shape.
type ProviderCostAuthorityError = ProviderCostRevisionAuthorityError

type ApplyProviderCostInput struct {
	AccountID string
	CallID    BillingCallID
	Leg       CallLegUsageRecord
	Result    OperatorCostResult
	// PostingOwner selects the B1 pin owner for the provider charge fence.
	// Empty preserves the legacy V1 default for backward compatibility.
	// Draining requires a classified V1 pin plus matching claim metadata;
	// v2_active permits only V2.
	PostingOwner string
	// Claim carries the B2a worker-claim metadata (owner/epoch) captured at
	// claim time. When present, posting validates it against the current
	// marker and pin to close TOCTOU between claim and posting. Nil preserves
	// legacy direct calls in v1_active/shadow; draining fences unpinned/stale
	// work even without a claim, and B2b2 draining requires a matching claim
	// for new postings.
	Claim *CutoverClaimMetadata
}
type ProviderCostStore interface {
	ApplyProviderCost(context.Context, ApplyProviderCostInput) (Posting, error)
}
type ProviderCostFailureStore interface {
	MarkProviderCostUnreconciled(context.Context, ApplyProviderCostInput, string) error
}

// ValidateProviderAuthority rejects a legacy result that would otherwise be
// mistaken for provider-reported money. Local rates and estimates remain
// usable as advisory valuation results, but they cannot cross the monetary
// provider COGS boundary.
func (r OperatorCostResult) ValidateProviderAuthority() error {
	if r.Authoritative {
		return nil
	}
	return providerCostRevisionAuthorityError("authoritative", "false", "authoritative provider-reported V1 cost")
}

// ValidateProviderCostAuthority validates the result together with the
// source-qualified V1 leg that it claims to represent. The result flag is not
// enough to turn local or estimated evidence into provider-reported money.
func ValidateProviderCostAuthority(leg CallLegUsageRecord, result OperatorCostResult) error {
	if err := result.ValidateProviderAuthority(); err != nil {
		return err
	}
	if !authoritativeProviderCost(leg.Evidence) || leg.Evidence.Source != EvidenceSourceProviderReported {
		return providerCostRevisionAuthorityError("evidence", string(leg.Evidence.Source), "provider-reported authoritative V1 cost")
	}
	return nil
}

func RateProviderCost(leg CallLegUsageRecord, rates OperatorRateSet, currency string) (OperatorCostResult, error) {
	currency = strings.TrimSpace(currency)
	if currency == "" {
		return OperatorCostResult{}, fmt.Errorf("%w: provider cost currency is required", ErrRatingCurrencyMismatch)
	}
	sealed, err := leg.Seal()
	if err != nil {
		return OperatorCostResult{}, err
	}
	if authoritativeProviderCost(sealed.Evidence) {
		if sealed.Evidence.Cost.Currency != currency {
			return OperatorCostResult{}, ErrRatingCurrencyMismatch
		}
		return OperatorCostResult{LURKey: sealed.Key, Amount: Money{Nano: sealed.Evidence.Cost.NanoUnits, Currency: currency}, AmountPresent: true, Reconciled: true, Authoritative: true}, nil
	}
	if !providerAcceptedEvidence(sealed.Evidence) {
		// A never-started shell has no provider exposure and can therefore be a
		// known zero. An attempted leg with no accepted evidence is different:
		// its payable amount is unknown and must remain explicitly incomplete.
		if sealed.Outcome != LegOutcomeNeverStarted && sealed.Outcome != LegOutcomeRejected {
			reason := "provider_evidence_unavailable"
			return OperatorCostResult{LURKey: sealed.Key, Amount: Money{Currency: currency}, UnreconciledReason: reason}, fmt.Errorf("%w: %s", ErrUnreconciledCost, reason)
		}
		return OperatorCostResult{LURKey: sealed.Key, Amount: Money{Currency: currency}, AmountPresent: true, Reconciled: true}, nil
	}
	rate, found := rates.Resolve(sealed.OperatorRateRef)
	amount, reason, ok := fallbackOperatorCost(sealed, rate, found, currency)
	if !ok {
		return OperatorCostResult{LURKey: sealed.Key, Amount: Money{Currency: currency}, UnreconciledReason: reason}, fmt.Errorf("%w: %s", ErrUnreconciledCost, reason)
	}
	return OperatorCostResult{LURKey: sealed.Key, Amount: Money{Nano: amount, Currency: currency}, AmountPresent: true, Reconciled: true}, nil
}
