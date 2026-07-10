package runtime

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execbackend"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
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

	p := attemptOpenParams{
		ctx:     context.Background(),
		bus:     hooks.New(hooks.Config{}),
		traceID: "trace-parallel",
		aLegID:  aLegID,
		baseline: lipapi.Call{
			ID:    "request-parallel",
			Route: lipapi.RouteIntent{Selector: "backend-1:model-1!backend-2:model-2"},
			Invocation: lipapi.Invocation{
				Operation:    lipapi.OperationOpenAIChatCompletions,
				DeliveryMode: lipapi.DeliveryModeStreaming,
			},
		},
	}
	candidates := []routing.AttemptCandidate{
		{Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}, Key: "backend-1:model-1"},
		{Primary: routing.Primary{Backend: "backend-2", Model: "model-2"}, Key: "backend-2:model-2"},
	}

	out, err := ex.tryOpenParallelGroup(p, candidates, nil, "", false)
	if err != nil {
		t.Fatalf("tryOpenParallelGroup: %v", err)
	}
	if !out.opened {
		t.Fatal("expected parallel race to open a backend")
	}
	if out.cand.Primary.Backend != "backend-1" {
		t.Fatalf("winner backend = %q, want backend-1", out.cand.Primary.Backend)
	}
	if out.authority.admissionInput.Correlation.TraceID == "" {
		t.Fatal("expected winner authority to have a trace ID")
	}
	if out.authority.admissionResult.ReservationID != "reservation-parallel" {
		t.Fatalf("winner authority reservation ID = %q, want reservation-parallel", out.authority.admissionResult.ReservationID)
	}
}
