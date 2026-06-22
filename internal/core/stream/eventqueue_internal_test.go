package stream

import (
	"reflect"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func TestPendingEventQueueInPlaceCompactClearsTail(t *testing.T) {
	t.Parallel()

	var q PendingEventQueue
	for i := range 128 {
		if err := q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: string(rune('a' + i))}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	oldLen := len(q.buf)

	for range 64 {
		if _, ok := q.PopFront(); !ok {
			t.Fatal("pop: ok=false")
		}
	}

	if q.head != 0 {
		t.Fatalf("head=%d want 0 after compaction", q.head)
	}
	if q.Len() != 64 {
		t.Fatalf("Len=%d want 64", q.Len())
	}

	backing := q.buf[:cap(q.buf)]
	for i := len(q.buf); i < oldLen; i++ {
		if !reflect.DeepEqual(backing[i], lipapi.Event{}) {
			t.Fatalf("backing[%d]=%+v, want zero event", i, backing[i])
		}
	}
}
