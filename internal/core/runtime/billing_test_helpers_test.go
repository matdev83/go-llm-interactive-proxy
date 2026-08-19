package runtime

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func testAttemptSlot(bleg b2bua.BLegRecord, cand routing.AttemptCandidate, authority authorityLifecycle, accounting ...attemptAccountingTracker) attemptSlot {
	in := attemptSessionInput{
		bleg:           bleg,
		cand:           cand,
		authority:      authority,
		finalStreamObs: &extensions.FinalStreamObservationSession{},
	}
	if len(accounting) > 0 {
		in.accounting = accounting[0]
	}
	return attemptSlot{current: newAttemptSession(in)}
}

// testAttemptSession installs a complete default fixture for direct retry-stream
// tests. Production assembly and replacement install complete sessions before
// exposing a stream, so production code never creates a partial attempt.
func testAttemptSession(s *retryRecvStream) *attemptSession {
	if s == nil {
		return nil
	}
	if attempt := s.attempt.snapshot(); attempt != nil {
		return attempt
	}
	attempt := newAttemptSession(attemptSessionInput{})
	s.attempt.install(attempt)
	return attempt
}

func testStoreInner(s *retryRecvStream, inner lipapi.ManagedEventStream) {
	testAttemptSession(s).storeInner(inner)
}

type exposureAdmissionFunc func(context.Context, BillingExposureAdmissionInput) (billing.CallExposure, error)

func (f exposureAdmissionFunc) Admit(ctx context.Context, in BillingExposureAdmissionInput) (billing.CallExposure, error) {
	return f(ctx, in)
}

type creditGateFunc func(context.Context, string) error

func (f creditGateFunc) Check(ctx context.Context, accountID string) error {
	return f(ctx, accountID)
}

func testBillingIdentity() BillingIdentity {
	return BillingIdentity{
		AccountID: func(context.Context, lipapi.Call) string { return "acct" },
		CustomerPricingRef: func(context.Context, lipapi.Call) billing.VersionRef {
			return billing.VersionRef{ID: "pricing:test", Version: "1"}
		},
		ChargePolicyRef: func(context.Context, lipapi.Call) billing.VersionRef {
			return billing.VersionRef{ID: "policy:test", Version: "1"}
		},
		OperatorRateRef: func(context.Context, string, string) billing.VersionRef {
			return billing.VersionRef{ID: "operator:test", Version: "1"}
		},
	}
}

func testRecvTurnFacts(f recvTurnFacts) recvTurnFacts {
	if f.billingCallState == nil {
		f.billingCallState = newBillingCallState(f.billingCallID)
	}
	return f
}

func withTestRecvFacts(s *retryRecvStream, update func(recvTurnFacts) recvTurnFacts) *retryRecvStream {
	if s == nil {
		return nil
	}
	// Test callers invoke this while constructing a stream, before any lock or
	// terminal state is used. Keep the original stream identity so this helper
	// does not copy mutex-bearing retryRecvStream state.
	s.facts = update(testRecvTurnFacts(s.facts))
	return s
}

func stampStreamIdentity(s *retryRecvStream) *retryRecvStream {
	if s == nil {
		return nil
	}
	return withTestRecvFacts(s, func(f recvTurnFacts) recvTurnFacts {
		f.billingAccountID = "acct"
		f.billingCustomerPricing = billing.VersionRef{ID: "pricing:test", Version: "1"}
		f.billingChargePolicy = billing.VersionRef{ID: "policy:test", Version: "1"}
		f.billingIdentityStamped = true
		return f
	})
}
