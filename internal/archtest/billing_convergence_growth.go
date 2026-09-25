package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EconomicsConvergenceGrowthOverlayMax caps the approved usage-economics growth
// allowance inside the billing-convergence denominator: only per-entry growth above
// the locked merge-base lines enters the allowance, so historical baseline code can
// never enter. PR #659 adversarial repair (R1-R10) grows component_rater.go
// (1652 -> 2327) and runtime/billing_leg.go (270 -> 412); the allowance re-measured
// 54,687 lines, reset to 54,712 with 25 headroom. PR #666 adversarial F1-F6
// behavior repairs grow component_rater.go (2327 -> 2587) and
// runtime/billing_leg.go (412 -> 454); the allowance re-measured 54,877 lines,
// reset to 54,902 with 25 headroom.
// PR #666 adversarial F1 pre-execution rejection grows billingadmission/adapter.go
// (367 -> 429) with the immutable V2 native-usage binding; the allowance
// re-measured 54,939 lines, reset to 54,964 with 25 headroom.
// PR #666 reviewed N1/N2 behavior repairs grow component_rater.go (2587 -> 2668,
// +81 measured); the allowance re-measured 54,939 -> 55,020 lines, reset to
// 55,045 with 25 headroom. PR #666 5B-1/2/3 settlement repair grows
// component_rater.go (2668 -> 2840, +172 measured); the allowance re-measured
// 55,020 -> 55,192 lines, reset to 55,217 with 25 headroom.
// PR #666 P1-A/P1-B rater fix grows component_rater.go (2840 -> 2892, +52
// measured); the allowance re-measured 55,192 -> 55,244 lines, reset to
// 55,269 with 25 headroom.
// PR #666 f356 P1-1/P1-2/P2 partition repair splits the frozen
// component-schema inclusion/partition state machine out of component_rater.go
// into the new component_rater_partition.go (1934 + 1279 = 3213, +321 measured
// against the 2892 audited pair); the allowance re-measured 55,244 -> 55,565
// lines, reset to 55,590 with 25 headroom.
// PR #666 62a follow-up adds the post-pricing commercial-relevance gate, the
// frozen subset quantity-consistency proof, and the unobserved-parent
// fail-closed cover denial: component_rater.go 1934 -> 1976 and
// component_rater_partition.go 1279 -> 1513 (1976 + 1513 = 3489, +276 measured
// against the 3213 audited pair); the allowance re-measured 55,565 -> 55,841
// lines, reset to 55,866 with 25 headroom.
const EconomicsConvergenceGrowthOverlayMax = 55866

// economicsConvergenceGrowthEntry is one allowlisted denominator file with its
// locked merge-base (c7fa4169) line count, audited credit, category attribution,
// and provenance. Baseline 0 with provenance "new" is allowed only for files
// genuinely absent at the fork (move-checked: no renames or production deletions in
// range); a renamed file keeps its old baseline under review instead of becoming
// baseline 0. Per-entry headroom is audited credit + 25 (derived, never stored).
type economicsConvergenceGrowthEntry struct {
	path       string
	baseline   int
	credit     int
	category   string
	provenance string // "new" | "modified"
}

// Growth allowance categories and their requirement/task mapping:
// rating (Req 7; T9.2/9.4/10.1-10.3), reconciliation (Req 12; T12.1-12.4),
// adjustment (Req 13 + 17.4 fences; T13.1-13.5), persistence (Req 11; T4/11),
// terminal (Req 6/10; T5/10/13.4), migration (Req 17; T17.1-17.5),
// query (Req 16; T16.1-16.2), settlement (Req 8/14; T10.4/14.2),
// lifecycle (Req 6/10 pre-existing call records), composition (Req 14/15;
// T9.1/14/15), admission (Req 14; T14.1-14.3), identity (Req 6.1/8.3; T10.2),
// package (package docs), evidence (Req 1/2; T2/5), aleg (Req 6 A-leg economics).

var economicsConvergenceGrowthManifest = []economicsConvergenceGrowthEntry{
	{path: "internal/core/billing/account.go", baseline: 102, credit: 0, category: "settlement", provenance: "modified"},
	{path: "internal/core/billing/accounting_cutover.go", baseline: 0, credit: 331, category: "migration", provenance: "new"},
	{path: "internal/core/billing/accounting_recovery.go", baseline: 0, credit: 194, category: "migration", provenance: "new"},
	{path: "internal/core/billing/aleg_authority.go", baseline: 0, credit: 613, category: "aleg", provenance: "new"},
	{path: "internal/core/billing/aleg_passthrough.go", baseline: 0, credit: 435, category: "aleg", provenance: "new"},
	{path: "internal/core/billing/aleg_provider.go", baseline: 0, credit: 1149, category: "aleg", provenance: "new"},
	{path: "internal/core/billing/aleg_report.go", baseline: 0, credit: 337, category: "query", provenance: "new"},
	{path: "internal/core/billing/allocation.go", baseline: 0, credit: 205, category: "settlement", provenance: "new"},
	{path: "internal/core/billing/append.go", baseline: 50, credit: 10, category: "lifecycle", provenance: "modified"},
	{path: "internal/core/billing/append_outbox.go", baseline: 18, credit: 0, category: "lifecycle", provenance: "modified"},
	{path: "internal/core/billing/authorize.go", baseline: 15, credit: 0, category: "admission", provenance: "modified"},
	{path: "internal/core/billing/call_id.go", baseline: 97, credit: 0, category: "identity", provenance: "modified"},
	{path: "internal/core/billing/call_post_usage_worker.go", baseline: 132, credit: 228, category: "terminal", provenance: "modified"},
	{path: "internal/core/billing/call_provider_cost_worker.go", baseline: 133, credit: 232, category: "terminal", provenance: "modified"},
	{path: "internal/core/billing/call_rating.go", baseline: 114, credit: 425, category: "rating", provenance: "modified"},
	{path: "internal/core/billing/call_usage.go", baseline: 332, credit: 182, category: "terminal", provenance: "modified"},
	{path: "internal/core/billing/commands.go", baseline: 213, credit: 9, category: "lifecycle", provenance: "modified"},
	{path: "internal/core/billing/complete_call.go", baseline: 43, credit: 3, category: "lifecycle", provenance: "modified"},
	{path: "internal/core/billing/component_rater.go", baseline: 0, credit: 1976, category: "rating", provenance: "new"},
	{path: "internal/core/billing/component_rater_finalize.go", baseline: 0, credit: 394, category: "rating", provenance: "new"},
	{path: "internal/core/billing/component_rater_partition.go", baseline: 0, credit: 1513, category: "rating", provenance: "new"},
	{path: "internal/core/billing/component_rater_validation.go", baseline: 0, credit: 216, category: "rating", provenance: "new"},
	{path: "internal/core/billing/component_rating_contract.go", baseline: 0, credit: 383, category: "rating", provenance: "new"},
	{path: "internal/core/billing/cost_pass_through.go", baseline: 0, credit: 348, category: "settlement", provenance: "new"},
	{path: "internal/core/billing/cost_selection.go", baseline: 0, credit: 514, category: "reconciliation", provenance: "new"},
	{path: "internal/core/billing/credit_screen.go", baseline: 61, credit: 0, category: "admission", provenance: "modified"},
	{path: "internal/core/billing/customer_settlement_fence.go", baseline: 0, credit: 101, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/customer_units.go", baseline: 0, credit: 1046, category: "settlement", provenance: "new"},
	{path: "internal/core/billing/cutover_claim_token.go", baseline: 0, credit: 190, category: "migration", provenance: "new"},
	{path: "internal/core/billing/cutover_coordinator.go", baseline: 0, credit: 258, category: "migration", provenance: "new"},
	{path: "internal/core/billing/doc.go", baseline: 1, credit: 0, category: "package", provenance: "modified"},
	{path: "internal/core/billing/economic_comparison_reconciler.go", baseline: 0, credit: 668, category: "reconciliation", provenance: "new"},
	{path: "internal/core/billing/economic_detail.go", baseline: 0, credit: 2243, category: "query", provenance: "new"},
	{path: "internal/core/billing/economic_detail_cost_coverage.go", baseline: 0, credit: 914, category: "query", provenance: "new"},
	{path: "internal/core/billing/economic_detail_statement.go", baseline: 0, credit: 717, category: "query", provenance: "new"},
	{path: "internal/core/billing/economic_evidence.go", baseline: 0, credit: 174, category: "evidence", provenance: "new"},
	{path: "internal/core/billing/economic_health.go", baseline: 0, credit: 450, category: "query", provenance: "new"},
	{path: "internal/core/billing/economic_job_queue.go", baseline: 0, credit: 427, category: "terminal", provenance: "new"},
	{path: "internal/core/billing/economic_job_runner.go", baseline: 0, credit: 548, category: "terminal", provenance: "new"},
	{path: "internal/core/billing/economic_posting_intent.go", baseline: 0, credit: 189, category: "terminal", provenance: "new"},
	{path: "internal/core/billing/economic_revision.go", baseline: 0, credit: 681, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/economic_revision_worker.go", baseline: 0, credit: 655, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/estimate.go", baseline: 348, credit: 31, category: "admission", provenance: "modified"},
	{path: "internal/core/billing/evidence.go", baseline: 0, credit: 207, category: "evidence", provenance: "new"},
	{path: "internal/core/billing/exposure.go", baseline: 328, credit: 42, category: "admission", provenance: "modified"},
	{path: "internal/core/billing/exposure_recovery.go", baseline: 8, credit: 0, category: "admission", provenance: "modified"},
	{path: "internal/core/billing/financial_adjustment_fence.go", baseline: 0, credit: 220, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/journal.go", baseline: 169, credit: 0, category: "lifecycle", provenance: "modified"},
	{path: "internal/core/billing/legacy_tariff.go", baseline: 0, credit: 141, category: "migration", provenance: "new"},
	{path: "internal/core/billing/maintenance.go", baseline: 81, credit: 0, category: "lifecycle", provenance: "modified"},
	{path: "internal/core/billing/money.go", baseline: 96, credit: 0, category: "lifecycle", provenance: "modified"},
	{path: "internal/core/billing/observation_work_bridge.go", baseline: 0, credit: 387, category: "terminal", provenance: "new"},
	{path: "internal/core/billing/operator_cost_selection.go", baseline: 0, credit: 731, category: "reconciliation", provenance: "new"},
	{path: "internal/core/billing/posting_ownership.go", baseline: 0, credit: 825, category: "terminal", provenance: "new"},
	{path: "internal/core/billing/provider_charge_fence.go", baseline: 0, credit: 167, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/provider_cost.go", baseline: 46, credit: 65, category: "rating", provenance: "modified"},
	{path: "internal/core/billing/provider_cost_fingerprint.go", baseline: 29, credit: 0, category: "adjustment", provenance: "modified"},
	{path: "internal/core/billing/provider_cost_revision.go", baseline: 0, credit: 1012, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/provider_cost_work.go", baseline: 26, credit: 8, category: "adjustment", provenance: "modified"},
	{path: "internal/core/billing/provision.go", baseline: 17, credit: 0, category: "lifecycle", provenance: "modified"},
	{path: "internal/core/billing/rating.go", baseline: 286, credit: 0, category: "rating", provenance: "modified"},
	{path: "internal/core/billing/reconcile.go", baseline: 188, credit: 0, category: "reconciliation", provenance: "modified"},
	{path: "internal/core/billing/reconciliation_aggregate.go", baseline: 0, credit: 682, category: "reconciliation", provenance: "new"},
	{path: "internal/core/billing/reconciliation_compare.go", baseline: 0, credit: 788, category: "reconciliation", provenance: "new"},
	{path: "internal/core/billing/reconciliation_monetary.go", baseline: 0, credit: 776, category: "reconciliation", provenance: "new"},
	{path: "internal/core/billing/reconciliation_retention.go", baseline: 0, credit: 435, category: "reconciliation", provenance: "new"},
	{path: "internal/core/billing/reconciliation_tolerance.go", baseline: 0, credit: 340, category: "reconciliation", provenance: "new"},
	{path: "internal/core/billing/reconciliation_validation.go", baseline: 0, credit: 959, category: "reconciliation", provenance: "new"},
	{path: "internal/core/billing/records.go", baseline: 182, credit: 56, category: "lifecycle", provenance: "modified"},
	{path: "internal/core/billing/reports.go", baseline: 255, credit: 0, category: "query", provenance: "modified"},
	{path: "internal/core/billing/retail_rating.go", baseline: 0, credit: 1320, category: "rating", provenance: "new"},
	{path: "internal/core/billing/retail_selector.go", baseline: 0, credit: 671, category: "rating", provenance: "new"},
	{path: "internal/core/billing/rich_quote.go", baseline: 0, credit: 532, category: "rating", provenance: "new"},
	{path: "internal/core/billing/route_tariff_binding.go", baseline: 0, credit: 215, category: "rating", provenance: "new"},
	{path: "internal/core/billing/selected_cost_adjustment.go", baseline: 0, credit: 142, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/selected_cost_head.go", baseline: 0, credit: 726, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/selected_cost_head_transition.go", baseline: 0, credit: 336, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/settlement.go", baseline: 28, credit: 0, category: "settlement", provenance: "modified"},
	{path: "internal/core/billing/shadow_evidence.go", baseline: 0, credit: 20, category: "migration", provenance: "new"},
	{path: "internal/core/billing/shadow_port_nil.go", baseline: 0, credit: 22, category: "migration", provenance: "new"},
	{path: "internal/core/billing/statement_import.go", baseline: 0, credit: 481, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/statement_import_adapter.go", baseline: 0, credit: 40, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/statement_match.go", baseline: 0, credit: 603, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/statement_match_contract.go", baseline: 0, credit: 372, category: "adjustment", provenance: "new"},
	{path: "internal/core/billing/submission_fee.go", baseline: 0, credit: 216, category: "rating", provenance: "new"},
	{path: "internal/core/billing/v1_migration_compat.go", baseline: 0, credit: 360, category: "migration", provenance: "new"},
	{path: "internal/core/billing/workload_identity.go", baseline: 130, credit: 0, category: "identity", provenance: "modified"},
	{path: "internal/core/runtime/billing_admission.go", baseline: 477, credit: 53, category: "terminal", provenance: "modified"},
	{path: "internal/core/runtime/billing_call_closure.go", baseline: 94, credit: 8, category: "terminal", provenance: "modified"},
	{path: "internal/core/runtime/billing_call_id.go", baseline: 22, credit: 61, category: "identity", provenance: "modified"},
	{path: "internal/core/runtime/billing_collector.go", baseline: 214, credit: 89, category: "terminal", provenance: "modified"},
	{path: "internal/core/runtime/billing_leg.go", baseline: 417, credit: 454, category: "terminal", provenance: "modified"},
	{path: "internal/infra/billingadmission/adapter.go", baseline: 186, credit: 243, category: "admission", provenance: "modified"},
	{path: "internal/infra/billingadmission/doc.go", baseline: 1, credit: 0, category: "package", provenance: "modified"},
	{path: "internal/infra/billingcompose/catalog.go", baseline: 467, credit: 247, category: "composition", provenance: "modified"},
	{path: "internal/infra/billingcompose/doc.go", baseline: 1, credit: 0, category: "package", provenance: "modified"},
	{path: "internal/infra/billingcompose/identity.go", baseline: 53, credit: 0, category: "composition", provenance: "modified"},
	{path: "internal/infra/billingcompose/keepwarm.go", baseline: 38, credit: 0, category: "composition", provenance: "modified"},
	{path: "internal/infra/billingcompose/operator_cost_selection_catalog.go", baseline: 0, credit: 53, category: "composition", provenance: "new"},
	{path: "internal/infra/billingcompose/resolver.go", baseline: 75, credit: 76, category: "composition", provenance: "modified"},
	{path: "internal/infra/billingstore/account_load.go", baseline: 45, credit: 0, category: "settlement", provenance: "modified"},
	{path: "internal/infra/billingstore/account_tx.go", baseline: 68, credit: 0, category: "settlement", provenance: "modified"},
	{path: "internal/infra/billingstore/accounting_cutover_store.go", baseline: 0, credit: 254, category: "migration", provenance: "new"},
	{path: "internal/infra/billingstore/accounting_recovery_store.go", baseline: 0, credit: 126, category: "migration", provenance: "new"},
	{path: "internal/infra/billingstore/allocation_store.go", baseline: 0, credit: 569, category: "settlement", provenance: "new"},
	{path: "internal/infra/billingstore/call_leg_usage_store.go", baseline: 163, credit: 453, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/call_settlement.go", baseline: 172, credit: 425, category: "settlement", provenance: "modified"},
	{path: "internal/infra/billingstore/call_usage_store.go", baseline: 454, credit: 519, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/cost_pass_through_store.go", baseline: 0, credit: 557, category: "settlement", provenance: "new"},
	{path: "internal/infra/billingstore/customer_settlement_fence_store.go", baseline: 0, credit: 52, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/customer_unit_store.go", baseline: 0, credit: 612, category: "settlement", provenance: "new"},
	{path: "internal/infra/billingstore/cutover_coordinator_store.go", baseline: 0, credit: 1284, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/cutover_economic_f2b_gate.go", baseline: 0, credit: 103, category: "migration", provenance: "new"},
	{path: "internal/infra/billingstore/cutover_economic_f2b_store.go", baseline: 0, credit: 204, category: "migration", provenance: "new"},
	{path: "internal/infra/billingstore/cutover_serialization_store.go", baseline: 0, credit: 277, category: "migration", provenance: "new"},
	{path: "internal/infra/billingstore/cutover_v2_admission_f3_store.go", baseline: 0, credit: 720, category: "migration", provenance: "new"},
	{path: "internal/infra/billingstore/doc.go", baseline: 1, credit: 0, category: "package", provenance: "modified"},
	{path: "internal/infra/billingstore/economic_detail.go", baseline: 0, credit: 1960, category: "persistence", provenance: "new"},
	{path: "internal/infra/billingstore/economic_health.go", baseline: 0, credit: 184, category: "persistence", provenance: "new"},
	{path: "internal/infra/billingstore/economic_job_queue_store.go", baseline: 0, credit: 443, category: "persistence", provenance: "new"},
	{path: "internal/infra/billingstore/economic_job_runner_store.go", baseline: 0, credit: 110, category: "persistence", provenance: "new"},
	{path: "internal/infra/billingstore/economic_posting_intent.go", baseline: 0, credit: 111, category: "persistence", provenance: "new"},
	{path: "internal/infra/billingstore/economic_revision_queue_state.go", baseline: 0, credit: 702, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/economic_revision_store.go", baseline: 0, credit: 915, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/exposure_reconcile.go", baseline: 161, credit: 13, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/exposure_store.go", baseline: 146, credit: 105, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/financial_adjustment_fence_store.go", baseline: 0, credit: 144, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/financial_adjustment_sync_b2b4_store.go", baseline: 0, credit: 373, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/historical_v1_migration.go", baseline: 0, credit: 234, category: "migration", provenance: "new"},
	{path: "internal/infra/billingstore/journal_in_tx.go", baseline: 62, credit: 0, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/journal_store.go", baseline: 366, credit: 0, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/legacy_billing_zero_reserved_nano.go", baseline: 40, credit: 0, category: "migration", provenance: "modified"},
	{path: "internal/infra/billingstore/models.go", baseline: 87, credit: 0, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/operator_queries.go", baseline: 31, credit: 0, category: "query", provenance: "modified"},
	{path: "internal/infra/billingstore/operator_readers.go", baseline: 0, credit: 873, category: "query", provenance: "new"},
	{path: "internal/infra/billingstore/posting_ownership_store.go", baseline: 0, credit: 439, category: "persistence", provenance: "new"},
	{path: "internal/infra/billingstore/provider_charge_fence_store.go", baseline: 0, credit: 142, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/provider_cost_execution_fence.go", baseline: 0, credit: 177, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/provider_cost_failure.go", baseline: 64, credit: 42, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/provider_cost_posting_fence.go", baseline: 0, credit: 304, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/provider_cost_revision_store.go", baseline: 0, credit: 1577, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/provider_cost_store.go", baseline: 134, credit: 367, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/provider_cost_work_store.go", baseline: 123, credit: 9, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/reconcile.go", baseline: 343, credit: 0, category: "reconciliation", provenance: "modified"},
	{path: "internal/infra/billingstore/reconciliation_retention_store.go", baseline: 0, credit: 256, category: "reconciliation", provenance: "new"},
	{path: "internal/infra/billingstore/report_integrity.go", baseline: 50, credit: 0, category: "query", provenance: "modified"},
	{path: "internal/infra/billingstore/reports.go", baseline: 439, credit: 4, category: "query", provenance: "modified"},
	{path: "internal/infra/billingstore/reports_aleg.go", baseline: 0, credit: 2004, category: "query", provenance: "new"},
	{path: "internal/infra/billingstore/reports_call_explanation.go", baseline: 196, credit: 105, category: "query", provenance: "modified"},
	{path: "internal/infra/billingstore/selected_cost_adjustment_store.go", baseline: 0, credit: 929, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/shadow_v2_capture.go", baseline: 0, credit: 245, category: "migration", provenance: "new"},
	{path: "internal/infra/billingstore/shadow_v2_compare.go", baseline: 0, credit: 180, category: "migration", provenance: "new"},
	{path: "internal/infra/billingstore/sqlutil.go", baseline: 63, credit: 0, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/statement_import_store.go", baseline: 0, credit: 436, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/store.go", baseline: 297, credit: 241, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/submission_fee_claim.go", baseline: 0, credit: 63, category: "adjustment", provenance: "new"},
	{path: "internal/infra/billingstore/trusted_operations.go", baseline: 316, credit: 177, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/unique.go", baseline: 39, credit: 0, category: "persistence", provenance: "modified"},
	{path: "internal/infra/billingstore/v2_economics_store.go", baseline: 0, credit: 1614, category: "persistence", provenance: "new"},
	{path: "internal/infra/runtimebundle/billing_compose.go", baseline: 116, credit: 65, category: "composition", provenance: "modified"},
	{path: "internal/infra/runtimebundle/billing_leg_observer.go", baseline: 44, credit: 0, category: "composition", provenance: "modified"},
}

// economicsConvergenceGrowthResult is the measured allowance: the summary used for
// the convergence subtraction and report plus the per-entry live credits the
// acceptance predicate bounds individually.
type economicsConvergenceGrowthResult struct {
	summary OverlayMeasurement
	credit  map[string]int
}

// enumerateEconomicsConvergenceDenominatorFiles lists every production file the
// denominator counts: the artifact roots walked with artifact exclusions plus the
// artifact files read directly.
func enumerateEconomicsConvergenceDenominatorFiles(root string, doc BillingFinalConvergenceBaselineFile) ([]string, error) {
	var out []string
	fs := &workingTreeFS{root: root}
	for _, r := range doc.IncludedRoots {
		err := fs.WalkRootFiles(r.Path, func(rel string, src []byte) error {
			if isBillingFinalConvergenceExcluded(rel, src, doc.ExcludedGlobs) {
				return nil
			}
			out = append(out, rel)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, f := range doc.IncludedFiles {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f.Path))); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out, nil
}

// measureEconomicsConvergenceGrowthOverlay computes the allowance from the manifest.
// Every enumerated denominator file must be allowlisted (unknown files fail); absent
// manifest files credit zero, so deletions always pass. Only manifest entries that
// survive enumeration (present after all artifact exclusions, including generated
// sources) can credit: an allowlisted path that is missing or excluded from the
// denominator receives zero and is never credited independently. Per-entry and
// total bounds are enforced by checkEconomicsConvergenceGrowthAllowance, not
// clamped here.
func measureEconomicsConvergenceGrowthOverlay(root string, doc BillingFinalConvergenceBaselineFile) (economicsConvergenceGrowthResult, error) {
	var zero economicsConvergenceGrowthResult
	files, err := enumerateEconomicsConvergenceDenominatorFiles(root, doc)
	if err != nil {
		return zero, err
	}
	byPath := make(map[string]economicsConvergenceGrowthEntry, len(economicsConvergenceGrowthManifest))
	for _, e := range economicsConvergenceGrowthManifest {
		byPath[e.path] = e
	}
	member := make(map[string]struct{}, len(files))
	for _, f := range files {
		rel := filepath.ToSlash(f)
		if _, ok := byPath[rel]; !ok {
			return zero, fmt.Errorf("denominator file outside pinned growth manifest: %s", f)
		}
		member[rel] = struct{}{}
	}
	summary := OverlayMeasurement{Name: "Economics convergence growth", Max: EconomicsConvergenceGrowthOverlayMax}
	credit := make(map[string]int, len(economicsConvergenceGrowthManifest))
	fs := &workingTreeFS{root: root}
	for _, e := range economicsConvergenceGrowthManifest {
		if _, ok := member[e.path]; !ok {
			continue
		}
		n, rerr := CountBillingFinalConvergenceFileLinesFS(fs, e.path)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			return economicsConvergenceGrowthResult{}, rerr
		}
		c := max(n-e.baseline, 0)
		credit[e.path] = c
		if c > 0 {
			summary.Lines += c
			summary.Files = append(summary.Files, e.path)
		}
	}
	sort.Strings(summary.Files)
	summary.Pass = summary.Lines <= summary.Max
	return economicsConvergenceGrowthResult{summary: summary, credit: credit}, nil
}

// budgetBoundaryError reports excess over a ceiling; empty means within budget.
// Deletions always pass (measured may sit arbitrarily far below the ceiling);
// only excess fails. Shared with the remediation-3A budget pin tests.
func budgetBoundaryError(label string, measured, ceiling int) string {
	if measured > ceiling {
		return fmt.Sprintf("%s: measured %d exceeds ceiling %d", label, measured, ceiling)
	}
	return ""
}

// checkEconomicsConvergenceGrowthAllowance is the complete acceptance predicate:
// the cap stays pinned, every credited entry belongs to the pinned allowlist, each
// entry stays within audited credit + 25, and the total stays within the cap.
// Deletions only shrink credits, so a missing or shrunken entry always passes;
// only excess, unknown entries, or table/cap drift fail.
func checkEconomicsConvergenceGrowthAllowance(result economicsConvergenceGrowthResult) string {
	growth := result.summary
	if growth.Name != "Economics convergence growth" {
		return "growth overlay name = " + growth.Name + ", want Economics convergence growth"
	}
	if growth.Max != EconomicsConvergenceGrowthOverlayMax {
		return "growth overlay max mismatch against EconomicsConvergenceGrowthOverlayMax cap"
	}
	allowed := make(map[string]economicsConvergenceGrowthEntry, len(economicsConvergenceGrowthManifest))
	for _, e := range economicsConvergenceGrowthManifest {
		allowed[e.path] = e
	}
	for _, f := range growth.Files {
		if _, ok := allowed[filepath.ToSlash(f)]; !ok {
			return "growth overlay file outside pinned allowlist: " + f
		}
	}
	for path, live := range result.credit {
		entry, ok := allowed[path]
		if !ok {
			return "growth credit for unlisted path: " + path
		}
		if live > entry.credit+25 {
			return fmt.Sprintf("growth entry %s credit %d exceeds audited %d + 25", path, live, entry.credit)
		}
	}
	if msg := budgetBoundaryError("economics convergence growth", growth.Lines, growth.Max); msg != "" {
		return msg
	}
	return ""
}

// economicsConvergenceGrowthCategories is the closed attribution vocabulary for
// manifest entries.
var economicsConvergenceGrowthCategories = map[string]struct{}{
	"rating": {}, "reconciliation": {}, "adjustment": {}, "persistence": {},
	"terminal": {}, "migration": {}, "query": {}, "settlement": {},
	"lifecycle": {}, "composition": {}, "admission": {}, "identity": {},
	"package": {}, "evidence": {}, "aleg": {},
}

// validateEconomicsConvergenceGrowthManifest checks the manifest table schema:
// non-empty slash paths, strict sort order, path uniqueness, closed category
// and provenance vocabularies, new=>baseline 0, modified=>baseline positive,
// and non-negative credits. Empty means valid. The table lock test pins the
// compiled-in manifest through this validator plus arithmetic sums, while
// injected negative tests prove each rule rejects.
func validateEconomicsConvergenceGrowthManifest(entries []economicsConvergenceGrowthEntry) string {
	seen := make(map[string]struct{}, len(entries))
	prev := ""
	for i, e := range entries {
		if e.path == "" || e.path != filepath.ToSlash(e.path) {
			return fmt.Sprintf("growth manifest[%d] has malformed path %q", i, e.path)
		}
		if _, dup := seen[e.path]; dup {
			return fmt.Sprintf("growth manifest duplicate path: %s", e.path)
		}
		seen[e.path] = struct{}{}
		if i > 0 && e.path <= prev {
			return fmt.Sprintf("growth manifest not strictly sorted at [%d]: %q after %q", i, e.path, prev)
		}
		prev = e.path
		if _, ok := economicsConvergenceGrowthCategories[e.category]; !ok {
			return fmt.Sprintf("growth manifest %s has malformed category %q", e.path, e.category)
		}
		switch e.provenance {
		case "new":
			if e.baseline != 0 {
				return fmt.Sprintf("growth manifest %s provenance new must carry baseline 0, got %d", e.path, e.baseline)
			}
		case "modified":
			if e.baseline <= 0 {
				return fmt.Sprintf("growth manifest %s provenance modified must carry baseline > 0, got %d", e.path, e.baseline)
			}
		default:
			return fmt.Sprintf("growth manifest %s has malformed provenance %q", e.path, e.provenance)
		}
		if e.credit < 0 {
			return fmt.Sprintf("growth manifest %s has negative credit %d", e.path, e.credit)
		}
	}
	return ""
}

// checkEconomicsConvergenceGrowthManifestScope ties every manifest entry to the
// artifact denominator scope: each entry must live in exactly one class,
// root-descendant XOR separately-followed file. Empty means valid.
func checkEconomicsConvergenceGrowthManifestScope(entries []economicsConvergenceGrowthEntry, doc BillingFinalConvergenceBaselineFile) string {
	underRoot := func(rel, rootPath string) bool {
		rel = filepath.ToSlash(rel)
		rootPath = filepath.ToSlash(rootPath)
		return rel == rootPath || strings.HasPrefix(rel, rootPath+"/")
	}
	fileSet := make(map[string]struct{}, len(doc.IncludedFiles))
	for _, f := range doc.IncludedFiles {
		fileSet[filepath.ToSlash(f.Path)] = struct{}{}
	}
	for _, e := range entries {
		inRoot := false
		for _, r := range doc.IncludedRoots {
			if underRoot(e.path, r.Path) {
				inRoot = true
				break
			}
		}
		_, isFile := fileSet[filepath.ToSlash(e.path)]
		if inRoot == isFile {
			return fmt.Sprintf("growth manifest %s must live in exactly one denominator class (root=%v file=%v)", e.path, inRoot, isFile)
		}
	}
	return ""
}

// checkEconomicsConvergenceGrowthDisjointness enforces no double count against
// the whole-file usage-economics overlay allowlist and the followed-declaration
// inventory. Empty means disjoint.
func checkEconomicsConvergenceGrowthDisjointness(entries []economicsConvergenceGrowthEntry, doc BillingFinalConvergenceBaselineFile) string {
	claimed := make(map[string]struct{}, len(usageEconomicsGrowthFiles))
	for _, f := range usageEconomicsGrowthFiles {
		claimed[f.path] = struct{}{}
	}
	declFiles := make(map[string]struct{}, len(doc.IncludedDeclarations))
	for _, d := range doc.IncludedDeclarations {
		declFiles[filepath.ToSlash(d.File)] = struct{}{}
	}
	for _, e := range entries {
		if _, ok := claimed[e.path]; ok {
			return fmt.Sprintf("growth manifest double-counts usage-economics overlay file: %s", e.path)
		}
		if _, ok := declFiles[e.path]; ok {
			return fmt.Sprintf("growth manifest double-counts followed-declaration file: %s", e.path)
		}
	}
	return ""
}
