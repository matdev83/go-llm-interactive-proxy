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
	if d.index >= len(d.events) {
		select {
		case <-ctx.Done():
			return lipapi.Event{}, ctx.Err()
		case <-time.After(d.delay):
			return lipapi.Event{}, io.EOF
		}
	}
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-time.After(d.delay):
		ev := d.events[d.index]
		d.index++
		return ev, nil
	}
}

func (d *delayedStream) Close() error { return nil }

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

	ka := stream.NewKeepalive(inner, stream.KeepaliveConfig{
		Interval: 20 * time.Millisecond,
	})
	defer ka.Close()

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
		delay: 50 * time.Millisecond,
	}

	ka := stream.NewKeepalive(inner, stream.KeepaliveConfig{
		Interval: 15 * time.Millisecond,
		NewKeepalive: func() lipapi.Event {
			return customEvent
		},
	})
	defer ka.Close()

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

	ka := stream.NewKeepalive(inner, stream.KeepaliveConfig{
		Interval: 50 * time.Millisecond,
	})
	defer ka.Close()

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

	ka := stream.NewKeepalive(blockingStream, stream.KeepaliveConfig{
		Interval: 200 * time.Millisecond,
	})
	defer ka.Close()

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
	select {
	case <-ctx.Done():
		return lipapi.Event{}, ctx.Err()
	case <-b.unblock:
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

	ka := stream.NewKeepalive(inner, stream.KeepaliveConfig{
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

	ka := stream.NewKeepalive(inner, stream.KeepaliveConfig{
		Interval: 100 * time.Millisecond,
	})
	defer ka.Close()

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
