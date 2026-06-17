package lipapi_test

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPreferredTransportMode_mapsDeliveryMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		delivery lipapi.DeliveryMode
		want     lipapi.TransportMode
	}{
		{lipapi.DeliveryModeStreaming, lipapi.TransportModeStreaming},
		{lipapi.DeliveryModeNonStreaming, lipapi.TransportModeNonStreaming},
	}
	for _, tc := range cases {
		if got := lipapi.PreferredTransportMode(tc.delivery); got != tc.want {
			t.Fatalf("PreferredTransportMode(%q) = %q, want %q", tc.delivery, got, tc.want)
		}
	}
}

func TestNegotiateTransport_exact_acceptsDeclaredSupport(t *testing.T) {
	t.Parallel()

	inv := lipapi.Invocation{
		Operation:    lipapi.OperationOpenAIChatCompletions,
		DeliveryMode: lipapi.DeliveryModeStreaming,
	}
	caps := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})

	res := lipapi.NegotiateTransport(inv, caps, lipapi.TransportFallbackExact)
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
	if res.Selected != lipapi.TransportModeStreaming {
		t.Fatalf("selected = %q", res.Selected)
	}
}

func TestNegotiateTransport_exact_rejectsMissingSupport(t *testing.T) {
	t.Parallel()

	inv := lipapi.Invocation{
		Operation:    lipapi.OperationOpenAIChatCompletions,
		DeliveryMode: lipapi.DeliveryModeNonStreaming,
	}
	caps := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})

	res := lipapi.NegotiateTransport(inv, caps, lipapi.TransportFallbackExact)
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
	err := res.Err()
	if err == nil {
		t.Fatal("expected reject error")
	}
	if !errors.Is(err, lipapi.ErrTransportReject) {
		t.Fatalf("expected ErrTransportReject: %v", err)
	}
}

func TestNegotiateTransport_compatibility_acceptsOmittedCaps(t *testing.T) {
	t.Parallel()

	inv := lipapi.Invocation{
		Operation:    lipapi.OperationOpenAIResponses,
		DeliveryMode: lipapi.DeliveryModeNonStreaming,
	}

	res := lipapi.NegotiateTransport(inv, nil, lipapi.TransportFallbackCompatibility)
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
	if res.Selected != lipapi.TransportModeNonStreaming {
		t.Fatalf("selected = %q", res.Selected)
	}
}

func TestNegotiateTransport_compatibility_acceptsUndeclaredOperation(t *testing.T) {
	t.Parallel()

	inv := lipapi.Invocation{
		Operation:    lipapi.OperationOpenAIResponses,
		DeliveryMode: lipapi.DeliveryModeStreaming,
	}
	caps := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})

	res := lipapi.NegotiateTransport(inv, caps, lipapi.TransportFallbackCompatibility)
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
}

func TestNegotiateTransport_compatibility_rejectsDeclaredUnsupportedMode(t *testing.T) {
	t.Parallel()

	inv := lipapi.Invocation{
		Operation:    lipapi.OperationOpenAIChatCompletions,
		DeliveryMode: lipapi.DeliveryModeNonStreaming,
	}
	caps := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})

	res := lipapi.NegotiateTransport(inv, caps, lipapi.TransportFallbackCompatibility)
	if res.Kind != lipapi.NegotiationReject {
		t.Fatalf("got %s", res.Kind)
	}
	err := res.Err()
	if err == nil {
		t.Fatal("expected reject error")
	}
	if !errors.Is(err, lipapi.ErrTransportReject) {
		t.Fatalf("expected ErrTransportReject: %v", err)
	}
}

func TestBackendTransportCaps_noProviderSpecificIdentifiers(t *testing.T) {
	t.Parallel()

	caps := lipapi.NewBackendTransportCaps(lipapi.OperationTransportSupport{
		Operation: lipapi.OperationOpenAIChatCompletions,
		Modes:     []lipapi.TransportMode{lipapi.TransportModeStreaming},
	})
	if !caps.Supports(lipapi.OperationOpenAIChatCompletions, lipapi.TransportModeStreaming) {
		t.Fatal("expected generic operation transport support")
	}
}
