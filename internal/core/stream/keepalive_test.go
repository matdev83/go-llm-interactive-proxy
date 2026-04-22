package stream_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

type delayedStream struct {
	events []lipapi.Event
	delay  time.Duration
	index  int
}

func (d *delayedStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	if d.index >= len(d.events) {
		t := time.NewTimer(d.delay)
		defer func() {
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}()
		select {
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		case <-t.C:
			// If cancellation became ready in the same scheduling window as the timer,
			// Go's select may pick the timer branch; prefer ctx for deterministic tests.
			select {
			case <-ctx.Done():
				return lipapi.Event{}, ctx.Err()
			default:
			}
			return lipapi.Event{}, io.EOF
		}
	}
	tm := time.NewTimer(d.delay)
	defer func() {
		if !tm.Stop() {
			select {
			case <-tm.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-tm.C:
		select {
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		default:
		}
		ev := d.events[d.index]
		d.index++
		return ev, nil
	}
}

func (d *delayedStream) Close() error { return nil }

func mustNewKeepalive(t *testing.T, inner lipapi.EventStream, cfg stream.KeepaliveConfig) *stream.Keepalive {
	t.Helper()
	ka, err := stream.NewKeepalive(inner, cfg)
	if err != nil {
		t.Fatalf("NewKeepalive: %v", err)
	}
	return ka
}

func TestKeepalive_emitsDuringIdle(t *testing.T) {
	t.Parallel()

	inner := &delayedStream{
		events: []lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventTextDelta, Delta: "hi"},
			{Kind: lipapi.EventResponseFinished},
		},
		delay: 60 * time.Millisecond,
	}

	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
		Interval: 20 * time.Millisecond,
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var keepaliveCount int
	var realCount int

	for {
		ev, err := ka.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.WarningCode == stream.KeepaliveEventCode {
			keepaliveCount++
		} else {
			realCount++
		}
	}

	if realCount != 3 {
		t.Fatalf("expected 3 real events, got %d", realCount)
	}
	if keepaliveCount == 0 {
		t.Fatal("expected at least one keepalive event during idle waits")
	}
}

func TestKeepalive_customKeepaliveEvent(t *testing.T) {
	t.Parallel()

	customEvent := lipapi.Event{
		Kind:           lipapi.EventWarning,
		WarningCode:    "custom_ka",
		WarningMessage: "custom keepalive",
	}

	inner := &delayedStream{
		events: []lipapi.Event{
			{Kind: lipapi.EventResponseFinished},
		},
		// Long delay vs keepalive interval so the first outer Recv is not flaky under
		// scheduler stalls: if both timer.C and k.result become ready together, select
		// chooses arbitrarily between them.
		delay: 300 * time.Millisecond,
	}

	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
		Interval: 15 * time.Millisecond,
		NewKeepalive: func() lipapi.Event {
			return customEvent
		},
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ev, err := ka.Recv(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.WarningCode != "custom_ka" {
		t.Fatalf("expected custom keepalive, got code %q", ev.WarningCode)
	}
}

func TestKeepalive_propagatesEOF(t *testing.T) {
	t.Parallel()

	inner := &delayedStream{
		events: []lipapi.Event{},
		delay:  5 * time.Millisecond,
	}

	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
		Interval: 50 * time.Millisecond,
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := ka.Recv(ctx)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestKeepalive_respectsCancellation(t *testing.T) {
	t.Parallel()

	blockCh := make(chan struct{})
	blockingStream := &blockingRecvStream{unblock: blockCh}

	ka := mustNewKeepalive(t, blockingStream, stream.KeepaliveConfig{
		Interval: 200 * time.Millisecond,
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := ka.Recv(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	close(blockCh)
}

type blockingRecvStream struct {
	unblock chan struct{}
}

func (b *blockingRecvStream) Recv(ctx context.Context) (lipapi.Event, error) {
	if ctx == nil {
		return lipapi.Event{}, lipapi.ErrNilContext
	}
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-b.unblock:
		// If unblock and cancellation become ready together, prefer ctx (deterministic).
		select {
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		default:
		}
		return lipapi.Event{}, io.EOF
	}
}

func (b *blockingRecvStream) Close() error { return nil }

func TestKeepalive_closeStopsEmission(t *testing.T) {
	t.Parallel()

	inner := &delayedStream{
		events: []lipapi.Event{
			{Kind: lipapi.EventResponseFinished},
		},
		delay: 10 * time.Second,
	}

	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
		Interval: 20 * time.Millisecond,
	})

	if err := ka.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := ka.Recv(ctx)
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestKeepalive_passesThroughRealEvents(t *testing.T) {
	t.Parallel()

	inner := &delayedStream{
		events: []lipapi.Event{
			{Kind: lipapi.EventResponseStarted},
			{Kind: lipapi.EventTextDelta, Delta: "hello"},
			{Kind: lipapi.EventResponseFinished},
		},
		delay: 1 * time.Millisecond,
	}

	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
		Interval: 100 * time.Millisecond,
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var deltas []string
	for {
		ev, err := ka.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.Kind == lipapi.EventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
	}

	if len(deltas) != 1 || deltas[0] != "hello" {
		t.Fatalf("expected [hello], got %v", deltas)
	}
}

// delayedStream must prefer ctx cancellation over a zero-delay timer when both are
// simultaneously ready; otherwise Recv can return a non-deterministic mix of
// context.Canceled vs the next event / io.EOF across runs (Go select fairness).
func TestDelayedStream_prefersCanceledContextOverZeroDelayEvents(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &delayedStream{
		events: []lipapi.Event{{Kind: lipapi.EventResponseStarted}},
		delay:  0,
	}
	for range 200 {
		_, err := d.Recv(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	}
}

func TestKeepalive_recvRejectsPreCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inner := lipapi.NewFixedEventStream([]lipapi.Event{{Kind: lipapi.EventResponseFinished}})
	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{Interval: time.Hour})
	defer func() { _ = ka.Close() }()
	_, err := ka.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestDelayedStream_prefersCanceledContextOverZeroDelayEOF(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &delayedStream{events: nil, delay: 0}
	for range 200 {
		_, err := d.Recv(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v (expected not io.EOF when ctx canceled)", err)
		}
	}
}
