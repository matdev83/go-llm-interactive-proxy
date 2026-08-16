package archtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Final certification: the pre-spec surface lock is measured against the
// activated deletion and net-shrinkage ratchets.

func TestBillingExposureBaselineFileExists(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, BillingExposureBaselineRelPath)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("billing exposure deletion baseline missing: %v", err)
	}
}

func TestBillingExposureBaselineInventoryLocked(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingExposureBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if doc.SchemaVersion != BillingExposureBaselineSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", doc.SchemaVersion, BillingExposureBaselineSchemaVersion)
	}
	if doc.Feature != "decoupled-exposure-and-post-usage-billing" {
		t.Fatalf("feature = %q", doc.Feature)
	}
	if doc.Phase != "0.1-brownfield-baseline" {
		t.Fatalf("phase = %q, want 0.1-brownfield-baseline", doc.Phase)
	}
	if !doc.RequireNetLOCReduction {
		t.Fatal("Phase 7.4 must activate require_net_loc_reduction")
	}
	if !doc.ForbidHoldSymbols {
		t.Fatal("Phase 7.1 must activate the hold-symbol deletion guard")
	}
	if doc.AuthoritativeFlow != "current" {
		t.Fatalf("authoritative_flow = %q, want current (target flow is documented, not cut over)", doc.AuthoritativeFlow)
	}
	if !strings.Contains(doc.CurrentFlow, "authorization hold") || !strings.Contains(doc.CurrentFlow, "reserved_nano") {
		t.Fatalf("current_flow must describe hold + reserved_nano admission:\n%s", doc.CurrentFlow)
	}
	if !strings.Contains(doc.TargetFlow, "atomic open-exposure insert") {
		t.Fatalf("target_flow must document the design Final Invariant as TARGET:\n%s", doc.TargetFlow)
	}

	wantIDs := []string{
		"core-billing",
		"billingstore",
		"billingcompose",
		"billingadmission",
		"runtime-billing",
		"runtimebundle-billing",
	}
	if len(doc.Surfaces) != len(wantIDs) {
		t.Fatalf("surface count = %d, want %d", len(doc.Surfaces), len(wantIDs))
	}
	sum := 0
	for i, surface := range doc.Surfaces {
		if surface.ID != wantIDs[i] {
			t.Fatalf("surface[%d].id = %q, want %q", i, surface.ID, wantIDs[i])
		}
		if surface.BaselineLines <= 0 {
			t.Fatalf("surface %s baseline_lines must be a positive measured lock", surface.ID)
		}
		sum += surface.BaselineLines
	}
	if doc.BaselineTotal != sum {
		t.Fatalf("baseline_total = %d, want sum of surfaces %d", doc.BaselineTotal, sum)
	}
}

// BillingExposureHonestBaselineSHA is the immutable pre-spec freeze commit.
// Req 17.11 locks against these values; the baseline must not be re-anchored upward.
const BillingExposureHonestBaselineSHA = "eef803a1312099070a56a68ff56ebe6765f9137b"

func TestBillingExposureBaselinePinnedToHonestFreeze(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingExposureBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	wantLines := []int{4123, 4579, 609, 243, 1057, 175}
	if doc.BaselineTotal != 10786 {
		t.Fatalf("baseline_total = %d, want immutable freeze 10786 from %s", doc.BaselineTotal, BillingExposureHonestBaselineSHA)
	}
	if len(doc.Surfaces) != len(wantLines) {
		t.Fatalf("surface count = %d, want %d", len(doc.Surfaces), len(wantLines))
	}
	for i, want := range wantLines {
		if doc.Surfaces[i].BaselineLines != want {
			t.Fatalf("surface %s baseline_lines = %d, want immutable freeze %d from %s",
				doc.Surfaces[i].ID, doc.Surfaces[i].BaselineLines, want, BillingExposureHonestBaselineSHA)
		}
	}
	if !doc.RequireNetLOCReduction || !doc.ForbidHoldSymbols {
		t.Fatal("honest freeze requires require_net_loc_reduction and forbid_hold_symbols")
	}
}

func TestBillingExposureBaselineMatchesMeasuredProductionLOC(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingExposureBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	got, err := MeasureBillingExposureBaseline(root)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if len(got.Surfaces) != len(doc.Surfaces) {
		t.Fatalf("measured surfaces = %d, want %d", len(got.Surfaces), len(doc.Surfaces))
	}
	var mismatches []string
	for i, want := range doc.Surfaces {
		measured := got.Surfaces[i]
		if measured.Path != want.Path || measured.Kind != want.Kind || measured.Match != want.Match || measured.ID != want.ID {
			t.Fatalf("surface %s identity drifted: got %+v want %+v", want.ID, measured, want)
		}
		if !doc.RequireNetLOCReduction && measured.CurrentLines != want.BaselineLines {
			mismatches = append(mismatches, fmt.Sprintf("%s: measured %d lock %d", want.ID, measured.CurrentLines, want.BaselineLines))
		}
	}
	if doc.RequireNetLOCReduction {
		if got.Total >= doc.BaselineTotal {
			mismatches = append(mismatches, fmt.Sprintf("total: measured %d is not below pre-spec baseline %d", got.Total, doc.BaselineTotal))
		}
	} else if got.Total != doc.BaselineTotal {
		mismatches = append(mismatches, fmt.Sprintf("total: measured %d lock %d", got.Total, doc.BaselineTotal))
	}
	if len(mismatches) > 0 {
		t.Fatalf("billing exposure LOC drifted; update %s only with an intentional 0.1 ratchet:\n%s",
			BillingExposureBaselineRelPath, strings.Join(mismatches, "\n"))
	}
}

func TestBillingExposureDeletionTargetsCurrentlyExist(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	doc, err := LoadBillingExposureBaseline(root)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if len(doc.DeletionTargets) == 0 {
		t.Fatal("deletion manifest must record brownfield hold/collector targets")
	}
	wantIDs := []string{
		"Authorization",
		"AuthorizationStore",
		"AuthorizationLookup",
		"HoldReleaser",
		"BillingAdmissionCleanup",
		"authorization_holds",
		"reserved_nano",
		"JournalBookLegacyAuthorization",
		"hold_expiry",
		"hold_remainder",
		"hold_release",
		"evidenceByALeg",
		"billing_parallel_barrier",
		"tur_rebuild_from_remembered_legs",
		"provider_cost_complete_prerequisite",
	}
	if len(doc.DeletionTargets) != len(wantIDs) {
		t.Fatalf("deletion target count = %d, want %d", len(doc.DeletionTargets), len(wantIDs))
	}
	for i, want := range wantIDs {
		got := doc.DeletionTargets[i]
		if got.ID != want {
			t.Fatalf("deletion_targets[%d].id = %q, want %q", i, got.ID, want)
		}
		found, err := BillingExposureDeletionTargetPresent(root, got)
		if err != nil {
			t.Fatalf("%s: %v", got.ID, err)
		}
		if got.Present && !found {
			t.Fatalf("%s: recorded as present but not found in production source; update the manifest only with an intentional deletion", got.ID)
		}
		if !got.Present && found {
			t.Fatalf("%s: recorded as deleted but still found in production source", got.ID)
		}
	}
}

func TestBillingExposureBaselineJSONRoundTripStable(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, BillingExposureBaselineRelPath))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	var doc BillingExposureBaselineFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d", doc.SchemaVersion)
	}
}

func TestBillingExposureMeasurementSkipsWorktreesVendorAndTests(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	production := filepath.Join(root, "internal", "core", "billing", "account.go")
	testFile := filepath.Join(root, "internal", "core", "billing", "account_test.go")
	sibling := filepath.Join(root, "internal", "core", "billing", ".worktrees", "other", "account.go")
	vendor := filepath.Join(root, "internal", "core", "billing", "vendor", "copy.go")
	testdata := filepath.Join(root, "internal", "core", "billing", "testdata", "fixture.go")
	for _, path := range []string{production, testFile, sibling, vendor, testdata} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package billing\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n, err := CountBillingExposurePackageLines(root, "internal/core/billing")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("package lines = %d, want 1 production file (skip tests, testdata, vendor, .worktrees)", n)
	}
}
