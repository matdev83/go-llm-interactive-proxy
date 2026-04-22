package stream_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNewKeepalive_nil_inner_returns_error(t *testing.T) {
	t.Parallel()
	ka, err := stream.NewKeepalive(nil, stream.KeepaliveConfig{Interval: time.Millisecond})
	if !errors.Is(err, stream.ErrNilEventStream) {
		t.Fatalf("expected ErrNilEventStream, got %v", err)
	}
	if !errors.Is(err, lipapi.ErrNilEventStream) {
		t.Fatalf("expected lipapi.ErrNilEventStream, got %v", err)
	}
	if ka != nil {
		t.Fatalf("expected nil Keepalive, got %v", ka)
	}
}

func TestKeepalive_nilRecv_returns_error(t *testing.T) {
	t.Parallel()
	var k *stream.Keepalive
	_, err := k.Recv(context.Background())
	if !errors.Is(err, stream.ErrNilKeepalive) {
		t.Fatalf("expected ErrNilKeepalive, got %v", err)
	}
}

func TestKeepalive_nilClose_returns_nil(t *testing.T) {
	t.Parallel()
	var k *stream.Keepalive
	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestKeepalive_Recv_nilContext(t *testing.T) {
	t.Parallel()
	inner := lipapi.NewFixedEventStream(nil)
	k, err := stream.NewKeepalive(inner, stream.KeepaliveConfig{Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	_, err = k.Recv(nil)
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}
