package app

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func spendCapRule(id string, limit int64) domain.Rule {
	return domain.Rule{
		ID:       id,
		Kind:     domain.RuleKindSpendCap,
		Mode:     domain.RuleModeStrict,
		Unit:     domain.AmountUnitMoneyNano,
		Currency: "usd",
		Limit:    domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: limit, Currency: "usd"},
		Match:    domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
}

func clampBaseInput(spend int64) AdmissionInput {
	return AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-clamp",
			RequestID:  "request-clamp",
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
		Request:   domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 5},
		Spend:     domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: spend, Currency: "usd"},
		Authority: domain.AuthorityLevelEstimated,
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: "request-clamp",
			ALegID:           "a-1",
			BLegID:           "b-1",
			AttemptID:        "attempt-clamp",
			RuleID:           "rule-clamp",
			Sequence:         1,
		},
	}
}

func clampLimitRow(ruleID string, limit, consumed, reserved int64) controlplane.AccountingLimitStatusRow {
	return controlplane.AccountingLimitStatusRow{
		RuleID:    ruleID,
		Unit:      string(domain.AmountUnitMoneyNano),
		Currency:  "usd",
		Limit:     limit,
		Consumed:  consumed,
		Reserved:  reserved,
		Remaining: limit - consumed - reserved,
		Correlation: controlplane.Correlation{
			BackendID: "backend-1",
			Model:     "model-1",
		},
	}
}

func TestAdmissionSpendCapClampPopulatesEffectiveMaxFromRemainingBudget(t *testing.T) {
	t.Parallel()

	rule := spendCapRule("tenant.spend_cap", 1000)
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	store.limitPage = controlplane.Page[controlplane.AccountingLimitStatusRow]{
		Items: []controlplane.AccountingLimitStatusRow{
			clampLimitRow("tenant.spend_cap", 1000, 200, 100),
		},
	}
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-clamp"}
	evidence := &fakeEvidenceSink{}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{rule},
	}}, store, evidence, fixedClock{now: time.Unix(100, 0).UTC()})

	got, err := svc.Admit(context.Background(), clampBaseInput(1500))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !got.Allowed || got.Outcome != domain.DecisionOutcomeClamp {
		t.Fatalf("spend cap exceeded must allow with clamp outcome: %#v", got)
	}
	if got.Clamp == nil {
		t.Fatalf("clamp must be populated: %#v", got)
	}
	if got.Clamp.RuleID != "tenant.spend_cap" {
		t.Fatalf("clamp rule id = %q, want tenant.spend_cap", got.Clamp.RuleID)
	}
	if got.Clamp.RequestedMax.Value != 1500 {
		t.Fatalf("clamp requested max = %d, want 1500", got.Clamp.RequestedMax.Value)
	}
	// remaining = Limit(1000) - Consumed(200) - Reserved(100) = 700.
	if got.Clamp.EffectiveMax.Value != 700 {
		t.Fatalf("clamp effective max = %d, want 700", got.Clamp.EffectiveMax.Value)
	}
	if got.Clamp.EffectiveMax.Unit != domain.AmountUnitMoneyNano {
		t.Fatalf("clamp effective max unit = %q, want money_nano", got.Clamp.EffectiveMax.Unit)
	}
	// The reservation must reserve the EffectiveMax (700), not the full
	// requested spend (1500), so reserved exposure equals the clamp.
	if len(store.reserveCalls) != 1 {
		t.Fatalf("expected one reserve call, got %d: %#v", len(store.reserveCalls), store.reserveCalls)
	}
	if store.reserveCalls[0].Request.Value != 700 {
		t.Fatalf("reserve request = %d, want 700 (EffectiveMax)", store.reserveCalls[0].Request.Value)
	}
	if len(evidence.policy) != 1 || evidence.policy[0].Annotations["accounting.requested_max"] == "" || evidence.policy[0].Annotations["accounting.effective_max"] == "" {
		t.Fatalf("clamp evidence must preserve requested/effective max annotations: %#v", evidence.policy)
	}
}

func TestAdmissionSpendCapClampsAgainstLiveRemainingBelowConfiguredLimit(t *testing.T) {
	t.Parallel()

	rule := spendCapRule("tenant.spend_cap", 100)
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	store.limitPage = controlplane.Page[controlplane.AccountingLimitStatusRow]{
		Items: []controlplane.AccountingLimitStatusRow{
			clampLimitRow("tenant.spend_cap", 100, 90, 0),
		},
	}
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-live-clamp"}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{rule},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	got, err := svc.Admit(context.Background(), clampBaseInput(20))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !got.Allowed || got.Outcome != domain.DecisionOutcomeClamp {
		t.Fatalf("live capacity must produce a clamp: %#v", got)
	}
	if got.Clamp == nil || got.Clamp.EffectiveMax.Value != 10 {
		t.Fatalf("effective live clamp = %#v, want 10", got.Clamp)
	}
	if len(store.reserveCalls) != 1 || store.reserveCalls[0].Request.Value != 10 {
		t.Fatalf("reservation must use live remaining capacity: %#v", store.reserveCalls)
	}
}

func TestAdmissionSpendCapClampPaginatesPastHistoricalWindows(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0).UTC()
	rule := spendCapRule("tenant.spend_cap", 100)
	rule.Window = domain.WindowSpec{Algorithm: domain.WindowAlgorithmFixed, Size: 1000 * time.Second, Anchor: time.Unix(0, 0).UTC()}
	bounds, err := rule.Window.Bounds(now)
	if err != nil {
		t.Fatalf("window bounds: %v", err)
	}
	first := make([]controlplane.AccountingLimitStatusRow, 32)
	for i := range first {
		first[i] = clampLimitRow("tenant.spend_cap", 100, 100, 0)
		first[i].WindowStart = bounds.Start.Add(-time.Duration(32-i+1) * rule.Window.Size)
		first[i].WindowEnd = first[i].WindowStart.Add(rule.Window.Size)
	}
	active := clampLimitRow("tenant.spend_cap", 100, 90, 0)
	active.WindowStart = bounds.Start
	active.WindowEnd = bounds.End
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	store.limitPages = []controlplane.Page[controlplane.AccountingLimitStatusRow]{
		{Items: first, Next: controlplane.Cursor{Token: "page-2"}},
		{Items: []controlplane.AccountingLimitStatusRow{active}},
	}
	store.activeLimitRow = active
	store.activeLimitOK = true
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-paged-clamp"}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{rule},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: now})

	got, err := svc.Admit(context.Background(), clampBaseInput(20))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if got.Clamp == nil || got.Clamp.EffectiveMax.Value != 10 {
		t.Fatalf("paged active window clamp = %#v, want 10", got.Clamp)
	}
	if len(store.activeQueries) != 1 || store.limitCalls != 0 {
		t.Fatalf("clamp must use one active lookup and no history scans: active=%d history=%d", len(store.activeQueries), store.limitCalls)
	}
}

func TestAdmissionSpendCapClampRejectsOtherScopeRows(t *testing.T) {
	t.Parallel()

	rule := spendCapRule("tenant.spend_cap", 100)
	rule.Match.Principal = domain.DimensionMatcher{Value: scope.Known("principal-1")}
	rule.Match.Credential = domain.DimensionMatcher{Value: scope.Known("credential-1")}
	rule.Match.Tenant = domain.DimensionMatcher{Value: scope.Known("tenant-1")}
	rule.Match.Labels = map[string]domain.DimensionMatcher{"tier": {Value: scope.Known("gold")}}
	wrong := clampLimitRow(rule.ID, 100, 99, 0)
	wrong.Scope = controlplane.ScopeSnapshot{PrincipalID: scope.Known("principal-2"), CredentialID: scope.Known("credential-1"), TenantID: scope.Known("tenant-1")}
	wrong.Scope.Principal.PolicyLabels = map[string]string{"tier": "gold"}
	correct := clampLimitRow(rule.ID, 100, 60, 0)
	correct.Scope = controlplane.ScopeSnapshot{PrincipalID: scope.Known("principal-1"), CredentialID: scope.Known("credential-1"), TenantID: scope.Known("tenant-1")}
	correct.Scope.Principal.PolicyLabels = map[string]string{"tier": "gold"}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady}
	store.limitPage = controlplane.Page[controlplane.AccountingLimitStatusRow]{Items: []controlplane.AccountingLimitStatusRow{wrong, correct}}
	store.activeLimitRow = correct
	store.activeLimitOK = true
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation"}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})
	input := clampBaseInput(50)
	input.Scope.CredentialID = scope.Known("credential-1")
	input.Scope.PolicyLabels = map[string]string{"tier": "gold"}
	input.Dimensions.Principal = scope.Known("principal-1")
	input.Dimensions.Credential = scope.Known("credential-1")
	input.Dimensions.Tenant = scope.Known("tenant-1")
	input.Dimensions.PolicyLabels = map[string]scope.Value{"tier": scope.Known("gold")}
	result, err := svc.Admit(context.Background(), input)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if result.Clamp == nil || result.Clamp.EffectiveMax.Value != 40 {
		t.Fatalf("clamp = %#v, want caller scope remaining 40", result.Clamp)
	}
	if len(store.activeQueries) == 0 {
		t.Fatal("expected active limit query")
	}
	dims := store.activeQueries[0].Dimensions
	if !dims.Principal.Equal(scope.Known("principal-1")) || !dims.Credential.Equal(scope.Known("credential-1")) || !dims.Tenant.Equal(scope.Known("tenant-1")) {
		t.Fatalf("active dimensions = %#v, want normalized caller scope", dims)
	}
}

func TestAdmissionSpendCapClampKeepsWildcardScopeDiscoverable(t *testing.T) {
	t.Parallel()
	rule := spendCapRule("global.spend_cap", 100)
	rule.Match.Backend = domain.DimensionMatcher{}
	row := clampLimitRow(rule.ID, 100, 80, 0)
	row.Correlation.BackendID = ""
	row.Correlation.Model = ""
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady}
	store.limitPage = controlplane.Page[controlplane.AccountingLimitStatusRow]{Items: []controlplane.AccountingLimitStatusRow{row}}
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation"}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{Status: store.readiness, Rules: []domain.Rule{rule}}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})
	result, err := svc.Admit(context.Background(), clampBaseInput(30))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if result.Clamp == nil || result.Clamp.EffectiveMax.Value != 20 {
		t.Fatalf("wildcard clamp = %#v, want remaining 20", result.Clamp)
	}
	if got := store.activeQueries[0].Dimensions.Backend; !got.Equal(scope.Known("backend-1")) {
		t.Fatalf("active backend dimension = %v, want backend-1", got)
	}
}

func TestAdmissionSpendCapClampFallsBackToRuleLimitWhenStoreReadUnavailable(t *testing.T) {
	t.Parallel()

	rule := spendCapRule("tenant.spend_cap", 1000)
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	// No limit rows returned: the store read is unavailable.
	store.reserveResult = ReserveResult{Applied: true, ReservationID: "reservation-clamp"}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{rule},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	got, err := svc.Admit(context.Background(), clampBaseInput(1500))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if got.Clamp == nil {
		t.Fatalf("clamp must be populated: %#v", got)
	}
	if got.Clamp.EffectiveMax.Value != 1000 {
		t.Fatalf("fallback effective max = %d, want rule limit 1000", got.Clamp.EffectiveMax.Value)
	}
	if got.Clamp.Reason != "spend_cap_exceeded_remaining_unavailable" {
		t.Fatalf("clamp reason = %q, want spend_cap_exceeded_remaining_unavailable", got.Clamp.Reason)
	}
}

func TestAdmissionNonSpendCapRulesNeverProduceClamp(t *testing.T) {
	t.Parallel()

	// A strict budget rule whose spend exceeds the limit produces a Deny, not a
	// clamp (only spend-cap rules clamp).
	budgetRule := domain.Rule{
		ID:       "tenant.budget",
		Kind:     domain.RuleKindBudget,
		Mode:     domain.RuleModeStrict,
		Unit:     domain.AmountUnitMoneyNano,
		Currency: "usd",
		Limit:    domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 1000, Currency: "usd"},
		Match:    domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	store := newFakeStateStore()
	store.readiness = domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
	svc := NewService(&fakeRuleSource{snapshot: RuleSnapshot{
		Status: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
		Rules:  []domain.Rule{budgetRule},
	}}, store, &fakeEvidenceSink{}, fixedClock{now: time.Unix(100, 0).UTC()})

	got, err := svc.Admit(context.Background(), clampBaseInput(1500))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if got.Allowed {
		t.Fatalf("budget exceeded must deny: %#v", got)
	}
	if got.Outcome != domain.DecisionOutcomeDeny {
		t.Fatalf("budget exceeded outcome = %s, want deny: %#v", got.Outcome, got)
	}
	if got.Clamp != nil {
		t.Fatalf("non-spend-cap rule must never produce a clamp: %#v", got.Clamp)
	}
}
