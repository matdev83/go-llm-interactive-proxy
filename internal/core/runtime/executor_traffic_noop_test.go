package runtime_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/extensions"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	sdktraffic "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
)

type recordingTrafficObserver struct {
	mu   sync.Mutex
	legs map[sdktraffic.Leg][]sdktraffic.Observation
}

func newRecordingTrafficObserver() *recordingTrafficObserver {
	return &recordingTrafficObserver{
		legs: make(map[sdktraffic.Leg][]sdktraffic.Observation),
	}
}

func (r *recordingTrafficObserver) OnObservation(_ context.Context, obs sdktraffic.Observation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.legs[obs.Leg] = append(r.legs[obs.Leg], obs)
	return nil
}

func (r *recordingTrafficObserver) observationsFor(leg sdktraffic.Leg) []sdktraffic.Observation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sdktraffic.Observation(nil), r.legs[leg]...)
}

func trafficTestExecutor(t testing.TB, tobs sdktraffic.Observer) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		TrafficObserver: tobs,
	})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.Backends = map[string]execbackend.Backend{
		"mock": {
			Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
				return lipapi.NewFixedEventStream([]lipapi.Event{
					{Kind: lipapi.EventResponseStarted},
					{Kind: lipapi.EventMessageStarted},
					{Kind: lipapi.EventTextDelta, Delta: "hello"},
					{Kind: lipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	return ex
}

func TestExecutor_Traffic_DisabledSkipsPayloadMarshal(t *testing.T) {
	t.Parallel()
	ex := trafficTestExecutor(t, nil)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "mock:model-1"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello world")}},
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	collected, err := lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected collect error: %v", err)
	}
	if collected.Text.String() != "hello" {
		t.Fatalf("expected text %q, got %q", "hello", collected.Text.String())
	}
}

func TestExecutor_Traffic_EnabledEmitsCTPAndPTB(t *testing.T) {
	t.Parallel()
	recorder := newRecordingTrafficObserver()
	ex := trafficTestExecutor(t, recorder)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "mock:model-1"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello world")}},
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	_, err = lipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected collect error: %v", err)
	}

	ctpObs := recorder.observationsFor(sdktraffic.LegCTP)
	if len(ctpObs) != 1 {
		t.Fatalf("expected 1 CTP observation, got %d", len(ctpObs))
	}
	var ctpCall lipapi.Call
	if err := json.Unmarshal(ctpObs[0].Body, &ctpCall); err != nil {
		t.Fatalf("failed to unmarshal CTP body: %v", err)
	}
	if len(ctpCall.Messages) != 1 {
		t.Fatalf("expected 1 message in CTP body, got %d", len(ctpCall.Messages))
	}

	ptbObs := recorder.observationsFor(sdktraffic.LegPTB)
	if len(ptbObs) != 1 {
		t.Fatalf("expected 1 PTB observation, got %d", len(ptbObs))
	}
	var ptbCall lipapi.Call
	if err := json.Unmarshal(ptbObs[0].Body, &ptbCall); err != nil {
		t.Fatalf("failed to unmarshal PTB body: %v", err)
	}
	if len(ptbCall.Messages) != 1 {
		t.Fatalf("expected 1 message in PTB body, got %d", len(ptbCall.Messages))
	}
}

func BenchmarkExecutor_TrafficDisabled(b *testing.B) {
	ex := trafficTestExecutor(b, nil)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "mock:model-1"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello world from benchmark prompt with realistic payload size")}},
		},
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stream, err := ex.Execute(ctx, call)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = lipapi.Collect(ctx, stream)
	}
}

func BenchmarkExecutor_TrafficEnabled(b *testing.B) {
	recorder := newRecordingTrafficObserver()
	ex := trafficTestExecutor(b, recorder)
	call := &lipapi.Call{
		Route: lipapi.RouteIntent{Selector: "mock:model-1"},
		Messages: []lipapi.Message{
			{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("hello world from benchmark prompt with realistic payload size")}},
		},
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stream, err := ex.Execute(ctx, call)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = lipapi.Collect(ctx, stream)
	}
}
