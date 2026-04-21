package stream_test

import (
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
)

func TestNewKeepalive_nil_inner_returns_error(t *testing.T) {
	t.Parallel()
	ka, err := stream.NewKeepalive(nil, stream.KeepaliveConfig{Interval: time.Millisecond})
	if !errors.Is(err, stream.ErrNilEventStream) {
		t.Fatalf("expected ErrNilEventStream, got %v", err)
	}
	if ka != nil {
		t.Fatalf("expected nil Keepalive, got %v", ka)
	}
}
