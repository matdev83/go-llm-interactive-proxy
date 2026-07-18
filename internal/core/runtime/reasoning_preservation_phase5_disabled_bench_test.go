package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/reasoningpreservation"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func BenchmarkPhase5_disabledRuntimeNoFeatureParticipants(b *testing.B) {
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{})
	if len(snap.AttemptTransforms()) != 0 || len(snap.StreamObserverFactories()) != 0 {
		b.Fatal("disabled snapshot must have empty reasoning stages")
	}
	empty := featurebundle.MergeBundles()
	if len(empty.AttemptTransforms) != 0 || len(empty.StreamObserverFactories) != 0 {
		b.Fatal("absent FeatureBundle merge must stay empty")
	}
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		b.Fatal(err)
	}
	var opened atomic.Int64
	ex := runtime.TestExecutor()
	ex.Store, ex.Bus, ex.RuntimeSnapshot, ex.Rand = st, bus, snap, routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"be": p5ReplayBackend(func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			opened.Add(1)
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		}),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		call := p5ObserveCall()
		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			b.Fatalf("execute: %v", err)
		}
		if _, err := lipapi.Collect(context.Background(), stream); err != nil {
			b.Fatalf("collect: %v", err)
		}
		p5AssertNoReasoningParticipants(b, ex.RuntimeSnapshot)
		inv := reasoningpreservation.BuildSafeInventory(reasoningpreservation.Config{}, nil)
		if inv.Enabled || len(inv.AggregateCounters) != 0 {
			b.Fatal("disabled inventory must stay empty during runtime execution")
		}
	}
	if opened.Load() == 0 {
		b.Fatal("disabled runtime must still open backend")
	}
}
