package capabilities

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestMapResolver_dispatchesByBackend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	call := lipapi.Call{}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "be1", Model: "m"}}
	m := MapResolver{
		"be1": func(_ context.Context, c routing.AttemptCandidate, _ lipapi.Call) lipapi.BackendCaps {
			if c.Primary.Model != "m" {
				t.Fatalf("model %q", c.Primary.Model)
			}
			return lipapi.NewBackendCaps(lipapi.CapabilityStreaming)
		},
	}
	got := m.DescribeCandidate(ctx, cand, call)
	if _, ok := got[lipapi.CapabilityStreaming]; !ok {
		t.Fatalf("caps %+v", got)
	}
}
