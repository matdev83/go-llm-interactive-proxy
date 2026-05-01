package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdk "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

// Profiling traffic JSON (manual):
//
//	go test -bench=BenchmarkEmitTrafficBTP_jsonMarshal -benchmem -count=8 -cpuprofile=traffic.cpu ./internal/core/runtime/
//	go tool pprof -top traffic.cpu
//
// Baseline isolate for json.Marshal alone:
//
//	go test -bench=BenchmarkMarshalTrafficEvent_jsonOnly -benchmem -memprofile=traffic.mem ./internal/core/runtime/
//	go tool pprof -top traffic.mem
//
// Today json.Marshal(ev) dominates emitTrafficBTP/PTC CPU/allocs vs the rest of the emit path.
// Further optimization (subset payloads, codegen, or pooling) needs a product decision on
// capture fidelity and must prove copy semantics for [sdktraffic.PortBundle.Emit] consumers.

type nopBenchTrafficObserver struct{}

func (nopBenchTrafficObserver) OnObservation(context.Context, sdktraffic.Observation) error {
	return nil
}

func benchRetryRecvForTrafficEmit() (*retryRecvStream, lipapi.Event, sdk.PartMeta) {
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		TrafficObserver: nopBenchTrafficObserver{},
	})
	ex := &Executor{RuntimeSnapshot: snap}
	s := &retryRecvStream{
		executor: ex,
		cand: routing.AttemptCandidate{
			Primary: routing.Primary{Backend: "bench-backend", Model: "m"},
			Key:     "bench-backend:m",
		},
	}
	ev := lipapi.Event{
		Kind:  lipapi.EventTextDelta,
		Delta: strings.Repeat("x", 512),
	}
	pm := sdk.PartMeta{
		TraceID: "trace", ALegID: "a", BLegID: "b", AttemptSeq: 1,
	}
	return s, ev, pm
}

func BenchmarkEmitTrafficBTP_jsonMarshal(b *testing.B) {
	s, ev, pm := benchRetryRecvForTrafficEmit()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.emitTrafficBTP(ctx, ev, pm)
	}
}

func BenchmarkEmitTrafficPTC_jsonMarshal(b *testing.B) {
	s, ev, pm := benchRetryRecvForTrafficEmit()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.emitTrafficPTC(ctx, ev, pm)
	}
}

func BenchmarkMarshalTrafficEvent_jsonOnly(b *testing.B) {
	_, ev, _ := benchRetryRecvForTrafficEmit()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = json.Marshal(ev)
	}
}
