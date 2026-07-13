package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
)

func TestAdmissionUsesFailingRuleBehaviorAndReservesHealthyRules(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	rules := []domain.Rule{
		{ID: "open-quota", Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict, Unit: domain.AmountUnitRequests, Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, FailureBehavior: domain.FailureBehaviorFailOpen},
		{ID: "closed-budget", Kind: domain.RuleKindBudget, Mode: domain.RuleModeStrict, Unit: domain.AmountUnitMoneyNano, Currency: "USD", Limit: domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 100, Currency: "USD"}, FailureBehavior: domain.FailureBehaviorFailClosed},
	}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady}
	store.reserveErrors = []error{&RuleReservationError{RuleID: "open-quota", Err: ErrUnavailable}}
	store.reserveResults = []ReserveResult{{}, {
		Applied: true, ReservationID: "budget-reservation", ReservedAmount: domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 20, Currency: "USD"},
		Reservations: []AdmissionReservation{{ReservationID: "budget-reservation", RuleID: "closed-budget", ReservedAmount: domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 20, Currency: "USD"}}},
	}}
	evidence := &fakeEvidenceSink{}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: rules}}, store, evidence, fixedClock{now: now})
	result, err := svc.Admit(context.Background(), AdmissionInput{
		Correlation: controlplane.Correlation{TraceID: "trace", BackendID: "backend", Model: "model"},
		Scope:       principalScope(), Dimensions: domain.Dimensions{}, RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		Spend: domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 20, Currency: "USD"}, Authority: domain.AuthorityLevelAuthoritative,
		ReservationKey: domain.ReservationKey{LogicalRequestID: "request", AttemptID: "attempt", Sequence: 1},
	})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !result.Allowed || result.Outcome != domain.DecisionOutcomeAdvisory {
		t.Fatalf("result = %#v, want allowed advisory", result)
	}
	if !result.Reserved || len(result.Reservations) != 1 || result.Reservations[0].RuleID != "closed-budget" {
		t.Fatalf("reservations = %#v, want healthy closed-budget reservation", result.Reservations)
	}
	if len(result.UnreservedRuleIDs) != 1 || result.UnreservedRuleIDs[0] != "open-quota" {
		t.Fatalf("unreserved = %#v, want open-quota", result.UnreservedRuleIDs)
	}
	if result.SelectedRuleID != "open-quota" || result.RuleKind != domain.RuleKindQuota {
		t.Fatalf("selected failure = %q/%q, want open-quota/quota", result.SelectedRuleID, result.RuleKind)
	}
	if len(store.reserveCalls) != 2 || len(store.reserveCalls[0].Reservations) != 2 || len(store.reserveCalls[1].Reservations) != 1 {
		t.Fatalf("reserve calls = %#v, want full set then healthy rule", store.reserveCalls)
	}
}

func TestAdmissionCapacityExhaustionNeverFailsOpen(t *testing.T) {
	t.Parallel()
	rule := domain.Rule{ID: "open-quota", Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict, Unit: domain.AmountUnitRequests, Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, FailureBehavior: domain.FailureBehaviorFailOpen}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady}
	store.reserveErr = &RuleReservationError{RuleID: rule.ID, Err: &ReservationCapacityError{Requested: domain.Amount{Unit: domain.AmountUnitRequests, Value: 2}, Remaining: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1}}}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	result, err := svc.Admit(context.Background(), AdmissionInput{RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 2}, Authority: domain.AuthorityLevelAuthoritative, ReservationKey: domain.ReservationKey{LogicalRequestID: "r", AttemptID: "a", Sequence: 1}})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if result.Allowed || result.Outcome != domain.DecisionOutcomeDeny || len(store.reserveCalls) != 1 {
		t.Fatalf("result=%#v calls=%d, want one-call capacity denial", result, len(store.reserveCalls))
	}
	if result.PolicyRecord.ReasonCode != "quota_exceeded" {
		t.Fatalf("reason = %q, want quota_exceeded", result.PolicyRecord.ReasonCode)
	}
}

func TestAdmissionDeterministicConflictNeverFailsOpen(t *testing.T) {
	t.Parallel()
	rule := domain.Rule{ID: "open-quota", Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict, Unit: domain.AmountUnitRequests, Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, FailureBehavior: domain.FailureBehaviorFailOpen}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady}
	store.reserveErr = ErrReservationConflict
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	result, err := svc.Admit(context.Background(), AdmissionInput{RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1}, Authority: domain.AuthorityLevelAuthoritative, ReservationKey: domain.ReservationKey{LogicalRequestID: "r", AttemptID: "a", Sequence: 1}})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if result.Allowed || result.Outcome != domain.DecisionOutcomeDeny || len(store.reserveCalls) != 1 {
		t.Fatalf("result=%#v calls=%d, want deterministic denial", result, len(store.reserveCalls))
	}
}

func TestAdmissionFailClosedAttributedRuleDoesNotRetry(t *testing.T) {
	t.Parallel()
	rule := domain.Rule{ID: "closed-quota", Kind: domain.RuleKindQuota, Mode: domain.RuleModeStrict, Unit: domain.AmountUnitRequests, Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, FailureBehavior: domain.FailureBehaviorFailClosed}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady}
	store.reserveErr = &RuleReservationError{RuleID: rule.ID, Err: ErrReservationConflict}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})
	result, err := svc.Admit(context.Background(), AdmissionInput{RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1}, Authority: domain.AuthorityLevelAuthoritative, ReservationKey: domain.ReservationKey{LogicalRequestID: "r", AttemptID: "a", Sequence: 1}})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if result.Allowed || result.Outcome != domain.DecisionOutcomeDeny || len(store.reserveCalls) != 1 {
		t.Fatalf("result=%#v calls=%d, want one-call denial", result, len(store.reserveCalls))
	}
	if !errors.Is(store.reserveErr, ErrReservationConflict) {
		t.Fatal("attributed error must preserve reservation conflict identity")
	}
}
