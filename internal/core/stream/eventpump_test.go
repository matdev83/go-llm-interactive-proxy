package stream_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestEventPump_returnsPendingBeforeReading(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	pending := stream.NewPendingEventQueue(0)
	if err := pending.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "queued"}); err != nil {
		t.Fatal(err)
	}
	readCalled := false
	pump := stream.EventPump[int]{
		Lock:    &mu,
		Pending: &pending,
		Read: func() (int, bool, error) {
			readCalled = true
			return 0, false, io.EOF
		},
	}

	ev, err := pump.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Delta != "queued" {
		t.Fatalf("event = %+v", ev)
	}
	if readCalled {
		t.Fatal("read called before pending queue drained")
	}
}

func TestEventPump_onEOFCanEmitPendingEvent(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	pending := stream.NewPendingEventQueue(0)
	closed := false
	pump := stream.EventPump[int]{
		Lock:     &mu,
		Pending:  &pending,
		IsClosed: func() bool { return closed },
		Read:     func() (int, bool, error) { return 0, false, nil },
		OnEOF: func() (bool, error) {
			return true, pending.Push(lipapi.Event{Kind: lipapi.EventResponseFinished})
		},
	}

	ev, err := pump.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != lipapi.EventResponseFinished {
		t.Fatalf("event = %+v", ev)
	}
	closed = true
	_, err = pump.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("after close: %v", err)
	}
}
