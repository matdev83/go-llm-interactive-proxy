package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	accountingapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/app"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

func TestAuthoritativeBillingSuccessFinishSettlesAttemptAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "authoritative-success-reservation",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	executor, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	stream := &retryRecvStream{
		executor: executor,
		bus:      hooks.New(hooks.Config{}),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "request-authoritative-success", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			aLegID:   aLegID,
			traceID:  "trace-authoritative-success",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), authorityLifecycle{}, newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	testAttemptSession(stream).authority = testAuthorityLifecycle(executor, attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(7),
		admissionResult: auth.admitResult,
	}, authorityCandidate())
	installTestTurnTerminal(stream)

	_, ok, err := stream.finalizeResponseFinishedAuthority(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected no synthesized usage when StreamUsage is unset")
	}
	if got := auth.settleCalls.Load(); got != 1 {
		t.Fatalf("attempt authority settle calls = %d, want 1 on authoritative success finish", got)
	}
	if !testAttemptSession(stream).authority.Settled() {
		t.Fatal("authoritative success finish left attempt authority unsettled")
	}
	if got := auth.lastSettle(); got.Kind != authorityapp.SettlementKindFinal {
		t.Fatalf("settle kind = %q, want final", got.Kind)
	}
}

func TestAuthoritativeBillingPreservesProtocolUsageProjection(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "authoritative-protocol-usage",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	executor, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	executor.StreamUsage = accountingstream.New(&stubStreamCounter{
		call:   accountingapp.CountResult{InputTokens: 7, TotalTokens: 7},
		output: accountingapp.CountResult{OutputTokens: 3, TotalTokens: 10},
	}, accountingstream.Config{})
	stream := &retryRecvStream{
		executor: executor,
		bus:      hooks.New(hooks.Config{}),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{ID: "request-authoritative-protocol", Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions}},
			aLegID:   aLegID,
			traceID:  "trace-authoritative-protocol",
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: aLegID, Seq: 1}, authorityCandidate(), authorityLifecycle{}, newAttemptAccountingTracker(time.Unix(1, 0))),
	}
	testAttemptSession(stream).authority = testAuthorityLifecycle(executor, attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(7),
		admissionResult: auth.admitResult,
	}, authorityCandidate())
	installTestTurnTerminal(stream)

	usage, ok, err := stream.finalizeResponseFinishedAuthority(context.Background(), lipapi.Event{Kind: lipapi.EventResponseFinished})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || usage.Kind == "" || usage.TotalTokens == 0 {
		t.Fatalf("protocol usage projection missing under authoritative cutover: ok=%t usage=%+v", ok, usage)
	}
	if usage.CostPresent || usage.CostNanoUnits != 0 || usage.CostSource != "" {
		t.Fatalf("stream-time monetary enrichment leaked into protocol usage: %+v", usage)
	}
	if got := auth.settleCalls.Load(); got != 1 {
		t.Fatalf("attempt authority settle calls = %d, want 1", got)
	}
	if !testAttemptSession(stream).authority.Settled() {
		t.Fatal("authoritative protocol-usage finish left attempt authority unsettled")
	}
}

func TestAuthoritativeBillingKeepsNonMoneyAuthorityCoordination(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Reserved:       true,
			ReservationID:  "authoritative-cutover-reservation",
			ReservedAmount: authorityInputAmount(7),
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	executor, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	stream := &retryRecvStream{
		executor: executor,
		facts: testRecvTurnFacts(recvTurnFacts{
			aLegID: aLegID,
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{}, authorityCandidate(), authorityLifecycle{}),
	}
	testAttemptSession(stream).authority = testAuthorityLifecycle(executor, attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(7),
		admissionResult: auth.admitResult,
	}, authorityCandidate())

	stream.recordPartialTokenAccounting(context.Background(), stream.attempt.snapshot(), "authoritative-cutover", nil)
	if got := auth.settleCalls.Load(); got != 1 {
		t.Fatalf("non-money authority settle calls = %d, want 1", got)
	}
	if !testAttemptSession(stream).authority.Settled() {
		t.Fatal("non-money authority lifecycle was not finalized")
	}
}

func TestAttemptAuthorityUsageAmountUsesQuantityEstimate(t *testing.T) {
	t.Parallel()
	got := attemptAuthorityUsageAmount(lipapi.Event{InputTokens: 3}, authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 4})
	if got.Unit != authoritydomain.AmountUnitInputTokens || got.Value != 3 {
		t.Fatalf("usage=%#v", got)
	}
}
