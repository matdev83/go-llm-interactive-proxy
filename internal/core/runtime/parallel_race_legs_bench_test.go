package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

// BenchmarkParallelRaceLegsAuthority measures parallel race open under usage-authority
// with 2/4/8 legs (design Performance.Benchmarks / req 16.6).
func BenchmarkParallelRaceLegsAuthority(b *testing.B) {
	for _, legs := range []int{2, 4, 8} {
		b.Run(fmt.Sprintf("legs_%d", legs), func(b *testing.B) {
			auth := &recordingAuthorityService{
				admitResult: authorityapp.AdmissionResult{
					Allowed: true, Reserved: true, ReservationID: "reservation-parallel",
					ReservedAmount: authorityInputAmount(1),
					PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
				},
				status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
			}
			store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				b.Fatal(err)
			}
			ex := TestExecutor()
			ex.Store = store
			ex.Bus = hooks.New(hooks.Config{})
			ex.UsageAuthority = auth
			ex.Rand = routing.NewSeededRng(42)
			ex.Backends = make(map[string]execbackend.Backend, legs)
			candidates := make([]routing.AttemptCandidate, 0, legs)
			for i := range legs {
				id := fmt.Sprintf("backend-%d", i+1)
				delta := " "
				if i == 0 {
					delta = "winner"
				}
				text := delta
				ex.Backends[id] = execbackend.Backend{
					Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
					TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
						Operation: lipapi.OperationOpenAIChatCompletions,
						Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
					}),
					Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
						return lipapi.NewFixedEventStream([]lipapi.Event{
							{Kind: lipapi.EventResponseStarted},
							{Kind: lipapi.EventTextDelta, Delta: text},
							{Kind: lipapi.EventResponseFinished},
						}), nil
					},
				}
				candidates = append(candidates, routing.AttemptCandidate{
					Primary: routing.Primary{Backend: id, Model: "model-1"},
					Key:     id + ":model-1",
				})
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				leg, err := store.CreateALeg(context.Background(), fmt.Sprintf("bench-parallel-%d", i))
				if err != nil {
					b.Fatal(err)
				}
				progress := &recoveryController{
					budget:   &attemptBudget{max: 10},
					ttft:     &ttftBudget{},
					excluded: map[string]struct{}{},
				}
				progress.failures = progress.budget.getFailures()
				progress.budget.failures = progress.failures

				req := openNextRequest{
					reqFacts: requestFacts{
						recvTurnFacts: recvTurnFacts{
							traceID: fmt.Sprintf("trace-%d", i),
							aLegID:  leg.ALegID,
							baseline: lipapi.Call{
								ID:    fmt.Sprintf("request-%d", i),
								Route: lipapi.RouteIntent{Selector: "backend-1:model-1"},
								Invocation: lipapi.Invocation{
									Operation:    lipapi.OperationOpenAIChatCompletions,
									DeliveryMode: lipapi.DeliveryModeStreaming,
								},
								Messages: []lipapi.Message{{
									Role:  lipapi.RoleUser,
									Parts: []lipapi.Part{lipapi.TextPart("benchmark")},
								}},
							},
						},
						bus: hooks.New(hooks.Config{}),
					},
					routeFacts: routeFacts{
						sel: &routing.Selector{},
						rng: routing.NewSeededRng(1),
					},
					progress:    progress,
					mode:        openModeInitial,
					interleaved: interleavedstate.State{},
				}
				out, err := ex.tryOpenParallelGroup(context.Background(), req, candidates, nil, "", false)
				if err != nil {
					b.Fatal(err)
				}
				if out.ready == nil || out.ready.session == nil {
					b.Fatal("expected parallel race to open")
				}
				if out.ready.session.inner != nil {
					_ = out.ready.session.inner.Close()
				}
			}
		})
	}
}
