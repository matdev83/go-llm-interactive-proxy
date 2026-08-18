package authoritystore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// advisoryQuotaRule builds an advisory quota rule over requests so advisory
// applyUsage can be exercised without a reservation.
func advisoryQuotaRule(id string) domain.Rule {
	return domain.Rule{
		ID:    id,
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeAdvisory,
		Unit:  domain.AmountUnitRequests,
		Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 100},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-advisory")},
			Model:   domain.DimensionMatcher{Value: scope.Known("model-advisory")},
		},
	}
}

func advisoryTokenRule(id string) domain.Rule {
	return domain.Rule{
		ID:    id,
		Kind:  domain.RuleKindQuota,
		Mode:  domain.RuleModeAdvisory,
		Unit:  domain.AmountUnitInputTokens,
		Limit: domain.Amount{Unit: domain.AmountUnitInputTokens, Value: 5000},
		Match: domain.DimensionsMatcher{
			Backend: domain.DimensionMatcher{Value: scope.Known("backend-advisory-tok")},
			Model:   domain.DimensionMatcher{Value: scope.Known("model-advisory-tok")},
		},
	}
}

func advisoryQuotaDimensions() domain.Dimensions {
	return domain.Dimensions{
		Principal: scope.Known("principal-advisory"),
		Tenant:    scope.Known("tenant-advisory"),
		Backend:   scope.Known("backend-advisory"),
		Model:     scope.Known("model-advisory"),
	}
}

func advisoryBudgetDimensions() domain.Dimensions {
	return domain.Dimensions{
		Principal: scope.Known("principal-advisory-budget"),
		Tenant:    scope.Known("tenant-advisory-budget"),
		Backend:   scope.Known("backend-advisory-budget"),
		Model:     scope.Known("model-advisory-budget"),
	}
}

func advisoryTokenDimensions() domain.Dimensions {
	return domain.Dimensions{
		Principal: scope.Known("principal-advisory-tok"),
		Tenant:    scope.Known("tenant-advisory-tok"),
		Backend:   scope.Known("backend-advisory-tok"),
		Model:     scope.Known("model-advisory-tok"),
	}
}

func applyUsageCmd(ruleID string, dims domain.Dimensions, usage domain.PreflightUsage, requestCount domain.Amount, _ domain.Amount, at time.Time, source string) app.ApplyUsageCommand {
	return app.ApplyUsageCommand{
		Correlation: controlplane.Correlation{
			TraceID:   "trace-advisory-" + ruleID,
			RequestID: "req-advisory-" + ruleID,
			ALegID:    "a-advisory-" + ruleID,
			BLegID:    "b-advisory-" + ruleID,
			BackendID: valueString(dims.Backend),
			Model:     valueString(dims.Model),
		},
		Dimensions:   dims,
		RuleIDs:      []string{ruleID},
		Usage:        usage,
		RequestCount: requestCount,
		At:           at,
		SourceKey:    source,
	}
}

// TestApplyUsageAdvisoryQuotaUpdatesWindowWithoutReservation proves requirement
// 7.7: advisory (no-reservation) usage updates the matched window's
// Consumed/Remaining even though no reservation record exists.
func TestApplyUsageAdvisoryQuotaUpdatesWindowWithoutReservation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{advisoryQuotaRule("advisory-quota-1")}, at)

	before := limitRow(t, store, "advisory-quota-1", string(domain.AmountUnitRequests))
	if before.Consumed != 0 || before.Remaining != 100 {
		t.Fatalf("seeded advisory window = consumed=%d remaining=%d, want 0/100", before.Consumed, before.Remaining)
	}

	res, err := store.ApplyUsage(context.Background(), applyUsageCmd(
		"advisory-quota-1", advisoryQuotaDimensions(),
		domain.PreflightUsage{},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		at.Add(time.Minute), "apply-1",
	))
	if err != nil {
		t.Fatalf("ApplyUsage: %v", err)
	}
	if !res.Applied || len(res.RuleIDs) != 1 || res.RuleIDs[0] != "advisory-quota-1" {
		t.Fatalf("ApplyUsage result = %#v, want applied rule advisory-quota-1", res)
	}

	after := limitRow(t, store, "advisory-quota-1", string(domain.AmountUnitRequests))
	if after.Consumed != 1 {
		t.Fatalf("advisory Consumed = %d, want 1", after.Consumed)
	}
	if after.Remaining != 99 {
		t.Fatalf("advisory Remaining = %d, want 99", after.Remaining)
	}
	if after.Reserved != 0 {
		t.Fatalf("advisory Reserved = %d, want 0 (no reservation)", after.Reserved)
	}
}

// TestApplyUsageReplaySameSourceKeyIsNoOp proves idempotency (requirement 7.8):
// replaying the same SourceKey does not double-count and reports Applied=false.
func TestApplyUsageReplaySameSourceKeyIsNoOp(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{advisoryQuotaRule("advisory-quota-1")}, at)

	cmd := applyUsageCmd("advisory-quota-1", advisoryQuotaDimensions(),
		domain.PreflightUsage{},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 3},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		at.Add(time.Minute), "apply-replay")
	if _, err := store.ApplyUsage(context.Background(), cmd); err != nil {
		t.Fatalf("first ApplyUsage: %v", err)
	}
	replay, err := store.ApplyUsage(context.Background(), cmd)
	if err != nil {
		t.Fatalf("replay ApplyUsage: %v", err)
	}
	if replay.Applied {
		t.Fatalf("replay must not apply: %#v", replay)
	}
	row := limitRow(t, store, "advisory-quota-1", string(domain.AmountUnitRequests))
	if row.Consumed != 3 {
		t.Fatalf("advisory Consumed after replay = %d, want 3 (no double-count)", row.Consumed)
	}
}

// TestApplyUsageReplacesPartialFactInsteadOfAdding proves that partial and
// final usage facts sharing one logical source key reconcile by delta rather
// than double-counting the window. A later authoritative fact also replaces an
// earlier estimate and cannot be overwritten by a lower-authority replay.
func TestApplyUsageReplacesPartialFactInsteadOfAdding(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{advisoryQuotaRule("advisory-reconcile-1")}, at)
	base := applyUsageCmd("advisory-reconcile-1", advisoryQuotaDimensions(), domain.PreflightUsage{}, domain.Amount{Unit: domain.AmountUnitRequests, Value: 3}, domain.Amount{}, at.Add(time.Minute), "reconcile-source")
	base.Authority = domain.AuthorityLevelEstimated
	base.Kind = app.SettlementKindPartial
	if result, err := store.ApplyUsage(context.Background(), base); err != nil || !result.Applied {
		t.Fatalf("partial ApplyUsage = %#v, err=%v", result, err)
	}
	final := base
	final.RequestCount.Value = 5
	final.Kind = app.SettlementKindFinal
	if result, err := store.ApplyUsage(context.Background(), final); err != nil || !result.Applied {
		t.Fatalf("final ApplyUsage = %#v, err=%v", result, err)
	}
	row := limitRow(t, store, "advisory-reconcile-1", string(domain.AmountUnitRequests))
	if row.Consumed != 5 {
		t.Fatalf("reconciled Consumed = %d, want 5 (partial must be replaced)", row.Consumed)
	}
	latePartial := base
	latePartial.RequestCount.Value = 7
	if result, err := store.ApplyUsage(context.Background(), latePartial); err != nil {
		t.Fatalf("late partial replay: %v", err)
	} else if result.Applied {
		t.Fatalf("late partial replay applied after final: %#v", result)
	}
	row = limitRow(t, store, "advisory-reconcile-1", string(domain.AmountUnitRequests))
	if row.Consumed != 5 {
		t.Fatalf("late partial Consumed = %d, want final 5", row.Consumed)
	}
	authoritative := final
	authoritative.RequestCount.Value = 4
	authoritative.Authority = domain.AuthorityLevelAuthoritative
	if result, err := store.ApplyUsage(context.Background(), authoritative); err != nil || !result.Applied {
		t.Fatalf("authoritative ApplyUsage = %#v, err=%v", result, err)
	}
	row = limitRow(t, store, "advisory-reconcile-1", string(domain.AmountUnitRequests))
	if row.Consumed != 4 {
		t.Fatalf("authoritative Consumed = %d, want 4", row.Consumed)
	}
	lower := authoritative
	lower.RequestCount.Value = 9
	lower.Authority = domain.AuthorityLevelEstimated
	if result, err := store.ApplyUsage(context.Background(), lower); err != nil {
		t.Fatalf("lower-authority replay: %v", err)
	} else if result.Applied {
		t.Fatalf("lower-authority replay applied: %#v", result)
	}
	row = limitRow(t, store, "advisory-reconcile-1", string(domain.AmountUnitRequests))
	if row.Consumed != 4 {
		t.Fatalf("lower-authority replay Consumed = %d, want 4", row.Consumed)
	}
}

// Retired monetary advisory behavior has no implementation.
func TestApplyUsageTokenAdvisoryConsumesFinalUsage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{advisoryTokenRule("advisory-tok-1")}, at)

	res, err := store.ApplyUsage(context.Background(), applyUsageCmd(
		"advisory-tok-1", advisoryTokenDimensions(),
		domain.PreflightUsage{InputTokens: 1200, OutputTokens: 800},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 9999},
		at.Add(time.Minute), "apply-tok-1",
	))
	if err != nil {
		t.Fatalf("ApplyUsage: %v", err)
	}
	if !res.Applied {
		t.Fatalf("ApplyUsage must apply: %#v", res)
	}
	row := limitRow(t, store, "advisory-tok-1", string(domain.AmountUnitInputTokens))
	if row.Consumed != 1200 {
		t.Fatalf("advisory token Consumed = %d, want 1200 (input tokens)", row.Consumed)
	}
}

// TestApplyUsageExpiredAdvisoryWindowAdvances proves rollover (requirement 3.5):
// applying usage after the advisory window has expired advances to a fresh
// window whose counters start at zero before accumulating the new usage.
func TestApplyUsageExpiredAdvisoryWindowAdvances(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := advisoryQuotaRule("advisory-roll-1")
	rule.Window = domain.WindowSpec{
		Algorithm: domain.WindowAlgorithmFixed,
		Size:      time.Hour,
		Anchor:    at,
	}
	store := storeFromRules(t, []domain.Rule{rule}, at)

	// Apply one unit inside the first window.
	first := applyUsageCmd("advisory-roll-1", advisoryQuotaDimensions(),
		domain.PreflightUsage{},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 1},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		at.Add(10*time.Minute), "apply-roll-1")
	if _, err := store.ApplyUsage(context.Background(), first); err != nil {
		t.Fatalf("first ApplyUsage: %v", err)
	}
	firstRow := limitRow(t, store, "advisory-roll-1", string(domain.AmountUnitRequests))
	if firstRow.Consumed != 1 {
		t.Fatalf("first window Consumed = %d, want 1", firstRow.Consumed)
	}

	// Apply after the window elapsed: advanceWindow creates a fresh window.
	second := applyUsageCmd("advisory-roll-1", advisoryQuotaDimensions(),
		domain.PreflightUsage{},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 5},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		at.Add(2*time.Hour), "apply-roll-2")
	if _, err := store.ApplyUsage(context.Background(), second); err != nil {
		t.Fatalf("second ApplyUsage: %v", err)
	}
	page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
		RuleID:     "advisory-roll-1",
		Unit:       string(domain.AmountUnitRequests),
		Limit:      10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus: %v", err)
	}
	// Two windows now: the expired one (consumed=1) and the fresh one (consumed=5).
	if len(page.Items) != 2 {
		t.Fatalf("LimitStatus items = %d, want 2 (expired + fresh)", len(page.Items))
	}
	var fresh controlplane.AccountingLimitStatusRow
	for _, row := range page.Items {
		if row.Consumed == 5 {
			fresh = row
		}
	}
	if fresh.RuleID == "" {
		t.Fatalf("fresh advisory window not found among %#v", page.Items)
	}
	if fresh.Remaining != 95 {
		t.Fatalf("fresh advisory window Remaining = %d, want 95", fresh.Remaining)
	}
	if fresh.WindowEnd.Before(at.Add(2 * time.Hour)) {
		t.Fatalf("fresh advisory window did not advance: WindowEnd = %v", fresh.WindowEnd)
	}
}

// TestApplyUsageRecordsAdvisoryDecision proves an advisory_usage decision row is
// appended and is query-visible via DecisionHistory.
func TestApplyUsageRecordsAdvisoryDecision(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := storeFromRules(t, []domain.Rule{advisoryQuotaRule("advisory-dec-1")}, at)

	if _, err := store.ApplyUsage(context.Background(), applyUsageCmd(
		"advisory-dec-1", advisoryQuotaDimensions(),
		domain.PreflightUsage{},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 2},
		domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		at.Add(time.Minute), "apply-dec-1",
	)); err != nil {
		t.Fatalf("ApplyUsage: %v", err)
	}
	page, err := store.DecisionHistory(context.Background(), controlplane.AccountingDecisionQuery{
		Common:     controlplane.CommonFilters{ReasonCode: "advisory_usage"},
		RuleID:     "advisory-dec-1",
		Limit:      10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("DecisionHistory: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("advisory decision items = %d, want 1", len(page.Items))
	}
	dec := page.Items[0]
	if dec.Authority != controlplane.AccountingAuthoritySourceAdvisory {
		t.Fatalf("advisory decision Authority = %v, want advisory", dec.Authority)
	}
	if dec.Outcome != controlplane.AccountingOutcomeReconcile {
		t.Fatalf("advisory decision Outcome = %v, want reconcile", dec.Outcome)
	}
	if dec.Consumed != 2 {
		t.Fatalf("advisory decision Consumed = %d, want 2", dec.Consumed)
	}
}

func TestApplyUsageBatchIsAtomicWhenOneRuleCannotBeMatched(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := advisoryQuotaRule("advisory-batch-1")
	store := storeFromRules(t, []domain.Rule{rule}, at)

	_, err := store.ApplyUsage(context.Background(), app.ApplyUsageCommand{
		Dimensions:   advisoryQuotaDimensions(),
		RuleIDs:      []string{rule.ID, "advisory-missing"},
		RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 5},
		UsagePresent: true,
		At:           at.Add(time.Minute),
		SourceKey:    "advisory-batch-atomic",
	})
	if !errors.Is(err, app.ErrUnavailable) {
		t.Fatalf("batch ApplyUsage error = %v, want unavailable", err)
	}
	row := limitRow(t, store, rule.ID, string(domain.AmountUnitRequests))
	if row.Consumed != 0 || row.Remaining != row.Limit {
		t.Fatalf("failed advisory batch mutated first row: consumed=%d remaining=%d", row.Consumed, row.Remaining)
	}
}

func TestApplyUsagePreservesAuthoritativeZeroRequestCount(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rule := advisoryQuotaRule("advisory-zero-1")
	store := storeFromRules(t, []domain.Rule{rule}, at)

	result, err := store.ApplyUsage(context.Background(), app.ApplyUsageCommand{
		Dimensions:   advisoryQuotaDimensions(),
		RuleIDs:      []string{rule.ID},
		RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 0},
		UsagePresent: true,
		At:           at.Add(time.Minute),
		SourceKey:    "advisory-zero",
	})
	if err != nil {
		t.Fatalf("ApplyUsage: %v", err)
	}
	if !result.Applied {
		t.Fatalf("zero-usage application should still record the authoritative event: %#v", result)
	}
	row := limitRow(t, store, rule.ID, string(domain.AmountUnitRequests))
	if row.Consumed != 0 {
		t.Fatalf("authoritative zero request count consumed=%d, want 0", row.Consumed)
	}
}

// valueString mirrors the store's local helper for building correlation IDs in tests.
func valueString(v scope.Value) string {
	if v.IsUnknown() {
		return ""
	}
	return v.String()
}
