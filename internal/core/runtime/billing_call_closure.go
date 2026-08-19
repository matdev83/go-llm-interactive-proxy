package runtime

import (
	"context"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

func (s *retryRecvStream) appendCallClosureLocked(ctx context.Context, command sdkterminal.Command) {
	if !s.executor.hasTerminalCallSink() || s.billingCallClosureSuccess {
		return
	}
	if err := s.facts.billingCallID.Validate(); err != nil {
		return
	}
	if !s.facts.billingIdentityStamped {
		return
	}
	accountID := strings.TrimSpace(s.facts.billingAccountID)
	if accountID == "" {
		return
	}
	ids := s.facts.billingCallState.freezeAllocatedBLegs()
	now := s.now()
	started, finished := s.facts.billingCallState.timingBounds(now)
	record := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             s.facts.billingCallID,
		AccountID:          accountID,
		ALegID:             strings.TrimSpace(s.facts.aLegID),
		SessionID:          strings.TrimSpace(s.facts.baseline.Session.AuthoritativeSessionID),
		StartedAt:          started,
		FinishedAt:         finished,
		Outcome:            turnOutcomeFromCommand(command),
		CustomerPricingRef: s.facts.billingCustomerPricing,
		ChargePolicyRef:    s.facts.billingChargePolicy,
		ExpectedBLegIDs:    ids,
		Workload:           s.executor.billingWorkloadIdentityForALeg(ctx, s.facts.aLegID),
	}
	sealed, err := record.Seal()
	if err != nil {
		if s.executor.Log != nil {
			s.executor.Log.DebugContext(ctx, "billing call-closure seal failed", "error", err)
		}
		return
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingHandoffTimeout)
	defer cancel()
	err = safety.Call(safety.BoundaryStream, "billing_call_closure", func() error {
		return s.executor.TerminalUsageSink.AppendCall(persistCtx, sealed)
	})
	if err != nil {
		s.executor.logBillingUsageAppendFailure(persistCtx, "billing_call_closure_append_critical", "billing call-closure append failed", err)
		return
	}
	s.billingCallClosureSuccess = true
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
