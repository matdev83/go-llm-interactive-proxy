package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Row-level semantic checks for the Task 18.2 migration disposition
// (parent Phase 18 review finding 2). Each corrected row is pinned to live
// source properties: retained V1 projections must show their V1 signature
// and name a separate, existing V2 owner; the owner-aware rater must expose
// both its V2 component path and its fenced V1 drain. These run as part of
// TestPhase182MigrationDispositionClosesCensus.

// assertPhase182Row7AggregateProjection pins row 7: Apply is a retained V1
// scalar query projection, not native V2. The real V2 reduction owner is
// ApplyObservations; no live financial consumer treats Apply as authority.
func assertPhase182Row7AggregateProjection(t *testing.T, root string) {
	t.Helper()
	assertContainsString(t, root, "internal/core/metering/aggregate/aggregate.go", "func Apply(facts []metering.Fact)",
		"row 7 anchor Apply must keep its V1 fact signature")
	assertContainsString(t, root, "internal/core/metering/aggregate/aggregate.go", "map[string]int64",
		"row 7 projection result is a scalar component map, not complete V2 component keys")
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("internal/core/metering/aggregate/aggregate.go")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "metering.Observation") {
		t.Fatal("row 7 aggregate.Apply consumes metering.Observation: V1 projection classification is false")
	}
	assertContainsString(t, root, "internal/core/metering/aggregate/observations.go", "func ApplyObservations(",
		"row 7 V2 owner ApplyObservations must exist in observations.go")
	assertContainsString(t, root, "internal/core/billing/component_rater.go", "aggregate.ApplyObservations(",
		"row 7 financial rating must consume the V2 owner, not the V1 projection")
	// No live financial consumer may call the V1 projection: production
	// allowlist is reconcile.Stream only; tests and journal query fixtures
	// may exercise it.
	offenders := phase182ProductionCallers(t, root, "internal", "aggregate.Apply(",
		[]string{"internal/core/metering/reconcile/reconcile.go"})
	if len(offenders) != 0 {
		t.Fatalf("row 7 V1 projection aggregate.Apply has live financial callers (must use ApplyObservations):\n%s",
			strings.Join(offenders, "\n"))
	}
}

// assertPhase182Row8ReconcileProjection pins row 8: Stream is V1 fact
// reconciliation, not V2 source/component reconciliation.
func assertPhase182Row8ReconcileProjection(t *testing.T, root string) {
	t.Helper()
	assertContainsString(t, root, "internal/core/metering/reconcile/reconcile.go", "aggregate.Apply(facts)",
		"row 8 Stream must invoke the V1 aggregator")
	assertContainsString(t, root, "internal/core/metering/reconcile/reconcile.go", "metering.Querier",
		"row 8 Stream must query V1 facts")
	assertContainsString(t, root, "internal/core/billing/reconciliation_compare.go", "func CompareComponentQuantities(",
		"row 8 V2 reconciliation owner CompareComponentQuantities must exist")
	offenders := phase182ProductionCallers(t, root, "", "reconcile.Stream(", nil)
	if len(offenders) != 0 {
		t.Fatalf("row 8 V1 reconcile.Stream has production callers (V2 reconciliation owns live economics):\n%s",
			strings.Join(offenders, "\n"))
	}
}

// assertPhase182Row33FinalizeProjection pins row 33:
// finalizeBillingResponseToEvent projects six scalar counters only; the
// negotiated V2 observation owners live in stream.go/backend.go.
func assertPhase182Row33FinalizeProjection(t *testing.T, root string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("internal/infra/backendplugins/adapter/finalize_billing.go")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "response.Usage") {
		t.Fatal("row 33 finalizeBillingResponseToEvent must read response.Usage scalar counters")
	}
	if strings.Contains(string(src), "AccountingV2") || strings.Contains(string(src), "Observation") {
		t.Fatal("row 33 finalizeBillingResponseToEvent touches V2 observations: scalar projection classification is false")
	}
	assertContainsString(t, root, "internal/infra/backendplugins/adapter/stream.go", "func (s *managedStream) DrainAccountingEvidenceV2(",
		"row 33 V2 owner DrainAccountingEvidenceV2 must exist in stream.go")
	assertContainsString(t, root, "internal/infra/backendplugins/adapter/stream.go", "func (s *managedStream) DrainEconomicObservations(",
		"row 33 V2 owner DrainEconomicObservations must exist in stream.go")
	assertContainsString(t, root, "internal/infra/backendplugins/adapter/backend.go", "AccountingV2",
		"row 33 V2 EconomicEvidence drain must exist in backend.go")
}

// assertPhase182Row34SidebandProjection pins row 34: the same six-counter
// overstatement pattern in accountingEvidenceToEvent.
func assertPhase182Row34SidebandProjection(t *testing.T, root string) {
	t.Helper()
	assertContainsString(t, root, "internal/infra/backendplugins/adapter/stream.go", "func accountingEvidenceToEvent(e *backendplugin.AccountingEvidence)",
		"row 34 anchor must keep its V1 AccountingEvidence signature")
	assertContainsString(t, root, "internal/infra/backendplugins/adapter/stream.go", "func (s *managedStream) DrainAccountingEvidenceV2(",
		"row 34 V2 sideband owner must exist in the same file")
}

// assertPhase182Row37CollectorProjection pins row 37: callFinalizeBilling
// returns the scalar Usage event only; V2 preservation is owned by
// callFinalizeBillingResult/FinalizeBillingV2.
func assertPhase182Row37CollectorProjection(t *testing.T, root string) {
	t.Helper()
	assertContainsString(t, root, "internal/core/runtime/billing_collector.go", "func (e *Executor) callFinalizeBilling(",
		"row 37 anchor must exist")
	assertContainsString(t, root, "internal/core/runtime/billing_collector.go", "return result.Usage, err",
		"row 37 anchor must return the scalar Usage projection")
	assertContainsString(t, root, "internal/core/runtime/billing_collector.go", "callFinalizeBillingResult",
		"row 37 V2-preserving owner callFinalizeBillingResult must exist")
	assertContainsString(t, root, "internal/core/runtime/billing_collector.go", "EconomicEvidence",
		"row 37 V2 EconomicEvidence must be handled by the owner, not the scalar anchor")
}

// assertPhase182Row52OwnerAwareRating pins row 52 after the scalar fix:
// RateCall is an owner-aware dispatcher with a V2 component path and an
// explicitly fenced historical V1 drain, never whole-function historical.
// Durable posting ownership (not envelope format) selects the frozen writer:
// V1 drain preserves scalar charge with additive V2 observations intact.
// The V2 valuation fence is generic and bound: worker before Apply (V2
// requires OwnerAware plus complete bound valuation) plus store transaction
// entry (direct V2 scalar/ID-only fails with zero effects).
func assertPhase182Row52OwnerAwareRating(t *testing.T, root string) {
	t.Helper()
	callRating := phase18ParseFile(t, "internal/core/billing/call_rating.go")
	for _, required := range []string{
		"effectiveCallRatingOwner",
		"rateV2ComponentCall",
		"rateV1DrainCall",
		"ValidateCallRatingResultForOwner",
		"ValidateCallRatingResultForSettlement",
		"OwnerAwareCallRatingResolver",
	} {
		if !phase18HasFuncDecl(callRating, required) && !phase18HasIdent(callRating, required) {
			t.Fatalf("row 52 internal/core/billing/call_rating.go lacks %s: owner-aware V1/V2 selection is required", required)
		}
	}
	if phase18FuncBodyHasIdent(callRating, "RateCall", "rateCustomerCharge") {
		t.Fatal("row 52 RateCall calls rateCustomerCharge: scalar engine must live only in rateV1DrainCall behind V1 ownership")
	}
	if !phase18FuncBodyHasIdent(callRating, "rateV1DrainCall", "rateCustomerCharge") {
		t.Fatal("row 52 rateV1DrainCall must retain rateCustomerCharge for authorized V1 drain/replay")
	}
	if phase18FuncBodyHasIdent(callRating, "rateV1DrainCall", "WriterVersionForLeg") {
		t.Fatal("row 52 rateV1DrainCall gates on WriterVersionForLeg: durable V1 ownership (not envelope format) must select the frozen writer with additive V2 preserved")
	}
	{
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("internal/core/billing/call_rating.go")))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "V1-owned rating cannot consume V2") {
			t.Fatal("row 52 RateCall rejects V1 with additive V2: V1 ownership must preserve frozen charge with additive V2 intact")
		}
	}
	if !phase18FuncBodyHasIdent(callRating, "rateV2ComponentCall", "RateSelectedRetailBLegs") {
		t.Fatal("row 52 rateV2ComponentCall must rate via RateSelectedRetailBLegs (component path, including mapped legacy semantics)")
	}
	assertContainsString(t, root, "internal/core/billing/retail_rating.go", "func RateSelectedRetailBLegs(",
		"row 52 V2 component rater must exist in retail_rating.go")
	resolver := phase18ParseFile(t, "internal/infra/billingcompose/resolver.go")
	if !phase18HasFuncDecl(resolver, "ResolveCallRatingForOwner") {
		t.Fatal("row 52 resolver lacks ResolveCallRatingForOwner: production resolver must propagate the B1 pin owner into rating")
	}
	if !phase18HasIdent(resolver, "ValidateCallRatingResultForSettlement") {
		t.Fatal("row 52 resolver must enforce ValidateCallRatingResultForSettlement for bound V2 component valuation")
	}
	worker := phase18ParseFile(t, "internal/core/billing/call_post_usage_worker.go")
	if !phase18HasIdent(worker, "ResolveCallRatingForOwner") {
		t.Fatal("row 52 worker must propagate the claim owner via ResolveCallRatingForOwner")
	}
	if !phase18HasIdent(worker, "OwnerAwareCallRatingResolver") {
		t.Fatal("row 52 worker must resolve via OwnerAwareCallRatingResolver")
	}
	if !phase18HasIdent(worker, "ValidateCallRatingResultForSettlement") {
		t.Fatal("row 52 worker must enforce ValidateCallRatingResultForSettlement before Apply (generic bound V2 result fence)")
	}
	assertContainsString(t, root, "internal/core/billing/call_post_usage_worker.go", "V2-owned rating requires an owner-aware resolver",
		"row 52 worker must require OwnerAwareCallRatingResolver for V2 (no legacy fallback)")
	settlement := phase18ParseFile(t, "internal/infra/billingstore/call_settlement.go")
	if !phase18HasIdent(settlement, "ValidateCallRatingResultForSettlement") {
		t.Fatal("row 52 store must enforce ValidateCallRatingResultForSettlement at ApplyCallBillingResult transaction entry (direct V2 scalar/ID-only fails with zero effects)")
	}
}

// assertPhase182Row68ReportProjection pins row 68:
// DualPlaneReportInputsFromFacts is a scalar V1 report projection; the
// distinct public V2 detail binding is UsageDetail plus the projector.
func assertPhase182Row68ReportProjection(t *testing.T, root string) {
	t.Helper()
	assertContainsString(t, root, "pkg/lipsdk/controlplane/economics_report_from_facts.go", "facts []metering.Fact",
		"row 68 anchor must take V1 facts")
	assertContainsString(t, root, "pkg/lipsdk/controlplane/economics_report_from_facts.go", "TokenQuantityInput",
		"row 68 projection must build scalar report inputs")
	assertContainsString(t, root, "pkg/lipsdk/controlplane/details.go", "type UsageDetail struct",
		"row 68 distinct public V2 detail binding UsageDetail must exist")
	assertContainsString(t, root, "internal/core/controlplane/usage_projector.go", "func ProjectMeteringFact(",
		"row 68 V2 detail projector ProjectMeteringFact must exist")
	assertContainsString(t, root, "internal/core/controlplane/usage_projector.go", "func ProjectUsageDetail(",
		"row 68 V2 detail projector ProjectUsageDetail must exist")
	assertContainsString(t, root, "internal/infra/billingstore/reports.go", "AccountReport",
		"row 68 live financial truth owner billingstore AccountReport must exist")
}

// phase182ProductionCallers lists non-test production files under walkRoot
// (empty means the whole repo, restricted to internal/ and pkg/) whose
// content references substr, minus explicitly allowlisted production files.
// Tests never count as live financial consumers.
func phase182ProductionCallers(t *testing.T, root, walkRoot, substr string, allow []string) []string {
	t.Helper()
	allowed := map[string]struct{}{}
	for _, rel := range allow {
		allowed[rel] = struct{}{}
	}
	base := root
	if walkRoot != "" {
		base = filepath.Join(root, filepath.FromSlash(walkRoot))
	}
	var offenders []string
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if walkRoot == "" && !strings.HasPrefix(rel, "internal/") && !strings.HasPrefix(rel, "pkg/") {
			return nil
		}
		if _, ok := allowed[rel]; ok {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), substr) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return offenders
}
