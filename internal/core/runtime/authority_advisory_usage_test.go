package runtime

import (
	"context"
	"testing"
	"time"

	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// advisoryApplyTestStore builds a ready in-memory store seeded from rules with
// the per-rule window spec map, mirroring runtimebundle composition.
func advisoryApplyTestStore(t *testing.T, rules []authoritydomain.Rule, at time.Time) *authoritystore.MemoryStore {
	t.Helper()
	limitRows, err := authoritystore.LimitRowsFromRules(rules, at)
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	ruleWindows := make(map[string]authoritydomain.WindowSpec, len(rules))
	for _, r := range rules {
		ruleWindows[r.ID] = r.Window
	}
	return authoritystore.NewMemory(authoritystore.Config{
		StoreID:     "advisory-apply-test",
		Backing:     authoritydomain.BackingCapabilityAtomic,
		Readiness:   authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
		LimitRows:   limitRows,
		RuleWindows: ruleWindows,
	})
}

func advisoryApplyService(t *testing.T, store *authoritystore.MemoryStore, rules []authoritydomain.Rule, at time.Time) *authorityapp.Service {
	t.Helper()
	return authorityapp.NewService(authorityRuleSource{
		snapshot: authorityapp.RuleSnapshot{
			Status:    authoritydomain.AuthorityStatus{State: authoritydomain.AuthorityStateReady, Reason: authoritydomain.StatusReasonNone},
			Rules:     rules,
			FetchedAt: at,
		},
	}, store, nil, nil)
}

func advisoryApplyLimitRow(t *testing.T, store *authoritystore.MemoryStore, ruleID, unit string) controlplane.AccountingLimitStatusRow {
	t.Helper()
	page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
		RuleID:     ruleID,
		Unit:       unit,
		Limit:      10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus(%s): %v", ruleID, err)
	}
	if len(page.Items) == 0 {
		t.Fatalf("LimitStatus(%s) items = 0", ruleID)
	}
	return page.Items[0]
}

// TestApplyAdvisoryUsageUpdatesAdvisoryWindowWithoutReservation proves
// requirement 7.7: a request matching only an advisory rule (not reserved)
// still updates the advisory window after final usage. ApplyAdvisoryUsage is
// NOT gated by IsActive()/Reserved.
func TestApplyAdvisoryUsageUpdatesAdvisoryWindowWithoutReservation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := authoritydomain.Rule{
		ID:    "tenant.input.advisory",
		Kind:  authoritydomain.RuleKindQuota,
		Mode:  authoritydomain.RuleModeAdvisory,
		Unit:  authoritydomain.AmountUnitInputTokens,
		Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 5000},
		Match: authoritydomain.DimensionsMatcher{Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	store := advisoryApplyTestStore(t, []authoritydomain.Rule{rule}, at)
	svc := advisoryApplyService(t, store, []authoritydomain.Rule{rule}, at)

	admissionInput := authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID: "trace-advisory-only", RequestID: "req-advisory-only",
			ALegID: "a-1", BLegID: "b-1", AttemptSeq: 1,
			BackendID: "backend-1", Model: "model-1",
		},
		Scope:      scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1"), TenantID: scope.Known("tenant-1")},
		Dimensions: authoritydomain.Dimensions{Backend: scope.Known("backend-1"), Model: scope.Known("model-1")},
		Request:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 200},
		Authority:  authoritydomain.AuthorityLevelEstimated,
		ReservationKey: authoritydomain.ReservationKey{
			LogicalRequestID: "req-advisory-only", ALegID: "a-1", BLegID: "b-1",
			AttemptID: "b-1", RuleID: "backend-1:model-1", Sequence: 1,
		},
	}
	admissionResult, err := svc.Admit(context.Background(), admissionInput)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if admissionResult.Reserved {
		t.Fatalf("advisory-only admission must not reserve: %#v", admissionResult)
	}
	if len(admissionResult.AdvisoryRuleIDs) != 1 || admissionResult.AdvisoryRuleIDs[0] != rule.ID {
		t.Fatalf("AdvisoryRuleIDs = %#v, want [%s]", admissionResult.AdvisoryRuleIDs, rule.ID)
	}

	state := attemptAuthorityState{admissionInput: admissionInput, admissionResult: admissionResult}
	lifecycle := newAuthorityLifecycle(svc, nil, state, authorityCandidate())

	if lifecycle.IsActive() {
		t.Fatalf("IsActive must be false for an unreserved advisory-only request")
	}

	usageEv := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 150, TotalTokens: 150}
	if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), usageEv); !applied {
		t.Fatal("ApplyAdvisoryUsage = false, want true (advisory window must update)")
	}

	row := advisoryApplyLimitRow(t, store, rule.ID, string(authoritydomain.AmountUnitInputTokens))
	if row.Consumed != 150 {
		t.Fatalf("advisory window Consumed = %d, want 150", row.Consumed)
	}
	if row.Reserved != 0 {
		t.Fatalf("advisory window Reserved = %d, want 0 (no reservation)", row.Reserved)
	}
}

func TestApplyAdvisoryMoneyUsesEstimateWhenProviderCostIsAbsent(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := authoritydomain.Rule{
		ID: "spend.advisory", Kind: authoritydomain.RuleKindBudget, Mode: authoritydomain.RuleModeAdvisory,
		Unit: authoritydomain.AmountUnitMoneyNano, Currency: "USD",
		Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 1000, Currency: "USD"},
		Match: authoritydomain.DimensionsMatcher{Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-money")}},
	}
	store := advisoryApplyTestStore(t, []authoritydomain.Rule{rule}, at)
	svc := advisoryApplyService(t, store, []authoritydomain.Rule{rule}, at)
	input := authorityapp.AdmissionInput{
		Correlation:    controlplane.Correlation{TraceID: "trace-money", RequestID: "request-money", ALegID: "a-money", BLegID: "b-money", BackendID: "backend-money", Model: "model-money"},
		Scope:          scope.PrincipalScopeView{PrincipalID: scope.Known("principal-money")},
		Dimensions:     authoritydomain.Dimensions{Backend: scope.Known("backend-money"), Model: scope.Known("model-money")},
		Request:        authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 1},
		Spend:          authoritydomain.Amount{Unit: authoritydomain.AmountUnitMoneyNano, Value: 250, Currency: "USD"},
		Authority:      authoritydomain.AuthorityLevelAuthoritative,
		ReservationKey: authoritydomain.ReservationKey{LogicalRequestID: "request-money", ALegID: "a-money", BLegID: "b-money", AttemptID: "attempt-money", RuleID: rule.ID, Sequence: 1},
	}
	admitted, err := svc.Admit(context.Background(), input)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if len(admitted.AdvisoryRuleIDs) != 1 {
		t.Fatalf("advisory rules = %#v, want one", admitted.AdvisoryRuleIDs)
	}
	lifecycle := newAuthorityLifecycle(svc, nil, attemptAuthorityState{admissionInput: input, admissionResult: admitted}, authorityCandidate())
	event := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 5, TotalTokens: 5, Accounting: lipapi.UsageAccountingMetadata{Plane: lipapi.UsagePlaneProviderBillable, Source: lipapi.UsageSourceProviderReported, Authority: lipapi.UsageAuthorityAuthoritative}}
	if !lifecycle.ApplyAdvisoryUsage(context.Background(), event) {
		t.Fatal("ApplyAdvisoryUsage = false, want estimate applied to money window")
	}
	row := advisoryApplyLimitRow(t, store, rule.ID, string(authoritydomain.AmountUnitMoneyNano))
	if row.Consumed != 250 {
		t.Fatalf("money advisory Consumed = %d, want estimated spend 250", row.Consumed)
	}
}

// TestApplyAdvisoryUsageStrictAndAdvisoryMixUpdatesBoth proves a request matching
// a strict rule and an advisory rule settles the strict reservation AND updates
// the advisory window (requirement 7.7). The strict and advisory windows are
// independent.
func TestApplyAdvisoryUsageStrictAndAdvisoryMixUpdatesBoth(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	strictRule := authoritydomain.Rule{
		ID:    "tenant.input.strict",
		Kind:  authoritydomain.RuleKindQuota,
		Mode:  authoritydomain.RuleModeStrict,
		Unit:  authoritydomain.AmountUnitInputTokens,
		Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 1000},
		Match: authoritydomain.DimensionsMatcher{Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	advisoryRule := authoritydomain.Rule{
		ID:    "tenant.output.advisory",
		Kind:  authoritydomain.RuleKindQuota,
		Mode:  authoritydomain.RuleModeAdvisory,
		Unit:  authoritydomain.AmountUnitOutputTokens,
		Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitOutputTokens, Value: 5000},
		Match: authoritydomain.DimensionsMatcher{Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	rules := []authoritydomain.Rule{strictRule, advisoryRule}
	store := advisoryApplyTestStore(t, rules, at)
	svc := advisoryApplyService(t, store, rules, at)

	admissionInput := authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID: "trace-mix", RequestID: "req-mix",
			ALegID: "a-1", BLegID: "b-1", AttemptSeq: 1,
			BackendID: "backend-1", Model: "model-1",
		},
		Scope:      scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1"), TenantID: scope.Known("tenant-1")},
		Dimensions: authoritydomain.Dimensions{Backend: scope.Known("backend-1"), Model: scope.Known("model-1")},
		Request:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 80},
		PreflightUsage: authoritydomain.PreflightUsage{
			InputTokens:  80,
			OutputTokens: 120,
			TotalTokens:  200,
		},
		Authority: authoritydomain.AuthorityLevelEstimated,
		ReservationKey: authoritydomain.ReservationKey{
			LogicalRequestID: "req-mix", ALegID: "a-1", BLegID: "b-1",
			AttemptID: "b-1", RuleID: "backend-1:model-1", Sequence: 1,
		},
	}
	admissionResult, err := svc.Admit(context.Background(), admissionInput)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !admissionResult.Reserved || len(admissionResult.Reservations) != 1 {
		t.Fatalf("strict+advisory mix must reserve the strict rule: %#v", admissionResult)
	}
	if len(admissionResult.AdvisoryRuleIDs) != 1 || admissionResult.AdvisoryRuleIDs[0] != advisoryRule.ID {
		t.Fatalf("AdvisoryRuleIDs = %#v, want [%s]", admissionResult.AdvisoryRuleIDs, advisoryRule.ID)
	}

	state := attemptAuthorityState{admissionInput: admissionInput, admissionResult: admissionResult}
	lifecycle := newAuthorityLifecycle(svc, nil, state, authorityCandidate())

	usageEv := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 80, OutputTokens: 120, TotalTokens: 200}
	if applied := lifecycle.Settle(context.Background(), authorityapp.SettlementKindFinal, usageEv, false); !applied {
		t.Fatal("Settle = false, want true (strict reservation must be reconciled)")
	}
	if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), usageEv); !applied {
		t.Fatal("ApplyAdvisoryUsage = false, want true (advisory window must update)")
	}

	strictRow := advisoryApplyLimitRow(t, store, strictRule.ID, string(authoritydomain.AmountUnitInputTokens))
	if strictRow.Consumed != 80 {
		t.Fatalf("strict window Consumed = %d, want 80 (settled usage)", strictRow.Consumed)
	}
	if strictRow.Reserved != 0 {
		t.Fatalf("strict window Reserved = %d, want 0 (settled)", strictRow.Reserved)
	}
	advisoryRow := advisoryApplyLimitRow(t, store, advisoryRule.ID, string(authoritydomain.AmountUnitOutputTokens))
	if advisoryRow.Consumed != 120 {
		t.Fatalf("advisory window Consumed = %d, want 120 (output tokens)", advisoryRow.Consumed)
	}
	if advisoryRow.Reserved != 0 {
		t.Fatalf("advisory window Reserved = %d, want 0 (advisory never reserves)", advisoryRow.Reserved)
	}
}

// TestApplyAdvisoryUsageIdempotentAcrossDuplicateCalls proves that replaying
// ApplyAdvisoryUsage for the same logical request + B-leg is a no-op (no
// double-count), via the store source key (requirement 7.8).
func TestApplyAdvisoryUsageIdempotentAcrossDuplicateCalls(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := authoritydomain.Rule{
		ID:    "tenant.input.advisory.idem",
		Kind:  authoritydomain.RuleKindQuota,
		Mode:  authoritydomain.RuleModeAdvisory,
		Unit:  authoritydomain.AmountUnitInputTokens,
		Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 5000},
		Match: authoritydomain.DimensionsMatcher{Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	store := advisoryApplyTestStore(t, []authoritydomain.Rule{rule}, at)
	svc := advisoryApplyService(t, store, []authoritydomain.Rule{rule}, at)

	admissionInput := authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID: "trace-idem", RequestID: "req-idem",
			ALegID: "a-1", BLegID: "b-1", AttemptSeq: 1,
			BackendID: "backend-1", Model: "model-1",
		},
		Scope:      scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1"), TenantID: scope.Known("tenant-1")},
		Dimensions: authoritydomain.Dimensions{Backend: scope.Known("backend-1"), Model: scope.Known("model-1")},
		Request:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 50},
		Authority:  authoritydomain.AuthorityLevelEstimated,
		ReservationKey: authoritydomain.ReservationKey{
			LogicalRequestID: "req-idem", ALegID: "a-1", BLegID: "b-1",
			AttemptID: "b-1", RuleID: "backend-1:model-1", Sequence: 1,
		},
	}
	admissionResult, err := svc.Admit(context.Background(), admissionInput)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	state := attemptAuthorityState{admissionInput: admissionInput, admissionResult: admissionResult}
	lifecycle := newAuthorityLifecycle(svc, nil, state, authorityCandidate())

	usageEv := lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 50, TotalTokens: 50}
	if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), usageEv); !applied {
		t.Fatal("first ApplyAdvisoryUsage = false, want true")
	}
	// Replay: same logical request + B-leg -> same source key -> store no-op.
	if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), usageEv); applied {
		t.Fatal("second ApplyAdvisoryUsage = true, want false (idempotent no-op)")
	}
	row := advisoryApplyLimitRow(t, store, rule.ID, string(authoritydomain.AmountUnitInputTokens))
	if row.Consumed != 50 {
		t.Fatalf("advisory window Consumed after replay = %d, want 50 (no double-count)", row.Consumed)
	}
}

// TestApplyAdvisoryUsageNoOpWhenNoAdvisoryRules proves ApplyAdvisoryUsage is a
// no-op when the admission result has no advisory rules (e.g., strict-only or
// no rules matched), so strict-only requests pay no advisory-apply overhead.
func TestApplyAdvisoryUsageNoOpWhenNoAdvisoryRules(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := authoritydomain.Rule{
		ID:    "tenant.input.strict.noop",
		Kind:  authoritydomain.RuleKindQuota,
		Mode:  authoritydomain.RuleModeStrict,
		Unit:  authoritydomain.AmountUnitInputTokens,
		Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 1000},
		Match: authoritydomain.DimensionsMatcher{Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")}},
	}
	store := advisoryApplyTestStore(t, []authoritydomain.Rule{rule}, at)
	svc := advisoryApplyService(t, store, []authoritydomain.Rule{rule}, at)

	admissionInput := authorityapp.AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID: "trace-strict-noop", RequestID: "req-strict-noop",
			ALegID: "a-1", BLegID: "b-1", AttemptSeq: 1,
			BackendID: "backend-1", Model: "model-1",
		},
		Scope:      scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1"), TenantID: scope.Known("tenant-1")},
		Dimensions: authoritydomain.Dimensions{Backend: scope.Known("backend-1"), Model: scope.Known("model-1")},
		Request:    authoritydomain.Amount{Unit: authoritydomain.AmountUnitInputTokens, Value: 30},
		Authority:  authoritydomain.AuthorityLevelEstimated,
		ReservationKey: authoritydomain.ReservationKey{
			LogicalRequestID: "req-strict-noop", ALegID: "a-1", BLegID: "b-1",
			AttemptID: "b-1", RuleID: "backend-1:model-1", Sequence: 1,
		},
	}
	admissionResult, err := svc.Admit(context.Background(), admissionInput)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if len(admissionResult.AdvisoryRuleIDs) != 0 {
		t.Fatalf("strict-only admission must have no AdvisoryRuleIDs: %#v", admissionResult.AdvisoryRuleIDs)
	}
	state := attemptAuthorityState{admissionInput: admissionInput, admissionResult: admissionResult}
	lifecycle := newAuthorityLifecycle(svc, nil, state, authorityCandidate())

	if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), lipapi.Event{Kind: lipapi.EventUsageDelta, InputTokens: 30}); applied {
		t.Fatal("ApplyAdvisoryUsage on strict-only request = true, want false (no advisory rules)")
	}
}
