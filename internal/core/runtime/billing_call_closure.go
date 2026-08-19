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
func (t *turnTerminal) handoffBillingTurn(ctx context.Context, facts recvTurnFacts, executor *Executor, command sdkterminal.Command) {
	if t == nil || executor == nil {
		return
	}
	t.billingClosureMu.Lock()
	defer t.billingClosureMu.Unlock()
	if !executor.hasTerminalCallSink() || t.billingClosureSuccess {
		return
	}
	if err := facts.billingCallID.Validate(); err != nil {
		return
	}
	if !facts.billingIdentityStamped {
		return
	}
	accountID := strings.TrimSpace(facts.billingAccountID)
	if accountID == "" {
		return
	}
	ids := facts.billingCallState.freezeAllocatedBLegs()
	now := executor.now()
	started, finished := facts.billingCallState.timingBounds(now)
	workloadCtx := ctx
	if facts.requestAuth != nil {
		workloadCtx = withRequestAuthority(workloadCtx, facts.requestAuth)
	}
	record := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             facts.billingCallID,
		AccountID:          accountID,
		ALegID:             strings.TrimSpace(facts.aLegID),
		SessionID:          strings.TrimSpace(facts.baseline.Session.AuthoritativeSessionID),
		StartedAt:          started,
		FinishedAt:         finished,
		Outcome:            turnOutcomeFromCommand(command),
		CustomerPricingRef: facts.billingCustomerPricing,
		ChargePolicyRef:    facts.billingChargePolicy,
		ExpectedBLegIDs:    ids,
		Workload:           executor.billingWorkloadIdentityForALeg(workloadCtx, facts.aLegID),
	}
	sealed, err := record.Seal()
	if err != nil {
		if executor.Log != nil {
			executor.Log.DebugContext(ctx, "billing call-closure seal failed", "error", err)
		}
		return
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	err = safety.Call(safety.BoundaryStream, "billing_call_closure", func() error {
		return executor.TerminalUsageSink.AppendCall(persistCtx, sealed)
	})
	if err != nil {
		executor.logBillingUsageAppendFailure(persistCtx, "billing_call_closure_append_critical", "billing call-closure append failed", err)
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
