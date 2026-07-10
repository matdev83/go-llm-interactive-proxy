package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestAdmissionService(t *testing.T) {
	t.Parallel()

	strictRule := domain.Rule{
		ID:    "tenant.requests",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}
	advisoryRule := strictRule
	advisoryRule.ID = "tenant.requests.advisory"
	advisoryRule.Mode = domain.RuleModeAdvisory

	baseInput := AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  "request-1",
			SessionID:  "session-1",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 3,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("principal-1"),
			TenantID:    scope.Known("tenant-1"),
			ProjectID:   scope.Known(""),
		},
		Dimensions: domain.Dimensions{
			Backend: scope.Known("backend-1"),
			Model:   scope.Known("model-1"),
		},
		Request:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 3},
		Spend:     domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 300, Currency: "usd"},
		Authority: domain.AuthorityLevelAuthoritative,
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: "request-1",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-1",
			RuleID:           "tenant.requests",
			Sequence:         1,
		},
	}

	t.Run("strict-allow-reserves", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		evidence := &fakeEvidenceSink{}
		store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
		store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-1", ReservedAmount: baseInput.Request}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{strictRule},
		}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

		got, err := svc.Admit(context.Background(), baseInput)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		if !got.Allowed || !got.Reserved || got.ReservationID != "reservation-1" {
			t.Fatalf("strict allow must reserve: %#v", got)
		}
		if len(evidence.policy) != 1 || len(evidence.accounting) != 1 {
			t.Fatalf("strict allow must emit policy and accounting evidence: %#v %#v", evidence.policy, evidence.accounting)
		}
		if !reflect.DeepEqual(got.PolicyRecord, evidence.policy[0]) {
			t.Fatalf("strict allow must return the emitted policy evidence: got %#v want %#v", got.PolicyRecord, evidence.policy[0])
		}
		if !reflect.DeepEqual(got.AccountingEvent, evidence.accounting[0]) {
			t.Fatalf("strict allow must return the emitted accounting evidence: got %#v want %#v", got.AccountingEvent, evidence.accounting[0])
		}
		if len(store.reserveCalls) != 1 {
			t.Fatalf("strict allow must touch store once: %#v", store.reserveCalls)
		}
	})

	t.Run("store-noop-reserve-keeps-noop-state", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
		store.reserveResult = ReserveResult{Applied: false, ReservationID: "existing"}
		evidence := &fakeEvidenceSink{}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{strictRule},
		}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

		got, err := svc.Admit(context.Background(), baseInput)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		if got.Reserved {
			t.Fatalf("noop reservation must not report reserved: %#v", got)
		}
		if got.ReservationID != "existing" {
			t.Fatalf("noop reservation must preserve reservation reference: %#v", got)
		}
		if len(evidence.policy) != 1 || len(evidence.accounting) != 1 {
			t.Fatalf("noop reservation must still emit evidence: %#v %#v", evidence.policy, evidence.accounting)
		}
		if !reflect.DeepEqual(got.PolicyRecord, evidence.policy[0]) {
			t.Fatalf("noop reservation must return the emitted policy evidence: got %#v want %#v", got.PolicyRecord, evidence.policy[0])
		}
		if !reflect.DeepEqual(got.AccountingEvent, evidence.accounting[0]) {
			t.Fatalf("noop reservation must return the emitted accounting evidence: got %#v want %#v", got.AccountingEvent, evidence.accounting[0])
		}
	})

	t.Run("strict-deny-does-not-reserve", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{strictRule},
		}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

		deniedInput := baseInput
		deniedInput.Request = domain.Amount{Unit: domain.AmountUnitRequests, Value: 11}

		got, err := svc.Admit(context.Background(), deniedInput)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		if got.Allowed || got.Reserved || got.ReservationID != "" {
			t.Fatalf("strict deny must not reserve: %#v", got)
		}
		if len(store.reserveCalls) != 0 {
			t.Fatalf("strict deny must not mutate store: %#v", store.reserveCalls)
		}
	})

	t.Run("advisory-does-not-block-or-reserve", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateAdvisoryOnly, Reason: domain.StatusReasonAdvisoryOnly},
			Rules:  []domain.Rule{advisoryRule},
		}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

		advisoryInput := baseInput
		advisoryInput.Request = domain.Amount{Unit: domain.AmountUnitRequests, Value: 11}

		got, err := svc.Admit(context.Background(), advisoryInput)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		if !got.Allowed || got.Reserved || got.ReservationID != "" {
			t.Fatalf("advisory admission must not reserve: %#v", got)
		}
		if len(store.reserveCalls) != 0 {
			t.Fatalf("advisory admission must not mutate store: %#v", store.reserveCalls)
		}
	})

	t.Run("estimate-only-does-not-mutate", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{strictRule},
		}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

		estimateOnlyInput := baseInput
		estimateOnlyInput.EstimateOnly = true

		got, err := svc.Admit(context.Background(), estimateOnlyInput)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		if !got.Allowed || got.ReservationID != "" || got.Reserved {
			t.Fatalf("estimate-only admission must not reserve: %#v", got)
		}
		if len(store.reserveCalls) != 0 {
			t.Fatalf("estimate-only admission must not mutate store: %#v", store.reserveCalls)
		}
	})

	t.Run("expired-context-yields-timeout", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{strictRule},
		}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

		ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
		defer cancel()

		_, err := svc.Admit(ctx, baseInput)
		if !errors.Is(err, ErrEvaluationTimeout) {
			t.Fatalf("expired context must map to evaluation timeout, got %v", err)
		}
	})
}

func TestAdmissionServiceStrictRateRulesReserveCumulativeUsage(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0).UTC()
	rateRule := domain.Rule{
		ID:   "tenant.rate.requests",
		Kind: domain.RuleKindRate,
		Mode: domain.RuleModeStrict,
		Unit: domain.AmountUnitRequests,
		Limit: domain.Amount{
			Unit:  domain.AmountUnitRequests,
			Value: 1,
		},
		Window: domain.WindowSpec{
			Algorithm: domain.WindowAlgorithmFixed,
			Size:      time.Hour,
			Anchor:    time.Unix(0, 0).UTC(),
		},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}

	baseInput := AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  "request-1",
			SessionID:  "session-1",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 1,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("principal-1"),
			TenantID:    scope.Known("tenant-1"),
		},
		Dimensions: domain.Dimensions{
			Backend: scope.Known("backend-1"),
			Model:   scope.Known("model-1"),
		},
		Request:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		Authority: domain.AuthorityLevelAuthoritative,
	}

	t.Run("first-request-reserves-with-rate-type", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		store.capacityLimit = 1
		store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{rateRule},
		}}, store, &fakeEvidenceSink{}, fixedClock{now: now})

		input := baseInput
		input.ReservationKey = domain.ReservationKey{
			LogicalRequestID: "request-1",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-1",
			RuleID:           rateRule.ID,
			Sequence:         1,
		}

		got, err := svc.Admit(context.Background(), input)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		if !got.Allowed || !got.Reserved || got.ReservationID == "" {
			t.Fatalf("first strict rate request must allow and reserve: %#v", got)
		}
		if len(store.reserveCalls) != 1 {
			t.Fatalf("strict rate allow must touch store once: %#v", store.reserveCalls)
		}
		if store.reserveCalls[0].RuleType != "rate" {
			t.Fatalf("strict rate reserve must use rate rule type: %#v", store.reserveCalls[0])
		}
	})

	t.Run("second-request-in-window-exceeds-capacity", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		store.capacityLimit = 1
		store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{rateRule},
		}}, store, &fakeEvidenceSink{}, fixedClock{now: now})

		firstInput := baseInput
		firstInput.ReservationKey = domain.ReservationKey{
			LogicalRequestID: "request-1",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-1",
			RuleID:           rateRule.ID,
			Sequence:         1,
		}
		first, err := svc.Admit(context.Background(), firstInput)
		if err != nil {
			t.Fatalf("first admit: %v", err)
		}
		if !first.Allowed || !first.Reserved {
			t.Fatalf("first request must allow and reserve: %#v", first)
		}

		secondInput := baseInput
		secondInput.Correlation.RequestID = "request-2"
		secondInput.ReservationKey = domain.ReservationKey{
			LogicalRequestID: "request-2",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-2",
			RuleID:           rateRule.ID,
			Sequence:         1,
		}
		_, err = svc.Admit(context.Background(), secondInput)
		if !errors.Is(err, ErrReservationConflict) {
			t.Fatalf("second request in same window must fail reservation conflict, got %v", err)
		}
		if len(store.reserveCalls) != 2 {
			t.Fatalf("second request must attempt store reservation: %#v", store.reserveCalls)
		}
	})
}

func TestAdmissionServiceReservesRequestCountForRequestUnitRules(t *testing.T) {
	t.Parallel()

	requestRule := domain.Rule{
		ID:    "tenant.requests",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}

	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-1", ReservedAmount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1}}
	evidence := &fakeEvidenceSink{}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{requestRule},
	}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

	input := AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  "request-1",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 1,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope: scope.PrincipalScopeView{TenantID: scope.Known("tenant-1")},
		Dimensions: domain.Dimensions{
			Backend: scope.Known("backend-1"),
			Model:   scope.Known("model-1"),
		},
		Request:      domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 500},
		RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		Authority:    domain.AuthorityLevelEstimated,
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: "request-1",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-1",
			RuleID:           "tenant.requests",
			Sequence:         1,
		},
	}

	got, err := svc.Admit(context.Background(), input)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !got.Allowed || !got.Reserved || got.ReservationID != "reservation-1" {
		t.Fatalf("request-based admission must allow and reserve: %#v", got)
	}
	if len(store.reserveCalls) != 1 {
		t.Fatalf("expected one reserve call, got %#v", store.reserveCalls)
	}
	reserved := store.reserveCalls[0].Request
	if reserved.Unit != domain.AmountUnitRequests || reserved.Value != 1 {
		t.Fatalf("reserve request amount = %v, want 1 requests", reserved)
	}
	if got.ReservedAmount.Unit != domain.AmountUnitRequests || got.ReservedAmount.Value != 1 {
		t.Fatalf("reserved amount = %v, want 1 requests", got.ReservedAmount)
	}
}

func TestAdmissionServiceUnknownAttributionModesAffectMatchingAndEvidence(t *testing.T) {
	t.Parallel()

	projectRule := domain.Rule{
		ID:    "tenant.project",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
			Project: domain.DimensionMatcher{Value: scope.Known("")},
		},
	}

	baseInput := AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  "request-1",
			SessionID:  "session-1",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 1,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("principal-1"),
			TenantID:    scope.Known("tenant-1"),
			ProjectID:   scope.Unknown(),
		},
		Dimensions: domain.Dimensions{
			Backend: scope.Known("backend-1"),
			Model:   scope.Known("model-1"),
			Project: scope.Unknown(),
		},
		Request:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		Authority: domain.AuthorityLevelAuthoritative,
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: "request-1",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-1",
			RuleID:           projectRule.ID,
			Sequence:         1,
		},
	}

	cases := []struct {
		name      string
		mode      domain.UnknownAttribution
		wantMatch bool
		wantScope scope.Value
	}{
		{name: "preserve", mode: domain.UnknownAttributionPreserve, wantMatch: false, wantScope: scope.Unknown()},
		{name: "known-empty", mode: domain.UnknownAttributionKnownEmpty, wantMatch: true, wantScope: scope.Known("")},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeStateStore()
			store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
			store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-1", ReservedAmount: baseInput.Request}
			evidence := &fakeEvidenceSink{}
			svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
				Status:             domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
				UnknownAttribution: tt.mode,
				Rules:              []domain.Rule{projectRule},
			}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

			got, err := svc.Admit(context.Background(), baseInput)
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if got.Reserved != tt.wantMatch {
				t.Fatalf("reserved = %v, want %v", got.Reserved, tt.wantMatch)
			}
			if tt.wantMatch {
				if len(store.reserveCalls) != 1 {
					t.Fatalf("expected one reserve call, got %#v", store.reserveCalls)
				}
				if got.ReservationID != "reservation-1" {
					t.Fatalf("reservation id = %q, want reservation-1", got.ReservationID)
				}
				if !store.reserveCalls[0].Dimensions.Project.Equal(tt.wantScope) {
					t.Fatalf("reserved dimensions project = %#v, want %#v", store.reserveCalls[0].Dimensions.Project, tt.wantScope)
				}
			} else if len(store.reserveCalls) != 0 {
				t.Fatalf("preserve mode must not reserve: %#v", store.reserveCalls)
			}
			if !got.PolicyRecord.Scope.ProjectID.Equal(tt.wantScope) {
				t.Fatalf("policy evidence project scope = %#v, want %#v", got.PolicyRecord.Scope.ProjectID, tt.wantScope)
			}
			if !got.AccountingEvent.AccountingAuthority.Scope.ProjectID.Equal(tt.wantScope) {
				t.Fatalf("accounting evidence project scope = %#v, want %#v", got.AccountingEvent.AccountingAuthority.Scope.ProjectID, tt.wantScope)
			}
		})
	}
}
