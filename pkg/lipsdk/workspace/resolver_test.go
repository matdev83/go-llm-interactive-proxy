package workspace_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func TestDisabledResolver(t *testing.T) {
	t.Parallel()
	var r workspace.Resolver = workspace.DisabledResolver{}
	_, err := r.Resolve(context.Background())
	if err != workspace.ErrResolverNotConfigured {
		t.Fatalf("got %v", err)
	}
}
