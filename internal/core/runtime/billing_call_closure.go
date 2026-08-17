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
	if s.executor.CallUsageAppender == nil || s.billingCallClosureSuccess {
		return
	}
	s.ensureBillingCallState()
	if err := s.billingCallID.Validate(); err != nil {
		return
	}
	if !s.billingIdentityStamped {
		return
	}
	accountID := strings.TrimSpace(s.billingAccountID)
	if accountID == "" {
		return
	}
	ids := s.billingCallState.freezeAllocatedBLegs()
	now := s.now()
	started, finished := s.billingCallState.timingBounds(now)
	record := billing.CallUsageRecord{
		SchemaVersion:      billing.CurrentRecordSchemaVersion,
		CallID:             s.billingCallID,
		AccountID:          accountID,
		ALegID:             strings.TrimSpace(s.aLegID),
		SessionID:          strings.TrimSpace(s.baseline.Session.AuthoritativeSessionID),
		StartedAt:          started,
		FinishedAt:         finished,
		Outcome:            turnOutcomeFromCommand(command),
		CustomerPricingRef: s.billingCustomerPricing,
		ChargePolicyRef:    s.billingChargePolicy,
		ExpectedBLegIDs:    ids,
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
		return s.executor.CallUsageAppender.AppendCallUsage(persistCtx, sealed)
	})
	if err != nil {
		s.executor.logBillingUsageAppendFailure(persistCtx, "billing_call_closure_append_critical", "billing call-closure append failed", err)
		return
	}
	s.billingCallClosureSuccess = true
}

func callClosureTimes(legs []billing.LegUsageRecord, now time.Time) (time.Time, time.Time) {
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
