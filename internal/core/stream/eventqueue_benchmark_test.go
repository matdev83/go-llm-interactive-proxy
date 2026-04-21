package stream

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

func BenchmarkPendingEventQueue_pushPop(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		var q PendingEventQueue
		for i := 0; i < 256; i++ {
			q.Push(lipapi.Event{Kind: lipapi.EventTextDelta, Delta: "d"})
		}
		for i := 0; i < 256; i++ {
			if _, ok := q.PopFront(); !ok {
				b.Fatal("pop")
			}
		}
	}
}
