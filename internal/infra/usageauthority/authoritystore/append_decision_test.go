package authoritystore

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// TestAppendDecisionMirrorsLimitWindow is a package-level regression test for
// the bug where appendDecision did not copy WindowStart/End/ResetAt from the
// matched limit row into the decision row. It locks the contract by calling
// appendDecision directly (not through the public API) so that any future
// refactor of the function boundary will fail immediately, even if the
// shared contract suite is reorganized.
//
// This test complements the shared contract test testDecisionRowMirrorsLimitWindow
// (which exercises the full Reserve → DecisionHistory flow) by testing the
// appendDecision function boundary directly.
func TestAppendDecisionMirrorsLimitWindow(t *testing.T) {
	t.Parallel()

	contractBaseTime := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	t.Run("windowed_row_copies_window_bounds", func(t *testing.T) {
		t.Parallel()
		limitRow := &controlplane.AccountingLimitStatusRow{
			Correlation: controlplane.Correlation{
				TraceID:   "trace-1",
				RequestID: "req-1",
				ALegID:    "a-1",
				BLegID:    "b-1",
				BackendID: "backend-1",
				Model:     "model-1",
			},
			Scope: controlplane.ScopeSnapshot{
				PrincipalID: scope.Known("principal-1"),
				TenantID:    scope.Known("tenant-1"),
			},
			RuleID:         "rule-windowed",
			RuleType:       "quota",
			Unit:           string(domain.AmountUnitRequests),
			Limit:          100,
			Remaining:      100,
			WindowStart:    contractBaseTime,
			WindowEnd:      contractBaseTime.Add(time.Hour),
			WindowResetAt:  contractBaseTime.Add(time.Hour),
			Authority:      controlplane.AccountingAuthoritySourceAuthoritative,
			EvidenceState:  controlplane.EvidenceRecorded,
			RedactionState: controlplane.RedactionSummarized,
		}
		store := NewMemory(Config{
			StoreID: "test-mirror-windowed",
			Backing: domain.BackingCapabilityAtomic,
		})
		snapshot := commandSnapshot{
			Correlation: limitRow.Correlation,
			Scope:       limitRow.Scope,
		}
		store.c.appendDecision(discardMutationLog{}, snapshot, limitRow, "res-1", "",
			controlplane.AccountingOutcomeReserve, "reserved",
			controlplane.AccountingAuthoritySourceReserved,
			controlplane.AccountingSettlementPending,
			domain.Amount{Unit: domain.AmountUnitRequests, Value: 10}, 0, 0, 0)

		if len(store.c.decisions) != 1 {
			t.Fatalf("decisions = %d, want 1", len(store.c.decisions))
		}
		got := store.c.decisions[0].Row
		if !got.WindowStart.Equal(contractBaseTime) {
			t.Errorf("decision WindowStart = %v, want %v", got.WindowStart, contractBaseTime)
		}
		if !got.WindowEnd.Equal(contractBaseTime.Add(time.Hour)) {
			t.Errorf("decision WindowEnd = %v, want %v", got.WindowEnd, contractBaseTime.Add(time.Hour))
		}
		if !got.WindowResetAt.Equal(contractBaseTime.Add(time.Hour)) {
			t.Errorf("decision WindowResetAt = %v, want %v", got.WindowResetAt, contractBaseTime.Add(time.Hour))
		}
	})

	t.Run("non_windowed_row_leaves_fields_zero", func(t *testing.T) {
		t.Parallel()
		limitRow := &controlplane.AccountingLimitStatusRow{
			Correlation: controlplane.Correlation{
				TraceID:   "trace-2",
				RequestID: "req-2",
				ALegID:    "a-2",
				BLegID:    "b-2",
				BackendID: "backend-2",
				Model:     "model-2",
			},
			Scope: controlplane.ScopeSnapshot{
				PrincipalID: scope.Known("principal-2"),
				TenantID:    scope.Known("tenant-2"),
			},
			RuleID:    "rule-no-window",
			RuleType:  "budget",
			Unit:      string(domain.AmountUnitMoneyNano),
			Currency:  "usd",
			Limit:     1000,
			Remaining: 1000,
			// WindowStart/End/ResetAt deliberately left as the zero time.Time
			// to exercise the "no window" semantic.
			Authority:      controlplane.AccountingAuthoritySourceAuthoritative,
			EvidenceState:  controlplane.EvidenceRecorded,
			RedactionState: controlplane.RedactionSummarized,
		}
		store := NewMemory(Config{
			StoreID: "test-mirror-nonwindowed",
			Backing: domain.BackingCapabilityAtomic,
		})
		snapshot := commandSnapshot{
			Correlation: limitRow.Correlation,
			Scope:       limitRow.Scope,
		}
		store.c.appendDecision(discardMutationLog{}, snapshot, limitRow, "res-2", "",
			controlplane.AccountingOutcomeReserve, "reserved",
			controlplane.AccountingAuthoritySourceReserved,
			controlplane.AccountingSettlementPending,
			domain.Amount{Unit: domain.AmountUnitMoneyNano, Value: 100}, 0, 0, 0)

		if len(store.c.decisions) != 1 {
			t.Fatalf("decisions = %d, want 1", len(store.c.decisions))
		}
		got := store.c.decisions[0].Row
		if !got.WindowStart.IsZero() {
			t.Errorf("decision WindowStart = %v, want zero", got.WindowStart)
		}
		if !got.WindowEnd.IsZero() {
			t.Errorf("decision WindowEnd = %v, want zero", got.WindowEnd)
		}
		if !got.WindowResetAt.IsZero() {
			t.Errorf("decision WindowResetAt = %v, want zero", got.WindowResetAt)
		}
	})
}
