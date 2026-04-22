package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestRetryRecvStream_Recv_nilContext(t *testing.T) {
	t.Parallel()
	s := &retryRecvStream{}
	_, err := s.Recv(nil)
	if !errors.Is(err, lipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestRetryRecvStream_Recv_nilReceiver(t *testing.T) {
	t.Parallel()
	var s *retryRecvStream
	_, err := s.Recv(context.Background())
	if !errors.Is(err, errNilRetryRecvStream) {
		t.Fatalf("got %v", err)
	}
}

func TestRetryRecvStream_Close_nilReceiver(t *testing.T) {
	t.Parallel()
	var s *retryRecvStream
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
