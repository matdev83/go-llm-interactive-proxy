package app

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestAdmissionDenialReasonUsesDecisiveRuleKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		kind       domain.RuleKind
		unit       domain.AmountUnit
		limit      domain.Amount
		request    domain.Amount
		spend      domain.Amount
		wantReason policydecision.AccountingReasonCode
	}{
		{
			name:       "rate",
			kind:       domain.RuleKindRate,
			unit:       domain.AmountUnitRequests,
			limit:      domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
			request:    domain.Amount{Unit: domain.AmountUnitRequests, Value: 2},
			wantReason: policydecision.AccountingReasonRateLimited,
		},
		{
			name:       "budget",
			kind:       domain.RuleKindBudget,
			unit:       domain.AmountUnitMoneyNano,
			limit:      domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 10, Currency: "USD"},
			request:    domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
			spend:      domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 11, Currency: "USD"},
			wantReason: policydecision.AccountingReasonBudgetExceeded,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rule := domain.Rule{
				ID:       "decisive." + tc.name,
				Kind:     tc.kind,
				Mode:     domain.RuleModeStrict,
				Unit:     tc.unit,
				Limit:    tc.limit,
				Currency: tc.limit.Currency,
				Match:    domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
			}
			store := newFakeStateStore()
			store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
			svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
				Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
				Rules:  []domain.Rule{rule},
			}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

			in := failureBehaviorBaseInput()
			in.ReservationKey.RuleID = rule.ID
			in.Request = tc.request
			in.Spend = tc.spend
			got, err := svc.Admit(context.Background(), in)
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			if got.Allowed || got.Outcome != domain.DecisionOutcomeDeny {
				t.Fatalf("admission = %#v, want decisive denial", got)
			}
			if got.SelectedRuleID != rule.ID || got.RuleKind != rule.Kind {
				t.Fatalf("selected rule metadata = id=%q kind=%q, want %q/%q", got.SelectedRuleID, got.RuleKind, rule.ID, rule.Kind)
			}
			if got.PolicyRecord.ReasonCode != string(tc.wantReason) {
				t.Fatalf("policy reason = %q, want %q", got.PolicyRecord.ReasonCode, tc.wantReason)
			}
			if got.AccountingEvent.AccountingAuthority() == nil || got.AccountingEvent.AccountingAuthority().ReasonCode != string(tc.wantReason) {
				t.Fatalf("accounting reason = %#v, want %q", got.AccountingEvent.AccountingAuthority(), tc.wantReason)
			}
		})
	}
}

func TestAdmissionEvidenceUsesDecisiveRuleInMultiRuleMatch(t *testing.T) {
	t.Parallel()

	quota := domain.Rule{
		ID:    "quota.first",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 100},
		Match: domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	budget := domain.Rule{
		ID:       "budget.decisive",
		Kind:     domain.RuleKindBudget,
		Mode:     domain.RuleModeStrict,
		Unit:     domain.AmountUnitMoneyNano,
		Currency: "USD",
		Limit:    domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 10, Currency: "USD"},
		Match:    domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{quota, budget},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})
	in := failureBehaviorBaseInput()
	in.Request = domain.Amount{Unit: domain.AmountUnitRequests, Value: 1}
	in.Spend = domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 11, Currency: "USD"}
	in.ReservationKey.RuleID = quota.ID

	got, err := svc.Admit(context.Background(), in)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if got.Allowed || got.SelectedRuleID != budget.ID || got.RuleKind != domain.RuleKindBudget {
		t.Fatalf("admission metadata = %#v, want budget decisive rule", got)
	}
	detail := got.AccountingEvent.AccountingAuthority()
	if detail == nil {
		t.Fatal("accounting authority detail is nil")
	}
	if detail.RuleID != budget.ID || detail.RuleType != string(domain.RuleKindBudget) || detail.Unit != string(domain.AmountUnitMoneyNano) || detail.Currency != "USD" {
		t.Fatalf("decisive evidence = %#v, want budget money rule", detail)
	}
	if len(got.RuleIDs) != 2 {
		t.Fatalf("matched rule IDs = %#v, want both matches", got.RuleIDs)
	}
}

func TestApplyUsageNormalizesKnownEmptyAttributionBeforeStore(t *testing.T) {
	t.Parallel()

	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	rule := domain.Rule{
		ID:    "advisory.known-empty",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeAdvisory,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 100},
	}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status:             store.readiness,
		UnknownAttribution: domain.UnknownAttributionKnownEmpty,
		Rules:              []domain.Rule{rule},
	}}, store, nil, fixedClock{now: time.Unix(100, 0).UTC()})

	_, err := svc.ApplyUsage(context.Background(), ApplyUsageCommand{
		Scope:        scope.PrincipalScopeView{TenantID: scope.Unknown()},
		Dimensions:   domain.Dimensions{Tenant: scope.Unknown()},
		RuleIDs:      []string{rule.ID},
		Usage:        domain.PreflightUsage{},
		RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		Authority:    domain.AuthorityLevelEstimated,
		SourceKey:    "known-empty-usage",
	})
	if err != nil {
		t.Fatalf("ApplyUsage: %v", err)
	}
	if len(store.applyUsageCalls) != 1 {
		t.Fatalf("ApplyUsage calls = %d, want one", len(store.applyUsageCalls))
	}
	got := store.applyUsageCalls[0]
	if !got.Scope.TenantID.IsKnown() || got.Scope.TenantID.String() != "" {
		t.Fatalf("normalized scope tenant = %#v, want known empty", got.Scope.TenantID)
	}
	if !got.Dimensions.Tenant.IsKnown() || got.Dimensions.Tenant.String() != "" {
		t.Fatalf("normalized dimension tenant = %#v, want known empty", got.Dimensions.Tenant)
	}
}

func TestSettlementStagesHaveDistinctReplayIdentity(t *testing.T) {
	t.Parallel()

	rule := domain.Rule{
		ID:    "settlement.identity",
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeStrict,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 100},
	}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: store.readiness,
		Rules:  []domain.Rule{rule},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})
	base := SettleInput{
		Correlation:    controlplane.Correlation{TraceID: "settlement-identity", RequestID: "settlement-identity"},
		ReservationKey: domain.ReservationKey{LogicalRequestID: "settlement-identity", RuleID: rule.ID, Sequence: 1},
		ReservationID:  "settlement-identity|reservation",
		RuleID:         rule.ID,
		FinalUsage:     domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		ReservedUsage:  domain.Amount{Unit: domain.AmountUnitRequests, Value: 2},
		Authority:      domain.AuthorityLevelEstimated,
	}
	base.Kind = SettlementKindPartial
	if _, err := svc.Settle(context.Background(), base); err != nil {
		t.Fatalf("partial settle: %v", err)
	}
	base.Kind = SettlementKindFinal
	if _, err := svc.Settle(context.Background(), base); err != nil {
		t.Fatalf("final settle: %v", err)
	}
	if len(store.settleCalls) != 2 {
		t.Fatalf("settle calls = %d, want two stages", len(store.settleCalls))
	}
	first, second := store.settleCalls[0], store.settleCalls[1]
	if first.SettlementKey.String() == second.SettlementKey.String() || first.SourceKey == second.SourceKey {
		t.Fatalf("settlement identities collapsed: first=%#v second=%#v", first, second)
	}
}
