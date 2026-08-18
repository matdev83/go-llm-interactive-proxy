package authoritystore_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/usageauthority/authoritystore/contract"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestAuthorityStoreMemorySQLiteDifferentialSequence(t *testing.T) {
	t.Parallel()
	config := authoritystore.Config{
		Backing:   domain.BackingCapabilityAtomic,
		LimitRows: contract.SeededLimitRows(),
		Readiness: contract.SeededReadiness(),
	}
	memory := authoritystore.NewMemory(config)
	t.Cleanup(func() { _ = memory.Close() })

	dsn := sqliteAuthorityDSN(filepath.Join(t.TempDir(), "differential.db"))
	sqliteDB := openSQLiteAuthorityBun(t, dsn)
	sqlite, err := authoritystore.NewDurable(context.Background(), sqliteDB, authoritystore.Config{
		StoreID:   "differential-sqlite",
		Backing:   config.Backing,
		LimitRows: config.LimitRows,
		Readiness: config.Readiness,
	})
	if err != nil {
		t.Fatalf("NewDurable: %v", err)
	}
	t.Cleanup(func() { _ = sqlite.Close() })

	runDifferentialSequence(t, "memory", memory)
	runDifferentialSequence(t, "sqlite", sqlite)
	assertEquivalentState(t, memory, sqlite)

	// Restart the durable adapter and compare the persisted observable state
	// again. This catches replay markers/facts that exist only in the process
	// projection.
	_ = sqlite.Close()
	reopenedDB := openSQLiteAuthorityBun(t, dsn)
	reopened, err := authoritystore.NewDurable(context.Background(), reopenedDB, authoritystore.Config{
		StoreID:   "differential-sqlite",
		Backing:   config.Backing,
		LimitRows: config.LimitRows,
		Readiness: config.Readiness,
	})
	if err != nil {
		t.Fatalf("reopen durable: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertEquivalentState(t, memory, reopened)
}

func runDifferentialSequence(t *testing.T, name string, store app.StateStore) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 7, 4, 12, 10, 0, 0, time.UTC)
	strictDims := domain.Dimensions{
		Principal: scope.Known("principal-1"), Tenant: scope.Known("tenant-1"),
		Workspace: scope.Known("workspace-1"), Project: scope.Known(""),
		Organization: scope.Unknown(), Department: scope.Unknown(), CostCenter: scope.Unknown(),
		Backend: scope.Known("backend-1"), Model: scope.Known("model-1"), Route: scope.Known("route-1"),
		PolicyLabels: map[string]scope.Value{"tier": scope.Known("standard")},
	}
	key := domain.ReservationKey{LogicalRequestID: "differential-request", ALegID: "a", BLegID: "b", AttemptID: "attempt-1", RuleID: "rule-strict", Sequence: 1}
	reserve := app.ReserveCommand{
		Correlation:    controlplane.Correlation{RequestID: "differential-request", ALegID: "a", BLegID: "b", BackendID: "backend-1", Model: "model-1"},
		Scope:          scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1"), TenantID: scope.Known("tenant-1"), WorkspaceID: scope.Known("workspace-1"), PolicyLabels: map[string]string{"tier": "standard"}},
		ReservationKey: key, RuleID: "rule-strict", RuleType: "quota", Dimensions: strictDims,
		Request:   domain.Amount{Unit: domain.AmountUnitRequests, Value: 30},
		Authority: domain.AuthorityLevelEstimated, At: at, SourceKey: "differential-reserve",
	}
	first, err := store.Reserve(ctx, reserve)
	if err != nil || !first.Applied {
		t.Fatalf("%s first reserve = %#v, err=%v", name, first, err)
	}
	duplicate, err := store.Reserve(ctx, reserve)
	if err != nil || duplicate.Applied || duplicate.ReservationID != first.ReservationID {
		t.Fatalf("%s duplicate reserve = %#v, err=%v", name, duplicate, err)
	}

	settle := app.SettleCommand{
		Correlation: reserve.Correlation, Scope: reserve.Scope, ReservationKey: key,
		ReservationID: first.ReservationID, RuleID: "rule-strict", Kind: app.SettlementKindFinal,
		FinalUsage:     domain.Amount{Unit: domain.AmountUnitRequests, Value: 15},
		EstimatedUsage: domain.Amount{Unit: domain.AmountUnitRequests, Value: 30},
		ReservedUsage:  reserve.Request, Authority: domain.AuthorityLevelEstimated,
		At: at.Add(time.Minute), SourceKey: "differential-settle",
	}
	settled, err := store.Settle(ctx, settle)
	if err != nil || !settled.Applied {
		t.Fatalf("%s settle = %#v, err=%v", name, settled, err)
	}
	replay, err := store.Settle(ctx, settle)
	if err != nil || replay.Applied {
		t.Fatalf("%s settle replay = %#v, err=%v", name, replay, err)
	}

	advisory := app.ApplyUsageCommand{
		Correlation: controlplane.Correlation{RequestID: "differential-advisory", BackendID: "backend-2", Model: "model-2"},
		Dimensions: domain.Dimensions{
			Principal: scope.Known("principal-2"), Tenant: scope.Unknown(), Workspace: scope.Known("workspace-2"), Project: scope.Known(""),
			Backend: scope.Known("backend-2"), Model: scope.Known("model-2"),
			PolicyLabels: map[string]scope.Value{"tier": scope.Known("advisory")},
		},
		RuleIDs: []string{"rule-advisory"}, RequestCount: domain.Amount{Unit: domain.AmountUnitRequests, Value: 7},
		At: at, Kind: app.SettlementKindPartial, SourceKey: "differential-advisory",
	}
	updated, err := store.ApplyUsage(ctx, advisory)
	if err != nil || !updated.Applied {
		t.Fatalf("%s advisory update = %#v, err=%v", name, updated, err)
	}
	replayUsage, err := store.ApplyUsage(ctx, advisory)
	if err != nil || replayUsage.Applied {
		t.Fatalf("%s advisory replay = %#v, err=%v", name, replayUsage, err)
	}
}

func assertEquivalentState(t *testing.T, want, got app.StateStore) {
	t.Helper()
	ctx := context.Background()
	query := controlplane.AccountingLimitStatusQuery{Limit: 20, Visibility: controlplane.VisibilityDefault}
	wantLimits, err := want.LimitStatus(ctx, query)
	if err != nil {
		t.Fatalf("want LimitStatus: %v", err)
	}
	gotLimits, err := got.LimitStatus(ctx, query)
	if err != nil {
		t.Fatalf("got LimitStatus: %v", err)
	}
	if !reflect.DeepEqual(limitSignatures(wantLimits.Items), limitSignatures(gotLimits.Items)) {
		t.Fatalf("limit state diverged:\nwant=%#v\ngot=%#v", limitSignatures(wantLimits.Items), limitSignatures(gotLimits.Items))
	}
	wantDecisions, err := want.DecisionHistory(ctx, controlplane.AccountingDecisionQuery{Limit: 50, Visibility: controlplane.VisibilityDefault})
	if err != nil {
		t.Fatalf("want DecisionHistory: %v", err)
	}
	gotDecisions, err := got.DecisionHistory(ctx, controlplane.AccountingDecisionQuery{Limit: 50, Visibility: controlplane.VisibilityDefault})
	if err != nil {
		t.Fatalf("got DecisionHistory: %v", err)
	}
	if !reflect.DeepEqual(decisionSignatures(wantDecisions.Items), decisionSignatures(gotDecisions.Items)) {
		t.Fatalf("decision state diverged:\nwant=%#v\ngot=%#v", decisionSignatures(wantDecisions.Items), decisionSignatures(gotDecisions.Items))
	}
}

type limitSignature struct {
	RuleID                                    string
	Unit                                      string
	Consumed, Reserved, Remaining, Adjustment int64
}

func limitSignatures(rows []controlplane.AccountingLimitStatusRow) []limitSignature {
	out := make([]limitSignature, 0, len(rows))
	for _, row := range rows {
		out = append(out, limitSignature{RuleID: row.RuleID, Unit: row.Unit, Consumed: row.Consumed, Reserved: row.Reserved, Remaining: row.Remaining, Adjustment: row.Adjustment})
	}
	return out
}

type decisionSignature struct {
	RuleID, Outcome, Reason, Unit                                string
	Consumed, Reserved, Remaining, Released, Overage, Adjustment int64
}

func decisionSignatures(rows []controlplane.AccountingDecisionRow) []decisionSignature {
	out := make([]decisionSignature, 0, len(rows))
	for _, row := range rows {
		out = append(out, decisionSignature{RuleID: row.RuleID, Outcome: string(row.Outcome), Reason: row.ReasonCode, Unit: row.Unit, Consumed: row.Consumed, Reserved: row.Reserved, Remaining: row.Remaining, Released: row.Released, Overage: row.Overage, Adjustment: row.Adjustment})
	}
	return out
}
