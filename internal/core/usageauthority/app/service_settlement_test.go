package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestSettlementAndRelease(t *testing.T) {
	t.Parallel()

	quotaRule := domain.Rule{
		ID:    "tenant.requests",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}

	baseKey := domain.ReservationKey{
		LogicalRequestID: "request-1",
		ALegID:           "a-1",
		BLegID:           "b-1",
		AttemptID:        "attempt-1",
		RuleID:           "tenant.requests",
		Sequence:         1,
	}

	baseInput := SettleInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  "request-1",
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
		ReservationKey:   baseKey,
		RuleID:           quotaRule.ID,
		ReservationID:    "reservation-1",
		Kind:             SettlementKindFinal,
		FinalUsage:       domain.Amount{Unit: domain.AmountUnitRequests, Value: 6},
		ReservedUsage:    domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Authority:        domain.AuthorityLevelAuthoritative,
		Stage:            feature.StageIDAttemptLifecycle,
		BackendAttempted: true,
		OutputCommitted:  true,
	}

	t.Run("final-surfaced-settlement-releases-unused-and-is-idempotent", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		evidence := &fakeEvidenceSink{}
		store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-1", ReservedAmount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{quotaRule},
		}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

		first, err := svc.Settle(context.Background(), baseInput)
		if err != nil {
			t.Fatalf("settle: %v", err)
		}
		if !first.Applied || first.ReleasedDelta.Value != 4 || first.OverageDelta.Value != 0 {
			t.Fatalf("lower-than-reserved settlement must release unused amount: %#v", first)
		}
		if len(store.settleCalls) != 1 {
			t.Fatalf("settlement store call count mismatch: %#v", store.settleCalls)
		}
		if len(store.reserveCalls) != 0 {
			t.Fatalf("settlement test should not reserve: %#v", store.reserveCalls)
		}
		if !reflect.DeepEqual(first.PolicyRecord, evidence.policy[0]) {
			t.Fatalf("settlement must return the emitted policy evidence: got %#v want %#v", first.PolicyRecord, evidence.policy[0])
		}
		if !reflect.DeepEqual(first.AccountingEvent, evidence.accounting[0]) {
			t.Fatalf("settlement must return the emitted accounting evidence: got %#v want %#v", first.AccountingEvent, evidence.accounting[0])
		}
		if first.PolicyRecord.Stage != feature.StageIDAttemptLifecycle || first.PolicyRecord.Provider.Stage != feature.StageIDAttemptLifecycle {
			t.Fatalf("settlement must project the late attempt lifecycle stage: %#v", first.PolicyRecord)
		}
		if !first.PolicyRecord.BackendAttempted || !first.PolicyRecord.OutputCommitted {
			t.Fatalf("settlement must preserve backend/output commitment flags: %#v", first.PolicyRecord)
		}

		second, err := svc.Settle(context.Background(), baseInput)
		if err != nil {
			t.Fatalf("duplicate settle: %v", err)
		}
		if second.Applied || second.ReleasedDelta.Value != 0 || second.OverageDelta.Value != 0 {
			t.Fatalf("duplicate settlement must be idempotent: %#v", second)
		}
		if len(store.settleCalls) != 2 {
			t.Fatalf("store should see both attempts for idempotent classification: %#v", store.settleCalls)
		}
	})

	t.Run("final-surfaced-overage-is-recorded", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		evidence := &fakeEvidenceSink{}
		store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-1", ReservedAmount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{quotaRule},
		}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

		overageInput := baseInput
		overageInput.FinalUsage = domain.Amount{Unit: domain.AmountUnitRequests, Value: 12}

		got, err := svc.Settle(context.Background(), overageInput)
		if err != nil {
			t.Fatalf("settle: %v", err)
		}
		if !got.Applied || got.OverageDelta.Value != 2 || got.ReleasedDelta.Value != 0 {
			t.Fatalf("overage settlement must record overage only: %#v", got)
		}
		if !reflect.DeepEqual(got.PolicyRecord, evidence.policy[0]) {
			t.Fatalf("overage settlement must return the emitted policy evidence: got %#v want %#v", got.PolicyRecord, evidence.policy[0])
		}
		if !reflect.DeepEqual(got.AccountingEvent, evidence.accounting[0]) {
			t.Fatalf("overage settlement must return the emitted accounting evidence: got %#v want %#v", got.AccountingEvent, evidence.accounting[0])
		}
	})

	t.Run("cancellation-still-settles-available-evidence", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		evidence := &fakeEvidenceSink{}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{quotaRule},
		}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

		cancelInput := baseInput
		cancelInput.Kind = SettlementKindCancellation
		cancelInput.ClientCanceled = true
		cancelInput.FinalUsage = domain.Amount{Unit: domain.AmountUnitRequests, Value: 2}
		cancelInput.OutputCommitted = false

		got, err := svc.Settle(context.Background(), cancelInput)
		if err != nil {
			t.Fatalf("cancellation settlement must not fail as accounting denial: %v", err)
		}
		if !got.Applied {
			t.Fatalf("cancellation settlement must still apply available evidence: %#v", got)
		}
		if !reflect.DeepEqual(got.PolicyRecord, evidence.policy[0]) {
			t.Fatalf("cancellation settlement must return the emitted policy evidence: got %#v want %#v", got.PolicyRecord, evidence.policy[0])
		}
		if !reflect.DeepEqual(got.AccountingEvent, evidence.accounting[0]) {
			t.Fatalf("cancellation settlement must return the emitted accounting evidence: got %#v want %#v", got.AccountingEvent, evidence.accounting[0])
		}
	})

	t.Run("swallowed-and-losing-attempts-release-idempotently", func(t *testing.T) {
		t.Parallel()

		store := newFakeStateStore()
		evidence := &fakeEvidenceSink{}
		svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
			Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:  []domain.Rule{quotaRule},
		}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

		releaseInput := ReleaseInput{
			Correlation:      baseInput.Correlation,
			Scope:            baseInput.Scope,
			ReservationKey:   baseKey,
			RuleID:           quotaRule.ID,
			ReservationID:    "reservation-1",
			Kind:             ReleaseKindSwallowed,
			Amount:           domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
			Stage:            feature.StageIDAttemptLifecycle,
			BackendAttempted: true,
			OutputCommitted:  false,
		}

		first, err := svc.Release(context.Background(), releaseInput)
		if err != nil {
			t.Fatalf("release: %v", err)
		}
		if !first.Applied || first.ReleasedDelta.Value != 10 {
			t.Fatalf("swallowed attempt must release reservation: %#v", first)
		}
		if !reflect.DeepEqual(first.PolicyRecord, evidence.policy[0]) {
			t.Fatalf("release must return the emitted policy evidence: got %#v want %#v", first.PolicyRecord, evidence.policy[0])
		}
		if !reflect.DeepEqual(first.AccountingEvent, evidence.accounting[0]) {
			t.Fatalf("release must return the emitted accounting evidence: got %#v want %#v", first.AccountingEvent, evidence.accounting[0])
		}
		if first.PolicyRecord.Stage != feature.StageIDAttemptLifecycle || first.PolicyRecord.Provider.Stage != feature.StageIDAttemptLifecycle {
			t.Fatalf("release must project the late attempt lifecycle stage: %#v", first.PolicyRecord)
		}
		if !first.PolicyRecord.BackendAttempted || first.PolicyRecord.OutputCommitted {
			t.Fatalf("release must preserve backend/output commitment flags: %#v", first.PolicyRecord)
		}

		releaseInput.Kind = ReleaseKindLosing
		second, err := svc.Release(context.Background(), releaseInput)
		if err != nil {
			t.Fatalf("release: %v", err)
		}
		if second.Applied || second.ReleasedDelta.Value != 0 {
			t.Fatalf("duplicate loser release must be idempotent: %#v", second)
		}
		if !reflect.DeepEqual(second.PolicyRecord, evidence.policy[1]) {
			t.Fatalf("duplicate release must return the emitted policy evidence: got %#v want %#v", second.PolicyRecord, evidence.policy[1])
		}
		if !reflect.DeepEqual(second.AccountingEvent, evidence.accounting[1]) {
			t.Fatalf("duplicate release must return the emitted accounting evidence: got %#v want %#v", second.AccountingEvent, evidence.accounting[1])
		}
		if len(store.releaseCalls) != 2 {
			t.Fatalf("release calls must be tracked: %#v", store.releaseCalls)
		}
	})
}

// Quantity settlement evidence remains covered by TestSettlementAndRelease.
func TestSettlementReleaseTolerantToSourceFailures(t *testing.T) {
	t.Parallel()

	quotaRule := domain.Rule{
		ID:    "tenant.requests",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")},
		},
	}
	correlation := controlplane.Correlation{
		TraceID: "trace-err", RequestID: "request-err",
		ALegID: "a-1", BLegID: "b-1", AttemptSeq: 3,
		BackendID: "backend-1", Model: "model-1",
	}
	scopeView := scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal-1"),
		TenantID:    scope.Known("tenant-1"),
		ProjectID:   scope.Known(""),
	}
	key := domain.ReservationKey{
		LogicalRequestID: "request-err",
		ALegID:           "a-1",
		BLegID:           "b-1",
		AttemptID:        "attempt-err",
		RuleID:           quotaRule.ID,
		Sequence:         1,
	}

	// newTolerantSvc builds an isolated service whose rule source and readiness
	// store both fail, so each subtest records evidence on its own sink.
	newTolerantSvc := func(evidence *fakeEvidenceSink) *Service {
		store := newFakeStateStore()
		store.readinessErr = errors.New("readiness store down")
		return NewService(
			&fakeRuleSource{snapshot: RuleSnapshot{Rules: []domain.Rule{quotaRule}}, err: errors.New("rule source down")},
			store, evidence, fixedClock{now: time.Unix(100, 0).UTC()},
		)
	}

	t.Run("settle-stays-error-tolerant-when-source-and-readiness-fail", func(t *testing.T) {
		t.Parallel()

		evidence := &fakeEvidenceSink{}
		svc := newTolerantSvc(evidence)
		in := SettleInput{
			Correlation:    correlation,
			Scope:          scopeView,
			ReservationKey: key,
			RuleID:         quotaRule.ID,
			ReservationID:  "reservation-err",
			Kind:           SettlementKindFinal,
			FinalUsage:     domain.Amount{Unit: domain.AmountUnitRequests, Value: 6},
			ReservedUsage:  domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
			Authority:      domain.AuthorityLevelAuthoritative,
		}

		got, err := svc.Settle(context.Background(), in)
		if err != nil {
			t.Fatalf("settle must not hard-fail when rule source/readiness are unavailable: %v", err)
		}
		if got.AccountingEvent.AccountingAuthority() == nil {
			t.Fatalf("settlement must still project evidence under source failure")
		}
		// With the rule source unavailable, selectedRuleKind returns "" over an
		// empty rule set, so the previously-fabricated "quota" literal is gone.
		if got.AccountingEvent.AccountingAuthority().RuleType != "" {
			t.Fatalf("settlement under rule-source failure must emit empty rule type, got %q", got.AccountingEvent.AccountingAuthority().RuleType)
		}
		if !reflect.DeepEqual(got.PolicyRecord, evidence.policy[0]) || !reflect.DeepEqual(got.AccountingEvent, evidence.accounting[0]) {
			t.Fatalf("settlement must return the same evidence it records")
		}
	})

	t.Run("release-stays-error-tolerant-when-source-and-readiness-fail", func(t *testing.T) {
		t.Parallel()

		evidence := &fakeEvidenceSink{}
		svc := newTolerantSvc(evidence)
		in := ReleaseInput{
			Correlation:    correlation,
			Scope:          scopeView,
			ReservationKey: key,
			RuleID:         quotaRule.ID,
			ReservationID:  "reservation-err",
			Kind:           ReleaseKindSwallowed,
			Amount:         domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
		}

		got, err := svc.Release(context.Background(), in)
		if err != nil {
			t.Fatalf("release must not hard-fail when rule source/readiness are unavailable: %v", err)
		}
		if got.AccountingEvent.AccountingAuthority() == nil {
			t.Fatalf("release must still project evidence under source failure")
		}
		if got.AccountingEvent.AccountingAuthority().RuleType != "" {
			t.Fatalf("release under rule-source failure must emit empty rule type, got %q", got.AccountingEvent.AccountingAuthority().RuleType)
		}
		if !reflect.DeepEqual(got.PolicyRecord, evidence.policy[0]) || !reflect.DeepEqual(got.AccountingEvent, evidence.accounting[0]) {
			t.Fatalf("release must return the same evidence it records")
		}
	})
}
