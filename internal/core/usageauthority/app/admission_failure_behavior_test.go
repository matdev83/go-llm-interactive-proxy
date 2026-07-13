package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// failingEvidenceSink is an EvidenceSink that returns the configured error on
// the chosen sink, used to exercise evidence-projection failure resolution.
type failingEvidenceSink struct {
	policyErr     error
	accountingErr error
}

func (f *failingEvidenceSink) RecordPolicyDecision(context.Context, policydecision.Record) error {
	return f.policyErr
}

func (f *failingEvidenceSink) RecordAccountingAuthority(context.Context, controlplane.Event) error {
	return f.accountingErr
}

func authoritativeOnlyRule(id string, behavior domain.FailureBehavior) domain.Rule {
	return domain.Rule{
		ID:                   id,
		Kind:                 domain.RuleKindQuota,
		Mode:                 domain.RuleModeStrict,
		Unit:                 domain.AmountUnitRequests,
		Limit:                domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		AuthorityRequirement: domain.AuthorityRequirementAuthoritative,
		FailureBehavior:      behavior,
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}
}

func failureBehaviorBaseInput() AdmissionInput {
	return AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-fb",
			RequestID:  "request-fb",
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
		Authority: domain.AuthorityLevelEstimated,
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: "request-fb",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-fb",
			RuleID:           "rule-fb",
			Sequence:         1,
		},
	}
}

func TestAdmissionAuthorityUnavailableResolvesViaFailureBehavior(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		behavior    domain.FailureBehavior
		status      domain.AuthorityStatus
		wantAllowed bool
		wantOutcome domain.DecisionOutcome
		wantReason  policydecision.AccountingReasonCode
	}{
		{
			name:        "fail_open_proceeds_advisory",
			behavior:    domain.FailureBehaviorFailOpen,
			status:      domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			wantAllowed: true,
			wantOutcome: domain.DecisionOutcomeAdvisory,
			wantReason:  policydecision.AccountingReasonUnavailable,
		},
		{
			name:        "fail_closed_denies",
			behavior:    domain.FailureBehaviorFailClosed,
			status:      domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			wantAllowed: false,
			wantOutcome: domain.DecisionOutcomeDeny,
			wantReason:  policydecision.AccountingReasonUnavailable,
		},
		{
			name:        "default_ready_is_fail_closed",
			behavior:    domain.FailureBehaviorDefault,
			status:      domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			wantAllowed: false,
			wantOutcome: domain.DecisionOutcomeDeny,
			wantReason:  policydecision.AccountingReasonUnavailable,
		},
		{
			name:        "default_advisory_only_is_fail_open",
			behavior:    domain.FailureBehaviorDefault,
			status:      domain.AuthorityStatus{State: domain.AuthorityStateAdvisoryOnly, Reason: domain.StatusReasonAdvisoryOnly},
			wantAllowed: true,
			wantOutcome: domain.DecisionOutcomeAdvisory,
			wantReason:  policydecision.AccountingReasonUnavailable,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rule := authoritativeOnlyRule("tenant.authoritative", tt.behavior)
			store := newFakeStateStore()
			store.readiness = tt.status
			svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
				Status: tt.status,
				Rules:  []domain.Rule{rule},
			}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

			got, err := svc.Admit(context.Background(), failureBehaviorBaseInput())
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if got.Allowed != tt.wantAllowed {
				t.Fatalf("allowed = %v, want %v: %#v", got.Allowed, tt.wantAllowed, got)
			}
			if got.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %s, want %s: %#v", got.Outcome, tt.wantOutcome, got)
			}
			if got.PolicyRecord.ReasonCode != string(tt.wantReason) {
				t.Fatalf("reason = %q, want %q", got.PolicyRecord.ReasonCode, tt.wantReason)
			}
			// Authority-unavailable must not reserve against the store.
			if len(store.reserveCalls) != 0 {
				t.Fatalf("authority-unavailable must not reserve: %#v", store.reserveCalls)
			}
		})
	}
}

func TestAdmissionReservationInfrastructureFailureResolvesViaFailureBehavior(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		behavior    domain.FailureBehavior
		wantAllowed bool
		wantOutcome domain.DecisionOutcome
	}{
		{
			name:        "fail_open_proceeds",
			behavior:    domain.FailureBehaviorFailOpen,
			wantAllowed: true,
			wantOutcome: domain.DecisionOutcomeAdvisory,
		},
		{
			name:        "fail_closed_denies",
			behavior:    domain.FailureBehaviorFailClosed,
			wantAllowed: false,
			wantOutcome: domain.DecisionOutcomeDeny,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rule := domain.Rule{
				ID:              "tenant.requests",
				Kind:            domain.RuleKindQuota,
				Mode:            domain.RuleModeStrict,
				Unit:            domain.AmountUnitRequests,
				Limit:           domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
				FailureBehavior: tt.behavior,
				Match:           domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
			}
			store := newFakeStateStore()
			store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
			store.reserveErr = WrapError(ErrUnavailable, "reserve", errors.New("store unavailable"))
			svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
				Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
				Rules:  []domain.Rule{rule},
			}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

			got, err := svc.Admit(context.Background(), failureBehaviorBaseInput())
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if got.Allowed != tt.wantAllowed {
				t.Fatalf("allowed = %v, want %v: %#v", got.Allowed, tt.wantAllowed, got)
			}
			if got.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %s, want %s: %#v", got.Outcome, tt.wantOutcome, got)
			}
			if got.PolicyRecord.ReasonCode != string(policydecision.AccountingReasonReservationFailed) {
				t.Fatalf("reason = %q, want %q", got.PolicyRecord.ReasonCode, policydecision.AccountingReasonReservationFailed)
			}
		})
	}
}

func TestAdmissionEvidenceFailureResolvesViaFailureBehavior(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		behavior    domain.FailureBehavior
		wantAllowed bool
		wantOutcome domain.DecisionOutcome
	}{
		{
			name:        "fail_open_proceeds",
			behavior:    domain.FailureBehaviorFailOpen,
			wantAllowed: true,
			wantOutcome: domain.DecisionOutcomeAdvisory,
		},
		{
			name:        "fail_closed_denies",
			behavior:    domain.FailureBehaviorFailClosed,
			wantAllowed: false,
			wantOutcome: domain.DecisionOutcomeDeny,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rule := domain.Rule{
				ID:              "tenant.requests",
				Kind:            domain.RuleKindQuota,
				Mode:            domain.RuleModeStrict,
				Unit:            domain.AmountUnitRequests,
				Limit:           domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
				FailureBehavior: tt.behavior,
				Match:           domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
			}
			store := newFakeStateStore()
			store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
			store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-fb", ReservedAmount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1}}
			sink := &failingEvidenceSink{policyErr: errors.New("evidence sink down")}
			svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
				Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
				Rules:  []domain.Rule{rule},
			}}, store, sink, fixedClock{now: time.Unix(100, 0).UTC()})

			got, err := svc.Admit(context.Background(), failureBehaviorBaseInput())
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			// Evidence projection failed during the normal allow path: the
			// failure resolves via the rule's failure behavior. Fail-open still
			// proceeds; fail-closed denies.
			if got.Allowed != tt.wantAllowed {
				t.Fatalf("allowed = %v, want %v: %#v", got.Allowed, tt.wantAllowed, got)
			}
			if got.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %s, want %s: %#v", got.Outcome, tt.wantOutcome, got)
			}
		})
	}
}

func TestAdmissionRequiredEvidenceFailureDeniesWithoutReservation(t *testing.T) {
	t.Parallel()
	rule := domain.Rule{
		ID:              "advisory.required-evidence",
		Kind:            domain.RuleKindQuota,
		Mode:            domain.RuleModeAdvisory,
		Unit:            domain.AmountUnitRequests,
		Limit:           domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		FailureBehavior: domain.FailureBehaviorFailOpen,
		Match:           domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	sink := &failingEvidenceSink{policyErr: WrapError(ErrRequiredEvidence, "record policy", errors.New("recorder unavailable"))}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, sink, fixedClock{now: time.Unix(100, 0).UTC()})

	got, err := svc.Admit(context.Background(), failureBehaviorBaseInput())
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if got.Allowed || got.Outcome != domain.DecisionOutcomeDeny || got.Reserved {
		t.Fatalf("result = %#v, want unreserved denial", got)
	}
	if len(store.releaseCalls) != 0 {
		t.Fatalf("release calls = %d, want none without reservation", len(store.releaseCalls))
	}
}

func TestAdmissionMixedFailureBehaviorMostRestrictiveWins(t *testing.T) {
	t.Parallel()

	// Two matched strict rules: one fail-open, one fail-closed. The effective
	// posture must be fail-closed (most restrictive) for the authority-
	// unavailable outcome both rules produce under estimated evidence.
	openRule := authoritativeOnlyRule("tenant.open", domain.FailureBehaviorFailOpen)
	closedRule := authoritativeOnlyRule("tenant.closed", domain.FailureBehaviorFailClosed)
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{openRule, closedRule},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	got, err := svc.Admit(context.Background(), failureBehaviorBaseInput())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if got.Allowed {
		t.Fatalf("mixed fail-open + fail-closed must deny (fail-closed wins): %#v", got)
	}
	if got.Outcome != domain.DecisionOutcomeDeny {
		t.Fatalf("outcome = %s, want deny: %#v", got.Outcome, got)
	}
}
