package contract

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

const (
	seedRuleIDStrict   = "rule-strict"
	seedRuleIDADvisory = "rule-advisory"
	seedPrincipalID    = "principal-1"
	seedTenantID       = "tenant-1"
	seedBackendID      = "backend-1"
	seedModel          = "model-1"
	seedRoute          = "route-1"
	seedRequestID      = "req-1"
	seedTraceID        = "trace-1"
	seedSessionID      = "session-1"
	seedALegID         = "a-1"
	seedBLegID         = "b-1"
)

var contractBaseTime = time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

// Factory builds a fresh store for one contract test.
type Factory interface {
	Build(t *testing.T) app.StateStore
}

// RunSuite executes the shared authority-store contract against the provided factory.
func RunSuite(t *testing.T, f Factory) {
	t.Helper()
	t.Run("StrictReservationIsAtomic", func(t *testing.T) { testStrictReservationIsAtomic(t, f) })
	t.Run("ReserveSettleReleaseIdempotent", func(t *testing.T) { testReserveSettleReleaseIdempotent(t, f) })
	t.Run("QueriesBoundAndSafe", func(t *testing.T) { testQueriesBoundAndSafe(t, f) })
	t.Run("ReadinessKnownStates", func(t *testing.T) { testReadinessKnownStates(t, f) })
	t.Run("DecisionRowMirrorsLimitWindow", func(t *testing.T) { testDecisionRowMirrorsLimitWindow(t, f) })
	t.Run("DecisionRowRecordsSettlementDeltas", func(t *testing.T) { testDecisionRowRecordsSettlementDeltas(t, f) })
}

func testStrictReservationIsAtomic(t *testing.T, f Factory) {
	t.Helper()
	t.Parallel()
	store := f.Build(t)
	t.Cleanup(closeStore(store))

	ctx := ctx(t)
	cmd1 := strictReserveCommand("reserve-1", 60, 1)
	cmd2 := strictReserveCommand("reserve-2", 60, 2)

	type result struct {
		out app.ReserveResult
		err error
	}
	results := make(chan result, 2)

	start := make(chan struct{})
	run := func(name string, cmd app.ReserveCommand) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			<-start
			out, err := store.Reserve(ctx, cmd)
			results <- result{out: out, err: err}
		})
	}
	run("reserve-1", cmd1)
	run("reserve-2", cmd2)
	close(start)
	t.Cleanup(func() {
		successes := 0
		var failed error
		for range 2 {
			res := <-results
			if res.err == nil {
				successes++
				continue
			}
			failed = res.err
		}
		if successes != 1 {
			t.Fatalf("strict reservation successes = %d, want 1", successes)
		}
		if failed == nil {
			t.Fatalf("expected one concurrent reservation to fail")
		}
		if !errors.Is(failed, app.ErrReservationConflict) {
			t.Fatalf("failed reservation error = %v, want reservation conflict", failed)
		}

		page, err := store.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
			Common: controlplane.CommonFilters{
				Scope: controlplane.ScopeFilters{
					PrincipalID: scope.Known(seedPrincipalID),
					TenantID:    scope.Known(seedTenantID),
				},
				BackendID: seedBackendID,
				Model:     seedModel,
				ALegID:    seedALegID,
				BLegID:    seedBLegID,
			},
			RuleID:     seedRuleIDStrict,
			Unit:       string(domain.AmountUnitRequests),
			Authority:  controlplane.AccountingAuthoritySourceAuthoritative,
			Limit:      10,
			Visibility: controlplane.VisibilityDefault,
		})
		if err != nil {
			t.Fatalf("LimitStatus() error = %v", err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("LimitStatus() items = %d, want 1", len(page.Items))
		}
		got := page.Items[0]
		if got.Reserved != 60 || got.Consumed != 0 || got.Remaining != 40 {
			t.Fatalf("strict reservation totals = %#v, want reserved=60 remaining=40", got)
		}
		if got.Scope.PrincipalID != scope.Known(seedPrincipalID) || got.Scope.TenantID != scope.Known(seedTenantID) {
			t.Fatalf("safe scope lost from limit row: %#v", got.Scope)
		}
	})
}

func testReserveSettleReleaseIdempotent(t *testing.T, f Factory) {
	t.Helper()
	t.Parallel()
	store := f.Build(t)
	t.Cleanup(closeStore(store))
	ctx := ctx(t)

	firstReserve, err := store.Reserve(ctx, strictReserveCommand("reserve-1", 60, 1))
	if err != nil {
		t.Fatalf("Reserve() first error = %v", err)
	}
	if !firstReserve.Applied || firstReserve.ReservationID == "" {
		t.Fatalf("Reserve() first result = %#v", firstReserve)
	}

	dupReserve, err := store.Reserve(ctx, strictReserveCommand("reserve-1", 60, 1))
	if err != nil {
		t.Fatalf("Reserve() duplicate error = %v", err)
	}
	if dupReserve.Applied {
		t.Fatalf("duplicate reserve must not apply: %#v", dupReserve)
	}
	if dupReserve.ReservationID != firstReserve.ReservationID {
		t.Fatalf("duplicate reserve reservation id = %q, want %q", dupReserve.ReservationID, firstReserve.ReservationID)
	}

	settleCmd := strictSettleCommand(firstReserve.ReservationID, "settle-1", 40, 60, 40)
	firstSettle, err := store.Settle(ctx, settleCmd)
	if err != nil {
		t.Fatalf("Settle() first error = %v", err)
	}
	if !firstSettle.Applied || firstSettle.ReservationID == "" {
		t.Fatalf("Settle() first result = %#v", firstSettle)
	}

	limitPageAfterSettle, err := store.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known(seedPrincipalID),
				TenantID:    scope.Known(seedTenantID),
			},
			BackendID: seedBackendID,
			Model:     seedModel,
			ALegID:    seedALegID,
			BLegID:    seedBLegID,
		},
		RuleID:     seedRuleIDStrict,
		Unit:       string(domain.AmountUnitRequests),
		Limit:      10,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus() after settle error = %v", err)
	}
	if len(limitPageAfterSettle.Items) != 1 {
		t.Fatalf("LimitStatus() after settle items = %d, want 1", len(limitPageAfterSettle.Items))
	}
	gotRow := limitPageAfterSettle.Items[0]
	if gotRow.Reserved != 0 || gotRow.Consumed != 40 || gotRow.Remaining != 60 {
		t.Fatalf("limit status after settle = %#v, want reserved=0 consumed=40 remaining=60", gotRow)
	}

	dupSettle, err := store.Settle(ctx, settleCmd)
	if err != nil {
		t.Fatalf("Settle() duplicate error = %v", err)
	}
	if dupSettle.Applied {
		t.Fatalf("duplicate settle must not apply: %#v", dupSettle)
	}

	releaseReserve, err := store.Reserve(ctx, strictReserveCommand("reserve-2", 20, 2))
	if err != nil {
		t.Fatalf("Reserve() second error = %v", err)
	}
	releaseCmd := strictReleaseCommand(releaseReserve.ReservationID, "release-1", 20)
	firstRelease, err := store.Release(ctx, releaseCmd)
	if err != nil {
		t.Fatalf("Release() first error = %v", err)
	}
	if !firstRelease.Applied || firstRelease.ReservationID == "" {
		t.Fatalf("Release() first result = %#v", firstRelease)
	}
	dupRelease, err := store.Release(ctx, releaseCmd)
	if err != nil {
		t.Fatalf("Release() duplicate error = %v", err)
	}
	if dupRelease.Applied {
		t.Fatalf("duplicate release must not apply: %#v", dupRelease)
	}

	decisionPage, err := store.DecisionHistory(ctx, controlplane.AccountingDecisionQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known(seedPrincipalID),
				TenantID:    scope.Known(seedTenantID),
			},
			BackendID: seedBackendID,
			Model:     seedModel,
			ALegID:    seedALegID,
			BLegID:    seedBLegID,
		},
		RuleID:     seedRuleIDStrict,
		Unit:       string(domain.AmountUnitRequests),
		Limit:      2,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("DecisionHistory() page 1 error = %v", err)
	}
	if len(decisionPage.Items) != 2 {
		t.Fatalf("DecisionHistory() page 1 items = %d, want 2", len(decisionPage.Items))
	}
	if decisionPage.Next.IsZero() {
		t.Fatalf("DecisionHistory() page 1 next cursor must be set")
	}
	if decisionPage.Items[0].Scope.PrincipalID != scope.Known(seedPrincipalID) {
		t.Fatalf("decision row lost safe scope: %#v", decisionPage.Items[0].Scope)
	}
	if decisionPage.Items[0].Correlation.ALegID != seedALegID || decisionPage.Items[0].Correlation.BLegID != seedBLegID {
		t.Fatalf("decision row lost correlation: %#v", decisionPage.Items[0].Correlation)
	}

	decisionPage2, err := store.DecisionHistory(ctx, controlplane.AccountingDecisionQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known(seedPrincipalID),
				TenantID:    scope.Known(seedTenantID),
			},
			BackendID: seedBackendID,
			Model:     seedModel,
			ALegID:    seedALegID,
			BLegID:    seedBLegID,
		},
		RuleID:     seedRuleIDStrict,
		Unit:       string(domain.AmountUnitRequests),
		Limit:      2,
		Cursor:     decisionPage.Next,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("DecisionHistory() page 2 error = %v", err)
	}
	if len(decisionPage2.Items) == 0 {
		t.Fatalf("DecisionHistory() page 2 items = 0, want more rows")
	}

	limitPage, err := store.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known(seedPrincipalID),
				TenantID:    scope.Known(seedTenantID),
			},
			BackendID: seedBackendID,
			Model:     seedModel,
		},
		RuleID:          seedRuleIDStrict,
		Unit:            string(domain.AmountUnitRequests),
		SettlementState: controlplane.AccountingSettlementPending,
		Limit:           10,
		Visibility:      controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus() unsupported query error = %v", err)
	}
	if len(limitPage.Unsupported) == 0 {
		t.Fatalf("LimitStatus() unsupported query must report unsupported filters")
	}
	foundUnsupported := false
	for _, u := range limitPage.Unsupported {
		if u.Field == "settlement_state" {
			foundUnsupported = true
		}
	}
	if !foundUnsupported {
		t.Fatalf("LimitStatus() unsupported filters = %#v, want settlement_state", limitPage.Unsupported)
	}
}

func testQueriesBoundAndSafe(t *testing.T, f Factory) {
	t.Helper()
	t.Parallel()
	store := f.Build(t)
	t.Cleanup(closeStore(store))
	ctx := ctx(t)

	limitPage, err := store.LimitStatus(ctx, controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known("principal-2"),
				TenantID:    scope.Unknown(),
				ProjectID:   scope.Known(""),
			},
			BackendID: "backend-2",
			Model:     "model-2",
		},
		RuleID:     seedRuleIDADvisory,
		Unit:       string(domain.AmountUnitRequests),
		Limit:      1,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("LimitStatus() error = %v", err)
	}
	if len(limitPage.Items) != 1 {
		t.Fatalf("LimitStatus() items = %d, want 1", len(limitPage.Items))
	}
	if !limitPage.Items[0].Scope.TenantID.IsUnknown() {
		t.Fatalf("unknown scope must remain unknown: %#v", limitPage.Items[0].Scope.TenantID)
	}
	if !limitPage.Items[0].Scope.ProjectID.IsKnownEmpty() {
		t.Fatalf("known-empty scope must remain known-empty: %#v", limitPage.Items[0].Scope.ProjectID)
	}

	decisionPage, err := store.DecisionHistory(ctx, controlplane.AccountingDecisionQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known(seedPrincipalID),
				TenantID:    scope.Known(seedTenantID),
			},
			TimeRange: controlplane.TimeRange{
				From: contractBaseTime.Add(-time.Hour),
				To:   contractBaseTime.Add(time.Hour),
			},
		},
		RuleID:     seedRuleIDStrict,
		Limit:      1,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("DecisionHistory() error = %v", err)
	}
	if len(decisionPage.Unsupported) == 0 {
		t.Fatalf("DecisionHistory() time-range query must report unsupported filters")
	}
}

func testReadinessKnownStates(t *testing.T, f Factory) {
	t.Helper()
	t.Parallel()
	store := f.Build(t)
	t.Cleanup(closeStore(store))
	ctx := ctx(t)

	status, err := store.CheckReadiness(ctx)
	if err != nil {
		t.Fatalf("CheckReadiness() error = %v", err)
	}
	if !status.State.IsKnown() {
		t.Fatalf("CheckReadiness() returned unknown state: %#v", status)
	}
}

func strictReserveCommand(source string, amount int64, seq int) app.ReserveCommand {
	return app.ReserveCommand{
		Correlation: seedCorrelation(),
		Scope:       seedPrincipalScope(),
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: seedRequestID,
			ALegID:           seedALegID,
			BLegID:           seedBLegID,
			AttemptID:        fmt.Sprintf("attempt-%d", seq),
			RuleID:           seedRuleIDStrict,
			Sequence:         seq,
		},
		RuleID:   seedRuleIDStrict,
		RuleType: "quota",
		Dimensions: domain.Dimensions{
			Principal:    scope.Known(seedPrincipalID),
			Tenant:       scope.Known(seedTenantID),
			Organization: scope.Unknown(),
			Workspace:    scope.Known("workspace-1"),
			Project:      scope.Known(""),
			Department:   scope.Unknown(),
			CostCenter:   scope.Unknown(),
			Backend:      scope.Known(seedBackendID),
			Model:        scope.Known(seedModel),
			Route:        scope.Known(seedRoute),
			PolicyLabels: map[string]scope.Value{"tier": scope.Known("standard")},
		},
		Request:      domain.Amount{Unit: domain.AmountUnitRequests, Value: amount},
		Authority:    domain.AuthorityLevelAuthoritative,
		EstimateOnly: false,
		At:           contractBaseTime.Add(time.Duration(seq) * time.Minute),
		SourceKey:    source,
	}
}

func strictSettleCommand(reservationID, source string, finalUsage, reservedUsage, estimatedUsage int64) app.SettleCommand {
	return app.SettleCommand{
		Correlation: seedCorrelation(),
		Scope:       seedPrincipalScope(),
		SettlementKey: domain.SettlementKey{
			ReservationKey: domain.ReservationKey{
				LogicalRequestID: seedRequestID,
				ALegID:           seedALegID,
				BLegID:           seedBLegID,
				AttemptID:        "attempt-1",
				RuleID:           seedRuleIDStrict,
				Sequence:         1,
			},
			Sequence: 1,
		},
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: seedRequestID,
			ALegID:           seedALegID,
			BLegID:           seedBLegID,
			AttemptID:        "attempt-1",
			RuleID:           seedRuleIDStrict,
			Sequence:         1,
		},
		ReservationID:  reservationID,
		RuleID:         seedRuleIDStrict,
		Kind:           app.SettlementKindFinal,
		FinalUsage:     domain.Amount{Unit: domain.AmountUnitRequests, Value: finalUsage},
		ReservedUsage:  domain.Amount{Unit: domain.AmountUnitRequests, Value: reservedUsage},
		EstimatedUsage: domain.Amount{Unit: domain.AmountUnitRequests, Value: estimatedUsage},
		Authority:      domain.AuthorityLevelAuthoritative,
		At:             contractBaseTime.Add(2 * time.Minute),
		SourceKey:      source,
	}
}

func strictReleaseCommand(reservationID, source string, amount int64) app.ReleaseCommand {
	return app.ReleaseCommand{
		Correlation: seedCorrelation(),
		Scope:       seedPrincipalScope(),
		ReleaseKey: domain.ReleaseKey{
			ReservationKey: domain.ReservationKey{
				LogicalRequestID: seedRequestID,
				ALegID:           seedALegID,
				BLegID:           seedBLegID,
				AttemptID:        "attempt-2",
				RuleID:           seedRuleIDStrict,
				Sequence:         2,
			},
			Sequence: 1,
		},
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: seedRequestID,
			ALegID:           seedALegID,
			BLegID:           seedBLegID,
			AttemptID:        "attempt-2",
			RuleID:           seedRuleIDStrict,
			Sequence:         2,
		},
		ReservationID: reservationID,
		RuleID:        seedRuleIDStrict,
		Kind:          app.ReleaseKindSwallowed,
		Amount:        domain.Amount{Unit: domain.AmountUnitRequests, Value: amount},
		At:            contractBaseTime.Add(3 * time.Minute),
		SourceKey:     source,
	}
}

func seedCorrelation() controlplane.Correlation {
	return controlplane.Correlation{
		TraceID:    seedTraceID,
		RequestID:  seedRequestID,
		SessionID:  seedSessionID,
		ALegID:     seedALegID,
		BLegID:     seedBLegID,
		AttemptSeq: 1,
		BackendID:  seedBackendID,
		Model:      seedModel,
	}
}

func seedPrincipalScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind:  scope.SubjectHuman,
		PrincipalID:  scope.Known(seedPrincipalID),
		TenantID:     scope.Known(seedTenantID),
		WorkspaceID:  scope.Known("workspace-1"),
		Origin:       scope.OriginClient,
		Roles:        []string{"analyst"},
		SafeClaims:   map[string]string{"team": "platform"},
		PolicyLabels: map[string]string{"tier": "standard"},
	}
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		t.Cleanup(cancel)
		return ctx
	}
	return context.Background()
}

func closeStore(store app.StateStore) func() {
	return func() {
		if c, ok := store.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}
}

// SeededLimitRows returns the canonical live-limit rows used by the store contract tests.
func SeededLimitRows() []controlplane.AccountingLimitStatusRow {
	return []controlplane.AccountingLimitStatusRow{
		seededStrictLimitRow(),
		seededAdvisoryLimitRow(),
		seededNoWindowLimitRow(),
	}
}

func seededStrictLimitRow() controlplane.AccountingLimitStatusRow {
	return controlplane.AccountingLimitStatusRow{
		Correlation: controlplane.Correlation{
			TraceID:    seedTraceID,
			RequestID:  seedRequestID,
			SessionID:  seedSessionID,
			ALegID:     seedALegID,
			BLegID:     seedBLegID,
			AttemptSeq: 1,
			BackendID:  seedBackendID,
			Model:      seedModel,
		},
		Scope: controlplane.ScopeSnapshot{
			Principal: scope.PrincipalScopeView{
				SubjectKind:  scope.SubjectHuman,
				PrincipalID:  scope.Known(seedPrincipalID),
				TenantID:     scope.Known(seedTenantID),
				WorkspaceID:  scope.Known("workspace-1"),
				Origin:       scope.OriginClient,
				Roles:        []string{"analyst"},
				SafeClaims:   map[string]string{"team": "platform"},
				PolicyLabels: map[string]string{"tier": "standard"},
			},
			PrincipalID:    scope.Known(seedPrincipalID),
			CredentialID:   scope.Unknown(),
			TenantID:       scope.Known(seedTenantID),
			OrganizationID: scope.Unknown(),
			WorkspaceID:    scope.Known("workspace-1"),
			ProjectID:      scope.Known(""),
			DepartmentID:   scope.Unknown(),
			CostCenterID:   scope.Unknown(),
		},
		RuleID:         seedRuleIDStrict,
		RuleType:       "quota",
		Unit:           string(domain.AmountUnitRequests),
		Limit:          100,
		Consumed:       0,
		Reserved:       0,
		Remaining:      100,
		Adjustment:     0,
		WindowStart:    contractBaseTime,
		WindowEnd:      contractBaseTime.Add(time.Hour),
		WindowResetAt:  contractBaseTime.Add(time.Hour),
		Authority:      controlplane.AccountingAuthoritySourceAuthoritative,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionSummarized,
	}
}

func seededAdvisoryLimitRow() controlplane.AccountingLimitStatusRow {
	return controlplane.AccountingLimitStatusRow{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-2",
			RequestID:  "req-2",
			SessionID:  "session-2",
			ALegID:     "a-2",
			BLegID:     "b-2",
			AttemptSeq: 2,
			BackendID:  "backend-2",
			Model:      "model-2",
		},
		Scope: controlplane.ScopeSnapshot{
			Principal: scope.PrincipalScopeView{
				SubjectKind:  scope.SubjectHuman,
				PrincipalID:  scope.Known("principal-2"),
				TenantID:     scope.Unknown(),
				WorkspaceID:  scope.Known("workspace-2"),
				Origin:       scope.OriginClient,
				Roles:        []string{"reader"},
				SafeClaims:   map[string]string{"team": "ops"},
				PolicyLabels: map[string]string{"tier": "advisory"},
			},
			PrincipalID:    scope.Known("principal-2"),
			CredentialID:   scope.Unknown(),
			TenantID:       scope.Unknown(),
			OrganizationID: scope.Unknown(),
			WorkspaceID:    scope.Known("workspace-2"),
			ProjectID:      scope.Known(""),
			DepartmentID:   scope.Unknown(),
			CostCenterID:   scope.Unknown(),
		},
		RuleID:         seedRuleIDADvisory,
		RuleType:       "rate",
		Unit:           string(domain.AmountUnitRequests),
		Limit:          50,
		Consumed:       10,
		Reserved:       0,
		Remaining:      40,
		Adjustment:     0,
		WindowStart:    contractBaseTime.Add(30 * time.Minute),
		WindowEnd:      contractBaseTime.Add(90 * time.Minute),
		WindowResetAt:  contractBaseTime.Add(90 * time.Minute),
		Authority:      controlplane.AccountingAuthoritySourceAdvisory,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionSummarized,
	}
}

// SeededDecisionRows returns the canonical history rows used by the store contract tests.
func SeededDecisionRows() []controlplane.AccountingDecisionRow {
	return nil
}

// testDecisionRowMirrorsLimitWindow proves that appendDecision copies
// WindowStart/End/ResetAt from the matched limit row into the decision row.
// Positive case: a windowed rule's decision row carries the same bounds as
// its limit row. Negative case: a non-windowed rule's decision row leaves all
// three fields as the zero time.Time, matching AccountingLimitStatusRow.
func testDecisionRowMirrorsLimitWindow(t *testing.T, f Factory) {
	t.Helper()
	t.Parallel()
	store := f.Build(t)
	t.Cleanup(closeStore(store))
	ctx := ctx(t)

	// Positive case: reserve against the strict (windowed) rule and assert the
	// decision row's window bounds match the strict limit row's.
	if _, err := store.Reserve(ctx, strictReserveCommand("window-mirror-strict", 10, 1)); err != nil {
		t.Fatalf("Reserve() strict error = %v", err)
	}
	strictDecision, err := store.DecisionHistory(ctx, controlplane.AccountingDecisionQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known(seedPrincipalID),
				TenantID:    scope.Known(seedTenantID),
			},
			BackendID: seedBackendID,
			Model:     seedModel,
			ALegID:    seedALegID,
			BLegID:    seedBLegID,
		},
		RuleID:     seedRuleIDStrict,
		Unit:       string(domain.AmountUnitRequests),
		Limit:      1,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("DecisionHistory() strict error = %v", err)
	}
	if len(strictDecision.Items) == 0 {
		t.Fatalf("DecisionHistory() strict items = 0, want at least 1")
	}
	got := strictDecision.Items[0]
	if !got.WindowStart.Equal(contractBaseTime) {
		t.Fatalf("strict decision WindowStart = %v, want %v", got.WindowStart, contractBaseTime)
	}
	if !got.WindowEnd.Equal(contractBaseTime.Add(time.Hour)) {
		t.Fatalf("strict decision WindowEnd = %v, want %v", got.WindowEnd, contractBaseTime.Add(time.Hour))
	}
	if !got.WindowResetAt.Equal(contractBaseTime.Add(time.Hour)) {
		t.Fatalf("strict decision WindowResetAt = %v, want %v", got.WindowResetAt, contractBaseTime.Add(time.Hour))
	}

	// Negative case: reserve against the non-windowed rule and assert the
	// decision row's window fields are all zero time.Time.
	noWindowCmd := noWindowReserveCommand("window-mirror-nowindow")
	if _, err := store.Reserve(ctx, noWindowCmd); err != nil {
		t.Fatalf("Reserve() no-window error = %v", err)
	}
	noWindowDecision, err := store.DecisionHistory(ctx, controlplane.AccountingDecisionQuery{
		Common: controlplane.CommonFilters{
			BackendID: seedNoWindowBackend,
			Model:     seedNoWindowModel,
			ALegID:    seedNoWindowALeg,
			BLegID:    seedNoWindowBLeg,
		},
		RuleID:     seedRuleIDNoWindow,
		Unit:       string(domain.AmountUnitRequests),
		Limit:      1,
		Visibility: controlplane.VisibilityDefault,
	})
	if err != nil {
		t.Fatalf("DecisionHistory() no-window error = %v", err)
	}
	if len(noWindowDecision.Items) == 0 {
		t.Fatalf("DecisionHistory() no-window items = 0, want at least 1")
	}
	nowRow := noWindowDecision.Items[0]
	if !nowRow.WindowStart.IsZero() {
		t.Fatalf("no-window decision WindowStart = %v, want zero", nowRow.WindowStart)
	}
	if !nowRow.WindowEnd.IsZero() {
		t.Fatalf("no-window decision WindowEnd = %v, want zero", nowRow.WindowEnd)
	}
	if !nowRow.WindowResetAt.IsZero() {
		t.Fatalf("no-window decision WindowResetAt = %v, want zero", nowRow.WindowResetAt)
	}
}

// testDecisionRowRecordsSettlementDeltas proves that appendDecision records the
// settlement/release deltas (Released/Overage/Adjustment) computed by settle
// and release, and leaves them zero for reserve decisions (allow and deny).
// It runs against both the memory and durable stores via the shared Factory.
func testDecisionRowRecordsSettlementDeltas(t *testing.T, f Factory) {
	t.Helper()
	t.Parallel()

	decisionQuery := func(outcome, reason string) controlplane.AccountingDecisionQuery {
		return controlplane.AccountingDecisionQuery{
			Common: controlplane.CommonFilters{
				Scope: controlplane.ScopeFilters{
					PrincipalID: scope.Known(seedPrincipalID),
					TenantID:    scope.Known(seedTenantID),
				},
				BackendID:  seedBackendID,
				Model:      seedModel,
				ALegID:     seedALegID,
				BLegID:     seedBLegID,
				Outcome:    outcome,
				ReasonCode: reason,
			},
			RuleID:     seedRuleIDStrict,
			Unit:       string(domain.AmountUnitRequests),
			Limit:      10,
			Visibility: controlplane.VisibilityDefault,
		}
	}

	requireDecision := func(t *testing.T, store app.StateStore, outcome, reason string) controlplane.AccountingDecisionRow {
		t.Helper()
		page, err := store.DecisionHistory(ctx(t), decisionQuery(outcome, reason))
		if err != nil {
			t.Fatalf("DecisionHistory() %s/%s error = %v", outcome, reason, err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("DecisionHistory() %s/%s items = %d, want 1", outcome, reason, len(page.Items))
		}
		return page.Items[0]
	}

	assertZeroDeltas := func(t *testing.T, row controlplane.AccountingDecisionRow, label string) {
		t.Helper()
		if row.Released != 0 || row.Overage != 0 || row.Adjustment != 0 {
			t.Fatalf("%s decision deltas = released=%d overage=%d adjustment=%d, want all 0",
				label, row.Released, row.Overage, row.Adjustment)
		}
	}

	// Scenario A: settle WITH overage (final usage > reserved).
	// final=50, reserved=30 -> released=0, overage=20, adjustment=-20.
	t.Run("SettleWithOverage", func(t *testing.T) {
		t.Parallel()
		store := f.Build(t)
		t.Cleanup(closeStore(store))
		c := ctx(t)

		reserve, err := store.Reserve(c, strictReserveCommand("delta-overage-reserve", 30, 1))
		if err != nil {
			t.Fatalf("Reserve() error = %v", err)
		}
		if _, err := store.Settle(c, strictSettleCommand(reserve.ReservationID, "delta-overage-settle", 50, 30, 30)); err != nil {
			t.Fatalf("Settle() error = %v", err)
		}

		reserveRow := requireDecision(t, store, string(controlplane.AccountingOutcomeReserve), "reserved")
		assertZeroDeltas(t, reserveRow, "reserve (allow)")

		settleRow := requireDecision(t, store, string(controlplane.AccountingOutcomeReconcile), "reconciled")
		if settleRow.Released != 0 || settleRow.Overage != 20 || settleRow.Adjustment != -20 {
			t.Fatalf("overage settle deltas = released=%d overage=%d adjustment=%d, want released=0 overage=20 adjustment=-20",
				settleRow.Released, settleRow.Overage, settleRow.Adjustment)
		}
	})

	// Scenario B: settle WITH release (final usage < reserved).
	// final=20, reserved=60 -> released=40, overage=0, adjustment=40.
	t.Run("SettleWithRelease", func(t *testing.T) {
		t.Parallel()
		store := f.Build(t)
		t.Cleanup(closeStore(store))
		c := ctx(t)

		reserve, err := store.Reserve(c, strictReserveCommand("delta-release-reserve", 60, 1))
		if err != nil {
			t.Fatalf("Reserve() error = %v", err)
		}
		if _, err := store.Settle(c, strictSettleCommand(reserve.ReservationID, "delta-release-settle", 20, 60, 20)); err != nil {
			t.Fatalf("Settle() error = %v", err)
		}

		settleRow := requireDecision(t, store, string(controlplane.AccountingOutcomeReconcile), "reconciled")
		if settleRow.Released != 40 || settleRow.Overage != 0 || settleRow.Adjustment != 40 {
			t.Fatalf("release settle deltas = released=%d overage=%d adjustment=%d, want released=40 overage=0 adjustment=40",
				settleRow.Released, settleRow.Overage, settleRow.Adjustment)
		}
	})

	// Scenario C: release (no settlement). released=25, no overage, adjustment
	// mirrors released â€” same logic as the limit-row row.Adjustment += released.
	t.Run("Release", func(t *testing.T) {
		t.Parallel()
		store := f.Build(t)
		t.Cleanup(closeStore(store))
		c := ctx(t)

		reserve, err := store.Reserve(c, strictReserveCommand("delta-release-only-reserve", 25, 2))
		if err != nil {
			t.Fatalf("Reserve() error = %v", err)
		}
		if _, err := store.Release(c, strictReleaseCommand(reserve.ReservationID, "delta-release-only", 25)); err != nil {
			t.Fatalf("Release() error = %v", err)
		}

		releaseRow := requireDecision(t, store, string(controlplane.AccountingOutcomeReconcile), "released")
		if releaseRow.Released != 25 || releaseRow.Overage != 0 || releaseRow.Adjustment != 25 {
			t.Fatalf("release deltas = released=%d overage=%d adjustment=%d, want released=25 overage=0 adjustment=25",
				releaseRow.Released, releaseRow.Overage, releaseRow.Adjustment)
		}
	})

	// Scenario D: reserve deny (strict cap exceeded) records zero deltas too.
	t.Run("ReserveDeny", func(t *testing.T) {
		t.Parallel()
		store := f.Build(t)
		t.Cleanup(closeStore(store))
		c := ctx(t)

		// Fill the strict limit (limit=100) so the next reserve must deny.
		if _, err := store.Reserve(c, strictReserveCommand("delta-deny-fill", 100, 1)); err != nil {
			t.Fatalf("Reserve() fill error = %v", err)
		}
		if _, err := store.Reserve(c, strictReserveCommand("delta-deny-over", 1, 2)); err == nil {
			t.Fatalf("Reserve() over-limit must deny")
		}

		denyRow := requireDecision(t, store, string(controlplane.AccountingOutcomeDeny), "quota_exceeded")
		assertZeroDeltas(t, denyRow, "reserve (deny)")
	})
}

// SeededReadiness returns the canonical ready posture used by the contract suite.
func SeededReadiness() domain.AuthorityStatus {
	return domain.AuthorityStatus{State: domain.AuthorityStateReady, Reason: domain.StatusReasonNone}
}

// SeededUnsupportedQueryFields are the documented live-limit filters that a store may report as unsupported.
func SeededUnsupportedQueryFields() []string {
	return []string{"settlement_state"}
}

const (
	seedRuleIDNoWindow    = "rule-no-window"
	seedNoWindowBackend   = "backend-3"
	seedNoWindowModel     = "model-3"
	seedNoWindowALeg      = "a-3"
	seedNoWindowBLeg      = "b-3"
	seedNoWindowTrace     = "trace-3"
	seedNoWindowRequest   = "req-3"
	seedNoWindowSession   = "session-3"
	seedNoWindowPrincipal = "principal-3"
)

func seededNoWindowLimitRow() controlplane.AccountingLimitStatusRow {
	return controlplane.AccountingLimitStatusRow{
		Correlation: controlplane.Correlation{
			TraceID:    seedNoWindowTrace,
			RequestID:  seedNoWindowRequest,
			SessionID:  seedNoWindowSession,
			ALegID:     seedNoWindowALeg,
			BLegID:     seedNoWindowBLeg,
			AttemptSeq: 3,
			BackendID:  seedNoWindowBackend,
			Model:      seedNoWindowModel,
		},
		Scope: controlplane.ScopeSnapshot{
			PrincipalID:    scope.Known(seedNoWindowPrincipal),
			CredentialID:   scope.Unknown(),
			TenantID:       scope.Known(seedTenantID),
			OrganizationID: scope.Unknown(),
			WorkspaceID:    scope.Known("workspace-3"),
			ProjectID:      scope.Known(""),
			DepartmentID:   scope.Unknown(),
			CostCenterID:   scope.Unknown(),
		},
		RuleID:     seedRuleIDNoWindow,
		RuleType:   "quota",
		Unit:       string(domain.AmountUnitRequests),
		Limit:      1000,
		Consumed:   0,
		Reserved:   0,
		Remaining:  1000,
		Adjustment: 0,
		// WindowStart/End/ResetAt deliberately left as the zero time.Time
		// to exercise the "no window" semantic for the negative case.
		Authority:      controlplane.AccountingAuthoritySourceAuthoritative,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionSummarized,
	}
}

func noWindowReserveCommand(source string) app.ReserveCommand {
	return app.ReserveCommand{
		Correlation: controlplane.Correlation{
			TraceID:    seedNoWindowTrace,
			RequestID:  seedNoWindowRequest,
			SessionID:  seedNoWindowSession,
			ALegID:     seedNoWindowALeg,
			BLegID:     seedNoWindowBLeg,
			AttemptSeq: 3,
			BackendID:  seedNoWindowBackend,
			Model:      seedNoWindowModel,
		},
		Scope: scope.PrincipalScopeView{
			SubjectKind: scope.SubjectHuman,
			PrincipalID: scope.Known(seedNoWindowPrincipal),
			TenantID:    scope.Known(seedTenantID),
			WorkspaceID: scope.Known("workspace-3"),
			Origin:      scope.OriginClient,
		},
		ReservationKey: domain.ReservationKey{
			LogicalRequestID: seedNoWindowRequest,
			ALegID:           seedNoWindowALeg,
			BLegID:           seedNoWindowBLeg,
			AttemptID:        "attempt-3",
			RuleID:           seedRuleIDNoWindow,
			Sequence:         3,
		},
		RuleID:   seedRuleIDNoWindow,
		RuleType: "quota",
		Dimensions: domain.Dimensions{
			Principal:    scope.Known(seedNoWindowPrincipal),
			Tenant:       scope.Known(seedTenantID),
			Organization: scope.Unknown(),
			Workspace:    scope.Known("workspace-3"),
			Project:      scope.Known(""),
			Department:   scope.Unknown(),
			CostCenter:   scope.Unknown(),
			Backend:      scope.Known(seedNoWindowBackend),
			Model:        scope.Known(seedNoWindowModel),
			Route:        scope.Known("route-3"),
		},
		Request:      domain.Amount{Unit: domain.AmountUnitRequests, Value: 100},
		Authority:    domain.AuthorityLevelAuthoritative,
		EstimateOnly: false,
		At:           contractBaseTime,
		SourceKey:    source,
	}
}

func parseCursorToken(t *testing.T, token string) int {
	t.Helper()
	if token == "" {
		return 0
	}
	n, err := strconv.Atoi(token)
	if err != nil {
		t.Fatalf("parse cursor token %q: %v", token, err)
	}
	return n
}
