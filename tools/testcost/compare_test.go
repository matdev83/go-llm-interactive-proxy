package testcost

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestReportJSONIncludesFalseOverride(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(Report{SchemaVersion: 1, Target: TargetTestUnit})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"overridden":false`) {
		t.Fatalf("report must make unauthorized status explicit: %s", encoded)
	}
}

func validPolicy(overall OverallPolicy, packagePolicy PackagePolicy, overrides map[string]PackagePolicy) Policy {
	return Policy{SchemaVersion: SchemaVersion, AnchorRef: "origin/main", Targets: map[string]TargetPolicy{TargetTestUnit: {CPU: overall.CPU, Processes: overall.Processes, IOOperations: overall.IOOperations, Wall: overall.Wall, Package: packagePolicy, PackageOverrides: overrides}}}
}

func validMeasurement(target string) Measurement {
	return Measurement{SchemaVersion: SchemaVersion, Target: target, Revision: "r", GOOS: "windows", GOARCH: "amd64", GoVersion: "go1.26.6", LogicalCPUs: 4, TestParallel: 4, Process: ProcessMetrics{}, Packages: map[string]PackageMetrics{}}
}

func TestCompareOverallUsesMultiplierOrAnchorDeltaAndProcessDelta(t *testing.T) {
	baseline := validMeasurement(TargetTestUnit)
	baseline.WallNanos = 100
	baseline.Process = ProcessMetrics{TotalCPUNanos: 100, TotalProcesses: 10, ReadOperations: 100, WriteOperations: 0, OtherOperations: 0}
	current := baseline
	current.WallNanos = 131
	current.Process = ProcessMetrics{TotalCPUNanos: 131, TotalProcesses: 13, ReadOperations: 131}
	policy := validPolicy(OverallPolicy{
		CPU:          AbsoluteBudget{Ratio: 1.10, DeltaSeconds: 0.00000003},
		Processes:    ProcessBudget{Ratio: 1.2, Delta: 2},
		IOOperations: CountBudget{Ratio: 1.10, Delta: 30},
		Wall:         AbsoluteBudget{Ratio: 1.10, DeltaSeconds: 0.00000003},
	}, PackagePolicy{}, nil)
	report, err := Compare(baseline, current, policy)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if report.Passed || len(report.Violations) != 4 {
		t.Fatalf("expected CPU/process/io/wall violations, got %#v", report)
	}
	for _, violation := range report.Violations {
		if violation.Package != "" {
			t.Errorf("overall violation package = %q, want empty", violation.Package)
		}
	}
}

func TestComparePackageLifecycleWarningsFailuresFloorAndArchtestOverride(t *testing.T) {
	baseline := validMeasurement(TargetTestUnit)
	baseline.Packages = map[string]PackageMetrics{
		"kept":              {ElapsedNanos: 10_000_000_000},
		"removed":           {ElapsedNanos: 10_000_000_000},
		"internal/archtest": {ElapsedNanos: 10_000_000_000},
		"override-merge":    {ElapsedNanos: 10_000_000_000},
		"small-kept":        {ElapsedNanos: 1_000_000_000},
	}
	current := baseline
	current.Packages = map[string]PackageMetrics{
		"kept":              {ElapsedNanos: 12_000_000_000},
		"new-warning":       {ElapsedNanos: 5_000_000_000},
		"new-failure":       {ElapsedNanos: 9_000_000_000},
		"below-floor":       {ElapsedNanos: 1_000_000_000},
		"internal/archtest": {ElapsedNanos: 15_000_000_000},
		"override-merge":    {ElapsedNanos: 15_000_000_000},
		"small-kept":        {ElapsedNanos: 3_000_000_000},
	}
	policy := validPolicy(OverallPolicy{}, PackagePolicy{ExistingRatio: 1, ExistingDeltaSeconds: 1, ExistingFloorSeconds: 2, NewWarnSeconds: 4, NewFailSeconds: 8}, map[string]PackagePolicy{
		"internal/archtest": {ExistingRatio: 2},
		"override-merge":    {ExistingDeltaSeconds: 3},
	})
	report, err := Compare(baseline, current, policy)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if report.Passed {
		t.Fatal("new-failure and kept package should fail")
	}
	if len(report.Warnings) != 2 {
		t.Fatalf("warnings = %#v", report.Warnings)
	}
	if report.Warnings[0].Package != "new-failure" || report.Warnings[1].Package != "new-warning" {
		t.Fatalf("warnings are not deterministic: %#v", report.Warnings)
	}
	for _, violation := range report.Violations {
		if violation.Package == "removed" || violation.Package == "below-floor" || violation.Package == "internal/archtest" {
			t.Fatalf("unexpected lifecycle/floor/override violation: %#v", violation)
		}
	}
	foundSmall := false
	for _, violation := range report.Violations {
		if violation.Package == "small-kept" {
			foundSmall = true
		}
	}
	if !foundSmall {
		t.Fatal("existing package below floor must still be compared")
	}
	if len(report.TopDeltas) != 4 {
		t.Fatalf("top package deltas = %#v, want kept, archtest, override-merge, and small-kept", report.TopDeltas)
	}
}

func TestCompareTopFifteenPackageElapsedIncreases(t *testing.T) {
	baseline := validMeasurement(TargetTestUnit)
	current := validMeasurement(TargetTestUnit)
	for i := 0; i < 20; i++ {
		name := string(rune('a' + i))
		baseline.Packages[name] = PackageMetrics{ElapsedNanos: 1}
		current.Packages[name] = PackageMetrics{ElapsedNanos: uint64(i + 2)}
	}
	report, err := Compare(baseline, current, validPolicy(OverallPolicy{}, PackagePolicy{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.TopDeltas) != 15 || report.TopDeltas[0].DeltaNanos <= report.TopDeltas[14].DeltaNanos {
		t.Fatalf("top deltas = %#v", report.TopDeltas)
	}
}

func TestCompareAuthorizedOverrideRetainsViolationsAndPasses(t *testing.T) {
	baseline := validMeasurement(TargetTestUnit)
	baseline.WallNanos = 1
	current := baseline
	current.WallNanos = 100
	policy := validPolicy(OverallPolicy{Wall: AbsoluteBudget{Ratio: 1, DeltaSeconds: 0.000000001}}, PackagePolicy{}, nil)
	report, err := CompareWithOptions(baseline, current, policy, CompareOptions{AllowOverride: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || !report.Overridden || len(report.Violations) != 1 {
		t.Fatalf("override report = %#v", report)
	}
}

func TestCompareFailsClosedForMissingTargetAndUnknownSchema(t *testing.T) {
	base := validMeasurement(TargetTestUnit)
	current := base
	policy := validPolicy(OverallPolicy{}, PackagePolicy{}, nil)
	for _, tc := range []struct {
		name    string
		base    Measurement
		current Measurement
		want    error
	}{
		{name: "missing baseline target", base: Measurement{SchemaVersion: SchemaVersion}, current: current, want: ErrMissingTarget},
		{name: "unknown current schema", base: base, current: Measurement{SchemaVersion: 99, Target: TargetTestUnit}, want: ErrUnknownSchema},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compare(tc.base, tc.current, policy)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Compare() error = %v, want %v", err, tc.want)
			}
		})
	}
}
