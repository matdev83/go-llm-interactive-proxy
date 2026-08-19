package runtime

import (
	"context"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

// handoffBillingTurn owns the request-level BillingCallID closure claim. The
// terminal mutex remains held through the sink append so a failed append is
// retryable while concurrent terminal paths cannot publish duplicate closures.
func (t *turnTerminal) handoffBillingTurn(ctx context.Context, facts requestTerminalFacts, command sdkterminal.Command) {
	if t == nil || t.appendBillingCall == nil || t.billingWorkload == nil {
		return
	}
	t.billingClosureMu.Lock()
	defer t.billingClosureMu.Unlock()
	if t.appendBillingCall == nil || t.billingClosureSuccess {
		return
	}
	if err := facts.billingCallID.Validate(); err != nil {
		return
	}
	if !facts.identityStamped {
		return
	}
	accountID := strings.TrimSpace(facts.accountID)
	if accountID == "" {
		return
	}
	ids := facts.billingState.freezeAllocatedBLegs()
	now := t.nowTime()
	started, finished := facts.billingState.timingBounds(now)
	workloadCtx := ctx
	if facts.requestAuth != nil {
		workloadCtx = withRequestAuthority(workloadCtx, facts.requestAuth)
	}
	record := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             facts.billingCallID,
		AccountID:          accountID,
		ALegID:             strings.TrimSpace(facts.aLegID),
		SessionID:          strings.TrimSpace(facts.sessionID),
		StartedAt:          started,
		FinishedAt:         finished,
		Outcome:            turnOutcomeFromCommand(command),
		CustomerPricingRef: facts.pricing,
		ChargePolicyRef:    facts.chargePolicy,
		ExpectedBLegIDs:    ids,
		Workload:           t.billingWorkload(workloadCtx, facts.aLegID),
	}
	sealed, err := record.Seal()
	if err != nil {
		if t.log != nil {
			t.log.DebugContext(ctx, "billing call-closure seal failed", "error", err)
		}
		return
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	err = safety.Call(safety.BoundaryStream, "billing_call_closure", func() error {
		return t.appendBillingCall(persistCtx, sealed)
	})
	if err != nil {
		if t.logBillingAppendFailure != nil {
			t.logBillingAppendFailure(persistCtx, "billing_call_closure_append_critical", "billing call-closure append failed", err)
		}
		return
	}
	t.billingClosureSuccess = true
}

func callClosureTimes(legs []billing.CallLegUsageRecord, now time.Time) (time.Time, time.Time) {
	var started, finished time.Time
	for _, leg := range legs {
		if !leg.StartedAt.IsZero() && (started.IsZero() || leg.StartedAt.Before(started)) {
			started = leg.StartedAt
		}
		if !leg.FinishedAt.IsZero() && (finished.IsZero() || leg.FinishedAt.After(finished)) {
			finished = leg.FinishedAt
		}
	}
	if started.IsZero() {
		started = now
	}
	if finished.IsZero() {
		finished = now
	}
	if finished.Before(started) {
		finished = started
	}
	return started, finished
}
