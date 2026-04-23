package execctx_test

import (
	"context"
	"slices"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/execctx"
)

func TestRouteCandidatePreferences_roundTrip(t *testing.T) {
	t.Parallel()
	base := context.Background()
	ctx := execctx.WithRouteCandidatePreferences(base, []string{"b", "a"})
	got := execctx.RouteCandidatePreferences(ctx)
	if !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("got %#v", got)
	}
}
