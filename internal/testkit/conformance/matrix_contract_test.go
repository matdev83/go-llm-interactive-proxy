package conformance_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/conformance"
)

func TestMatrixContract_EvidenceValidation(t *testing.T) {
	t.Parallel()

	validEv := conformance.FeatureEvidence{
		Outcome:       conformance.OutcomeLossless,
		ScenarioIDs:   []string{"openresponses-json-basic-v1"},
		TestArtifacts: []string{"internal/testkit/conformance/matrix_contract_test.go"},
		Rationale:     "Lossless canonical roundtrip verified",
	}

	if err := validEv.ValidateReleaseReady(); err != nil {
		t.Fatalf("valid feature evidence failed validation: %v", err)
	}

	// Out of scope does not require scenario IDs or artifacts
	outOfScopeEv := conformance.FeatureEvidence{
		Outcome:   conformance.OutcomeOutOfScope,
		Rationale: "Not applicable for text-only adapter",
	}
	if err := outOfScopeEv.ValidateReleaseReady(); err != nil {
		t.Fatalf("out_of_scope evidence failed validation: %v", err)
	}
}

func TestMatrixContract_NoUnclassifiedOrPlanned(t *testing.T) {
	t.Parallel()

	plannedEv := conformance.FeatureEvidence{
		Outcome:       conformance.OutcomePlanned,
		ScenarioIDs:   []string{"openresponses-json-basic-v1"},
		TestArtifacts: []string{"internal/testkit/conformance/matrix_contract_test.go"},
	}

	if err := plannedEv.ValidateReleaseReady(); err == nil {
		t.Fatal("expected error for 'planned' outcome in release-ready evidence, got nil")
	}

	unclassifiedEv := conformance.FeatureEvidence{
		Outcome:       conformance.OutcomeUnclassified,
		ScenarioIDs:   []string{"openresponses-json-basic-v1"},
		TestArtifacts: []string{"internal/testkit/conformance/matrix_contract_test.go"},
	}

	if err := unclassifiedEv.ValidateReleaseReady(); err == nil {
		t.Fatal("expected error for 'unclassified' outcome in release-ready evidence, got nil")
	}

	// Missing scenario IDs for non-out_of_scope
	missingScenariosEv := conformance.FeatureEvidence{
		Outcome:       conformance.OutcomeLossless,
		TestArtifacts: []string{"internal/testkit/conformance/matrix_contract_test.go"},
	}
	if err := missingScenariosEv.ValidateReleaseReady(); err == nil {
		t.Fatal("expected error for missing scenario IDs, got nil")
	}

	// Missing test artifacts for non-out_of_scope
	missingArtifactsEv := conformance.FeatureEvidence{
		Outcome:     conformance.OutcomeLossless,
		ScenarioIDs: []string{"openresponses-json-basic-v1"},
	}
	if err := missingArtifactsEv.ValidateReleaseReady(); err == nil {
		t.Fatal("expected error for missing test artifacts, got nil")
	}
}

func TestMatrixContract_OutOfScopeRequiresRationale(t *testing.T) {
	t.Parallel()

	emptyRationaleEv := conformance.FeatureEvidence{
		Outcome:   conformance.OutcomeOutOfScope,
		Rationale: "   ",
	}

	if err := emptyRationaleEv.ValidateReleaseReady(); err == nil {
		t.Fatal("expected error for empty rationale on out_of_scope outcome, got nil")
	}
}
