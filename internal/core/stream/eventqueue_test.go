package stream_test

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/stream"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPendingEventQueue_ordering(t *testing.T) {
	t.Parallel()
	var q stream.PendingEventQueue
	for i := 0; i < 5; i++ {
		q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: string(rune('a' + i))})
	}
	for i := 0; i < 5; i++ {
		ev, ok := q.PopFront()
		if !ok {
			t.Fatalf("pop %d: ok=false", i)
		}
		want := string(rune('a' + i))
		if ev.Delta != want {
			t.Fatalf("pop %d: Delta=%q want %q", i, ev.Delta, want)
		}
	}
	if q.Len() != 0 {
		t.Fatalf("Len after drain = %d", q.Len())
	}
	_, ok := q.PopFront()
	if ok {
		t.Fatal("pop empty: want false")
	}
}

func TestPendingEventQueue_manyPushPop(t *testing.T) {
	t.Parallel()
	var q stream.PendingEventQueue
	for round := 0; round < 3; round++ {
		for i := 0; i < 500; i++ {
			q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "x"})
		}
		for i := 0; i < 500; i++ {
			ev, ok := q.PopFront()
			if !ok || ev.Kind != lipapi.EventTextDelta {
				t.Fatalf("round %d pop %d", round, i)
			}
		}
	}
	if q.Len() != 0 {
		t.Fatalf("final Len = %d", q.Len())
	}
}

func TestDrainPending(t *testing.T) {
	t.Parallel()
	var q stream.PendingEventQueue
	q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "a"})
	q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "b"})
	got := stream.DrainPending(&q)
	if len(got) != 2 || got[0].Delta != "a" || got[1].Delta != "b" {
		t.Fatalf("got %+v", got)
	}
	if q.Len() != 0 {
		t.Fatalf("queue not empty: Len=%d", q.Len())
	}
	if again := stream.DrainPending(&q); len(again) != 0 {
		t.Fatalf("second drain: %+v", again)
	}
}
