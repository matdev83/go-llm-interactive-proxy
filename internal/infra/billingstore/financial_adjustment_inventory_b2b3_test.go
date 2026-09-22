package billingstore

// Phase 17.3 B2b3/B2b4 architecture/source inventory guard: lists every live
// monetary adjustment entrypoint and requires ownership fencing. Evidence-only
// appenders must not gain money writes. Fails closed when a new money writer
// appears without a fence marker. B2b4 requires marker + pin completion for
// the remaining synchronous writers (gate-only is insufficient: it does not
// bind owner/epoch or atomically complete replay).
//
// Disposition (non-cutover writers, explicitly excluded from
// financial_adjustment pins unless spec/source proves they are cutover
// adjustment writers):
//   - PostFunding/PostPayment (trusted_operations.go, kind funding/payment):
//     provisioning transfers, not B-leg/adjustment economics; share the file
//     with PostAdjustment but keep existing behavior (no pin, no gate).
//   - ChangeCreditPolicy (trusted_operations.go, kind credit_policy): policy
//     event, no journal; excluded.
//   - Customer unit ledger (customer_unit_store.go), allocation, exposure,
//     statement, reconciliation, economic revision/job: evidence-only or
//     unit-ledger, must not call postJournalInTx (checked below).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// monetaryEntrypoint is one live money-writing adjustment entrypoint with its
// required fence marker.
type monetaryEntrypoint struct {
	file        string
	symbol      string
	fenceMarker string
	disposition string
}

func TestB2b3MonetaryAdjustmentInventoryGuard(t *testing.T) {
	t.Parallel()
	entries := []monetaryEntrypoint{
		// Already fenced B2b1/B2b2: must retain their own-kind pins, must not
		// be rewired to financial_adjustment.
		{file: "call_settlement.go", symbol: "ApplyCallBillingResult", fenceMarker: "b2b1", disposition: "customer_call_settlement pin (B2b1), excluded from financial_adjustment"},
		{file: "provider_cost_store.go", symbol: "ApplyProviderCost", fenceMarker: "b2b2", disposition: "provider_charge pin (B2b2), excluded"},
		{file: "provider_cost_revision_store.go", symbol: "ApplyProviderCostRevision", fenceMarker: "b2b2", disposition: "provider_charge pin (B2b2), excluded"},
		// B2b3: the only head/subject monetary adjustment not already covered.
		{file: "selected_cost_adjustment_store.go", symbol: "ApplySelectedCostAdjustment", fenceMarker: "b2b3", disposition: "financial_adjustment head pin (B2b3)"},
		// B2b4: remaining synchronous writers now carry atomic
		// financial_adjustment pins (marker + pin completion, not marker-only).
		{file: "cost_pass_through_store.go", symbol: "ApplyCostPassThroughRevision", fenceMarker: "b2b4", disposition: "financial_adjustment per-head pin (B2b4, cost pass-through)"},
		{file: "trusted_operations.go", symbol: "PostAdjustment", fenceMarker: "b2b4", disposition: "financial_adjustment per-source pin (B2b4, direct adjustment)"},
	}
	for _, entry := range entries {
		path := filepath.Join("internal", "infra", "billingstore", entry.file)
		// Tests run with CWD at repo root via go test ./internal/...; fall back
		// to runtime caller dir when invoked otherwise.
		candidates := []string{path, filepath.Join("..", "..", "..", path)}
		var content string
		var found bool
		for _, candidate := range candidates {
			data, err := os.ReadFile(candidate)
			if err == nil {
				content = string(data)
				found = true
				break
			}
		}
		if !found {
			// Resolve relative to this source file as last resort.
			t.Fatalf("inventory guard: cannot read %s", entry.file)
		}
		if !strings.Contains(content, entry.symbol) {
			t.Fatalf("inventory guard: %s must define %s", entry.file, entry.symbol)
		}
		if !strings.Contains(content, entry.fenceMarker) {
			t.Fatalf("inventory guard: %s %s must contain fence marker %q (disposition: %s)", entry.file, entry.symbol, entry.fenceMarker, entry.disposition)
		}
	}
	// Every postJournalInTx money writer must carry an ownership pin marker in
	// its file (marker + pin completion, not marker-only). The helper
	// definition itself is excluded. B2b4 removes the cutoverGateForNewV1-only
	// allowance: gate-only does not bind owner/epoch or atomically complete
	// replay.
	files, err := filepath.Glob(filepath.Join("internal", "infra", "billingstore", "*.go"))
	if err != nil || len(files) == 0 {
		files, _ = filepath.Glob(filepath.Join("..", "..", "..", "internal", "infra", "billingstore", "*.go"))
	}
	unfenced := []string{}
	for _, file := range files {
		base := filepath.Base(file)
		if base == "journal_in_tx.go" || strings.HasSuffix(base, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		content := string(data)
		if !strings.Contains(content, "postJournalInTx") {
			continue
		}
		if !(strings.Contains(content, "b2b1") || strings.Contains(content, "b2b2") || strings.Contains(content, "b2b3") || strings.Contains(content, "b2b4")) {
			unfenced = append(unfenced, base)
		}
	}
	if len(unfenced) != 0 {
		t.Fatalf("inventory guard: money writers without ownership fence: %s (wire financial_adjustment/customer/provider pins with atomic completion; gate-only cutoverGateForNewV1 is insufficient; do not modify evidence-only appenders)", strings.Join(unfenced, ", "))
	}
	// B2b4 pin-completion guard: every monetary adjustment writer must
	// atomically complete its pin in the same transaction (not marker-only).
	// Check that the two B2b4 files contain both the b2b4 fence and a pin
	// completion helper.
	for _, base := range []string{"cost_pass_through_store.go", "trusted_operations.go"} {
		candidates := []string{filepath.Join("internal", "infra", "billingstore", base), filepath.Join("..", "..", "..", "internal", "infra", "billingstore", base)}
		for _, candidate := range candidates {
			data, err := os.ReadFile(candidate)
			if err != nil {
				continue
			}
			content := string(data)
			if !strings.Contains(content, "b2b4") || !strings.Contains(content, "Complete") || !strings.Contains(content, "PostingPin") {
				t.Fatalf("inventory guard: %s must contain b2b4 marker + pin completion (not marker-only)", base)
			}
		}
	}
	// Evidence-only appenders must not post journals: spot-check that
	// statement/reconciliation/allocation/unit/exposure evidence paths do not
	// call postJournalInTx.
	evidenceFiles := []string{"statement_import_store.go", "reconciliation_retention_store.go", "allocation_store.go", "customer_unit_store.go", "exposure_store.go", "economic_revision_store.go", "economic_job_queue_store.go"}
	for _, base := range evidenceFiles {
		candidates := []string{filepath.Join("internal", "infra", "billingstore", base), filepath.Join("..", "..", "..", "internal", "infra", "billingstore", base)}
		for _, candidate := range candidates {
			data, err := os.ReadFile(candidate)
			if err != nil {
				continue
			}
			if strings.Contains(string(data), "postJournalInTx") {
				t.Fatalf("inventory guard: evidence-only %s must not post journals (immutable evidence, no money)", base)
			}
		}
	}
}
