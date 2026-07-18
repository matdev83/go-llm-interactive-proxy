package snapshotgen_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/snapshotgen"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/authority"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/economics"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/runtimegen"
)

func TestPhase55_MetadataPublishIsCompatibilityNotEnforcement(t *testing.T) {
	t.Parallel()
	pub := snapshotgen.NewPublisher()
	meta := pub.Publish(snapshotgen.RuntimeGeneration{
		State: economics.SnapshotReady,
		Usage: economics.Snapshot[economics.PolicyRulesView]{
			ID: "usage_authority", Version: "meta-label-v1", State: economics.SnapshotReady,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
		},
	})
	if meta == nil || meta.Usage.Version != "meta-label-v1" {
		t.Fatalf("compatibility metadata view missing: %#v", meta)
	}
	if pub.CurrentExecutable() != nil {
		t.Fatal("metadata Publish must not install executable enforcement generation")
	}
	contrib := runtimegen.GenerationContribution{
		SourceID: "static-config",
		Version:  "exec-v1",
		State:    economics.SnapshotReady,
		OperatorRaters: []economics.RaterRegistration{{
			ID: "eval-obj-1", Perspective: metering.PerspectiveOperator, Rater: stubRater{id: "eval-obj-1"},
		}},
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: stubRequestProvider{id: "req-a"}.Describe(),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   stubRequestProvider{id: "req-a"},
		}},
	}
	exec, err := snapshotgen.CompileExecutable(contrib)
	if err != nil {
		t.Fatal(err)
	}
	published, err := pub.PublishExecutable(exec)
	if err != nil {
		t.Fatal(err)
	}
	if published.EvidenceObjectID() != "eval-obj-1" {
		t.Fatalf("evidence=%q want evaluator object", published.EvidenceObjectID())
	}
	if published.EvidenceObjectID() == meta.Usage.Version {
		t.Fatal("evidence must not be metadata-only usage version label")
	}
	if pub.Current() == nil || pub.Current().Usage.Version != "meta-label-v1" {
		t.Fatal("additive metadata compatibility view must remain after executable publish")
	}
}
