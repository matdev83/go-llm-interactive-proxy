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

func TestRequestDetachedPolicyIsExplicitAndContentFree(t *testing.T) {
	t.Parallel()
	var zero auxiliary.Request
	if zero.SessionMode != auxiliary.SessionModeDefault {
		t.Fatalf("zero request mode=%v want default", zero.SessionMode)
	}
	if zero.ParentBranchBinding != "" {
		t.Fatalf("zero request branch binding=%q want empty", zero.ParentBranchBinding)
	}
	if auxiliary.SessionModeDetached == auxiliary.SessionModeDefault {
		t.Fatal("detached mode must be distinct from the default")
	}
}
