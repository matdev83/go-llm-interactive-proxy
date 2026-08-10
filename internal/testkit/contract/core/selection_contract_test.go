package core

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/semantic"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type selectionCoreHarness struct{ subject semantic.SubjectDescriptor }

func (h selectionCoreHarness) Subject() semantic.SubjectDescriptor { return h.subject }
func (selectionCoreHarness) Derivation(context.Context) (RequirementDerivationView, error) {
	return selectionDerivation{}, nil
}

type selectionDerivation struct{}

func (selectionDerivation) DeriveRequirements(*lipapi.Call) (lipapi.ProtocolRequirements, error) {
	return lipapi.ProtocolRequirements{}, nil
}
func (selectionDerivation) MatchRequirements(lipapi.ProtocolRequirements, []lipapi.Capability) bool {
	return true
}
func (selectionDerivation) Probe(_ context.Context, scenario semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, Derived: true, ExactMatch: true}, nil
}

func TestCertifyCoreRejectsMissingProofEvidence(t *testing.T) {
	cert, err := CertifyCore(context.Background(), coreNoEvidenceHarness{selectionCoreHarness{subject: semantic.SubjectDescriptor{ID: "core", Kind: semantic.KindCanonicalCore}}})
	if err == nil || cert.Executed {
		t.Fatalf("expected missing core evidence to fail: cert=%+v err=%v", cert, err)
	}
}

type coreNoEvidenceHarness struct{ selectionCoreHarness }

func (coreNoEvidenceHarness) Derivation(context.Context) (RequirementDerivationView, error) {
	return noEvidenceCoreView{}, nil
}

type noEvidenceCoreView struct{}

func (noEvidenceCoreView) DeriveRequirements(*lipapi.Call) (lipapi.ProtocolRequirements, error) {
	return lipapi.ProtocolRequirements{}, nil
}
func (noEvidenceCoreView) MatchRequirements(lipapi.ProtocolRequirements, []lipapi.Capability) bool {
	return true
}
func (noEvidenceCoreView) Probe(context.Context, semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{}, nil
}

func TestCertifyCoreSelectionIncludesKindTransportAndEvidence(t *testing.T) {
	subject := semantic.SubjectDescriptor{ID: "core", Kind: semantic.KindCanonicalCore, Capabilities: nil, Transports: []semantic.ScenarioTransport{semantic.TransportHTTP}}
	cert, err := CertifyCore(context.Background(), selectionCoreHarness{subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	if cert.SubjectKind != semantic.KindCanonicalCore || len(cert.Passed) != 1 || cert.Passed[0] != "text-baseline" {
		t.Fatalf("unexpected positive evidence: %+v", cert)
	}
	if err := cert.ValidateReleaseReady(); err != nil {
		t.Fatalf("incomplete core evidence: %v", err)
	}
}
