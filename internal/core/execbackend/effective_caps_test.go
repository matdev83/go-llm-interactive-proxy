package execbackend

import (
	"context"
	"maps"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestEffectiveCaps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	call := lipapi.Call{}
	cand := routing.AttemptCandidate{}

	tests := []struct {
		name string
		be   Backend
		want lipapi.BackendCaps
	}{
		{
			name: "nil resolve uses static caps",
			be: Backend{
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
			},
			want: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
		},
		{
			name: "resolve caps overrides static",
			be: Backend{
				Caps: lipapi.NewBackendCaps(lipapi.CapabilityStreaming),
				ResolveCaps: func(context.Context, lipapi.Call, routing.AttemptCandidate) lipapi.BackendCaps {
					return lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools)
				},
			},
			want: lipapi.NewBackendCaps(lipapi.CapabilityStreaming, lipapi.CapabilityTools),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := EffectiveCaps(ctx, tt.be, call, cand)
			if !maps.Equal(got, tt.want) {
				t.Fatalf("want caps %v, got %v", tt.want, got)
			}
		})
	}
}
