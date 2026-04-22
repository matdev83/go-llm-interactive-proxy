package lipapi_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestSentinelErrorPrefixes(t *testing.T) {
	t.Parallel()
	tests := map[string]error{
		"ErrInvalidCall":          lipapi.ErrInvalidCall,
		"ErrCollectLimitExceeded": lipapi.ErrCollectLimitExceeded,
		"ErrNilEventStream":       lipapi.ErrNilEventStream,
		"ErrNilContext":           lipapi.ErrNilContext,
		"ErrNilFixedEventStream":  lipapi.ErrNilFixedEventStream,
		"ErrMaxRouteAttempts":     lipapi.ErrMaxRouteAttempts,
		"ErrCapabilityReject":     lipapi.ErrCapabilityReject,
		"ErrHookMutation":         lipapi.ErrHookMutation,
	}
	for name, err := range tests {
		if got := err.Error(); len(got) < len("lipapi: ") || got[:len("lipapi: ")] != "lipapi: " {
			t.Fatalf("%s prefix = %q, want lipapi-prefixed sentinel", name, got)
		}
	}
}
