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

// ProviderCostWorkCutoverStore atomically acknowledges legacy work that is
// already owned by the durable revision posting authority. It lets a stale
// legacy worker retire its queue item without invoking a resolver or emitting
// an unreconciled marker after a revision has won the same B-leg lineage.
type ProviderCostWorkCutoverStore interface {
	ClaimProviderCostWorkForRevision(context.Context, ProviderCostWork) (bool, error)
}

// ProviderCostWorkStore combines reader and failure handling for provider cost work.
type ProviderCostWorkStore interface {
	ProviderCostWorkReader
	ProviderCostWorkFailureStore
}
