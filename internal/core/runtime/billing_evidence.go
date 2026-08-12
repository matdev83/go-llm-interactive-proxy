package runtime

import (
	"context"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
)

// billingHandoffTimeout bounds detached TUR handoff work (barrier wait + persist).
// It must exceed parallel loser cleanup (cancelLosersTimeout) plus per-leg
// FinalizeBilling budgets so client cancellation cannot strand sealed money.
var billingHandoffTimeout = 2 * time.Minute

// billingFinalizeTimeout is the per-leg FinalizeBilling observation budget.
const billingFinalizeTimeout = 2 * time.Second

func (e *Executor) addBillingEvidence(ctx context.Context, record billing.LegUsageRecord) {
	if e == nil {
		return
	}
	e.billingTurns().record(ctx, record)
}

func (e *Executor) claimBillingEvidence(aLegID string) []billing.LegUsageRecord {
	if e == nil {
		return nil
	}
	return e.billingTurns().claim(aLegID)
}

func (e *Executor) restoreBillingEvidence(aLegID string, legs []billing.LegUsageRecord) {
	if e == nil {
		return
	}
	e.billingTurns().restore(aLegID, legs)
}

func (e *Executor) peekBillingEvidence(aLegID string) []billing.LegUsageRecord {
	if e == nil {
		return nil
	}
	return e.billingTurns().peek(aLegID)
}

func (e *Executor) beginBillingEvidenceBarrier(aLegID string) (complete func()) {
	if e == nil {
		return func() {}
	}
	return e.billingTurns().beginBarrier(aLegID)
}

func (e *Executor) waitBillingEvidenceBarrier(ctx context.Context, aLegID string) bool {
	if e == nil {
		return true
	}
	return e.billingTurns().waitBarrier(ctx, aLegID)
}
