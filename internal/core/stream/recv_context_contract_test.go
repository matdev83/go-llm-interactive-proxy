package stream

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// sdkLikeRecvBlocker models a vendor stream whose blocking Recv ignores ctx until Close
// unblocks it (same contract as several backend adapters).
type sdkLikeRecvBlocker struct {
	mu          sync.Mutex
	ch          chan struct{}
	recvStarted chan struct{}
}

func newSDKLikeRecvBlocker() *sdkLikeRecvBlocker {
	return &sdkLikeRecvBlocker{
		ch:          make(chan struct{}),
		recvStarted: make(chan struct{}),
	}
}

func (s *sdkLikeRecvBlocker) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	select {
	case s.recvStarted <- struct{}{}:
	default:
	}
	<-s.ch
	return lipapi.Event{}, context.Canceled
}

func (s *sdkLikeRecvBlocker) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch != nil {
		close(s.ch)
		s.ch = nil
	}
	return nil
}

// TestWrapRecoveryKeepalive_cancelReturnsBeforeClose verifies that with the recovery
// keepalive wrapper, canceling the consumer context returns from Recv promptly even
// when the inner stream ignores ctx while blocked (mirrors SSE-style backends).
func TestWrapRecoveryKeepalive_cancelReturnsBeforeClose(t *testing.T) {
	t.Parallel()
	inner := newSDKLikeRecvBlocker()
	ka, err := WrapRecoveryKeepalive(inner)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	recvErr := make(chan error, 1)
	go func() {
		_, err := ka.Recv(ctx)
		recvErr <- err
	}()

	select {
	case <-inner.recvStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("inner Recv did not start")
	}

	cancel()

	select {
	case err := <-recvErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Recv: want context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Recv did not return after ctx cancel")
	}
	// defer ka.Close() tears down inner and the keepalive reader goroutine.
}

// TestSDKLikeRecvBlocker_cancelAloneDoesNotUnblock documents that without Close, a
// stream that ignores ctx during Recv stays blocked (callers must Close).
func TestSDKLikeRecvBlocker_cancelAloneDoesNotUnblock(t *testing.T) {
	t.Parallel()
	inner := newSDKLikeRecvBlocker()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = inner.Recv(ctx)
		close(done)
	}()

	select {
	case <-inner.recvStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("inner Recv did not start")
	}

	cancel()

	select {
	case <-done:
		t.Fatal("Recv returned without Close despite ctx cancel (unexpected if inner respected ctx)")
	case <-time.After(150 * time.Millisecond):
	}

	if err := inner.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Recv did not return after Close")
	}
}
