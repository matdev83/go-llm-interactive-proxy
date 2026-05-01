package workspace_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

func TestDisabledResolver_returnsNotConfigured(t *testing.T) {
	t.Parallel()
	var r workspace.DisabledResolver
	_, err := r.Resolve(context.Background())
	if !errors.Is(err, workspace.ErrResolverNotConfigured) {
		t.Fatalf("got %v", err)
	}
}
