package lipapi_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestSentinelErrorPrefixes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "ErrInvalidCall", err: lipapi.ErrInvalidCall},
		{name: "ErrCollectLimitExceeded", err: lipapi.ErrCollectLimitExceeded},
		{name: "ErrNilEventStream", err: lipapi.ErrNilEventStream},
		{name: "ErrNilContext", err: lipapi.ErrNilContext},
		{name: "ErrNilFixedEventStream", err: lipapi.ErrNilFixedEventStream},
		{name: "ErrMaxRouteAttempts", err: lipapi.ErrMaxRouteAttempts},
		{name: "ErrAllCandidatesContextLimitExceeded", err: lipapi.ErrAllCandidatesContextLimitExceeded},
		{name: "ErrCapabilityReject", err: lipapi.ErrCapabilityReject},
		{name: "ErrHookMutation", err: lipapi.ErrHookMutation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.err.Error()
			if len(got) < len("lipapi: ") || got[:len("lipapi: ")] != "lipapi: " {
				t.Fatalf("prefix = %q, want lipapi-prefixed sentinel", got)
			}
		})
	}
}

func TestIsAllCandidatesContextLimitExceeded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "sentinel", err: lipapi.ErrAllCandidatesContextLimitExceeded, want: true},
		{name: "wrapped", err: fmt.Errorf("plan: %w", lipapi.ErrAllCandidatesContextLimitExceeded), want: true},
		{name: "double_wrapped", err: fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", lipapi.ErrAllCandidatesContextLimitExceeded)), want: true},
		{name: "nil", err: nil, want: false},
		{name: "other_sentinel", err: lipapi.ErrMaxRouteAttempts, want: false},
		{name: "unrelated", err: errors.New("some failure"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := lipapi.IsAllCandidatesContextLimitExceeded(tt.err); got != tt.want {
				t.Fatalf("IsAllCandidatesContextLimitExceeded(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
