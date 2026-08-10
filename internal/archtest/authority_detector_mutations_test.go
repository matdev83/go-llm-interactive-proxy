package archtest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContributionAndDiagnosticsAuthorityMutationFixtures(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"internal/standardplugins/contrib.go": `package standardplugins
var ContosoBackendContribution = []string{"contoso"}
var PoolRegistry = []string{"pool"}`,
		"internal/core/diag/central.go": `package diag
type ContosoDiagnosticRow struct { ID string }
type NeutralDiagnostic struct { ID string }`,
		"internal/infra/runtimebundle/config.go": `package runtimebundle
var DefaultModel = "contoso"`,
	}
	for name, source := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	regs, err := DetectDuplicateAuthoritativeRegistries(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := regs["ContosoBackendContribution"]; !ok {
		t.Fatalf("arbitrary contribution authority not found: %+v", regs)
	}
	if _, ok := regs["PoolRegistry"]; ok {
		t.Fatalf("runtime PoolRegistry incorrectly treated as contribution authority: %+v", regs)
	}
	debt, err := DetectCentralProtocolDiagnosticsDebt(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := debt["ContosoDiagnosticRow"]; !ok {
		t.Fatalf("arbitrary central diagnostic DTO not found: %+v", debt)
	}
	if _, ok := debt["DefaultModel"]; ok {
		t.Fatalf("config/default model incorrectly treated as diagnostics debt: %+v", debt)
	}
}

func TestAuthorityDetectors_IgnoreOwnedProjectorsAndState(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"internal/standardplugins/owned.go": `package standardplugins
func ProjectContosoRows() []string { return []string{"x"} }`,
		"internal/infra/runtimebundle/state.go": `package runtimebundle
var RouteRegistry = map[string]string{"x":"y"}
var HealthState = map[string]string{"x":"y"}`,
	}
	for name, source := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	regs, err := DetectDuplicateAuthoritativeRegistries(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 0 {
		t.Fatalf("owned projector/state leaked into authority findings: %+v", regs)
	}
}
