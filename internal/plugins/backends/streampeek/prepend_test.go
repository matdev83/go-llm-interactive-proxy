package streampeek_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/backends/streampeek"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestNewPrependFirst_recvOrder(t *testing.T) {
	t.Parallel()
	first := lipapi.Event{Kind: lipapi.EventMessageStarted}
	rest := lipapi.NewFixedEventStream([]lipapi.Event{
		{Kind: lipapi.EventTextDelta, Delta: "x"},
		{Kind: lipapi.EventResponseFinished},
	})
	es := streampeek.NewPrependFirst(first, rest)
	ctx := context.Background()
	ev1, err := es.Recv(ctx)
	if err != nil || ev1.Kind != lipapi.EventMessageStarted {
		t.Fatalf("first: %+v err=%v", ev1, err)
	}
	ev2, err := es.Recv(ctx)
	if err != nil || ev2.Delta != "x" {
		t.Fatalf("second: %+v err=%v", ev2, err)
	}
	ev3, err := es.Recv(ctx)
	if err != nil || ev3.Kind != lipapi.EventResponseFinished {
		t.Fatalf("third: %+v err=%v", ev3, err)
	}
	if err := es.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewPrependFirst_nilRest_secondRecvEOF(t *testing.T) {
	t.Parallel()
	first := lipapi.Event{Kind: lipapi.EventMessageStarted}
	es := streampeek.NewPrependFirst(first, nil)
	ctx := context.Background()
	_, err := es.Recv(ctx)
	if err != nil {
		t.Fatalf("first recv: %v", err)
	}
	_, err = es.Recv(ctx)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("second recv: want io.EOF, got %v", err)
	}
}
