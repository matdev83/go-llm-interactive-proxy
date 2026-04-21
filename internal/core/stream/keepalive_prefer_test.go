package stream

import (
	"context"
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPreferBufferedItemOrKeepalive_returnsBufferedItem(t *testing.T) {
	t.Parallel()
	ch := make(chan item, 1)
	want := lipapi.Event{Kind: lipapi.EventResponseStarted}
	ch <- item{ev: want, err: nil}

	got, err := preferBufferedItemOrKeepalive(ch, func() lipapi.Event {
		t.Fatal("keepalive must not be used when a buffered item exists")
		return lipapi.Event{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != want.Kind {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestPreferBufferedItemOrKeepalive_closedChannel(t *testing.T) {
	t.Parallel()
	ch := make(chan item)
	close(ch)

	_, err := preferBufferedItemOrKeepalive(ch, func() lipapi.Event {
		t.Fatal("keepalive must not be used when channel is closed")
		return lipapi.Event{}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestPreferBufferedItemOrKeepalive_emptyUsesKeepalive(t *testing.T) {
	t.Parallel()
	ch := make(chan item, 1)
	var called bool
	got, err := preferBufferedItemOrKeepalive(ch, func() lipapi.Event {
		called = true
		return DefaultKeepaliveEvent()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected keepalive callback when channel empty")
	}
	if got.WarningCode != KeepaliveEventCode {
		t.Fatalf("got %#v", got)
	}
}
