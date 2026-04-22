package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/routing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestRetryRecvStream_Recv_nilContext(t *testing.T) {
	t.Parallel()
	s := &retryRecvStream{}
	_, err := s.Recv(nil) //nolint:staticcheck // deliberate nil ctx; expect lipapi.ErrNilContext
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

// blockingUntilCloseInner blocks Recv until Close closes unblock.
type blockingUntilCloseInner struct {
	recvEntered chan struct{}
	unblock     chan struct{}
	closeOnce   sync.Once
}

func (b *blockingUntilCloseInner) Recv(ctx context.Context) (lipapi.Event, error) {
	select {
	case b.recvEntered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-b.unblock:
		return lipapi.Event{}, io.EOF
	}
}

func (b *blockingUntilCloseInner) Close() error {
	b.closeOnce.Do(func() { close(b.unblock) })
	return nil
}

func TestRetryRecvStream_Close_concurrentWhileRecvBlocked(t *testing.T) {
	t.Parallel()
	inner := &blockingUntilCloseInner{
		recvEntered: make(chan struct{}, 1),
		unblock:     make(chan struct{}),
	}
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	sel, err := routing.Parse("openai:gpt-4")
	if err != nil {
		t.Fatal(err)
	}
	s := &retryRecvStream{
		executor: &Executor{Store: st, Bus: bus},
		bus:      bus,
		baseline: lipapi.Call{
			Route:    lipapi.RouteIntent{Selector: "openai:gpt-4"},
			Messages: []lipapi.Message{{Role: lipapi.RoleUser, Parts: []lipapi.Part{lipapi.TextPart("x")}}},
		},
		budget:   &attemptBudget{max: 3, used: 0},
		aLegID:   "a1",
		traceID:  "t1",
		sel:      sel,
		session:  &routing.SessionRoutingState{},
		excluded: map[string]struct{}{},
		rng:      routing.NewSeededRng(1),
		bleg:     b2bua.BLegRecord{BLegID: "b1", Seq: 1},
		cand: routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		},
	}
	s.storeInner(inner)

	var wg sync.WaitGroup
	wg.Go(func() {
		_, err := s.Recv(context.Background())
		if err != nil && !errors.Is(err, io.EOF) {
			t.Errorf("Recv: %v", err)
		}
	})

	waitTimer := time.NewTimer(2 * time.Second)
	defer waitTimer.Stop()
	select {
	case <-inner.recvEntered:
	case <-waitTimer.C:
		t.Fatal("Recv did not reach inner block")
	}

	const n = 32
	var closes sync.WaitGroup
	for range n {
		closes.Go(func() {
			_ = s.Close()
		})
	}
	closes.Wait()
	wg.Wait()
}

func TestCancellationAttemptReason(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := cancellationAttemptReason(ctx, context.Canceled); got != "context canceled" {
		t.Fatalf("canceled recv: got %q", got)
	}
	ctx2, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel2()
	if got := cancellationAttemptReason(ctx2, errors.New("wrapped transport error")); got != "context deadline exceeded" {
		t.Fatalf("deadline ctx: got %q want context deadline exceeded", got)
	}
	if got := cancellationAttemptReason(context.Background(), nil); got != "cancelled" {
		t.Fatalf("no ctx err: got %q", got)
	}
}
