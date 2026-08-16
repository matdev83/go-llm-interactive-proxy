package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase 7.1 activates the hold/collector deletion guard; the LOC ratchet remains
// deferred until the final Phase 7.4 certification.

func TestBillingExposurePlannedRatchetsRecordedOnCommittedLock(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingExposureBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if !doc.ForbidHoldSymbols {
		t.Fatal("7.1 must activate forbid_hold_symbols")
	}
	if !doc.RequireNetLOCReduction {
		t.Fatal("7.4 must activate require_net_loc_reduction")
	}
	if !strings.Contains(doc.TargetFlow, "atomic open-exposure insert") ||
		!strings.Contains(doc.TargetFlow, "no billing/exposure mutation") {
		t.Fatalf("target_flow must document the end-state execution isolation:\n%s", doc.TargetFlow)
	}

	findings := ValidateBillingExposurePlannedRatchets(doc)
	if len(findings) > 0 {
		t.Fatalf("planned ratchets invalid on committed lock:\n%s", formatRatchetFindings(findings))
	}

	want := []struct {
		id     string
		status string
		flag   string
		task   string
	}{
		{BillingExposureRatchetStreamMoneyMutation, BillingExposureRatchetStatusActive, "", "already-enforced"},
		{BillingExposureRatchetHoldLifecycle, BillingExposureRatchetStatusActive, BillingExposureActivationForbidHoldSymbols, "7.1"},
		{BillingExposureRatchetALegOnlySettlementIdentity, BillingExposureRatchetStatusActive, "", "already-enforced"},
		{BillingExposureRatchetRuntimeFinancialEvidenceBarrier, BillingExposureRatchetStatusActive, BillingExposureActivationForbidHoldSymbols, "7.1"},
		{BillingExposureRatchetNetLOCReduction, BillingExposureRatchetStatusActive, BillingExposureActivationRequireNetLOCReduction, "7.4"},
	}
	if len(doc.PlannedRatchets) != len(want) {
		t.Fatalf("planned_ratchets count = %d, want %d", len(doc.PlannedRatchets), len(want))
	}
	for i, row := range want {
		got := doc.PlannedRatchets[i]
		if got.ID != row.id || got.Status != row.status || got.ActivationFlag != row.flag || got.ActivationTask != row.task {
			t.Fatalf("planned_ratchets[%d] = id=%q status=%q flag=%q task=%q, want %+v",
				i, got.ID, got.Status, got.ActivationFlag, got.ActivationTask, row)
		}
		if strings.TrimSpace(got.EndState) == "" {
			t.Fatalf("%s: end_state must document the forbid", got.ID)
		}
	}
}

func TestBillingExposurePlannedRatchetsPassAgainstCurrentRepo(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingExposureBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	del, err := EvaluateBillingExposureDeletionRatchet(root, doc)
	if err != nil {
		t.Fatalf("deletion ratchet: %v", err)
	}
	if len(del) > 0 {
		t.Fatalf("planned hold/collector ratchet must pass while symbols remain:\n%s", formatRatchetFindings(del))
	}
	ident, err := EvaluateBillingExposureIdentityRatchet(root, doc)
	if err != nil {
		t.Fatalf("identity ratchet: %v", err)
	}
	if len(ident) > 0 {
		t.Fatalf("planned A-leg settlement identity ratchet must pass while TUR/A-leg keys remain:\n%s", formatRatchetFindings(ident))
	}
	measured, err := MeasureBillingExposureBaseline(root)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	loc := EvaluateBillingExposureLOCRatchet(doc, measured)
	if len(loc) > 0 {
		t.Fatalf("planned LOC lock must match measured total (7.4 requires reduction later):\n%s", formatRatchetFindings(loc))
	}
}

func TestBillingExposureHoldRatchetActivationWouldFailOnCurrentCode(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingExposureBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	doc.ForbidHoldSymbols = true
	del, err := EvaluateBillingExposureDeletionRatchet(root, doc)
	if err != nil {
		t.Fatalf("deletion ratchet: %v", err)
	}
	if len(del) != 0 {
		t.Fatalf("activated hold/collector ratchet must pass after Phase 6.4 deletion: %s", formatRatchetFindings(del))
	}
	ident, err := EvaluateBillingExposureIdentityRatchet(root, doc)
	if err != nil {
		t.Fatalf("identity ratchet: %v", err)
	}
	if len(ident) != 0 {
		t.Fatalf("A-leg-only settlement identity ratchet should already pass after BillingCallID migration: %s", formatRatchetFindings(ident))
	}
}

func TestEvaluateBillingExposureDeletionRatchetPlannedVsActivated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		forbid    bool
		present   bool
		writeSrc  bool
		wantEmpty bool
	}{
		{name: "planned recorded and present", forbid: false, present: true, writeSrc: true, wantEmpty: true},
		{name: "planned missing present flag", forbid: false, present: false, writeSrc: true, wantEmpty: false},
		{name: "planned symbol already gone", forbid: false, present: true, writeSrc: false, wantEmpty: false},
		{name: "activated still present", forbid: true, present: true, writeSrc: true, wantEmpty: false},
		{name: "activated absent", forbid: true, present: false, writeSrc: false, wantEmpty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			rel := "internal/core/billing/authorize.go"
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if tt.writeSrc {
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(abs, []byte("package billing\n\ntype Authorization struct{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			doc := BillingExposureBaselineFile{
				ForbidHoldSymbols: tt.forbid,
				DeletionTargets: []BillingExposureDeletionTarget{{
					ID:      "Authorization",
					Kind:    "ident",
					Files:   []string{rel},
					Marker:  "type Authorization struct",
					Present: tt.present,
					Status:  "record-current-presence",
				}},
			}
			got, err := EvaluateBillingExposureDeletionRatchet(root, doc)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if tt.wantEmpty && len(got) != 0 {
				t.Fatalf("want no findings, got:\n%s", formatRatchetFindings(got))
			}
			if !tt.wantEmpty && len(got) == 0 {
				t.Fatal("want findings, got none")
			}
		})
	}
}

func TestEvaluateBillingExposureIdentityRatchetPlannedVsActivated(t *testing.T) {
	t.Parallel()

	rel := "internal/core/billing/settlement.go"
	alegBody := "package billing\n\n// CustomerSettlementSourceKey is the only customer-revenue source identity for\n// one sealed A-leg.\nfunc CustomerSettlementSourceKey(turKey string) (string, error) { return turKey, nil }\n"
	targetBody := "package billing\n\n// CustomerSettlementSourceKey is unique by account + BillingCallID.\nfunc CustomerSettlementSourceKey(accountID string, callID BillingCallID) (string, error) { return string(callID), nil }\n"

	tests := []struct {
		name      string
		forbid    bool
		body      string
		wantEmpty bool
	}{
		{name: "planned current A-leg key", forbid: false, body: alegBody, wantEmpty: true},
		{name: "planned already migrated", forbid: false, body: targetBody, wantEmpty: false},
		{name: "activated A-leg key remains", forbid: true, body: alegBody, wantEmpty: false},
		{name: "activated account plus BillingCallID", forbid: true, body: targetBody, wantEmpty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			doc := BillingExposureBaselineFile{
				ForbidHoldSymbols: tt.forbid,
				PlannedRatchets: []BillingExposurePlannedRatchet{{
					ID:                     BillingExposureRatchetALegOnlySettlementIdentity,
					Status:                 BillingExposureRatchetStatusPlanned,
					ActivationFlag:         BillingExposureActivationForbidHoldSymbols,
					ActivationTask:         "7.1",
					Files:                  []string{rel},
					CurrentMarkers:         []string{"one sealed A-leg", "CustomerSettlementSourceKey(turKey"},
					ForbiddenWhenActivated: []string{"one sealed A-leg", "CustomerSettlementSourceKey(turKey"},
					RequiredWhenActivated:  []string{"BillingCallID"},
					EndState:               "customer settlement key is account+BillingCallID, not A-leg or session alone",
				}},
			}
			got, err := EvaluateBillingExposureIdentityRatchet(root, doc)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if tt.wantEmpty && len(got) != 0 {
				t.Fatalf("want no findings, got:\n%s", formatRatchetFindings(got))
			}
			if !tt.wantEmpty && len(got) == 0 {
				t.Fatal("want findings, got none")
			}
		})
	}
}

func TestEvaluateBillingExposureLOCRatchetPlannedVsActivated(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		require   bool
		baseline  int
		measured  int
		wantEmpty bool
		wantRule  string
	}{
		{name: "planned lock matches", require: false, baseline: 11261, measured: 11261, wantEmpty: true},
		{name: "planned lock drift", require: false, baseline: 11261, measured: 11262, wantEmpty: false, wantRule: "billing_exposure_loc_lock"},
		{name: "activated net reduction", require: true, baseline: 11261, measured: 10000, wantEmpty: true},
		{name: "activated no reduction", require: true, baseline: 11261, measured: 11261, wantEmpty: false, wantRule: "billing_exposure_loc_reduction"},
		{name: "activated growth", require: true, baseline: 11261, measured: 12000, wantEmpty: false, wantRule: "billing_exposure_loc_reduction"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := BillingExposureBaselineFile{
				RequireNetLOCReduction: tt.require,
				BaselineTotal:          tt.baseline,
			}
			got := EvaluateBillingExposureLOCRatchet(doc, BillingExposureMeasurement{Total: tt.measured})
			if tt.wantEmpty {
				if len(got) != 0 {
					t.Fatalf("want no findings, got:\n%s", formatRatchetFindings(got))
				}
				return
			}
			if len(got) == 0 {
				t.Fatal("want findings, got none")
			}
			if got[0].Rule != tt.wantRule {
				t.Fatalf("rule = %q, want %q", got[0].Rule, tt.wantRule)
			}
		})
	}
}

func TestDecodeBillingExposureBaselineFromTempJSON(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(BillingExposureBaselineFile{
		SchemaVersion:     1,
		ForbidHoldSymbols: true,
		TargetFlow:        "atomic open-exposure insert",
		PlannedRatchets: []BillingExposurePlannedRatchet{{
			ID:             BillingExposureRatchetHoldLifecycle,
			Status:         BillingExposureRatchetStatusPlanned,
			ActivationFlag: BillingExposureActivationForbidHoldSymbols,
			ActivationTask: "7.1",
			EndState:       "no monetary hold lifecycle",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := DecodeBillingExposureBaseline(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !doc.ForbidHoldSymbols {
		t.Fatal("fixture must preserve forbid_hold_symbols=true without flipping the committed lock")
	}
	if len(doc.PlannedRatchets) != 1 || doc.PlannedRatchets[0].ID != BillingExposureRatchetHoldLifecycle {
		t.Fatalf("planned ratchets = %+v", doc.PlannedRatchets)
	}
}

func TestBillingExposureSchemaMarkerScanCatchesProductionOwnership(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pkg := "internal/infra/billingstore"
	mig := filepath.Join(root, filepath.FromSlash(pkg), "20260812000000_billing_baseline.go")
	prod := filepath.Join(root, filepath.FromSlash(pkg), "store.go")
	if err := os.MkdirAll(filepath.Dir(mig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mig, []byte("package billingstore\nconst _ = `authorization_holds`\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prod, []byte("package billingstore\nconst q = `SELECT 1 FROM authorization_holds`\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := BillingExposureDeletionTarget{
		ID:     "authorization_holds",
		Kind:   "schema",
		Files:  []string{pkg + "/20260812000000_billing_baseline.go"},
		Marker: "authorization_holds",
	}
	found, err := BillingExposureDeletionTargetPresent(root, target)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("migration-only schema inventory must still detect production ownership markers")
	}
	target.LegacyRecoveryFiles = []string{pkg + "/store.go"}
	found, err = BillingExposureDeletionTargetPresent(root, target)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("legacy_recovery_files must exempt explicit recovery readers")
	}
}

func formatRatchetFindings(findings []RuleFinding) string {
	var b strings.Builder
	for _, f := range findings {
		b.WriteString(f.String())
		b.WriteByte('\n')
	}
	return b.String()
}
