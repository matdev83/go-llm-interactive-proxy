package authoritystore_test

import (
	"testing"
	"time"

	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestLimitRowsFromRulesDerivesLiveRows(t *testing.T) {
	t.Parallel()

	anchor := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	at := anchor.Add(30 * time.Minute)
	rules := []authoritydomain.Rule{
		{
			ID:    "tenant.requests",
			Kind:  authoritydomain.RuleKindQuota,
			Mode:  authoritydomain.RuleModeStrict,
			Unit:  authoritydomain.AmountUnitRequests,
			Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 10},
			Window: authoritydomain.WindowSpec{
				Algorithm: authoritydomain.WindowAlgorithmFixed,
				Size:      time.Hour,
				Anchor:    anchor,
			},
			Match: authoritydomain.DimensionsMatcher{
				Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")},
			},
		},
	}

	rows, err := authoritystore.LimitRowsFromRules(rules, at)
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.RuleID != "tenant.requests" || row.RuleType != "quota" {
		t.Fatalf("rule metadata = %#v", row)
	}
	if row.Unit != string(authoritydomain.AmountUnitRequests) || row.Limit != 10 || row.Remaining != 10 {
		t.Fatalf("limit totals = %#v", row)
	}
	if row.Correlation.BackendID != "backend-1" || row.Correlation.Model != "" {
		t.Fatalf("correlation = %#v", row.Correlation)
	}
	if !row.Scope.TenantID.IsUnknown() {
		t.Fatalf("wildcard tenant must remain unknown: %#v", row.Scope.TenantID)
	}
	if row.Authority != controlplane.AccountingAuthoritySourceAuthoritative {
		t.Fatalf("authority = %q, want authoritative", row.Authority)
	}
	if !row.WindowStart.Equal(anchor) || !row.WindowEnd.Equal(anchor.Add(time.Hour)) {
		t.Fatalf("window bounds = %s..%s, want %s..%s", row.WindowStart, row.WindowEnd, anchor, anchor.Add(time.Hour))
	}
}

func TestLimitRowsFromRulesWildcardRowMatchesKnownRequestDimensions(t *testing.T) {
	t.Parallel()

	rules := []authoritydomain.Rule{
		{
			ID:    "tenant.requests",
			Kind:  authoritydomain.RuleKindQuota,
			Mode:  authoritydomain.RuleModeStrict,
			Unit:  authoritydomain.AmountUnitRequests,
			Limit: authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 10},
			Match: authoritydomain.DimensionsMatcher{
				Backend: authoritydomain.DimensionMatcher{Value: scope.Known("backend-1")},
			},
		},
	}
	rows, err := authoritystore.LimitRowsFromRules(rules, time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}

	store := authoritystore.NewMemory(authoritystore.Config{
		Backing:   authoritydomain.BackingCapabilityAtomic,
		Readiness: authoritydomain.StatusFromBacking(authoritydomain.BackingCapabilityAtomic),
		LimitRows: rows,
	})

	cmd := strictReserveCommandForPersistence()
	cmd.RuleID = "tenant.requests"
	cmd.ReservationKey.RuleID = "tenant.requests"
	cmd.Dimensions = authoritydomain.Dimensions{
		Principal: scope.Known("principal-1"),
		Tenant:    scope.Known("tenant-1"),
		Backend:   scope.Known("backend-1"),
		Model:     scope.Known("model-1"),
	}
	cmd.Request = authoritydomain.Amount{Unit: authoritydomain.AmountUnitRequests, Value: 1}
	cmd.At = time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	got, err := store.Reserve(t.Context(), cmd)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !got.Applied {
		t.Fatalf("Reserve must apply against seeded wildcard row: %#v", got)
	}
}
