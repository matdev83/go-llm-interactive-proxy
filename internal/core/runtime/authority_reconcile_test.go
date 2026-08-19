package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	accountingstream "github.com/matdev83/go-llm-interactive-proxy/internal/core/tokenaccounting/streamusage"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// TestSettleReservationAuthorityLevel proves the settle authority level is
// derived from the settlement kind and whether provider usage is present in the
// event (requirement 8.4-8.6): Final with usage is Authoritative; Final with no
// usage (estimated fallback), Partial, Cancellation, and Unavailable are
// Estimated.
func TestSettleReservationAuthorityLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		kind      authorityapp.SettlementKind
		usageEv   lipapi.Event
		wantLevel authoritydomain.AuthorityLevel
	}{
		{
			name: "final-with-usage-is-authoritative",
			kind: authorityapp.SettlementKindFinal,
			usageEv: lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 5, TotalTokens: 5, Accounting: lipapi.UsageAccountingMetadata{
				Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
			}},
			wantLevel: authoritydomain.AuthorityLevelAuthoritative,
		},
		{
			name:      "final-no-usage-fallback-is-estimated",
			kind:      authorityapp.SettlementKindFinal,
			usageEv:   lipapi.Event{},
			wantLevel: authoritydomain.AuthorityLevelEstimated,
		},
		{
			name: "final-local-estimate-is-not-authoritative",
			kind: authorityapp.SettlementKindFinal,
			usageEv: lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 5, TotalTokens: 5, Accounting: lipapi.UsageAccountingMetadata{
				Plane: lipapi.UsagePlaneClientVisible, Source: lipapi.UsageSourceLocalEstimator, Authority: lipapi.UsageAuthorityEstimated,
			}},
			wantLevel: authoritydomain.AuthorityLevelEstimated,
		},
		{
			name:      "partial-is-estimated",
			kind:      authorityapp.SettlementKindPartial,
			usageEv:   lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 3, TotalTokens: 3},
			wantLevel: authoritydomain.AuthorityLevelEstimated,
		},
		{
			name:      "cancellation-is-estimated",
			kind:      authorityapp.SettlementKindCancellation,
			usageEv:   lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 2, TotalTokens: 2},
			wantLevel: authoritydomain.AuthorityLevelEstimated,
		},
		{
			name:      "unavailable-is-estimated",
			kind:      authorityapp.SettlementKindUnavailable,
			usageEv:   lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 1, TotalTokens: 1},
			wantLevel: authoritydomain.AuthorityLevelEstimated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			auth := &recordingAuthorityService{
				admitResult: authorityapp.AdmissionResult{
					Allowed:        true,
					Reserved:       true,
					ReservationID:  "reservation-authority-level",
					ReservedAmount: authorityInputAmount(8),
					PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
				},
				status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
			}
			state := attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(8),
				admissionResult: auth.admitResult,
			}
			l := newAuthorityLifecycle(auth, nil, state, authorityCandidate())

			l.Settle(context.Background(), tc.kind, tc.usageEv, false)

			if auth.settleCalls.Load() != 1 {
				t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
			}
			got := auth.lastSettle()
			if got.Authority != tc.wantLevel {
				t.Fatalf("settle authority = %q, want %q", got.Authority, tc.wantLevel)
			}
		})
	}
}

// TestReconcileAuthoritativeAdjustsEstimatedSettlement proves that after an
// estimated settlement, ReconcileAuthoritative calls the store with a new
// authoritative source key and the store adjusts Consumed via
// authoritativeResettle (requirement 7.6, 8.4-8.6). Uses the real
// authoritystore.MemoryStore + real authorityapp.Service, not fakes.
func TestReconcileAuthoritativeAdjustsEstimatedSettlement(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := authoritydomain.Rule{
		ID:    "tenant.input.reconcile",
		Kind:  authoritydomain.RuleKindQuota,
		Mode:  authoritydomain.RuleModeStrict,
		Unit:  authoritydomain.AmountUnitInputTokens,
		Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 100},
		Match: authoritydomain.DimensionsMatcher{Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	limitRows, err := authoritystore.LimitRowsFromRules([]authoritydomain.Rule{rule}, at)
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	store := authoritystore.NewMemory(authoritystore.Config{
		StoreID:   "reconcile-authoritative",
		Backing:   authoritydomain.BackingCapabilityAtomic,
		LimitRows: limitRows,
		Readiness: authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
	})
	svc := authorityapp.NewService(authorityRuleSource{
		snapshot: authorityapp.RuleSnapshot{
			Status:    authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
			Rules:     []authoritydomain.Rule{rule},
			FetchedAt: at,
		},
	}, store, nil, nil)

	admissionInput := authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID: "trace-reconcile", RequestID: "req-reconcile",
			ALegID: "a-1", BLegID: "b-1", AttemptSeq: 1,
			BackendID: "backend-1", Model: "model-1",
		},
		Scope:          scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1"), TenantID: scope.Known("tenant-1")},
		Dimensions:     authoritydomain.Dimensions{Backend: scope.Known("backend-1"), Model: scope.Known("model-1")},
		Request:        authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 8},
		PreflightUsage: authoritydomain.PreflightUsage{InputTokens: 8, TotalTokens: 8},
		Authority:      authoritydomain.AuthorityLevelEstimated,
		ReservationKey: authoritydomain.ReservationKey{
			LogicalRequestID: "req-reconcile", ALegID: "a-1", BLegID: "b-1",
			AttemptID: "b-1", RuleID: "backend-1:model-1", Sequence: 1,
		},
	}
	admissionResult, err := svc.Admit(context.Background(), admissionInput)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !admissionResult.Reserved {
		t.Fatalf("admit must reserve: %#v", admissionResult)
	}

	state := attemptAuthorityState{admissionInput: admissionInput, admissionResult: admissionResult}
	lifecycle := newAuthorityLifecycle(svc, nil, state, authorityCandidate())

	limitRow := func(t *testing.T) (reserved, consumed int64) {
		t.Helper()
		page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
			Common:     controlplane.CommonFilters{BackendID: "backend-1"},
			RuleID:     rule.ID,
			Unit:       string(authoritydomain.AmountUnitInputTokens),
			Limit:      10,
			Visibility: controlplane.VisibilityDefault,
		})
		if err != nil {
			t.Fatalf("LimitStatus: %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("LimitStatus items = %d, want 1", len(page.Items))
		}
		return page.Items[0].Reserved, page.Items[0].Consumed
	}

	// Step 1: settle with an empty event (no usage) — the estimate fallback
	// applies, so Consumed = reserved estimate (8), and the settlement is
	// Estimated authority. This simulates a cancellation/partial settle before
	// authoritative usage is available.
	if applied := lifecycle.Settle(context.Background(), authorityapp.SettlementKindPartial, lipapi.Event{}, false); !applied {
		t.Fatal("first Settle = false, want true")
	}
	reserved, consumed := limitRow(t)
	if reserved != 0 {
		t.Fatalf("after settle, reserved = %d, want 0", reserved)
	}
	if consumed != 8 {
		t.Fatalf("after estimated settle, consumed = %d, want 8 (estimate fallback)", consumed)
	}
	if !lifecycle.Settled() {
		t.Fatal("expected lifecycle settled after first Settle")
	}

	// Step 2: ReconcileAuthoritative with actual usage (5). The store should
	// adjust Consumed from 8 to 5 (delta = -3) via authoritativeResettle.
	authoritativeUsage := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 5, TotalTokens: 5, Accounting: lipapi.UsageAccountingMetadata{
		Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative,
	}}
	if applied := lifecycle.ReconcileAuthoritative(context.Background(), authoritativeUsage); !applied {
		t.Fatal("ReconcileAuthoritative = false, want true (adjustment must apply)")
	}
	_, consumed = limitRow(t)
	if consumed != 5 {
		t.Fatalf("after authoritative reconcile, consumed = %d, want 5 (adjusted from 8)", consumed)
	}

	// Step 3: replay ReconcileAuthoritative with the same usage — the source key
	// is deterministic, so the store's settleBySrc catches it as a no-op.
	if applied := lifecycle.ReconcileAuthoritative(context.Background(), authoritativeUsage); applied {
		t.Fatal("replay ReconcileAuthoritative = true, want false (idempotent no-op)")
	}
	_, consumed = limitRow(t)
	if consumed != 5 {
		t.Fatalf("after replay, consumed = %d, want 5 (no double-adjustment)", consumed)
	}
}

// TestCancellationPathReconcileAuthoritativeAdjustsPriorSettle proves the full
// cancellation path: when a reservation is already settled (estimated) and
// finalizeBillingAfterCancel recovers authoritative usage, persistCancellationBilling
// calls ReconcileAuthoritative to adjust the prior settlement instead of no-op
// (requirement 7.6, 8.4-8.6). Uses the real authoritystore.MemoryStore + real
// authorityapp.Service wired through a retryRecvStream.
func TestCancellationPathReconcileAuthoritativeAdjustsPriorSettle(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := authoritydomain.Rule{
		ID:    "tenant.input.cancel-reconcile",
		Kind:  authoritydomain.RuleKindQuota,
		Mode:  authoritydomain.RuleModeStrict,
		Unit:  authoritydomain.AmountUnitInputTokens,
		Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 100},
		Match: authoritydomain.DimensionsMatcher{Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	limitRows, err := authoritystore.LimitRowsFromRules([]authoritydomain.Rule{rule}, at)
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	store := authoritystore.NewMemory(authoritystore.Config{
		StoreID:   "cancel-reconcile-authoritative",
		Backing:   authoritydomain.BackingCapabilityAtomic,
		LimitRows: limitRows,
		Readiness: authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
	})
	svc := authorityapp.NewService(authorityRuleSource{
		snapshot: authorityapp.RuleSnapshot{
			Status:    authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
			Rules:     []authoritydomain.Rule{rule},
			FetchedAt: at,
		},
	}, store, nil, nil)

	admissionInput := authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID: "trace-cancel-reconcile", RequestID: "req-cancel-reconcile",
			ALegID: "a-cr", BLegID: "b-cr", AttemptSeq: 1,
			BackendID: "backend-1", Model: "model-1",
		},
		Scope:          scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1"), TenantID: scope.Known("tenant-1")},
		Dimensions:     authoritydomain.Dimensions{Backend: scope.Known("backend-1"), Model: scope.Known("model-1")},
		Request:        authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 8},
		PreflightUsage: authoritydomain.PreflightUsage{InputTokens: 8, TotalTokens: 8},
		Authority:      authoritydomain.AuthorityLevelEstimated,
		ReservationKey: authoritydomain.ReservationKey{
			LogicalRequestID: "req-cancel-reconcile", ALegID: "a-cr", BLegID: "b-cr",
			AttemptID: "b-cr", RuleID: "backend-1:model-1", Sequence: 1,
		},
	}
	admissionResult, err := svc.Admit(context.Background(), admissionInput)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !admissionResult.Reserved {
		t.Fatalf("admit must reserve: %#v", admissionResult)
	}

	// Build the executor with the real store-backed service and a FinalizeBilling
	// hook that returns authoritative usage (5, less than the estimate of 8).
	ex := TestExecutor()
	ex.Store = mustNewB2BUAStore(t)
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"backend-1": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
				Operation: lipapi.OperationOpenAIChatCompletions,
				Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
			}),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}}), nil
			},
			FinalizeBilling: func(ctx context.Context, in execbackend.BillingFinalizationInput) (lipapi.Event, error) {
				return lipapi.Event{
					Kind:          lipapi.EventUsageDelta,
					InputTokens:   5,
					TotalTokens:   5,
					CostNanoUnits: 30,
					Currency:      "USD",
					Accounting: lipapi.UsageAccountingMetadata{
						Plane:     lipapi.UsagePlaneProviderBillable,
						Source:    lipapi.UsageSourceProviderReported,
						Authority: lipapi.UsageAuthorityAuthoritative,
					},
				}, nil
			},
		},
	}
	ex.UsageAuthority = svc
	ex.StreamUsage = accountingstream.New(nil, accountingstream.Config{})

	state := attemptAuthorityState{admissionInput: admissionInput, admissionResult: admissionResult}

	rs := &retryRecvStream{
		executor: ex,
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: lipapi.Call{
				ID:         "req-cancel-reconcile",
				Invocation: lipapi.Invocation{Operation: lipapi.OperationOpenAIChatCompletions},
			},
			traceID: "trace-cancel-reconcile",
			aLegID:  "a-cr",
		}),
		bleg:       b2bua.BLegRecord{BLegID: "b-cr", Seq: 1},
		cand:       authorityCandidate(),
		authority:  newAuthorityLifecycle(svc, nil, state, authorityCandidate()),
		seenEvents: []lipapi.Event{},
		accounting: newAttemptAccountingTracker(at),
	}

	// Step 1: settle as Partial with no usage — the estimate fallback applies,
	// so Consumed = 8 (the reserved estimate), and the lifecycle is marked settled.
	// Use rs.authority directly so the settled guard is set on the stream's
	// authority value, not a separate copy.
	rs.authority.Settle(context.Background(), authorityapp.SettlementKindPartial, lipapi.Event{}, false)
	if !rs.authority.Settled() {
		t.Fatal("expected lifecycle settled after partial settle")
	}

	limitRow := func(t *testing.T) (reserved, consumed int64) {
		t.Helper()
		page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
			Common:     controlplane.CommonFilters{BackendID: "backend-1"},
			RuleID:     rule.ID,
			Unit:       string(authoritydomain.AmountUnitInputTokens),
			Limit:      10,
			Visibility: controlplane.VisibilityDefault,
		})
		if err != nil {
			t.Fatalf("LimitStatus: %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("LimitStatus items = %d, want 1", len(page.Items))
		}
		return page.Items[0].Reserved, page.Items[0].Consumed
	}

	reserved, consumed := limitRow(t)
	if reserved != 0 {
		t.Fatalf("after partial settle, reserved = %d, want 0", reserved)
	}
	if consumed != 8 {
		t.Fatalf("after partial settle, consumed = %d, want 8 (estimate)", consumed)
	}

	// Step 2: persistCancellationBilling. usageObserved is false, so it calls
	// finalizeBillingAfterCancel, which recovers authoritative usage {5} and
	// adds it to seenEvents. The reservation is already settled, so
	// reconcileOrSettleCancellationAuthority routes to ReconcileAuthoritative,
	// which adjusts Consumed from 8 to 5.
	if rs.accounting.usageObserved {
		t.Fatal("test staging: usageObserved must be false before persistCancellationBilling (path 2)")
	}
	rs.persistCancellationBilling(context.Background(), "client canceled")

	reserved, consumed = limitRow(t)
	if reserved != 0 {
		t.Fatalf("after reconcile, reserved = %d, want 0", reserved)
	}
	if consumed != 5 {
		t.Fatalf("after reconcile, consumed = %d, want 5 (authoritative adjustment from 8)", consumed)
	}
	if !rs.authority.Settled() {
		t.Fatal("expected lifecycle to remain settled after reconcile")
	}
}

// mustNewB2BUAStore creates a b2bua MemoryStore for test executors, failing the
// test on error.
func mustNewB2BUAStore(t *testing.T) *b2bua.MemoryStore {
	t.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new b2bua store: %v", err)
	}
	return store
}
