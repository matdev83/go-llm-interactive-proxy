package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/interleavedstate"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	authorityapp "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/app"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controlplane"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/policydecision"
)

func TestParallelRaceWinnerPropagatesAuthority(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: authorityapp.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-parallel",
			ReservedAmount: authorityInputAmount(7),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	backend.openFn = func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
		return lipapi.NewFixedEventStream([]lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventTextDelta, Delta: "winner"},
			{Kind: lipapi.EventResponseFinished},
		}), nil
	}
	ex.Backends["backend-2"] = execbackend.Backend{
		Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		TransportCaps: lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
			Operation: lipapi.OperationOpenAIChatCompletions,
			Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
		}),
		Open: func(context.Context, lipapi.Call, routing.AttemptCandidate) (lipapi.ManagedEventStream, error) {
			return lipapi.NewFixedEventStream([]lipapi.Event{
				{Kind: lipapi.EventResponseStarted},
				{Kind: lipapi.EventTextDelta, Delta: " "},
				{Kind: lipapi.EventResponseFinished},
			}), nil
		},
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
				traceID: "trace-parallel",
				aLegID:  aLegID,
				baseline: lipapi.Call{
					ID:    "request-parallel",
					Route: lipapi.RouteIntent{Selector: "backend-1:model-1!backend-2:model-2"},
					Invocation: lipapi.Invocation{
						Operation:    lipapi.OperationOpenAIChatCompletions,
						DeliveryMode: lipapi.DeliveryModeStreaming,
					},
					Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{{Kind: lipapi.PartText, Text: "hi"}}}},
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

	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"},
		{Primary: routing.Primary{Backend: "backend-2", Model: "model-2"}, Key: "backend-2:model-2"},
	}

	out, err := ex.tryOpenParallelGroup(context.Background(), req, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if out.session == nil {
		t.Fatal("expected parallel race to open a backend")
	}
	if out.session.cand.Primary.Backend != "backend-1" {
		t.Fatalf("winner backend = %q, want backend-1", out.session.cand.Primary.Backend)
	}
	var authState attemptAuthorityState
	if out.session.authority.control != nil {
		authState = out.session.authority.control.state
	}
	if authState.admissionInput.Correlation.TraceID == "" {
		t.Fatal("expected winner authority to have a trace ID")
	}
	if authState.admissionResult.ReservationID != "reservation-parallel" {
		t.Fatalf("winner authority reservation ID = %q, want reservation-parallel", authState.admissionResult.ReservationID)
	}
}
