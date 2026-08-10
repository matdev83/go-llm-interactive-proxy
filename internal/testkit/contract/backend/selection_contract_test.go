package backend

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/contract/semantic"
)

type selectionBackendHarness struct{ subject semantic.SubjectDescriptor }

func (h selectionBackendHarness) Subject() semantic.SubjectDescriptor { return h.subject }
func (selectionBackendHarness) Backend(context.Context) (BackendView, error) {
	return selectionBackendView{}, nil
}
func (selectionBackendHarness) Upstream() UpstreamProbe     { return &selectionProbe{} }
func (selectionBackendHarness) Reset(context.Context) error { return nil }

type selectionBackendView struct{}

func (selectionBackendView) Open(context.Context, *lipapi.Call) (lipapi.EventStream, error) {
	return lipapi.NewFixedEventStream(nil), nil
}
func (selectionBackendView) EffectiveCapabilities(context.Context, *lipapi.Call) BackendFacts {
	return BackendFacts{}
}
func (selectionBackendView) Reject(context.Context, *lipapi.Call, semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{Executed: true, Rejected: true, BoundaryCalls: 1}, nil
}
func (selectionBackendView) Probe(_ context.Context, scenario semantic.ScenarioDescriptor, probe UpstreamProbe) (semantic.ExecutionEvidence, error) {
	probe.(*selectionProbe).count++
	return semantic.ExecutionEvidence{ScenarioID: scenario.ID, Executed: true, BoundaryCalls: 1, UpstreamCalls: 1, Opened: true, EffectiveCapabilities: true, StreamValidated: true, UsagePresent: true, LifecycleClosed: true}, nil
}

type selectionProbe struct{ count int }

func (p *selectionProbe) RequestCount() int          { return p.count }
func (*selectionProbe) LastRequest() CapturedRequest { return CapturedRequest{} }
func (*selectionProbe) Reset()                       {}

func TestCertifyBackendRejectsMissingUpstreamEvidence(t *testing.T) {
	cert, err := CertifyBackend(context.Background(), backendNoEvidenceHarness{selectionBackendHarness{subject: semantic.SubjectDescriptor{ID: "be", Kind: semantic.KindBackendFamily}}})
	if err == nil || cert.Executed {
		t.Fatalf("expected missing backend evidence to fail: cert=%+v err=%v", cert, err)
	}
}

type backendNoEvidenceHarness struct{ selectionBackendHarness }

func (backendNoEvidenceHarness) Backend(context.Context) (BackendView, error) {
	return noEvidenceBackendView{}, nil
}

type noEvidenceBackendView struct{}

func (noEvidenceBackendView) Open(context.Context, *lipapi.Call) (lipapi.EventStream, error) {
	return lipapi.NewFixedEventStream(nil), nil
}
func (noEvidenceBackendView) EffectiveCapabilities(context.Context, *lipapi.Call) BackendFacts {
	return BackendFacts{}
}
func (noEvidenceBackendView) Reject(context.Context, *lipapi.Call, semantic.ScenarioDescriptor) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{}, nil
}
func (noEvidenceBackendView) Probe(context.Context, semantic.ScenarioDescriptor, UpstreamProbe) (semantic.ExecutionEvidence, error) {
	return semantic.ExecutionEvidence{}, nil
}

func TestCertifyBackendSelectionIncludesKindTransportAndEvidence(t *testing.T) {
	subject := semantic.SubjectDescriptor{ID: "be", Kind: semantic.KindBackendFamily, Capabilities: nil, Transports: []semantic.ScenarioTransport{semantic.TransportHTTP, semantic.TransportStreaming}}
	cert, err := CertifyBackend(context.Background(), selectionBackendHarness{subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	if cert.SubjectKind != semantic.KindBackendFamily || len(cert.Passed) != 1 || cert.Passed[0] != "text-baseline" {
		t.Fatalf("unexpected positive evidence: %+v", cert)
	}
	if err := cert.ValidateReleaseReady(); err != nil {
		t.Fatalf("incomplete backend evidence: %v", err)
	}
}
