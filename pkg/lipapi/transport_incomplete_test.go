package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNegotiateTransport_skipsIncompleteInvocationMetadata(t *testing.T) {
	t.Parallel()

	res := lipapi.NegotiateTransport(lipapi.Invocation{}, nil, lipapi.TransportFallbackExact)
	if res.Kind != lipapi.NegotiationLossless {
		t.Fatalf("got %s", res.Kind)
	}
	if res.Selected != "" {
		t.Fatalf("selected = %q", res.Selected)
	}
}
