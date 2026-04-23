package auxiliary_test

import (
	"context"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/auxiliary"
)

func TestDisabledClient(t *testing.T) {
	t.Parallel()
	var c auxiliary.Client = auxiliary.DisabledClient{}
	ctx := context.Background()
	_, err := c.Collect(ctx, auxiliary.Request{Call: &lipapi.Call{}})
	if err != auxiliary.ErrNotConfigured {
		t.Fatalf("collect: %v", err)
	}
	_, err = c.Stream(ctx, auxiliary.Request{Call: &lipapi.Call{}})
	if err != auxiliary.ErrNotConfigured {
		t.Fatalf("stream: %v", err)
	}
}
