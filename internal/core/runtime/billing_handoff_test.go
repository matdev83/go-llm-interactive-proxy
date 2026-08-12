package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdkterminal "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminal"
)

func testBillingIdentity() BillingIdentity {
	return BillingIdentity{
		AccountID:       func(context.Context, lipapi.Call) string { return "acct" },
		AuthorizationID: func(_ context.Context, _ lipapi.Call, aLegID string) string { return "auth:" + aLegID },
	}
}

func TestBillingCollectorEvictsFinalizeCacheAfterTURSeal(t *testing.T) {
	prev := billingHandoffRetryMaxAttempts
	billingHandoffRetryMaxAttempts = 2
	t.Cleanup(func() { billingHandoffRetryMaxAttempts = prev })

	var released atomic.Int32
	probe := billingHoldReleaseProbe{release: func(context.Context, billing.ReleaseAuthorizationInput) (billing.Posting, error) {
		released.Add(1)
		return billing.Posting{}, nil
	}}
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: probe,
		BillingHoldReleaser:    probe,
		BillingIdentity:        testBillingIdentity(),
		Backends: map[string]execbackend.Backend{
			"backend": {
				FinalizeBilling: func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error) {
					return lipapi.Event{
						Kind:          lipapi.EventUsageDelta,
						InputTokens:   1,
						UsagePresence: lipapi.UsagePresence{InputTokens: true},
					}, nil
				},
			},
		},
	}}
	t.Cleanup(executor.WaitBillingHandoffRetries)
	stream := &retryRecvStream{
		executor: executor, aLegID: "a-1", baseline: lipapi.Call{},
		bleg: b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
	}
	stream.lastAuthorityUsage = lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 1,
		UsagePresence: lipapi.UsagePresence{InputTokens: true},
	}
	_ = stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil)
	executor.WaitBillingHandoffRetries()
	coll := executor.billingTurns()
	coll.finalizeMu.Lock()
	_, cached := coll.finalizeByKey["b-1"]
	coll.finalizeMu.Unlock()
	if cached {
		t.Fatal("finalize cache must evict the sealed B-leg")
	}
	if coll.sealed("a-1") {
		t.Fatal("sealed A-leg marker must be evicted after TUR sealing")
	}
	late := coll.sealTurn(context.Background(), billingHandoffRetryJob{
		accountID: "acct", authorizationID: "auth:a-1", aLegID: "a-1",
		command: sdkterminal.CommandNormalFinish, upstreamOpened: true,
	})
	if late {
		t.Fatal("late seal without retained evidence must not report a new persist")
	}
	executor.WaitBillingHandoffRetries()
	if released.Load() != 0 {
		t.Fatalf("late seal after eviction must not release the hold, released=%d", released.Load())
	}
}

func TestPhase4_BillingTerminalHandoffCoversAllTerminalCommands(t *testing.T) {
	commands := []sdkterminal.Command{
		sdkterminal.CommandNormalFinish,
		sdkterminal.CommandEOF,
		sdkterminal.CommandCancel,
		sdkterminal.CommandClose,
		sdkterminal.CommandTimeout,
		sdkterminal.CommandPartialError,
		sdkterminal.CommandFrontendEncoderFailure,
	}
	for _, command := range commands {
		called := 0
		executor := &Executor{CoreRuntime: CoreRuntime{
			BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
				called++
				if len(record.Legs) != 1 || record.Legs[0].BLegID != "b-1" {
					t.Fatalf("handoff record for %s = %+v", command, record)
				}
				return nil
			}),
			BillingIdentity: testBillingIdentity(),
		}}
		stream := &retryRecvStream{
			executor: executor, aLegID: "a-1", baseline: lipapi.Call{},
			bleg: b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1},
			cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
		}
		stream.lastAuthorityUsage = lipapi.Event{
			Kind: lipapi.EventUsageDelta, InputTokens: 1,
			UsagePresence: lipapi.UsagePresence{InputTokens: true},
		}
		_ = stream.runStreamTerminal(context.Background(), command, nil)
		if called != 1 {
			t.Fatalf("handoff calls for %s = %d, want 1", command, called)
		}
	}
}

func TestBillingTerminalHandoffSealsAuthoritativeSessionIDNotClientHint(t *testing.T) {
	var got billing.TurnUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
			got = record
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	stream := &retryRecvStream{
		executor: executor, aLegID: "a-1",
		baseline: lipapi.Call{Session: lipapi.SessionRef{ClientSessionID: "client-hint", AuthoritativeSessionID: "proxy-sess"}},
		bleg:     b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1},
		cand:     routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
	}
	stream.observeBillingShadow(context.Background(), sdkterminal.CommandNormalFinish)
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if got.SessionID != "proxy-sess" {
		t.Fatalf("SessionID = %q, want proxy-sess (authoritative)", got.SessionID)
	}
}

func TestBillingTerminalHandoffIgnoresClientSessionHintWhenAuthoritativeEmpty(t *testing.T) {
	var got billing.TurnUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
			got = record
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	stream := &retryRecvStream{
		executor: executor, aLegID: "a-1",
		baseline: lipapi.Call{Session: lipapi.SessionRef{ClientSessionID: "client-hint"}},
		bleg:     b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1},
		cand:     routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
	}
	stream.observeBillingShadow(context.Background(), sdkterminal.CommandNormalFinish)
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if got.SessionID != "" {
		t.Fatalf("SessionID = %q, want empty when only client hint is present", got.SessionID)
	}
}

func TestBillingTerminalHandoffAggregatesAllBLegsOnce(t *testing.T) {
	var calls []billing.TurnUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
			calls = append(calls, record)
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}, bleg: b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}}}
	if got := stream.runAttemptTerminal(context.Background(), sdkterminal.CommandSwallowedAttempt, nil); got.Err != nil {
		t.Fatal(got.Err)
	}
	stream.bleg = b2bua.BLegRecord{BLegID: "b-2", ALegID: "a-1", Seq: 2}
	stream.cand = routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-b", Model: "model-b"}}
	stream.resetAttemptTerminal()
	if got := stream.runAttemptTerminal(context.Background(), sdkterminal.CommandSwallowedAttempt, nil); got.Err != nil {
		t.Fatal(got.Err)
	}
	stream.bleg = b2bua.BLegRecord{BLegID: "b-2", ALegID: "a-1", Seq: 2}
	stream.resetAttemptTerminal()
	if got := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil); got.Err != nil {
		t.Fatal(got.Err)
	}
	if got := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil); got.Err != nil {
		t.Fatal(got.Err)
	}
	if len(calls) != 1 || len(calls[0].Legs) != 2 {
		t.Fatalf("handoff calls = %+v", calls)
	}
	if calls[0].AccountID != "acct" || calls[0].ALegID != "a-1" || calls[0].AuthorizationID != "auth:a-1" {
		t.Fatalf("handoff identity = %+v", calls[0])
	}
}

func TestBillingTerminalHandoffIncludesSharedParallelLoserEvidence(t *testing.T) {
	var calls []billing.TurnUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
		calls = append(calls, record)
		return nil
	}), BillingIdentity: testBillingIdentity()}}
	leg1 := &parallelLeg{bleg: b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-2", Seq: 2}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, startedAt: time.Unix(1, 0).UTC()}
	leg2 := &parallelLeg{bleg: b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-1", Seq: 1}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "a", Model: "m"}}, startedAt: time.Unix(1, 0).UTC()}
	executor.recordParallelBillingShadow(context.Background(), leg1, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	executor.recordParallelBillingShadow(context.Background(), leg2, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}}
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if len(calls) != 1 || len(calls[0].Legs) != 2 || calls[0].Legs[0].Seq != 1 || calls[0].Legs[1].Seq != 2 {
		t.Fatalf("parallel handoff = %+v", calls)
	}
}

func TestBillingTerminalHandoffFailureDoesNotChangeTerminalResult(t *testing.T) {
	prevEvidence := billingHandoffEvidenceRetryMaxAttempts
	billingHandoffEvidenceRetryMaxAttempts = 3
	t.Cleanup(func() { billingHandoffEvidenceRetryMaxAttempts = prevEvidence })

	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(context.Context, billing.TurnUsageRecord) error {
			return errors.New("durable unavailable")
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	t.Cleanup(executor.WaitBillingHandoffRetries)
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}, bleg: b2bua.BLegRecord{BLegID: "b-1", ALegID: "a-1", Seq: 1}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}}}
	result := stream.runStreamTerminal(context.Background(), sdkterminal.CommandNormalFinish, nil)
	if result.Err != nil {
		t.Fatalf("handoff failure changed terminal result: %v", result.Err)
	}
}

func TestBillingTerminalHandoffMissingAccountPreservesParallelLoserEvidence(t *testing.T) {
	var calls []billing.TurnUsageRecord
	accountID := ""
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
			calls = append(calls, record)
			return nil
		}),
		BillingIdentity: BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return accountID },
			AuthorizationID: func(_ context.Context, _ lipapi.Call, aLegID string) string { return "auth:" + aLegID },
		},
	}}
	leg := &parallelLeg{
		bleg:      b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-loser", Seq: 1},
		cand:      routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}},
		startedAt: time.Unix(100, 0).UTC(),
	}
	executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{
		Kind: lipapi.EventUsageDelta, InputTokens: 3, UsagePresence: lipapi.UsagePresence{InputTokens: true},
	}, sdkterminal.CommandParallelLoser, false)

	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}}
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if len(calls) != 0 {
		t.Fatalf("handoff must skip without account identity, got %+v", calls)
	}
	accountID = "acct"
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if len(calls) != 1 || len(calls[0].Legs) != 1 || calls[0].Legs[0].BLegID != "b-loser" {
		t.Fatalf("retry handoff after identity resolution = %+v", calls)
	}
}

func TestBillingTerminalHandoffUsesStampedIdentityWhenResolversEmpty(t *testing.T) {
	t.Parallel()
	var got billing.TurnUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
			got = record
			return nil
		}),
		BillingIdentity: BillingIdentity{
			AccountID:          func(context.Context, lipapi.Call) string { return "" },
			AuthorizationID:    func(context.Context, lipapi.Call, string) string { return "" },
			CustomerPricingRef: func(context.Context, lipapi.Call) billing.VersionRef { return billing.VersionRef{} },
			ChargePolicyRef:    func(context.Context, lipapi.Call) billing.VersionRef { return billing.VersionRef{} },
		},
	}}
	leg := &parallelLeg{
		bleg:      b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-1", Seq: 1},
		cand:      routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}},
		startedAt: time.Unix(1, 0).UTC(),
	}
	executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	stream := &retryRecvStream{
		executor:               executor,
		aLegID:                 "a-1",
		baseline:               lipapi.Call{},
		billingAccountID:       "acct",
		billingAuthorizationID: "auth-a-1",
		billingCustomerPricing: billing.VersionRef{ID: "prices", Version: "v1"},
		billingChargePolicy:    billing.VersionRef{ID: "policy", Version: "v2"},
		billingIdentityStamped: true,
	}
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if got.AccountID != "acct" || got.AuthorizationID != "auth-a-1" {
		t.Fatalf("stamped handoff identity = account %q auth %q, want acct/auth-a-1", got.AccountID, got.AuthorizationID)
	}
	if got.CustomerPricingRef != (billing.VersionRef{ID: "prices", Version: "v1"}) || got.ChargePolicyRef != (billing.VersionRef{ID: "policy", Version: "v2"}) {
		t.Fatalf("stamped pricing refs = %+v / %+v", got.CustomerPricingRef, got.ChargePolicyRef)
	}
}

func TestBillingAbortHandoffUsesStampedIdentityWhenResolversEmpty(t *testing.T) {
	t.Parallel()
	var got billing.TurnUsageRecord
	var calls int
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
			calls++
			got = record
			return nil
		}),
		BillingIdentity: BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "" },
			AuthorizationID: func(context.Context, lipapi.Call, string) string { return "" },
		},
	}}
	t.Cleanup(executor.WaitBillingHandoffRetries)
	leg := &parallelLeg{
		bleg:      b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-1", Seq: 1},
		cand:      routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}},
		startedAt: time.Unix(1, 0).UTC(),
	}
	executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	prep := &preparedRequest{
		baseline:               lipapi.Call{Session: lipapi.SessionRef{AuthoritativeSessionID: "proxy-sess"}},
		aLeg:                   b2bua.ALegRecord{ALegID: "a-1"},
		billingAccountID:       "acct",
		billingAuthorizationID: "auth-a-1",
		billingCustomerPricing: billing.VersionRef{ID: "prices", Version: "v1"},
		billingChargePolicy:    billing.VersionRef{ID: "policy", Version: "v2"},
		billingIdentityStamped: true,
	}
	prep.billingUpstreamOpened.Store(true)
	executor.releaseOrHandoffAfterAdmissionAbort(context.Background(), prep, &routePlanState{})
	executor.WaitBillingHandoffRetries()
	if calls != 1 {
		t.Fatalf("abort handoff calls = %d, want 1", calls)
	}
	if got.AccountID != "acct" || got.AuthorizationID != "auth-a-1" {
		t.Fatalf("abort stamped identity = account %q auth %q, want acct/auth-a-1", got.AccountID, got.AuthorizationID)
	}
	if got.CustomerPricingRef != (billing.VersionRef{ID: "prices", Version: "v1"}) || got.ChargePolicyRef != (billing.VersionRef{ID: "policy", Version: "v2"}) {
		t.Fatalf("abort stamped pricing refs = %+v / %+v", got.CustomerPricingRef, got.ChargePolicyRef)
	}
	if got.SessionID != "proxy-sess" {
		t.Fatalf("abort SessionID = %q, want proxy-sess", got.SessionID)
	}
}

type billingAdmitOK struct{}

func (billingAdmitOK) Authorize(context.Context, BillingAdmissionInput) (billing.Authorization, error) {
	return billing.Authorization{}, nil
}

type billingAdmitHold struct{ auth billing.Authorization }

func (a billingAdmitHold) Authorize(context.Context, BillingAdmissionInput) (billing.Authorization, error) {
	return a.auth, nil
}

func TestAuthorizeBillingOnceStampsIdentityFromAdmissionContext(t *testing.T) {
	t.Parallel()
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingIdentity: BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "acct" },
			AuthorizationID: func(_ context.Context, _ lipapi.Call, aLeg string) string { return "auth-" + aLeg },
			CustomerPricingRef: func(context.Context, lipapi.Call) billing.VersionRef {
				return billing.VersionRef{ID: "prices", Version: "v1"}
			},
			ChargePolicyRef: func(context.Context, lipapi.Call) billing.VersionRef {
				return billing.VersionRef{ID: "policy", Version: "v2"}
			},
		},
	}}
	executor.BillingAdmission = billingAdmitOK{}
	prep := &preparedRequest{baseline: lipapi.Call{ID: "call-1"}, aLeg: b2bua.ALegRecord{ALegID: "a-1"}}
	if err := executor.authorizeBillingOnce(context.Background(), prep, &routePlanState{}); err != nil {
		t.Fatalf("authorizeBillingOnce: %v", err)
	}
	if !prep.billingIdentityStamped || prep.billingAccountID != "acct" || prep.billingAuthorizationID != "auth-a-1" {
		t.Fatalf("stamp = stamped=%v account=%q auth=%q", prep.billingIdentityStamped, prep.billingAccountID, prep.billingAuthorizationID)
	}
	if prep.billingCustomerPricing != (billing.VersionRef{ID: "prices", Version: "v1"}) || prep.billingChargePolicy != (billing.VersionRef{ID: "policy", Version: "v2"}) {
		t.Fatalf("stamped refs = %+v / %+v", prep.billingCustomerPricing, prep.billingChargePolicy)
	}
}

func TestAuthorizeBillingOnceStampsEconomicRefsFromHold(t *testing.T) {
	t.Parallel()
	hold := billing.Authorization{
		ID: "hold-auth", AccountID: "hold-acct",
		PricingRef:      billing.VersionRef{ID: "hold-prices", Version: "v9"},
		ChargePolicyRef: billing.VersionRef{ID: "hold-policy", Version: "v3"},
	}
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingIdentity: BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "identity-acct" },
			AuthorizationID: func(context.Context, lipapi.Call, string) string { return "identity-auth" },
			CustomerPricingRef: func(context.Context, lipapi.Call) billing.VersionRef {
				return billing.VersionRef{ID: "identity-prices", Version: "v1"}
			},
			ChargePolicyRef: func(context.Context, lipapi.Call) billing.VersionRef {
				return billing.VersionRef{ID: "identity-policy", Version: "v2"}
			},
		},
	}}
	executor.BillingAdmission = billingAdmitHold{auth: hold}
	prep := &preparedRequest{baseline: lipapi.Call{ID: "call-1"}, aLeg: b2bua.ALegRecord{ALegID: "a-1"}}
	if err := executor.authorizeBillingOnce(context.Background(), prep, &routePlanState{}); err != nil {
		t.Fatalf("authorizeBillingOnce: %v", err)
	}
	if !prep.billingIdentityStamped || prep.billingAccountID != "hold-acct" || prep.billingAuthorizationID != "hold-auth" {
		t.Fatalf("stamp = stamped=%v account=%q auth=%q", prep.billingIdentityStamped, prep.billingAccountID, prep.billingAuthorizationID)
	}
	if prep.billingCustomerPricing != hold.PricingRef || prep.billingChargePolicy != hold.ChargePolicyRef {
		t.Fatalf("stamped refs = %+v / %+v, want hold refs", prep.billingCustomerPricing, prep.billingChargePolicy)
	}
}

func TestBillingTerminalHandoffMissingAuthorizationPreservesEvidence(t *testing.T) {
	var calls []billing.TurnUsageRecord
	authID := ""
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
			calls = append(calls, record)
			return nil
		}),
		BillingIdentity: BillingIdentity{
			AccountID:       func(context.Context, lipapi.Call) string { return "acct" },
			AuthorizationID: func(context.Context, lipapi.Call, string) string { return authID },
		},
	}}
	leg := &parallelLeg{bleg: b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-1", Seq: 1}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, startedAt: time.Unix(1, 0).UTC()}
	executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}}
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if len(calls) != 0 {
		t.Fatalf("handoff must skip without authorization identity, got %+v", calls)
	}
	authID = "auth:a-1"
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if len(calls) != 1 || calls[0].AuthorizationID != "auth:a-1" {
		t.Fatalf("retry handoff after auth identity = %+v", calls)
	}
}

func TestBillingTerminalHandoffWaitsForParallelEvidenceBarrier(t *testing.T) {
	var calls []billing.TurnUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
			calls = append(calls, record)
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	complete := executor.beginBillingEvidenceBarrier("a-1")
	go func() {
		time.Sleep(40 * time.Millisecond)
		leg := &parallelLeg{bleg: b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-loser", Seq: 1}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, startedAt: time.Unix(1, 0).UTC()}
		executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
		complete()
	}()
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}}
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if len(calls) != 1 || len(calls[0].Legs) != 1 || calls[0].Legs[0].BLegID != "b-loser" {
		t.Fatalf("handoff must wait for barrier evidence, got %+v", calls)
	}
}

func TestBillingTerminalHandoffEmptyEvidenceDoesNotMarkSuccess(t *testing.T) {
	var calls int
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(context.Context, billing.TurnUsageRecord) error {
			calls++
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}}
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if calls != 0 || stream.billingHandoffSuccess {
		t.Fatalf("empty evidence must not succeed handoff, calls=%d success=%v", calls, stream.billingHandoffSuccess)
	}
	leg := &parallelLeg{bleg: b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-1", Seq: 1}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, startedAt: time.Unix(1, 0).UTC()}
	executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	if calls != 1 || !stream.billingHandoffSuccess {
		t.Fatalf("retry after evidence arrives = calls=%d success=%v", calls, stream.billingHandoffSuccess)
	}
}

func TestBillingTerminalHandoffPersistFailureRestoresEvidenceForRetry(t *testing.T) {
	var mu sync.Mutex
	var calls int
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(context.Context, billing.TurnUsageRecord) error {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return errors.New("durable unavailable")
			}
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	t.Cleanup(executor.WaitBillingHandoffRetries)
	leg := &parallelLeg{
		bleg:      b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-loser", Seq: 1},
		cand:      routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}},
		startedAt: time.Unix(50, 0).UTC(),
	}
	executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}}
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := calls
		mu.Unlock()
		stream.billingHandoffMu.Lock()
		ok := stream.billingHandoffSuccess
		stream.billingHandoffMu.Unlock()
		if n >= 2 && ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	t.Fatalf("persist failure must restore evidence and retry asynchronously, calls=%d success=%v", n, stream.billingHandoffSuccess)
}

func TestBillingTerminalHandoffIgnoresCanceledRequestContext(t *testing.T) {
	var calls int
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(ctx context.Context, record billing.TurnUsageRecord) error {
			if err := ctx.Err(); err != nil {
				t.Fatalf("persist must use detached context, got %v", err)
			}
			calls++
			if len(record.Legs) != 1 {
				t.Fatalf("legs = %+v", record.Legs)
			}
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	t.Cleanup(executor.WaitBillingHandoffRetries)
	leg := &parallelLeg{bleg: b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-1", Seq: 1}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}, startedAt: time.Unix(1, 0).UTC()}
	executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	stream.handoffBillingTurn(canceled, sdkterminal.CommandNormalFinish)
	if calls != 1 || !stream.billingHandoffSuccess {
		t.Fatalf("canceled request ctx must not strand handoff, calls=%d success=%v", calls, stream.billingHandoffSuccess)
	}
}

func TestBillingTerminalHandoffRefusesPartialSealOnBarrierTimeout(t *testing.T) {
	old := billingHandoffTimeout
	billingHandoffTimeout = 40 * time.Millisecond
	defer func() { billingHandoffTimeout = old }()

	var mu sync.Mutex
	var calls []billing.TurnUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, record)
			return nil
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	t.Cleanup(executor.WaitBillingHandoffRetries)
	complete := executor.beginBillingEvidenceBarrier("a-1")
	winner := &parallelLeg{bleg: b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-winner", Seq: 1}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "w", Model: "m"}}, startedAt: time.Unix(1, 0).UTC()}
	executor.recordParallelBillingShadow(context.Background(), winner, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, true)
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}}
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)
	mu.Lock()
	immediate := len(calls)
	mu.Unlock()
	if immediate != 0 || stream.billingHandoffSuccess {
		t.Fatalf("barrier timeout must not seal partial TUR, calls=%d success=%v", immediate, stream.billingHandoffSuccess)
	}
	// Restore production timeout before completing the barrier so the detached
	// retry is not stuck in the test's short incomplete-wait loop.
	billingHandoffTimeout = old
	loser := &parallelLeg{bleg: b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-loser", Seq: 2}, cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "l", Model: "m"}}, startedAt: time.Unix(2, 0).UTC()}
	executor.recordParallelBillingShadow(context.Background(), loser, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	complete()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(calls)
		var legs int
		if n > 0 {
			legs = len(calls[0].Legs)
		}
		mu.Unlock()
		stream.billingHandoffMu.Lock()
		ok := stream.billingHandoffSuccess
		stream.billingHandoffMu.Unlock()
		if n == 1 && legs == 2 && ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("async retry must seal complete evidence after barrier, calls=%+v success=%v", calls, stream.billingHandoffSuccess)
}

func TestBillingEvidenceClaimRestoreIsAtomic(t *testing.T) {
	executor := &Executor{CoreRuntime: CoreRuntime{BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(context.Context, billing.TurnUsageRecord) error { return nil })}}
	leg := billing.LegUsageRecord{ALegID: "a-1", BLegID: "b-1", Seq: 1}
	executor.addBillingEvidence(context.Background(), leg)
	claimed := executor.claimBillingEvidence("a-1")
	if len(claimed) != 1 || len(executor.peekBillingEvidence("a-1")) != 0 {
		t.Fatalf("claim must remove evidence, claimed=%+v remaining=%+v", claimed, executor.peekBillingEvidence("a-1"))
	}
	executor.restoreBillingEvidence("a-1", claimed)
	executor.addBillingEvidence(context.Background(), billing.LegUsageRecord{ALegID: "a-1", BLegID: "b-1", Seq: 1, BackendID: "newer"})
	got := executor.peekBillingEvidence("a-1")
	if len(got) != 1 || got[0].BackendID != "newer" {
		t.Fatalf("restore+add must dedupe by B-leg, got %+v", got)
	}
}

func TestBillingEvidencePersistFailureKeepsSharedLegs(t *testing.T) {
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(context.Context, billing.TurnUsageRecord) error {
			return errors.New("durable unavailable")
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	executor.addBillingEvidence(context.Background(), billing.LegUsageRecord{ALegID: "a-1", BLegID: "b-loser", Seq: 1})
	executor.addBillingEvidence(context.Background(), billing.LegUsageRecord{ALegID: "a-1", BLegID: "b-winner", Seq: 2})
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}}
	err := stream.persistBillingTurnLocked(context.Background(), billingHandoffRetryJob{
		accountID: "acct", authorizationID: "auth:a-1", aLegID: "a-1",
		command: sdkterminal.CommandNormalFinish,
	})
	if err == nil {
		t.Fatal("expected persist failure")
	}
	got := executor.peekBillingEvidence("a-1")
	if len(got) != 2 {
		t.Fatalf("failed persist must keep shared evidence for retry, got %+v", got)
	}
}

func TestBillingHandoffRetryDoesNotAbandonNoEvidenceImmediately(t *testing.T) {
	prev := billingHandoffRetryMaxAttempts
	billingHandoffRetryMaxAttempts = 3
	t.Cleanup(func() { billingHandoffRetryMaxAttempts = prev })

	var released atomic.Int32
	store := billingHoldReleaseProbe{release: func(context.Context, billing.ReleaseAuthorizationInput) (billing.Posting, error) {
		released.Add(1)
		return billing.Posting{}, nil
	}}
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: store,
		BillingHoldReleaser:    store,
	}}
	job := billingHandoffRetryJob{accountID: "acct", authorizationID: "auth:a-empty", aLegID: "a-empty", command: sdkterminal.CommandCancel}
	start := time.Now()
	executor.runBillingHandoffRetry(job)
	if time.Since(start) < 100*time.Millisecond {
		t.Fatalf("no-evidence must retry with backoff instead of returning immediately")
	}
	if released.Load() != 1 {
		t.Fatalf("exhausted no-evidence must release unused hold, released=%d", released.Load())
	}
}

func TestBillingHandoffProductionRetryBudgets(t *testing.T) {
	t.Parallel()
	if billingHandoffRetryMaxAttempts != 10 {
		t.Fatalf("no-evidence retry budget = %d, want 10", billingHandoffRetryMaxAttempts)
	}
	if billingHandoffEvidenceRetryMaxAttempts != 0 {
		t.Fatalf("evidence retry budget = %d, want 0 (unlimited while process is up)", billingHandoffEvidenceRetryMaxAttempts)
	}
	if billingHandoffCloseWait != 10*time.Second {
		t.Fatalf("close wait = %s, want 10s", billingHandoffCloseWait)
	}
}

func TestBillingHandoffNoEvidenceRetryReleasesExecutionNotStarted(t *testing.T) {
	prev := billingHandoffRetryMaxAttempts
	billingHandoffRetryMaxAttempts = 3
	t.Cleanup(func() { billingHandoffRetryMaxAttempts = prev })

	var reason billing.ReleaseReason
	store := billingHoldReleaseProbe{release: func(_ context.Context, in billing.ReleaseAuthorizationInput) (billing.Posting, error) {
		reason = in.Reason
		return billing.Posting{}, nil
	}}
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: store,
		BillingHoldReleaser:    store,
	}}
	executor.runBillingHandoffRetry(billingHandoffRetryJob{
		accountID: "acct", authorizationID: "auth:a-empty", aLegID: "a-empty",
		command: sdkterminal.CommandCancel, upstreamOpened: false,
	})
	if reason != billing.ReleaseExecutionNotStarted {
		t.Fatalf("release reason = %q, want %q", reason, billing.ReleaseExecutionNotStarted)
	}
}

func TestBillingHandoffEvidenceRetriesStayUnlimitedUntilStop(t *testing.T) {
	if billingHandoffEvidenceRetryMaxAttempts != 0 {
		t.Fatalf("production evidence retries must be unlimited, got %d", billingHandoffEvidenceRetryMaxAttempts)
	}
	var appends atomic.Int32
	var released atomic.Int32
	store := billingHoldReleaseProbe{
		appendErr: errors.New("durable unavailable"),
		onAppend:  func() { appends.Add(1) },
		release: func(context.Context, billing.ReleaseAuthorizationInput) (billing.Posting, error) {
			released.Add(1)
			return billing.Posting{}, nil
		},
	}
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: store,
		BillingHoldReleaser:    store,
		BillingIdentity:        testBillingIdentity(),
	}}
	executor.addBillingEvidence(context.Background(), billing.LegUsageRecord{
		ALegID: "a-ev", BLegID: "b-1", Seq: 1, BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(),
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		executor.runBillingHandoffRetry(billingHandoffRetryJob{
			accountID: "acct", authorizationID: "auth:a-ev", aLegID: "a-ev",
			command: sdkterminal.CommandNormalFinish, upstreamOpened: true,
		})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for appends.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if appends.Load() < 4 {
		t.Fatalf("evidence retries stopped too early, appends=%d", appends.Load())
	}
	executor.stopBillingHandoffRetries()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stopRetries must unblock evidence retry loop")
	}
	if released.Load() != 0 {
		t.Fatalf("evidence retries must retain the hold, released=%d", released.Load())
	}
}

func TestBillingHandoffSealTurnDoesNotReleaseAfterUpstreamOpenWithoutEvidence(t *testing.T) {
	prev := billingHandoffRetryMaxAttempts
	billingHandoffRetryMaxAttempts = 3
	t.Cleanup(func() { billingHandoffRetryMaxAttempts = prev })

	var released atomic.Int32
	store := billingHoldReleaseProbe{release: func(context.Context, billing.ReleaseAuthorizationInput) (billing.Posting, error) {
		released.Add(1)
		return billing.Posting{}, nil
	}}
	executor := &Executor{CoreRuntime: CoreRuntime{BillingTerminalHandoff: store, BillingHoldReleaser: store}}
	t.Cleanup(executor.WaitBillingHandoffRetries)
	ok := executor.billingTurns().sealTurn(context.Background(), billingHandoffRetryJob{
		accountID: "acct", authorizationID: "auth:a-open", aLegID: "a-open",
		command: sdkterminal.CommandPartialError, upstreamOpened: true,
	})
	if ok {
		t.Fatal("seal without evidence must not succeed")
	}
	if released.Load() != 0 {
		t.Fatalf("first empty persist after Open must not release as execution_not_started, released=%d", released.Load())
	}
	executor.WaitBillingHandoffRetries()
	if released.Load() != 0 {
		t.Fatalf("retry exhaustion after Open must retain the hold, released=%d", released.Load())
	}
}

func TestBillingHandoffRetryRetainsHoldAfterUpstreamOpenWithoutEvidence(t *testing.T) {
	prev := billingHandoffRetryMaxAttempts
	billingHandoffRetryMaxAttempts = 3
	t.Cleanup(func() { billingHandoffRetryMaxAttempts = prev })

	var released atomic.Int32
	store := billingHoldReleaseProbe{release: func(context.Context, billing.ReleaseAuthorizationInput) (billing.Posting, error) {
		released.Add(1)
		return billing.Posting{}, nil
	}}
	executor := &Executor{CoreRuntime: CoreRuntime{BillingTerminalHandoff: store, BillingHoldReleaser: store}}
	executor.runBillingHandoffRetry(billingHandoffRetryJob{
		accountID: "acct", authorizationID: "auth:a-open", aLegID: "a-open",
		command: sdkterminal.CommandCancel, upstreamOpened: true,
	})
	if released.Load() != 0 {
		t.Fatalf("upstream Open without LUR must retain the hold, released=%d", released.Load())
	}
}

type billingHoldReleaseProbe struct {
	release   func(context.Context, billing.ReleaseAuthorizationInput) (billing.Posting, error)
	appendErr error
	onAppend  func()
}

func (p billingHoldReleaseProbe) AppendUsageRecord(context.Context, billing.TurnUsageRecord) error {
	if p.onAppend != nil {
		p.onAppend()
	}
	if p.appendErr != nil {
		return p.appendErr
	}
	return nil
}

func (p billingHoldReleaseProbe) ReleaseAuthorization(ctx context.Context, in billing.ReleaseAuthorizationInput) (billing.Posting, error) {
	if p.release != nil {
		return p.release(ctx, in)
	}
	return billing.Posting{}, nil
}

func TestBeginBillingEvidenceBarrierDoesNotOverwrite(t *testing.T) {
	executor := &Executor{}
	first := executor.beginBillingEvidenceBarrier("a-1")
	second := executor.beginBillingEvidenceBarrier("a-1")
	second() // no-op must not close the live barrier
	waited := make(chan struct{})
	go func() {
		if !executor.waitBillingEvidenceBarrier(context.Background(), "a-1") {
			t.Error("wait should complete")
		}
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("second complete must not close the first barrier")
	case <-time.After(40 * time.Millisecond):
	}
	first()
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("first complete must release waiters")
	}
}

func TestParallelBillingShadowPreservesLegStartTime(t *testing.T) {
	started := time.Unix(42, 0).UTC()
	finished := time.Unix(99, 0).UTC()
	var got billing.LegUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		Now: func() time.Time { return finished },
		BillingShadowObserver: BillingShadowObserverFunc(func(_ context.Context, record billing.LegUsageRecord) {
			got = record
		}),
	}}
	leg := &parallelLeg{
		bleg:      b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-2", Seq: 2},
		cand:      routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-x", Model: "model-x"}},
		startedAt: started,
	}
	executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	if !got.StartedAt.Equal(started) {
		t.Fatalf("StartedAt = %v, want %v", got.StartedAt, started)
	}
	if !got.FinishedAt.Equal(finished) {
		t.Fatalf("FinishedAt = %v, want %v", got.FinishedAt, finished)
	}
	if got.StartedAt.Equal(got.FinishedAt) {
		t.Fatal("fabricated identical start/finish timestamps")
	}
}

func TestWaitBillingHandoffRetriesForCloseDoesNotBlockForever(t *testing.T) {
	prevWait := billingHandoffCloseWait
	billingHandoffCloseWait = 80 * time.Millisecond
	t.Cleanup(func() { billingHandoffCloseWait = prevWait })

	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(context.Context, billing.TurnUsageRecord) error {
			return errors.New("durable unavailable")
		}),
		BillingIdentity: testBillingIdentity(),
	}}
	t.Cleanup(func() {
		executor.stopBillingHandoffRetries()
		executor.WaitBillingHandoffRetries()
	})
	leg := &parallelLeg{
		bleg:      b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-loser", Seq: 1},
		cand:      routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}},
		startedAt: time.Unix(50, 0).UTC(),
	}
	executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	stream := &retryRecvStream{executor: executor, aLegID: "a-1", baseline: lipapi.Call{}}
	stream.handoffBillingTurn(context.Background(), sdkterminal.CommandNormalFinish)

	started := time.Now()
	executor.WaitBillingHandoffRetriesForClose()
	elapsed := time.Since(started)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("close wait blocked for %v, want bounded return", elapsed)
	}
}

func TestParallelBillingShadowEmptyBLegIDUsesColonFreeSyntheticID(t *testing.T) {
	var got billing.LegUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingShadowObserver: BillingShadowObserverFunc(func(_ context.Context, record billing.LegUsageRecord) {
			got = record
		}),
	}}
	leg := &parallelLeg{
		bleg:      b2bua.BLegRecord{ALegID: "a-1", BLegID: "", Seq: 4},
		cand:      routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-x", Model: "model-x"}},
		startedAt: time.Unix(1, 0).UTC(),
	}
	executor.recordParallelBillingShadow(context.Background(), leg, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	if got.BLegID != "seq_4" {
		t.Fatalf("synthetic BLegID = %q, want seq_4", got.BLegID)
	}
}

func TestParallelBillingShadowPreservesStreamAuthoritativeZeroCostAcrossFinalize(t *testing.T) {
	var got billing.LegUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		Backends: map[string]execbackend.Backend{
			"backend-x": {
				FinalizeBilling: func(context.Context, execbackend.BillingFinalizationInput) (lipapi.Event, error) {
					return lipapi.Event{
						Kind:          lipapi.EventUsageDelta,
						InputTokens:   5,
						UsagePresence: lipapi.UsagePresence{InputTokens: true},
					}, nil
				},
			},
		},
		BillingShadowObserver: BillingShadowObserverFunc(func(_ context.Context, record billing.LegUsageRecord) {
			got = record
		}),
	}}
	leg := &parallelLeg{
		bleg:      b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-loser", Seq: 1},
		cand:      routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-x", Model: "model-x"}},
		startedAt: time.Unix(1, 0).UTC(),
	}
	streamCost := lipapi.Event{
		Kind:          lipapi.EventUsageDelta,
		CostNanoUnits: 0,
		Currency:      "USD",
		CostPresent:   true,
		Accounting: lipapi.UsageAccountingMetadata{
			Authority: lipapi.UsageAuthorityAuthoritative,
		},
	}
	executor.recordParallelBillingShadow(context.Background(), leg, streamCost, sdkterminal.CommandParallelLoser, false)
	if !got.Evidence.Cost.Present || got.Evidence.Cost.NanoUnits != 0 || got.Evidence.Cost.Currency != "USD" {
		t.Fatalf("parallel finalize dropped stream authoritative zero cost: %+v", got.Evidence)
	}
	if got.Evidence.Authority != billing.EvidenceAuthorityAuthoritative {
		t.Fatalf("parallel merge dropped stream authority: %+v", got.Evidence)
	}
	if !got.Evidence.InputTokens.Present || got.Evidence.InputTokens.Value != 5 {
		t.Fatalf("parallel finalize tokens lost: %+v", got.Evidence)
	}
}

func TestRecordParallelBillingShadowOmitsNeverOpenedLegs(t *testing.T) {
	var records []billing.LegUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingShadowObserver: BillingShadowObserverFunc(func(_ context.Context, record billing.LegUsageRecord) {
			records = append(records, record)
		}),
	}}
	neverOpened := &parallelLeg{
		bleg: b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-never", Seq: 1},
		cand: routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-a", Model: "model-a"}},
	}
	opened := &parallelLeg{
		bleg:      b2bua.BLegRecord{ALegID: "a-1", BLegID: "b-opened", Seq: 2},
		cand:      routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend-b", Model: "model-b"}},
		startedAt: time.Unix(10, 0).UTC(),
	}
	executor.recordParallelBillingShadow(context.Background(), neverOpened, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	executor.recordParallelBillingShadow(context.Background(), opened, lipapi.Event{Kind: lipapi.EventUsageDelta}, sdkterminal.CommandParallelLoser, false)
	if len(records) != 1 || records[0].BLegID != "b-opened" {
		t.Fatalf("never-opened loser must be omitted, got %+v", records)
	}
	if records[0].StartedAt.IsZero() {
		t.Fatal("opened loser StartedAt must be preserved")
	}
}

func TestPersistBillingTurnOmitsZeroTimestampLegs(t *testing.T) {
	var got billing.TurnUsageRecord
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(_ context.Context, record billing.TurnUsageRecord) error {
			got = record
			return nil
		}),
	}}
	openedStart := time.Unix(1, 0).UTC()
	openedFinish := time.Unix(2, 0).UTC()
	executor.addBillingEvidence(context.Background(), billing.LegUsageRecord{
		ALegID: "a-1", BLegID: "b-opened", Seq: 1, BackendID: "backend", ProviderID: "provider", ModelID: "model",
		StartedAt: openedStart, FinishedAt: openedFinish,
		Outcome: billing.LegOutcomeWinner, Surfaced: billing.SurfacedYes,
		Evidence: billing.FinalBillingEvidence{InputTokens: billing.Quantity{Value: 4, Present: true}},
	})
	executor.addBillingEvidence(context.Background(), billing.LegUsageRecord{
		ALegID: "a-1", BLegID: "b-never", Seq: 2, BackendID: "backend", ProviderID: "provider", ModelID: "model",
		Outcome:  billing.LegOutcomeLoser,
		Evidence: billing.FinalBillingEvidence{InputTokens: billing.Quantity{Value: 9, Present: true}},
	})
	err := executor.billingTurns().persist(context.Background(), billingHandoffRetryJob{
		accountID: "acct", authorizationID: "auth", aLegID: "a-1",
		command: sdkterminal.CommandNormalFinish,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Legs) != 1 || got.Legs[0].BLegID != "b-opened" || got.Legs[0].Evidence.InputTokens.Value != 4 {
		t.Fatalf("record = %+v, want only opened leg", got)
	}
}

func TestPersistBillingTurnRejectsOnlyZeroTimestampLegs(t *testing.T) {
	var calls int
	executor := &Executor{CoreRuntime: CoreRuntime{
		BillingTerminalHandoff: billing.UsageRecordAppenderFunc(func(context.Context, billing.TurnUsageRecord) error {
			calls++
			return nil
		}),
	}}
	executor.addBillingEvidence(context.Background(), billing.LegUsageRecord{
		ALegID: "a-1", BLegID: "b-never", Seq: 1, BackendID: "backend", ProviderID: "provider", ModelID: "model",
		Outcome: billing.LegOutcomeLoser,
	})
	err := executor.billingTurns().persist(context.Background(), billingHandoffRetryJob{
		accountID: "acct", authorizationID: "auth", aLegID: "a-1",
		command: sdkterminal.CommandCancel,
	})
	if !errors.Is(err, errBillingHandoffNoEvidence) {
		t.Fatalf("error = %v, want no B-leg evidence", err)
	}
	if calls != 0 {
		t.Fatalf("appender calls = %d, want 0", calls)
	}
}

func TestMergeStreamCostOntoLURCopiesAuthoritativeZero(t *testing.T) {
	finalize := billing.FinalBillingEvidence{
		InputTokens: billing.Quantity{Value: 99, Present: true},
		Source:      billing.EvidenceSourceProviderReported,
	}
	stream := billing.FinalBillingEvidence{
		Cost:      billing.MoneyEvidence{NanoUnits: 0, Currency: "USD", Present: true},
		Authority: billing.EvidenceAuthorityAuthoritative,
	}
	got := mergeStreamCostOntoLUR(finalize, stream)
	if !got.InputTokens.Present || got.InputTokens.Value != 99 {
		t.Fatalf("finalize tokens lost: %+v", got)
	}
	if !got.Cost.Present || got.Cost.NanoUnits != 0 || got.Cost.Currency != "USD" {
		t.Fatalf("stream authoritative zero cost was dropped: %+v", got)
	}
	if got.Authority != billing.EvidenceAuthorityAuthoritative {
		t.Fatalf("stream authority was dropped with cost: %+v", got)
	}
	if got.Source != billing.EvidenceSourceProviderReported {
		t.Fatalf("finalize provenance lost: %+v", got)
	}
}
