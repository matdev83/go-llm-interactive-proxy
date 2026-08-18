package billing

import (
	"context"
	"fmt"
	"strings"
)

type ApplyProviderCostInput struct {
	AccountID string
	CallID    BillingCallID
	Leg       CallLegUsageRecord
	Result    OperatorCostResult
}
type ProviderCostStore interface {
	ApplyProviderCost(context.Context, ApplyProviderCostInput) (Posting, error)
}
type ProviderCostFailureStore interface {
	MarkProviderCostUnreconciled(context.Context, ApplyProviderCostInput, string) error
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
		return OperatorCostResult{LURKey: sealed.Key, Amount: Money{Currency: currency}, AmountPresent: true, Reconciled: true}, nil
	}
	rate, found := rates.Resolve(sealed.OperatorRateRef)
	amount, reason, ok := fallbackOperatorCost(sealed, rate, found, currency)
	if !ok {
		return OperatorCostResult{LURKey: sealed.Key, Amount: Money{Currency: currency}, UnreconciledReason: reason}, fmt.Errorf("%w: %s", ErrUnreconciledCost, reason)
	}
	return OperatorCostResult{LURKey: sealed.Key, Amount: Money{Nano: amount, Currency: currency}, AmountPresent: true, Reconciled: true}, nil
}
