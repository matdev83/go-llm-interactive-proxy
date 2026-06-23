package bedrock

import (
	"context"
	"testing"
)

func TestNewWithContext_exposesInventoryPrefix(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	be := NewWithContext(ctx, Config{Region: "us-east-1"})
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != ID {
		t.Fatalf("BackendPrefixes = %#v, want [%q]", be.BackendPrefixes, ID)
	}
}
