package stream

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func BenchmarkPendingEventQueue_pushPop(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		q := NewPendingEventQueue(0)
		for range 256 {
			if err := q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "d"}); err != nil {
				b.Fatal(err)
			}
		}
		for range 256 {
			if _, ok := q.PopFront(); !ok {
				b.Fatal("pop")
			}
		}
	}
}
