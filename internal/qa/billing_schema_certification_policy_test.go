package qa

import (
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestQAFastPreflight_BillingSchemaCertification(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(repoRoot(t), "internal", "core", "billing")
	for _, name := range []string{"billing_schema_generated_test.go", "billing_schema_sweeps_integration_test.go", "billing_schema_stress_integration_test.go"} {
		plain := build.Default
		plain.BuildTags = nil
		match, err := plain.MatchFile(dir, name)
		if err != nil || match {
			t.Fatalf("%s must be outside the default unit tier: match=%v err=%v", name, match, err)
		}
		certification := plain
		certification.BuildTags = []string{"integration"}
		match, err = certification.MatchFile(dir, name)
		if err != nil || !match {
			t.Fatalf("%s must be in integration certification: match=%v err=%v", name, match, err)
		}
	}
	// Keep named regressions and the bounded metamorphic suite available to the
	// default build. Only exhaustive products move to certification.
	for _, name := range []string{"billing_schema_model_test.go", "billing_schema_metamorphic_test.go"} {
		plain := build.Default
		plain.BuildTags = nil
		match, err := plain.MatchFile(dir, name)
		if err != nil || !match {
			t.Fatalf("%s must retain default regression coverage: match=%v err=%v", name, match, err)
		}
	}
	want := []string{
		"TestGeneratedSchemaCommercialSweep", "TestGeneratedSchemaDirectionUnitIsolation",
		"TestGeneratedSchemaOrderInvariance", "TestGeneratedSchemaStructureSweep",
		"TestGeneratedSchemaTransformIsNotContainment", "TestSchemaModelCommercialSweep",
		"TestSchemaModelStructureSweep",
		"TestMetamorphicPricingMetamorphism", "TestMetamorphicStructuralVerdictAgreesWithModel",
		"TestSchemaModelOrderInvariance", "TestSupportAgreementShadowPredicate",
		"TestReplayDeepestPublishableChainTraversalIsBounded",
	}
	slices.Sort(want)
	var found []string
	for _, name := range []string{"billing_schema_generated_test.go", "billing_schema_sweeps_integration_test.go", "billing_schema_stress_integration_test.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for name := range file.Scope.Objects {
			if strings.HasPrefix(name, "Test") {
				found = append(found, name)
			}
		}
	}
	slices.Sort(found)
	if !slices.Equal(found, want) {
		t.Fatalf("certification population changed: got %v want %v", found, want)
	}
	var workflow ciWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", "ci.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(parseCINeeds(workflow.Jobs["repo-hygiene"].Needs), "billing-schema") {
		t.Fatal("required Repo hygiene must wait for billing certification")
	}
	var propagate, run, discover bool
	for _, step := range workflow.Jobs["repo-hygiene"].Steps {
		propagate = propagate || (step.Env["BILLING_RESULT"] == "${{ needs.billing-schema.result }}" && strings.Contains(step.Run, `test "$BILLING_RESULT" = success`))
	}
	job := workflow.Jobs["billing-schema"]
	if job.If != "always() && !cancelled()" || !slices.Contains(parseCINeeds(job.Needs), "changes") {
		t.Fatal("certification must report failure when scope detection fails and honor cancellation")
	}
	for _, step := range job.Steps {
		run = run || strings.Contains(step.Run, "make test-billing-schema")
		discover = discover || (strings.Contains(step.Run, "-tags=integration") && strings.Contains(step.Run, "-list") && strings.Contains(step.Run, "-eq 12"))
	}
	if !propagate || !run || !discover {
		t.Fatal("certification must discover twelve certification tests, execute them, and fail the required status on failure")
	}
	makefile := readRepositoryFile(t, "Makefile")
	if !strings.Contains(makefile, "test-billing-schema:") || !strings.Contains(makefile, "-tags=integration -run '^(TestGeneratedSchema|TestSchemaModel(Structure|Commercial)Sweep|TestSchemaModelOrderInvariance|TestMetamorphic(PricingMetamorphism|StructuralVerdictAgreesWithModel)|TestSupportAgreementShadowPredicate|TestReplayDeepestPublishableChainTraversalIsBounded)") {
		t.Fatal("explicit certification target must execute the complete sweep selector")
	}
}
