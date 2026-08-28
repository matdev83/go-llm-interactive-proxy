package feature_test

import (
	"context"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

type stubStreamObserverFactory struct {
	id  string
	ord int
}

func (s stubStreamObserverFactory) ID() string                        { return s.id }
func (s stubStreamObserverFactory) Order() int                        { return s.ord }
func (s stubStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return stubStreamObserver{}, nil
}

type stubStreamObserver struct{}

func (stubStreamObserver) Observe(context.Context, lipapi.Event) error          { return nil }
func (stubStreamObserver) Finish(context.Context, response.StreamOutcome) error { return nil }

func TestFeatureBundle_StreamObserverFactories_requiresSchemaV1(t *testing.T) {
	t.Parallel()
	only := feature.FeatureBundle{
		StreamObserverFactories: []response.StreamObserverFactory{stubStreamObserverFactory{id: "obs", ord: 0}},
	}
	if err := only.Validate(); err == nil {
		t.Fatal("non-empty StreamObserverFactories with schema 0 must fail Validate")
	}
	ok := feature.FeatureBundle{
		SchemaVersion:           feature.SchemaVersionV1,
		StreamObserverFactories: []response.StreamObserverFactory{stubStreamObserverFactory{id: "obs", ord: 0}},
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureBundle_StreamObserverFactories_rejectsNilEntry(t *testing.T) {
	t.Parallel()
	b := feature.FeatureBundle{
		SchemaVersion:           feature.SchemaVersionV1,
		StreamObserverFactories: []response.StreamObserverFactory{nil},
	}
	if err := b.Validate(); err == nil {
		t.Fatal("nil StreamObserverFactories entry must fail Validate")
	}
}

func TestFeatureBundle_StreamObserverFactories_omittedRemainsNoOp(t *testing.T) {
	t.Parallel()
	var b feature.FeatureBundle
	if b.StreamObserverFactories != nil {
		t.Fatal("omitted StreamObserverFactories must be nil on zero value")
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	v1Empty := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1}
	if err := v1Empty.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLegalPipeline_finalStreamObservationBetweenCompletionGatingAndTraffic(t *testing.T) {
	t.Parallel()
	gateIdx := feature.LegalStageDescriptorIndex(feature.StageIDCompletionGating)
	obsIdx := feature.LegalStageDescriptorIndex(feature.StageIDFinalStreamObservation)
	trafficIdx := feature.LegalStageDescriptorIndex(feature.StageIDTrafficObservation)
	egressIdx := feature.LegalStageDescriptorIndex(feature.StageIDEgressEncoding)
	if gateIdx < 0 || obsIdx < 0 || trafficIdx < 0 || egressIdx < 0 {
		t.Fatalf("missing stages gate=%d obs=%d traffic=%d egress=%d", gateIdx, obsIdx, trafficIdx, egressIdx)
	}
	if gateIdx >= obsIdx || obsIdx >= trafficIdx || trafficIdx >= egressIdx {
		t.Fatalf("want completion_gating(%d) < final_stream_observation(%d) < traffic_observation(%d) < egress(%d)",
			gateIdx, obsIdx, trafficIdx, egressIdx)
	}
	desc, ok := feature.StageDescriptorByID(feature.StageIDFinalStreamObservation)
	if !ok || desc.MutationRole != feature.StageRoleObserve {
		t.Fatalf("final_stream_observation descriptor: ok=%v role=%v", ok, desc.MutationRole)
	}
	life, ok := feature.StageDescriptorByID(feature.StageIDAttemptLifecycle)
	if !ok || life.MutationRole != feature.StageRoleObserve {
		t.Fatalf("attempt_lifecycle must remain observe-only: ok=%v role=%v", ok, life.MutationRole)
	}
}

func TestFreezeRequestPlanes_panicsOnNilStreamObserverFactory(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("want panic on nil StreamObserverFactory")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "StreamObserverFactories contains nil entry") {
			t.Fatalf("panic=%v", r)
		}
	}()
	frozen := feature.NewMalformedGeneratedFrozenStreamObserversCandidateForTest([]response.StreamObserverFactory{nil})
	_ = feature.FreezeRequestPlanes(frozen)
}
