package authoritystore_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// snapshotRuleSource is a minimal app.RuleSource stub for admission integration.
type snapshotRuleSource struct {
	snapshot app.RuleSnapshot
}

func (s *snapshotRuleSource) Snapshot(context.Context) (app.RuleSnapshot, error) {
	return s.snapshot, nil
}

// fixedClock is a deterministic app.Clock for admission integration tests.
type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

// noopEvidenceSink is a minimal app.EvidenceSink that discards projected evidence.
type noopEvidenceSink struct{}

func (noopEvidenceSink) RecordPolicyDecision(context.Context, policydecision.Record) error {
	return nil
}

func (noopEvidenceSink) RecordAccountingAuthority(context.Context, controlplane.Event) error {
	return nil
}

// TestMemoryStore_AdmitReservesAllMatchedStrictRules wires the real MemoryStore
// to the app Service and proves that a request matching two strict quota rules
// reserves against BOTH rule windows: each matched limit row's Reserved counter
// is incremented. This is the accounting-correctness symptom of the multi-rule
// reservation bug (only the first rule reserved, letting later admissions
// over-commit against the unreserved rule).
func TestMemoryStore_AdmitReservesAllMatchedStrictRules(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	rules := []domain.Rule{
		{
			ID:    "tenant.requests",
			Kind:  domain.RuleKindQuota,
			Mode:  domain.RuleModeStrict,
			Unit:  domain.AmountUnitRequests,
			Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 10},
			Match: domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
		},
		{
			ID:    "tenant.requests.daily",
			Kind:  domain.RuleKindQuota,
			Mode:  domain.RuleModeStrict,
			Unit:  domain.AmountUnitRequests,
			Limit: domain.Amount{Unit: domain.AmountUnitRequests, Value: 100},
			Match: domain.DimensionsMatcher{Backend: domain.DimensionMatcher{Value: scope.Known("backend-1")}},
		},
	}
	limitRows, err := authoritystore.LimitRowsFromRules(rules, now)
	if err != nil {
		t.Fatalf("LimitRowsFromRules: %v", err)
	}
	store := authoritystore.NewMemory(authoritystore.Config{
		StoreID:   "memory-multi-rule-admit",
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: limitRows,
		Readiness: domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
	})

	svc := app.NewService(
		&snapshotRuleSource{snapshot: app.RuleSnapshot{
			Status:    domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone},
			Rules:     rules,
			FetchedAt: now,
		}},
		store,
		noopEvidenceSink{},
		fixedClock{now: now},
	)

	input := app.AdmissionInput{
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

	got, err := svc.Admit(context.Background(), input)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !got.Allowed || !got.Reserved || got.ReservationID == "" {
		t.Fatalf("admission must allow and reserve: %#v", got)
	}

	assertReserved := func(t *testing.T, ruleID string, want int64) {
		t.Helper()
		page, err := store.LimitStatus(context.Background(), controlplane.AccountingLimitStatusQuery{
			Common:     controlplane.CommonFilters{BackendID: "backend-1"},
			RuleID:     ruleID,
			Unit:       string(domain.AmountUnitRequests),
			Limit:      10,
			Visibility: controlplane.VisibilityDefault,
		})
		if err != nil {
			t.Fatalf("LimitStatus(%s): %v", ruleID, err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("LimitStatus(%s) items = %d, want 1: %#v", ruleID, len(page.Items), page.Items)
		}
		if page.Items[0].Reserved != want {
			t.Fatalf("LimitStatus(%s) Reserved = %d, want %d", ruleID, page.Items[0].Reserved, want)
		}
	}
	assertReserved(t, "tenant.requests", 3)
	assertReserved(t, "tenant.requests.daily", 3)
}
