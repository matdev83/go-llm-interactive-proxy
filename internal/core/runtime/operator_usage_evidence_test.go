package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

func TestOperatorUsageForFinalizePrefersLastAuthorityUsage(t *testing.T) {
	t.Parallel()

	authoritative := lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  10,
		OutputTokens: 4,
		TotalTokens:  14,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	estimate := lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  99,
		OutputTokens: 99,
		TotalTokens:  198,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceLocalEstimator,
			Authority: lipapi.UsageAuthorityEstimated,
		},
	}
	s := &retryRecvStream{
		lastAuthorityUsage: authoritative,
		seenEvents:         []lipapi.Event{estimate},
	}

	got := s.operatorUsageForFinalize()
	if got.InputTokens != authoritative.InputTokens || got.OutputTokens != authoritative.OutputTokens {
		t.Fatalf("operator finalize usage = %#v, want authoritative lastAuthorityUsage", got)
	}

	customer := s.usageEvidenceOrEmpty()
	if customer.InputTokens != estimate.InputTokens {
		t.Fatalf("customer usageEvidenceOrEmpty = %#v, want seenEvents-first estimate", customer)
	}
}

func TestOperatorUsageForFinalizeFallsBackToSeenEventsThenEmptyShell(t *testing.T) {
	t.Parallel()

	seen := lipapi.Event{
		Kind:        lipapi.EventUsageDelta,
		InputTokens: 3,
		TotalTokens: 3,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	withSeen := &retryRecvStream{seenEvents: []lipapi.Event{seen}}
	got := withSeen.operatorUsageForFinalize()
	if got.InputTokens != seen.InputTokens || got.Kind != lipapi.EventUsageDelta {
		t.Fatalf("seen-events fallback = %#v, want %#v", got, seen)
	}

	empty := (&retryRecvStream{}).operatorUsageForFinalize()
	if empty.Kind != lipapi.EventUsageDelta || empty.InputTokens != 0 || empty.TotalTokens != 0 {
		t.Fatalf("unobserved shell = %#v, want empty UsageDelta shell", empty)
	}
	if !usageEventPresent(empty) {
		t.Fatal("empty operator shell must be present usage (req 2.9), not absent")
	}

	if got := (*retryRecvStream)(nil).operatorUsageForFinalize(); got.Kind != lipapi.EventUsageDelta {
		t.Fatalf("nil receiver = %#v, want empty UsageDelta shell", got)
	}
}

func TestSettleCancellationAuthorityUsesOperatorUsageForFinalize(t *testing.T) {
	t.Parallel()

	authoritative := lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  7,
		OutputTokens: 2,
		TotalTokens:  9,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceProviderReported,
			Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	estimate := lipapi.Event{
		Kind:         lipapi.EventUsageDelta,
		InputTokens:  40,
		OutputTokens: 40,
		TotalTokens:  80,
		Accounting: lipapi.UsageAccountingMetadata{
			Plane:     lipapi.UsagePlaneProviderBillable,
			Source:    lipapi.UsageSourceLocalEstimator,
			Authority: lipapi.UsageAuthorityEstimated,
		},
	}

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-op-usage",
			ReservedAmount: authorityInputAmount(10),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	state := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(10),
		admissionResult: auth.admitResult,
	}
	cand := routing.AttemptCandidate{Key: "cand", Primary: routing.Primary{Backend: "b", Model: "m"}}
	rs := &retryRecvStream{
		executor: ex,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID:  aLegID,
			traceID: "trace-op-usage",
		}),
		attempt:            testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-op", Seq: 1}, cand, testAuthorityLifecycle(ex, state, cand)),
		lastAuthorityUsage: authoritative,
		seenEvents:         []lipapi.Event{estimate},
	}

	rs.settleCancellationAuthority(context.Background())
	if auth.settleCalls.Load() != 1 {
		t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
	}
	got := auth.lastSettle()
	if got.FinalUsage.Value != int64(authoritative.InputTokens) {
		t.Fatalf("operator settle FinalUsage = %#v, want authoritative input %d (not seenEvents estimate)", got.FinalUsage, authoritative.InputTokens)
	}
}
