package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
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
		evidence := &fakeEvidenceSink{}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{strictRule},
		}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

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
		// Estimate-only precheck still records durable evidence (denial audit /
		// advisory posture). Clamp preview must use SkipEvidence instead.
		if len(evidence.policy) != 1 || len(evidence.accounting) != 1 {
			t.Fatalf("estimate-only admit still records evidence: policy=%d accounting=%d", len(evidence.policy), len(evidence.accounting))
		}
	})

	t.Run("skip-evidence-does-not-record", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		evidence := &fakeEvidenceSink{}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{strictRule},
		}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

		previewInput := baseInput
		previewInput.EstimateOnly = true
		previewInput.SkipEvidence = true

		got, err := svc.Admit(context.Background(), previewInput)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		if !got.Allowed || got.ReservationID != "" || got.Reserved {
			t.Fatalf("skip-evidence admission must not reserve: %#v", got)
		}
		if len(store.reserveCalls) != 0 {
			t.Fatalf("skip-evidence admission must not mutate store: %#v", store.reserveCalls)
		}
		if len(evidence.policy) != 0 || len(evidence.accounting) != 0 {
			t.Fatalf("skip-evidence admission must not record durable evidence: policy=%#v accounting=%#v", evidence.policy, evidence.accounting)
		}
	})

	t.Run("expired-context-resolves-via-failure-behavior", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name        string
			behavior    domain.FailureBehavior
			status      domain.AuthorityStatus
			wantAllow   bool
			wantOutcome domain.DecisionOutcome
		}{
			{
				name:        "fail_closed_default",
				behavior:    domain.FailureBehaviorDefault,
				status:      domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
				wantAllow:   false,
				wantOutcome: domain.DecisionOutcomeDeny,
			},
			{
				name:        "fail_open_explicit",
				behavior:    domain.FailureBehaviorFailOpen,
				status:      domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
				wantAllow:   true,
				wantOutcome: domain.DecisionOutcomeAdvisory,
			},
			{
				name:        "fail_open_global_advisory_only",
				behavior:    domain.FailureBehaviorDefault,
				status:      domain.AuthorityStatus{State: domain.AuthorityStateAdvisoryOnly, Reason: domain.StatusReasonAdvisoryOnly},
				wantAllow:   true,
				wantOutcome: domain.DecisionOutcomeAdvisory,
			},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				rule := strictRule
				rule.FailureBehavior = tt.behavior
				store := newFakeStateStore()
				svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
					Status: tt.status,
					Rules:  []domain.Rule{rule},
				}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

				ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
				defer cancel()

				got, err := svc.Admit(ctx, baseInput)
				if err != nil {
					t.Fatalf("admit: %v", err)
				}
				if got.Allowed != tt.wantAllow {
					t.Fatalf("allowed = %v, want %v (outcome %s): %#v", got.Allowed, tt.wantAllow, tt.wantOutcome, got)
				}
				if got.Outcome != tt.wantOutcome {
					t.Fatalf("outcome = %s, want %s: %#v", got.Outcome, tt.wantOutcome, got)
				}
				if got.PolicyRecord.ReasonCode == "" {
					t.Fatalf("resolved failure must project a reason code: %#v", got.PolicyRecord)
				}
			})
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
		second, err := svc.Admit(context.Background(), secondInput)
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		// Atomic capacity exhaustion is a deterministic rate denial regardless
		// of failure behavior, returned as a normal admission result.
		if second.Allowed || second.Outcome != domain.DecisionOutcomeDeny {
			t.Fatalf("second request in same window must be denied via fail-closed, got %#v", second)
		}
		if second.PolicyRecord.ReasonCode != string(policydecision.AccountingReasonRateLimited) {
			t.Fatalf("second request reason = %q, want %q", second.PolicyRecord.ReasonCode, policydecision.AccountingReasonRateLimited)
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

func TestRetiredMoneyAdmissionBehaviorRemoved(t *testing.T) { t.Parallel() }

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
			if !got.AccountingEvent.AccountingAuthority().Scope.ProjectID.Equal(tt.wantScope) {
				t.Fatalf("accounting evidence project scope = %#v, want %#v", got.AccountingEvent.AccountingAuthority().Scope.ProjectID, tt.wantScope)
			}
		})
	}
}

func TestAdmissionServiceUnknownAttributionNormalizesConfiguredPolicyLabels(t *testing.T) {
	t.Parallel()

	rule := domain.Rule{
		ID:    "tenant.label-empty",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
			Labels:  map[string]domain.DimensionMatcher{"tier": {Value: scope.Known("")}},
		},
	}
	base := AdmissionInput{
		Correlation:    controlplane.Correlation{TraceID: "trace-label", RequestID: "request-label", BackendID: "backend-1", Model: "model-1"},
		Scope:          scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1")},
		Dimensions:     domain.Dimensions{Backend: scope.Known("backend-1"), Model: scope.Known("model-1")},
		Request:        domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		Authority:      domain.AuthorityLevelAuthoritative,
		ReservationKey: domain.ReservationKey{LogicalRequestID: "request-label", RuleID: rule.ID, Sequence: 1},
	}
	for _, mode := range []domain.UnknownAttribution{domain.UnknownAttributionPreserve, domain.UnknownAttributionKnownEmpty} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			store := newFakeStateStore()
			store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
			store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-label", ReservedAmount: base.Request}
			svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
				Status:             domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
				UnknownAttribution: mode,
				Rules:              []domain.Rule{rule},
			}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})
			got, err := svc.Admit(context.Background(), base)
			if err != nil {
				t.Fatalf("Admit: %v", err)
			}
			wantReserved := mode == domain.UnknownAttributionKnownEmpty
			if got.Reserved != wantReserved {
				t.Fatalf("reserved=%v, want %v: %#v", got.Reserved, wantReserved, got)
			}
			if wantReserved {
				labels := store.reserveCalls[0].Reservations[0].Dimensions.PolicyLabels
				if value, ok := labels["tier"]; !ok || !value.Equal(scope.Known("")) {
					t.Fatalf("reservation label = %#v, want known-empty", labels)
				}
				if value, ok := got.PolicyRecord.Scope.PolicyLabels["tier"]; !ok || value != "" {
					t.Fatalf("evidence label = %#v, want known-empty", got.PolicyRecord.Scope.PolicyLabels)
				}
			}
		})
	}
}

// TestAdmissionServiceReservesAgainstAllMatchedStrictRules proves that when a
// single request matches more than one strict quota/rate/budget/spend-cap rule,
// admission reserves against EVERY matched rule window, not just the first.
// Reserving only the first leaves the other matched windows unreserved, so
// later admissions can over-commit against them.
func TestAdmissionServiceReservesAgainstAllMatchedStrictRules(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0).UTC()
	requestsRule := domain.Rule{
		ID:    "tenant.requests",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	dailyRule := requestsRule
	dailyRule.ID = "tenant.requests.daily"
	dailyRule.Unit = domain.AmountUnitOutputTokens
	dailyRule.Limit = domain.Amount{Unit: domain.AmountUnitOutputTokens, Value: 100}

	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	store.reserveResult = ReserveResult{
		Applied:        true,
		ReservationID:  "reservation-primary",
		ReservedAmount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 3},
		Reservations: []AdmissionReservation{
			{ReservationID: "reservation-primary", RuleID: requestsRule.ID, ReservedAmount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 3}},
			{ReservationID: "reservation-secondary", RuleID: dailyRule.ID, ReservedAmount: domain.Amount{Unit: domain.AmountUnitOutputTokens, Value: 4}},
		},
	}
	evidence := &fakeEvidenceSink{}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{requestsRule, dailyRule},
	}}, store, evidence, fixedClock{now: now})

	input := AdmissionInput{
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
		Request: domain.Amount{Unit: domain.AmountUnitRequests, Value: 3},
		PreflightUsage: domain.PreflightUsage{
			InputTokens:  3,
			OutputTokens: 4,
			TotalTokens:  7,
		},
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

	got, err := svc.Admit(context.Background(), input)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !got.Allowed || !got.Reserved || got.ReservationID != "reservation-primary" {
		t.Fatalf("multi-rule admission must allow and reserve: %#v", got)
	}
	if len(store.reserveCalls) != 1 {
		t.Fatalf("expected one atomic reserve call for the matched strict set, got %d: %#v", len(store.reserveCalls), store.reserveCalls)
	}
	if len(got.Reservations) != 2 {
		t.Fatalf("expected two returned reservations, got %#v", got.Reservations)
	}
	if got.Reservations[0].ReservationID != "reservation-primary" {
		t.Fatalf("primary reservation id = %q, want reservation-primary", got.Reservations[0].ReservationID)
	}
	if got.Reservations[0].RuleID != "tenant.requests" {
		t.Fatalf("primary reservation rule id = %q, want tenant.requests", got.Reservations[0].RuleID)
	}
	if got.Reservations[0].ReservedAmount.Unit != domain.AmountUnitRequests || got.Reservations[0].ReservedAmount.Value != 3 {
		t.Fatalf("primary reservation amount = %#v, want 3 requests", got.Reservations[0].ReservedAmount)
	}
	if got.Reservations[1].ReservationID != "reservation-secondary" {
		t.Fatalf("secondary reservation id = %q, want reservation-secondary", got.Reservations[1].ReservationID)
	}
	if got.Reservations[1].RuleID != "tenant.requests.daily" {
		t.Fatalf("secondary reservation rule id = %q, want tenant.requests.daily", got.Reservations[1].RuleID)
	}
	if got.Reservations[1].ReservedAmount.Unit != domain.AmountUnitOutputTokens || got.Reservations[1].ReservedAmount.Value != 4 {
		t.Fatalf("secondary reservation amount = %#v, want 4 output_tokens", got.Reservations[1].ReservedAmount)
	}
	if got.ReservationID != got.Reservations[0].ReservationID {
		t.Fatalf("primary reservation id = %q, want %q", got.ReservationID, got.Reservations[0].ReservationID)
	}
	if got.ReservedAmount.Unit != domain.AmountUnitRequests || got.ReservedAmount.Value != 3 {
		t.Fatalf("primary reserved amount = %#v, want 3 requests", got.ReservedAmount)
	}
	command := store.reserveCalls[0]
	if len(command.Reservations) != 2 {
		t.Fatalf("atomic reserve must carry both descriptors, got %#v", command.Reservations)
	}
	if command.Reservations[0].RuleID != "tenant.requests" || command.Reservations[0].ReservationKey.RuleID != "tenant.requests" {
		t.Fatalf("primary descriptor must use the matched rule key, got %#v", command.Reservations[0])
	}
	if command.Reservations[1].RuleID != "tenant.requests.daily" || command.Reservations[1].ReservationKey.RuleID != "tenant.requests.daily" {
		t.Fatalf("secondary descriptor must use its matched rule key, got %#v", command.Reservations[1])
	}
	if command.Reservations[0].Amount.Unit != domain.AmountUnitRequests || command.Reservations[0].Amount.Value != 3 {
		t.Fatalf("primary descriptor amount = %#v, want 3 requests", command.Reservations[0].Amount)
	}
	if command.Reservations[1].Amount.Unit != domain.AmountUnitOutputTokens || command.Reservations[1].Amount.Value != 4 {
		t.Fatalf("secondary descriptor amount = %#v, want 4 output_tokens", command.Reservations[1].Amount)
	}
	if command.Reservations[0].ReservationKey.String() == command.Reservations[1].ReservationKey.String() {
		t.Fatalf("primary and secondary reservations must not share a reservation key")
	}
}

func TestAdmissionServiceDoesNotCompensateAfterAtomicReserveFailure(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0).UTC()
	requestsRule := domain.Rule{
		ID:    "tenant.requests",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}
	dailyRule := requestsRule
	dailyRule.ID = "tenant.requests.daily"
	dailyRule.Limit = domain.Amount{Unit: domain.AmountUnitRequests, Value: 100}

	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	store.reserveErr = WrapError(ErrReservationConflict, "reserve", errors.New("atomic reservation set rejected"))
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{requestsRule, dailyRule},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: now})

	input := AdmissionInput{
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
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: "request-1",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-1",
			RuleID:           requestsRule.ID,
			Sequence:         1,
		},
	}

	_, err := svc.Admit(context.Background(), input)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	// The atomic set failure resolves via fail-closed (default behavior, Ready
	// backing). No per-rule compensation call is legal because the store owns
	// the all-or-nothing mutation boundary.
	if len(store.reserveCalls) != 1 {
		t.Fatalf("expected one atomic reserve call, got %d: %#v", len(store.reserveCalls), store.reserveCalls)
	}
	if len(store.releaseCalls) != 0 {
		t.Fatalf("atomic reserve failure must not trigger application compensation, got %#v", store.releaseCalls)
	}
	if len(store.reservations) != 0 {
		t.Fatalf("atomic reserve failure must leave no reservation, got %#v", store.reservations)
	}
	if store.cumulativeReserved != 0 {
		t.Fatalf("atomic reserve failure must leave capacity unchanged, got %d", store.cumulativeReserved)
	}
}
