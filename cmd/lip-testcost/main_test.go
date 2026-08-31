package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/tools/testcost"
)

func TestRunCompareWritesStableJSONAndSummary(t *testing.T) {
	dir := t.TempDir()
	baseline := testcost.Measurement{SchemaVersion: 1, Target: "test-unit", Revision: "a", GOOS: "windows", GOARCH: "amd64", GoVersion: "go1.26.6", LogicalCPUs: 1, TestParallel: 1, WallNanos: 100, Packages: map[string]testcost.PackageMetrics{"pkg": {ElapsedNanos: 1}}}
	current := baseline
	current.Revision = "b"
	current.WallNanos = 200
	policy := testcost.Policy{SchemaVersion: 1, AnchorRef: "origin/main", Targets: map[string]testcost.TargetPolicy{"test-unit": {Wall: testcost.AbsoluteBudget{Ratio: 1, DeltaSeconds: 0.000000001}}}}
	basePath := filepath.Join(dir, "baseline.json")
	currPath := filepath.Join(dir, "current.json")
	policyPath := filepath.Join(dir, "policy.json")
	outPath := filepath.Join(dir, "report.json")
	writeFixtureJSON(t, basePath, baseline)
	writeFixtureJSON(t, currPath, current)
	policyBytes, err := testcost.EncodePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"compare", "--baseline", basePath, "--current", currPath, "--policy", policyPath, "--out", outPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run() exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "target=test-unit") || !strings.Contains(stderr.String(), "violations=1") {
		t.Fatalf("unstable/incomplete summary: %s", stderr.String())
	}
	for _, needle := range []string{"CPU anchor=", "processes anchor=", "I/O ops anchor=", "wall anchor=", "diagnostics active_anchor=", "page_faults_anchor=", "read_bytes_anchor=", "package | anchor | head | delta"} {
		if !strings.Contains(stderr.String(), needle) {
			t.Fatalf("summary missing %q: %s", needle, stderr.String())
		}
	}
	if data, err := os.ReadFile(outPath); err != nil || !bytes.Contains(data, []byte(`"schema_version": 1`)) {
		t.Fatalf("report output = %s, err=%v", data, err)
	}
}

func TestRunCompareAuthorizedOverrideSucceeds(t *testing.T) {
	dir := t.TempDir()
	base := testcost.Measurement{SchemaVersion: 1, Target: "test-unit", GOOS: "windows", GOARCH: "amd64", Packages: map[string]testcost.PackageMetrics{}}
	current := base
	base.WallNanos, current.WallNanos = 1, 100
	policy := testcost.Policy{SchemaVersion: 1, AnchorRef: "origin/main", Targets: map[string]testcost.TargetPolicy{"test-unit": {Wall: testcost.AbsoluteBudget{Ratio: 1, DeltaSeconds: 0.000000001}}}}
	basePath := filepath.Join(dir, "baseline.json")
	currPath := filepath.Join(dir, "current.json")
	policyPath := filepath.Join(dir, "policy.json")
	writeFixtureJSON(t, basePath, base)
	writeFixtureJSON(t, currPath, current)
	policyBytes, err := testcost.EncodePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"compare", "--baseline", basePath, "--current", currPath, "--policy", policyPath, "--allow-override"}, &stdout, &stderr); code != 0 {
		t.Fatalf("authorized run() exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "overridden=true") {
		t.Fatalf("override summary = %s", stderr.String())
	}
}

func TestRunRejectsIncompleteCompareInputs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"compare", "--baseline", "missing.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("incomplete compare exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "baseline, current, and policy are required") {
		t.Fatalf("incomplete compare error = %s", stderr.String())
	}
}

func TestRunCompareRejectsEmptyOut(t *testing.T) {
	dir := t.TempDir()
	base := testcost.Measurement{SchemaVersion: 1, Target: "test-unit", GOOS: "windows", GOARCH: "amd64", Packages: map[string]testcost.PackageMetrics{}}
	current := base
	policy := testcost.Policy{SchemaVersion: 1, AnchorRef: "origin/main", Targets: map[string]testcost.TargetPolicy{"test-unit": {Wall: testcost.AbsoluteBudget{Ratio: 1, DeltaSeconds: 1}}}}
	basePath := filepath.Join(dir, "baseline.json")
	currPath := filepath.Join(dir, "current.json")
	policyPath := filepath.Join(dir, "policy.json")
	writeFixtureJSON(t, basePath, base)
	writeFixtureJSON(t, currPath, current)
	policyBytes, err := testcost.EncodePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"compare", "--baseline", basePath, "--current", currPath, "--policy", policyPath, "--out", ""}, &stdout, &stderr); code != 2 {
		t.Fatalf("empty out exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--out cannot be empty") {
		t.Fatalf("empty out error = %s", stderr.String())
	}
}

func TestRunRequiresLiteralMeasureOrCompareSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--target", "test-unit"}, &stdout, &stderr); code != 2 {
		t.Fatalf("missing subcommand exit = %d, stderr=%s", code, stderr.String())
	}
}

func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
