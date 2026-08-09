package featurebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	sdkhooks "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/response"
)

type mergeStubAttemptTransform struct{ id string }

func (s mergeStubAttemptTransform) ID() string                        { return s.id }
func (s mergeStubAttemptTransform) Order() int                        { return 0 }
func (s mergeStubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (s mergeStubAttemptTransform) HandleAttempt(context.Context, *lipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type mergeStubStreamObserverFactory struct{ id string }

func (s mergeStubStreamObserverFactory) ID() string                        { return s.id }
func (s mergeStubStreamObserverFactory) Order() int                        { return 0 }
func (s mergeStubStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (s mergeStubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

func TestMergedFeatureSurface_carriesAttemptTransformsAndStreamObservers(t *testing.T) {
	t.Parallel()
	bundle := lipfeature.FeatureBundle{
		SchemaVersion:           lipfeature.SchemaVersionV1,
		AttemptTransforms:       []request.AttemptTransform{mergeStubAttemptTransform{id: "at"}},
		StreamObserverFactories: []response.StreamObserverFactory{mergeStubStreamObserverFactory{id: "obs"}},
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	merged := featurebundle.MergeBundles(bundle)
	if len(merged.AttemptTransforms) != 1 || merged.AttemptTransforms[0].ID() != "at" {
		t.Fatalf("AttemptTransforms=%v", merged.AttemptTransforms)
	}
	if len(merged.StreamObserverFactories) != 1 || merged.StreamObserverFactories[0].ID() != "obs" {
		t.Fatalf("StreamObserverFactories=%v", merged.StreamObserverFactories)
	}
}

func TestSnapshotOptions_carriesAttemptTransformsAndStreamObservers(t *testing.T) {
	t.Parallel()
	opts := extensions.SnapshotOptions{
		AttemptTransforms:       []request.AttemptTransform{mergeStubAttemptTransform{id: "at"}},
		StreamObserverFactories: []response.StreamObserverFactory{mergeStubStreamObserverFactory{id: "obs"}},
	}
	if len(opts.AttemptTransforms) != 1 || len(opts.StreamObserverFactories) != 1 {
		t.Fatalf("SnapshotOptions fields missing: %+v", opts)
	}
}

func TestRequestRuntimeSnapshot_exposesAttemptTransformsAndStreamObservers(t *testing.T) {
	t.Parallel()
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		AttemptTransforms:       []request.AttemptTransform{mergeStubAttemptTransform{id: "at"}},
		StreamObserverFactories: []response.StreamObserverFactory{mergeStubStreamObserverFactory{id: "obs"}},
	})
	gotAT := snap.AttemptTransforms()
	gotSO := snap.StreamObserverFactories()
	if len(gotAT) != 1 || gotAT[0].ID() != "at" {
		t.Fatalf("AttemptTransforms()=%v", gotAT)
	}
	if len(gotSO) != 1 || gotSO[0].ID() != "obs" {
		t.Fatalf("StreamObserverFactories()=%v", gotSO)
	}
	gotAT[0] = nil
	gotSO[0] = nil
	if len(snap.AttemptTransforms()) != 1 || snap.AttemptTransforms()[0] == nil {
		t.Fatal("AttemptTransforms must return a defensive copy")
	}
	if len(snap.StreamObserverFactories()) != 1 || snap.StreamObserverFactories()[0] == nil {
		t.Fatal("StreamObserverFactories must return a defensive copy")
	}
}
