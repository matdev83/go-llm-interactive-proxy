package featurebundle_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/compaction"
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

func TestGeneratedMergeSurface_carriesAttemptTransforms(t *testing.T) {
	t.Parallel()
	bundle := lipfeature.FeatureBundle{
		SchemaVersion:     lipfeature.SchemaVersionV1,
		AttemptTransforms: []request.AttemptTransform{mergeStubAttemptTransform{id: "at"}},
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeBundlesGenerated(bundle)
	if err != nil {
		t.Fatal(err)
	}
	at := lipfeature.Get(gen.Frozen, lipfeature.PlaneAttemptTransforms)
	if len(at) != 1 || at[0].ID() != "at" {
		t.Fatalf("AttemptTransforms=%v", at)
	}
}

func TestGeneratedMergeSurface_carriesStreamObservers(t *testing.T) {
	t.Parallel()
	bundle := lipfeature.FeatureBundle{
		SchemaVersion:           lipfeature.SchemaVersionV1,
		StreamObserverFactories: []response.StreamObserverFactory{mergeStubStreamObserverFactory{id: "obs"}},
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeBundlesGenerated(bundle)
	if err != nil {
		t.Fatal(err)
	}
	so := lipfeature.Get(gen.Frozen, lipfeature.PlaneStreamObserverFactories)
	if len(so) != 1 || so[0].ID() != "obs" {
		t.Fatalf("StreamObserverFactories=%v", so)
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

type mergeStubCompactionObserver struct{ id string }

func (s mergeStubCompactionObserver) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

type mergeStubCompactionPreserver struct{ id string }

func (s mergeStubCompactionPreserver) ID() string { return s.id }

func (mergeStubCompactionPreserver) BeforeRequest(context.Context, *lipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (mergeStubCompactionPreserver) RequestOpened(context.Context, lipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (mergeStubCompactionPreserver) BeforeResponseRelease(context.Context, *lipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

// TestCompactionObservers_portWiring proves the compaction observer surface is
// additive through the single merge point, the snapshot options, and the frozen
// snapshot accessor with defensive-copy semantics (requirements 2.1-2.2).
func TestCompactionObservers_portWiring(t *testing.T) {
	t.Parallel()
	bundle := lipfeature.FeatureBundle{
		SchemaVersion:       lipfeature.SchemaVersionV1,
		CompactionObservers: []compaction.Observer{mergeStubCompactionObserver{id: "a"}, mergeStubCompactionObserver{id: "z"}},
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeBundlesGenerated(bundle, lipfeature.FeatureBundle{
		SchemaVersion:       lipfeature.SchemaVersionV1,
		CompactionObservers: []compaction.Observer{mergeStubCompactionObserver{id: "m"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	obs := lipfeature.Get(gen.Frozen, lipfeature.PlaneCompactionObservers)
	if len(obs) != 3 {
		t.Fatalf("CompactionObservers len=%d want 3", len(obs))
	}
	for i, want := range []string{"a", "z", "m"} {
		got, ok := obs[i].(mergeStubCompactionObserver)
		if !ok || got.id != want {
			t.Fatalf("merged observer[%d]=%T/%q want %q", i, obs[i], got.id, want)
		}
	}

	opts := extensions.SnapshotOptions{CompactionObservers: obs}
	if len(opts.CompactionObservers) != 3 {
		t.Fatalf("SnapshotOptions CompactionObservers len=%d want 3", len(opts.CompactionObservers))
	}

	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), opts)
	got := snap.CompactionObservers()
	if len(got) != 3 {
		t.Fatalf("snapshot CompactionObservers len=%d want 3", len(got))
	}
	for i, want := range []string{"a", "z", "m"} {
		observer, ok := got[i].(mergeStubCompactionObserver)
		if !ok || observer.id != want {
			t.Fatalf("snapshot observer[%d]=%T/%q want %q", i, got[i], observer.id, want)
		}
	}
	// The accessor must return a defensive copy: mutating it cannot change the
	// frozen snapshot backing store.
	got[0] = nil
	again := snap.CompactionObservers()
	if len(again) != 3 || again[0] == nil {
		t.Fatal("CompactionObservers() must return a defensive copy of the frozen slice")
	}
}

func TestCompactionPreservers_portWiringAndDefensiveSnapshot(t *testing.T) {
	t.Parallel()
	bundle := lipfeature.FeatureBundle{
		SchemaVersion:        lipfeature.SchemaVersionV1,
		CompactionPreservers: []compaction.Preserver{mergeStubCompactionPreserver{id: "a"}, mergeStubCompactionPreserver{id: "z"}},
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeBundlesGenerated(bundle, lipfeature.FeatureBundle{
		SchemaVersion:        lipfeature.SchemaVersionV1,
		CompactionPreservers: []compaction.Preserver{mergeStubCompactionPreserver{id: "m"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pres := lipfeature.Get(gen.Frozen, lipfeature.PlaneCompactionPreservers)
	if len(pres) != 3 {
		t.Fatalf("CompactionPreservers len=%d want 3", len(pres))
	}
	for i, want := range []string{"a", "z", "m"} {
		got, ok := pres[i].(mergeStubCompactionPreserver)
		if !ok || got.id != want {
			t.Fatalf("merged preserver[%d]=%T/%q want %q", i, pres[i], got.id, want)
		}
	}

	opts := extensions.SnapshotOptions{CompactionPreservers: pres}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), opts)
	got := snap.CompactionPreservers()
	if len(got) != 3 {
		t.Fatalf("snapshot CompactionPreservers len=%d want 3", len(got))
	}
	pres[0] = nil
	if frozen := snap.CompactionPreservers(); frozen[0] == nil {
		t.Fatal("snapshot must freeze an input defensive copy")
	}
	got[0] = nil
	if again := snap.CompactionPreservers(); len(again) != 3 || again[0] == nil {
		t.Fatal("CompactionPreservers() must return a defensive copy")
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
