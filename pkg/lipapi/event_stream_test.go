package lipapi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestFixedEventStream_Recv_nilContext(t *testing.T) {
	t.Parallel()
	s := lipapi.NewFixedEventStream(nil)
	_, err := s.Recv(nil) //nolint:staticcheck // deliberate nil ctx; expect lipapi.ErrNilContext
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestFixedEventStream_Recv_nilReceiver(t *testing.T) {
	t.Parallel()
	var s *lipapi.FixedEventStream
	_, err := s.Recv(context.Background())
	if !errors.Is(err, lipapi.ErrNilFixedEventStream) {
		t.Fatalf("got %v", err)
	}
}

func TestCollectWithLimits_nilContext(t *testing.T) {
	t.Parallel()
	s := lipapi.NewFixedEventStream(nil)
	_, err := lipapi.CollectWithLimits(nil, s, lipapi.CollectLimits{}) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestCollect_nilContext(t *testing.T) {
	t.Parallel()
	s := lipapi.NewFixedEventStream(nil)
	_, err := lipapi.Collect(nil, s) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestCollectUnbounded_nilContext(t *testing.T) {
	t.Parallel()
	s := lipapi.NewFixedEventStream(nil)
	_, err := lipapi.CollectUnbounded(nil, s) //nolint:staticcheck // deliberate nil ctx
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}
