package featurebundle_test

import (
	"context"
	"reflect"
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

func TestMergedFeatureSurface_carriesAttemptTransformsAndStreamObservers_RED(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(featurebundle.MergedFeatureSurface{})
	for _, name := range []string{"AttemptTransforms", "StreamObserverFactories"} {
		if _, ok := typ.FieldByName(name); !ok {
			t.Fatalf("RED: MergedFeatureSurface must expose field %s (Phase 2.2 merge wiring)", name)
		}
	}

	bundle := lipfeature.FeatureBundle{
		SchemaVersion:           lipfeature.SchemaVersionV1,
		AttemptTransforms:       []request.AttemptTransform{mergeStubAttemptTransform{id: "at"}},
		StreamObserverFactories: []response.StreamObserverFactory{mergeStubStreamObserverFactory{id: "obs"}},
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	merged := featurebundle.MergeBundles(bundle)
	mv := reflect.ValueOf(merged)
	at := mv.FieldByName("AttemptTransforms")
	so := mv.FieldByName("StreamObserverFactories")
	if !at.IsValid() || !so.IsValid() {
		t.Fatal("RED: MergedFeatureSurface fields missing at runtime")
	}
	if at.Len() != 1 || so.Len() != 1 {
		t.Fatalf("RED: MergeBundles must preserve AttemptTransforms/StreamObserverFactories; got at=%d obs=%d", at.Len(), so.Len())
	}
}

func TestSnapshotOptions_carriesAttemptTransformsAndStreamObservers_RED(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(extensions.SnapshotOptions{})
	for _, name := range []string{"AttemptTransforms", "StreamObserverFactories"} {
		if _, ok := typ.FieldByName(name); !ok {
			t.Fatalf("RED: SnapshotOptions must expose field %s (Phase 2.2 snapshot wiring)", name)
		}
	}
}

func TestRequestRuntimeSnapshot_exposesAttemptTransformsAndStreamObservers_RED(t *testing.T) {
	t.Parallel()
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{})
	typ := reflect.TypeOf(snap)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	methods := map[string]bool{}
	for i := 0; i < typ.NumMethod(); i++ {
		methods[typ.Method(i).Name] = true
	}
	ptrMethods := map[string]bool{}
	ptrType := reflect.TypeOf(snap)
	for i := 0; i < ptrType.NumMethod(); i++ {
		ptrMethods[ptrType.Method(i).Name] = true
	}
	for _, name := range []string{"AttemptTransforms", "StreamObserverFactories"} {
		if !methods[name] && !ptrMethods[name] {
			if _, ok := typ.FieldByName(name); !ok {
				t.Fatalf("RED: RequestRuntimeSnapshot must expose accessor or field %s (Phase 2.2)", name)
			}
		}
	}
}
