package billing

import (
	"context"
	"errors"
)

var ErrProviderCostCallUnavailable = errors.New("billing: provider-cost call closure unavailable")

type ProviderCostWork struct {
	AccountID string
	CallID    BillingCallID
	Leg       CallLegUsageRecord
}
type ProviderCostWorkReader interface {
	ListPendingProviderCostWork(context.Context, int) ([]ProviderCostWork, error)
}
type ProviderCostWorkFailureStore interface {
	DeferProviderCostWork(context.Context, ProviderCostWork, string) error
}

// ProviderCostWorkStore combines reader and failure handling for provider cost work.
type ProviderCostWorkStore interface {
	ProviderCostWorkReader
	ProviderCostWorkFailureStore
}
