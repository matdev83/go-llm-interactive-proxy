package codexcatalog_test

import (
	"context"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/codexcatalog"
)

func TestDiscover_NoExecutableReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := codexcatalog.Discover(context.Background(), "", time.Second); err == nil {
		t.Fatal("Discover(empty exe) = nil error, want error")
	}
}

func TestDiscover_MissingBinaryReturnsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := codexcatalog.Discover(ctx, "definitely-not-a-real-codex-binary-xyz-123", time.Second); err == nil {
		t.Fatal("Discover(missing exe) = nil error, want error")
	}
}

func TestDiscover_CancelledContextReturnsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := codexcatalog.Discover(ctx, "codex", time.Second); err == nil {
		t.Fatal("Discover(cancelled ctx) = nil error, want error")
	}
}
